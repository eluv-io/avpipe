//go:build !linux

package transport

import (
	"net"
)

func setPlatformOptions(conn *net.UDPConn) error {
	return nil
}
