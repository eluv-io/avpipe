//go:build linux

package transport

import (
	"net"
	"syscall"
)

func setPlatformOptions(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_ALL, 0)
	})
	return sockErr
}
