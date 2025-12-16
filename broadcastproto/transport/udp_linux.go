//go:build linux

package transport

import (
	"net"

	"golang.org/x/sys/unix"
)

func setPlatformOptions(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	raw.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0)
	})
	return sockErr
}
