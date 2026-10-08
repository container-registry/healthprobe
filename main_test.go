package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVersionDefault(t *testing.T) {
	// Guards the -ldflags contract: the variable must exist and be settable.
	if version == "" {
		t.Error("version must never be empty; it defaults to \"dev\"")
	}
}

// probeArgs points the probe at the listener behind rawURL.
func probeArgs(t *testing.T, rawURL string, extra ...string) []string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{"-host", u.Hostname(), "-port", u.Port()}, extra...)
}

func runProbe(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func TestStatusCodes(t *testing.T) {
	var (
		mu                      sync.Mutex
		gotPath, gotHost, gotUA string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotHost, gotUA = r.URL.RequestURI(), r.Host, r.UserAgent()
		mu.Unlock()
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/nocontent":
			w.WriteHeader(http.StatusNoContent)
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/down":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"200 is healthy", []string{"-endpoint", "/ok"}, exitHealthy, ""},
		{"endpoint without slash", []string{"-endpoint", "ok"}, exitHealthy, ""},
		{"204 is healthy", []string{"-endpoint", "/nocontent"}, exitHealthy, ""},
		{"503 is unhealthy", []string{"-endpoint", "/down"}, exitUnhealthy, "503"},
		{"404 is unhealthy", []string{"-endpoint", "/missing"}, exitUnhealthy, "404"},
		{"redirect is not followed", []string{"-endpoint", "/redirect"}, exitUnhealthy, "302"},
		{"redirect accepted by -http-codes", []string{"-endpoint", "/redirect", "-http-codes", "200-299,302"}, exitHealthy, ""},
		{"503 accepted by -http-codes", []string{"-endpoint", "/down", "-http-codes", "503"}, exitHealthy, ""},
		{"lprobe-style flags", []string{"-mode=http", "-endpoint=/ok", "-connect-timeout", "2s"}, exitHealthy, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runProbe(t, probeArgs(t, srv.URL, tc.args...)...)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d; output: %s", code, tc.wantCode, out)
			}
			if tc.wantOut != "" && !strings.Contains(out, tc.wantOut) {
				t.Errorf("output %q does not mention %q", out, tc.wantOut)
			}
		})
	}

	_, _ = runProbe(t, probeArgs(t, srv.URL, "-endpoint", "/ok?full=1")...)
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/ok?full=1" {
		t.Errorf("request URI = %q, want the query string preserved", gotPath)
	}
	if !strings.HasPrefix(gotHost, "localhost:") {
		t.Errorf("Host = %q, want localhost:<port> for a loopback target", gotHost)
	}
	if gotUA != "healthprobe/"+version {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

func TestConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	code, out := runProbe(t, probeArgs(t, "http://"+addr)...)
	if code != exitUnhealthy || !strings.Contains(out, "refused") {
		t.Fatalf("exit = %d, output %q; want 1 and a refused connection", code, out)
	}
}

// A server that accepts and then never answers is the failure a deadline exists
// for: without one the probe hangs until the runtime kills it.
func TestHungServerTimesOut(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }()
		}
	}()

	start := time.Now()
	code, out := runProbe(t, probeArgs(t, "http://"+l.Addr().String(), "-timeout", "300ms")...)
	elapsed := time.Since(start)
	if code != exitUnhealthy || !strings.Contains(out, "timeout") {
		t.Fatalf("exit = %d, output %q; want 1 and a timeout", code, out)
	}
	if elapsed > 2*time.Second {
		t.Errorf("probe took %v, the 300ms deadline was not enforced", elapsed)
	}
}

func TestMalformedResponses(t *testing.T) {
	cases := map[string]string{
		"not HTTP":           "SSH-2.0-OpenSSH_9.6\r\n",
		"HTTP/2 preface":     "HTTP/2 200\r\n",
		"no status code":     "HTTP/1.1 OK\r\n",
		"four-digit status":  "HTTP/1.1 2000 OK\r\n",
		"closed immediately": "",
		"no line end":        "HTTP/1.1 200 OK",
		"oversized line":     "HTTP/1.1 200 " + strings.Repeat("x", 2*maxStatusLine) + "\r\n",
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = l.Close() }()
			go func() {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_, _ = c.Write([]byte(reply))
				_ = c.Close()
			}()
			code, out := runProbe(t, probeArgs(t, "http://"+l.Addr().String(), "-timeout", "2s")...)
			if code != exitUnhealthy {
				t.Fatalf("exit = %d, want 1 for %q; output: %s", code, name, out)
			}
		})
	}
}

func TestIPv6(t *testing.T) {
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback:", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Listener = l
	srv.Start()
	defer srv.Close()

	_, port, _ := net.SplitHostPort(l.Addr().String())
	if code, out := runProbe(t, "-ipv6", "-port", port); code != exitHealthy {
		t.Fatalf("exit = %d; output: %s", code, out)
	}
}

func TestTLS(t *testing.T) {
	if !tlsSupported {
		t.Skip("built with -tags notls")
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"self-signed fails verification", []string{"-tls"}, exitUnhealthy, "certificate"},
		{"-tls-no-verify accepts it", []string{"-tls", "-tls-no-verify"}, exitHealthy, ""},
		{"-tls-ca-cert verifies the IP SAN", []string{"-tls", "-tls-ca-cert", caFile}, exitHealthy, ""},
		{"-tls-server-name is checked", []string{"-tls", "-tls-ca-cert", caFile, "-tls-server-name", "wrong.example"}, exitUnhealthy, "wrong.example"},
		{"plain HTTP against TLS fails", nil, exitUnhealthy, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runProbe(t, probeArgs(t, srv.URL, append(tc.args, "-timeout", "2s")...)...)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d; output: %s", code, tc.wantCode, out)
			}
			if tc.wantOut != "" && !strings.Contains(out, tc.wantOut) {
				t.Errorf("output %q does not mention %q", out, tc.wantOut)
			}
		})
	}
}

func TestTLSAgainstPlainHTTP(t *testing.T) {
	if !tlsSupported {
		t.Skip("built with -tags notls")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	code, out := runProbe(t, probeArgs(t, srv.URL, "-tls", "-tls-no-verify", "-timeout", "2s")...)
	if code != exitUnhealthy || !strings.Contains(out, "TLS handshake") {
		t.Fatalf("exit = %d, output %q; want a failed handshake", code, out)
	}
}

func TestTLSUnavailableWithoutSupport(t *testing.T) {
	if tlsSupported {
		t.Skip("only meaningful with -tags notls")
	}
	code, out := runProbe(t, "-tls")
	if code != exitUnhealthy || !strings.Contains(out, "without TLS") {
		t.Fatalf("exit = %d, output %q", code, out)
	}
}

func TestFlagValidation(t *testing.T) {
	cases := map[string][]string{
		"grpc mode":                {"-mode", "grpc"},
		"unknown lprobe grpc flag": {"-service", "foo"},
		"port zero":                {"-port", "0"},
		"port too high":            {"-port", "70000"},
		"zero timeout":             {"-timeout", "0s"},
		"negative timeout":         {"-connect-timeout", "-1s"},
		"no-verify without tls":    {"-tls-no-verify"},
		"ca without tls":           {"-tls-ca-cert", "/x"},
		"no-verify with ca":        {"-tls", "-tls-no-verify", "-tls-ca-cert", "/x"},
		"ipv6 with host":           {"-ipv6", "-host", "10.0.0.1"},
		"empty host":               {"-host", ""},
		"code out of range":        {"-http-codes", "99"},
		"code not a number":        {"-http-codes", "2xx"},
		"reversed range":           {"-http-codes", "299-200"},
		"endpoint with space":      {"-endpoint", "/a b"},
		"endpoint with CRLF":       {"-endpoint", "/a\r\nX-Injected: 1"},
		"user-agent with CRLF":     {"-user-agent", "a\r\nX-Injected: 1"},
		"positional argument":      {"http://localhost:8080/"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code, out := runProbe(t, args...); code != exitUnhealthy {
				t.Fatalf("exit = %d, want 1; output: %s", code, out)
			}
		})
	}
}

func TestVersionAndHelp(t *testing.T) {
	code, out := runProbe(t, "-version")
	if code != exitHealthy || strings.TrimSpace(out) != version {
		t.Errorf("-version: exit %d, output %q", code, out)
	}
	if code, _ := runProbe(t, "-h"); code != exitHealthy {
		t.Errorf("-h: exit %d, want 0", code)
	}
}

func TestParseCodes(t *testing.T) {
	set, err := parseCodes("200, 204,300-302")
	if err != nil {
		t.Fatal(err)
	}
	for code, want := range map[int]bool{199: false, 200: true, 201: false, 204: true, 299: false, 300: true, 302: true, 303: false, 1000: false, -1: false} {
		if got := set.contains(code); got != want {
			t.Errorf("contains(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestHostHeader(t *testing.T) {
	cases := []struct {
		cfg  config
		want string
	}{
		{config{host: "127.0.0.1", port: 8080}, "localhost:8080"},
		{config{host: "::1", port: 8080}, "localhost:8080"},
		{config{host: "127.0.0.1", port: 80}, "localhost"},
		{config{host: "127.0.0.1", port: 443, tls: true}, "localhost"},
		{config{host: "10.1.2.3", port: 9000}, "10.1.2.3:9000"},
		{config{host: "fd00::1", port: 9000}, "[fd00::1]:9000"},
		{config{host: "127.0.0.1", port: 8443, tls: true, tlsServerName: "core.harbor"}, "core.harbor"},
	}
	for _, tc := range cases {
		if got := hostHeader(tc.cfg); got != tc.want {
			t.Errorf("hostHeader(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

// Keeps the test binary's TLS client honest about what it accepts: the probe
// must refuse anything below TLS 1.2.
func TestTLSMinimumVersion(t *testing.T) {
	if !tlsSupported {
		t.Skip("built with -tags notls")
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}
	srv.StartTLS()
	defer srv.Close()
	code, out := runProbe(t, probeArgs(t, srv.URL, "-tls", "-tls-no-verify", "-timeout", "2s")...)
	if code != exitUnhealthy {
		t.Fatalf("exit = %d, want 1 against a TLS 1.1 server; output: %s", code, out)
	}
}

// rawServer answers every connection with reply, verbatim.
func rawServer(t *testing.T, reply string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				// Drain the request first: closing with unread input sends RST,
				// which can discard the reply before the probe reads it.
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				_, _ = c.Write([]byte(reply))
			}()
		}
	}()
	return "http://" + l.Addr().String()
}

func TestInterimResponses(t *testing.T) {
	hints := "HTTP/1.1 103 Early Hints\r\nLink: </style.css>; rel=preload\r\n\r\n"
	cases := []struct {
		name     string
		reply    string
		wantCode int
	}{
		{"103 then 200", hints + "HTTP/1.1 200 OK\r\n\r\n", exitHealthy},
		{"two interim then 204", "HTTP/1.1 100 Continue\r\n\r\n" + hints + "HTTP/1.1 204 No Content\r\n\r\n", exitHealthy},
		{"103 then 503", hints + "HTTP/1.1 503 Service Unavailable\r\n\r\n", exitUnhealthy},
		{"101 is final", "HTTP/1.1 101 Switching Protocols\r\n\r\n", exitUnhealthy},
		{"endless interim", strings.Repeat("HTTP/1.1 100 Continue\r\n\r\n", maxInterim+1) + "HTTP/1.1 200 OK\r\n\r\n", exitUnhealthy},
		{"interim without final", hints, exitUnhealthy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runProbe(t, probeArgs(t, rawServer(t, tc.reply), "-timeout", "2s")...)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d; output: %s", code, tc.wantCode, out)
			}
		})
	}
}

// SNI selects the certificate on routed listeners, so -tls-no-verify must turn
// off verification without also dropping the server name.
func TestTLSNoVerifySendsSNI(t *testing.T) {
	if !tlsSupported {
		t.Skip("built with -tags notls")
	}
	sni := make(chan string, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		sni <- hello.ServerName
		return nil, nil
	}}
	srv.StartTLS()
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	code, out := runProbe(t, "-host", "localhost", "-port", u.Port(), "-tls", "-tls-no-verify", "-timeout", "2s")
	if code != exitHealthy {
		t.Fatalf("exit = %d; output: %s", code, out)
	}
	if got := <-sni; got != "localhost" {
		t.Errorf("SNI = %q, want localhost", got)
	}
}
