//go:build !linux

package transport

import (
	"net"
)

// FIONREAD constant for platforms where it's not in unix package (Darwin value)
const FIONREAD = 0x4004667f

func setPlatformOptions(conn *net.UDPConn) error {
	return nil
}
