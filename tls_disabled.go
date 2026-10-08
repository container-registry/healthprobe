//go:build notls

package main

import (
	"errors"
	"net"
)

const tlsSupported = false

func wrapTLS(net.Conn, config) (net.Conn, error) {
	return nil, errors.New("built without TLS support")
}
