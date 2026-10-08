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
		ServerName:         cfg.tlsServerName,
		InsecureSkipVerify: cfg.tlsNoVerify, //nolint:gosec // explicit user opt-in
		MinVersion:         tls.VersionTLS12,
	}
	if tc.ServerName == "" {
		// Also the SNI value, so it is set with -tls-no-verify too.
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
