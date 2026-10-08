//go:build notls

package main

import (
	"errors"
	"net"
)

// The notls build leaves crypto/tls and crypto/x509 out of the binary, which
// halves its size for the common case of a plain-HTTP health endpoint.
const tlsSupported = false

func wrapTLS(net.Conn, config) (net.Conn, error) {
	return nil, errors.New("built without TLS support")
}
