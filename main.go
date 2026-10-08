// Command healthprobe checks an HTTP endpoint on the loopback interface and
// exits 0 when it answers with an accepted status code, 1 otherwise. It is
// built for container HEALTHCHECK and Kubernetes exec probes in images that
// have no shell, curl or wget.
//
// The request is written by hand on a raw connection instead of going through
// net/http: that package alone more than doubles the binary, and a probe needs
// only the status line.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// Docker reserves exit code 2 and treats anything but 0 and 1 as undefined,
// so every failure, including bad flags, exits 1.
const (
	exitHealthy   = 0
	exitUnhealthy = 1
)

// maxStatusLine bounds how much a misbehaving server can make the probe read.
const maxStatusLine = 4096

type config struct {
	host          string
	port          int
	endpoint      string
	codes         codeSet
	timeout       time.Duration
	userAgent     string
	tls           bool
	tlsNoVerify   bool
	tlsCACert     string
	tlsServerName string
	verbose       bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, showVersion, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitHealthy
		}
		fmt.Fprintln(stderr, "healthprobe:", err)
		return exitUnhealthy
	}
	if showVersion {
		fmt.Fprintln(stdout, version)
		return exitHealthy
	}
	if err := probe(cfg, stderr); err != nil {
		fmt.Fprintln(stderr, "healthprobe:", err)
		return exitUnhealthy
	}
	return exitHealthy
}

func parseFlags(args []string, stderr io.Writer) (config, bool, error) {
	var (
		cfg         config
		mode        string
		ipv6        bool
		codes       string
		showVersion bool
	)
	fs := flag.NewFlagSet("healthprobe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&mode, "mode", "http", "probe mode; only http is supported")
	fs.StringVar(&cfg.host, "host", "127.0.0.1", "host or IP address to connect to")
	fs.BoolVar(&ipv6, "ipv6", false, "connect to ::1 instead of 127.0.0.1")
	fs.IntVar(&cfg.port, "port", 8080, "port to connect to")
	fs.StringVar(&cfg.endpoint, "endpoint", "/", "request path, with an optional query string")
	fs.StringVar(&codes, "http-codes", "200-299", "accepted status codes, comma-separated, ranges allowed (e.g. 200,204,300-399)")
	fs.DurationVar(&cfg.timeout, "timeout", 5*time.Second, "deadline for the whole check: connect, TLS handshake, request and status line")
	fs.DurationVar(&cfg.timeout, "connect-timeout", 5*time.Second, "alias of -timeout, for lprobe compatibility")
	fs.StringVar(&cfg.userAgent, "user-agent", "", `User-Agent header (default "healthprobe/<version>")`)
	fs.BoolVar(&cfg.tls, "tls", false, "use HTTPS")
	fs.BoolVar(&cfg.tlsNoVerify, "tls-no-verify", false, "with -tls, do not verify the server certificate")
	fs.StringVar(&cfg.tlsCACert, "tls-ca-cert", "", "with -tls, PEM file with the CA certificates to verify the server against")
	fs.StringVar(&cfg.tlsServerName, "tls-server-name", "", "with -tls, name to verify the server certificate against")
	fs.BoolVar(&cfg.verbose, "v", false, "log the request and the status line to stderr")
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		return cfg, false, err
	}
	if showVersion {
		return cfg, true, nil
	}
	if fs.NArg() > 0 {
		return cfg, false, fmt.Errorf("unexpected argument %q; every option is a flag", fs.Arg(0))
	}

	if mode != "http" {
		return cfg, false, fmt.Errorf("unsupported -mode %q; only http is supported", mode)
	}
	hostSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "host" {
			hostSet = true
		}
	})
	if ipv6 {
		if hostSet {
			return cfg, false, errors.New("-ipv6 and -host are mutually exclusive")
		}
		cfg.host = "::1"
	}
	if cfg.host == "" {
		return cfg, false, errors.New("-host must not be empty")
	}
	if cfg.port < 1 || cfg.port > 65535 {
		return cfg, false, fmt.Errorf("-port %d is out of range 1-65535", cfg.port)
	}
	if cfg.timeout <= 0 {
		return cfg, false, fmt.Errorf("-timeout must be greater than zero (got %v)", cfg.timeout)
	}
	if !strings.HasPrefix(cfg.endpoint, "/") {
		cfg.endpoint = "/" + cfg.endpoint
	}
	// A space or control character would end the request line early and let
	// the rest of the value be read as headers.
	if strings.ContainsFunc(cfg.endpoint, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return cfg, false, fmt.Errorf("-endpoint %q contains whitespace or control characters; percent-encode them", cfg.endpoint)
	}
	if cfg.userAgent == "" {
		cfg.userAgent = "healthprobe/" + version
	}
	if strings.ContainsAny(cfg.userAgent, "\r\n") {
		return cfg, false, errors.New("-user-agent must not contain line breaks")
	}

	set, err := parseCodes(codes)
	if err != nil {
		return cfg, false, err
	}
	cfg.codes = set

	if !cfg.tls && (cfg.tlsNoVerify || cfg.tlsCACert != "" || cfg.tlsServerName != "") {
		return cfg, false, errors.New("-tls-no-verify, -tls-ca-cert and -tls-server-name require -tls")
	}
	if cfg.tlsNoVerify && (cfg.tlsCACert != "" || cfg.tlsServerName != "") {
		return cfg, false, errors.New("-tls-no-verify cannot be combined with -tls-ca-cert or -tls-server-name")
	}
	if cfg.tls && !tlsSupported {
		return cfg, false, errors.New("-tls is not available: this binary was built without TLS support")
	}
	return cfg, false, nil
}

func probe(cfg config, stderr io.Writer) error {
	deadline := time.Now().Add(cfg.timeout)
	addr := net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port))

	conn, err := (&net.Dialer{Deadline: deadline}).Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}

	scheme := "http"
	if cfg.tls {
		scheme = "https"
		tlsConn, err := wrapTLS(conn, cfg)
		if err != nil {
			return err
		}
		conn = tlsConn
	}
	if cfg.verbose {
		fmt.Fprintf(stderr, "GET %s://%s%s\n", scheme, addr, cfg.endpoint)
	}

	// HTTP/1.1 with Connection: close rather than HTTP/1.0: some servers
	// answer 1.0 requests differently, and a probe should see what clients see.
	req := "GET " + cfg.endpoint + " HTTP/1.1\r\n" +
		"Host: " + hostHeader(cfg) + "\r\n" +
		"User-Agent: " + cfg.userAgent + "\r\n" +
		"Accept: */*\r\n" +
		"Connection: close\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return fmt.Errorf("sending request: %w", err)
	}

	code, line, err := readFinalStatus(bufio.NewReaderSize(conn, maxStatusLine))
	if err != nil {
		return err
	}
	if cfg.verbose {
		fmt.Fprintln(stderr, line)
	}
	if !cfg.codes.contains(code) {
		return fmt.Errorf("unexpected status: %s", line)
	}
	return nil
}

// hostHeader is "localhost" for loopback addresses because virtual-host
// routing (nginx server_name, ingress-style muxes) matches names, not IPs.
func hostHeader(cfg config) string {
	host := cfg.host
	if cfg.tls && cfg.tlsServerName != "" {
		host = cfg.tlsServerName
	} else if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		host = "localhost"
	}
	if cfg.port == 80 && !cfg.tls || cfg.port == 443 && cfg.tls {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(cfg.port))
}

// maxInterim caps how many 1xx responses are skipped, so a server cannot keep
// the probe reading until its deadline with an endless stream of them.
const maxInterim = 8

// readFinalStatus skips interim 1xx responses (103 Early Hints, an unasked-for
// 100 Continue) and returns the final status. 101 is final: nothing follows it
// on a connection that switched protocols. The body is never read.
func readFinalStatus(r *bufio.Reader) (int, string, error) {
	for range maxInterim {
		code, line, err := readStatus(r)
		if err != nil || code >= 200 || code == 101 {
			return code, line, err
		}
		if err := skipHeaders(r); err != nil {
			return 0, line, err
		}
	}
	return 0, "", fmt.Errorf("more than %d interim 1xx responses", maxInterim)
}

func skipHeaders(r *bufio.Reader) error {
	for {
		raw, err := r.ReadSlice('\n')
		if err != nil {
			return fmt.Errorf("reading interim response headers: %w", err)
		}
		if len(strings.TrimRight(string(raw), "\r\n")) == 0 {
			return nil
		}
	}
}

// readStatus returns the status code from an HTTP/1.x status line.
func readStatus(r *bufio.Reader) (int, string, error) {
	raw, err := r.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return 0, "", errors.New("status line too long")
		}
		if errors.Is(err, io.EOF) && len(raw) == 0 {
			return 0, "", errors.New("connection closed before a response")
		}
		return 0, "", fmt.Errorf("reading status line: %w", err)
	}
	line := strings.TrimRight(string(raw), "\r\n")
	proto, rest, ok := strings.Cut(line, " ")
	if !ok || len(proto) != len("HTTP/1.1") || !strings.HasPrefix(proto, "HTTP/1.") || proto[7] < '0' || proto[7] > '9' {
		return 0, line, fmt.Errorf("not an HTTP/1.x response: %q", line)
	}
	codeText, _, _ := strings.Cut(rest, " ")
	code, err := strconv.Atoi(codeText)
	if err != nil || len(codeText) != 3 {
		return 0, line, fmt.Errorf("malformed status line: %q", line)
	}
	return code, line, nil
}

// codeSet is a bitmap over 100-599, the only codes a status line can carry.
type codeSet [600]bool

func (s *codeSet) contains(code int) bool {
	return code >= 0 && code < len(s) && s[code]
}

func parseCodes(spec string) (codeSet, error) {
	var set codeSet
	parse := func(s string) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 100 || n > 599 {
			return 0, fmt.Errorf("-http-codes: %q is not a status code between 100 and 599", s)
		}
		return n, nil
	}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		start, err := parse(lo)
		if err != nil {
			return set, err
		}
		end := start
		if isRange {
			if end, err = parse(hi); err != nil {
				return set, err
			}
			if end < start {
				return set, fmt.Errorf("-http-codes: range %q is reversed", part)
			}
		}
		for c := start; c <= end; c++ {
			set[c] = true
		}
	}
	return set, nil
}
