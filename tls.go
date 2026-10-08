//go:build !notls

package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
)

const tlsSupported = true

func wrapTLS(conn net.Conn, cfg config) (net.Conn, error) {
	tc := &tls.Config{
		ServerName: cfg.tlsServerName,
		// Probes target a local listener that commonly holds a self-signed or
		// internal-CA certificate; -tls-no-verify is the documented opt-out.
		InsecureSkipVerify: cfg.tlsNoVerify, //nolint:gosec // explicit user opt-in
		MinVersion:         tls.VersionTLS12,
	}
	if tc.ServerName == "" {
		// Set even with -tls-no-verify, because it is also the SNI value an
		// SNI-routed listener selects its certificate by. Go sends no SNI for an
		// IP literal and verifies one against the certificate's IP SANs, so the
		// host is passed through unchanged; an IPv6 literal must not be bracketed.
		tc.ServerName = cfg.host
	}
	if cfg.tlsCACert != "" {
		pem, err := os.ReadFile(cfg.tlsCACert)
		if err != nil {
			return nil, fmt.Errorf("reading -tls-ca-cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("-tls-ca-cert: no PEM certificates found")
		}
		tc.RootCAs = pool
	}
	c := tls.Client(conn, tc)
	if err := c.Handshake(); err != nil {
		return nil, fmt.Errorf("TLS handshake: %w", err)
	}
	return c, nil
}
