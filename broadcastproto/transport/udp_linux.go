//go:build linux

package transport

import (
	"net"

	"golang.org/x/sys/unix"
)

// FIONREAD ioctl constant for Linux
const FIONREAD = 0x541B

func setPlatformOptions(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	err = raw.Control(func(fd uintptr) {
		// Disable IP_MULTICAST_ALL to only receive multicast for joined groups
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0)
		if sockErr != nil {
			return
		}
		// Enable SO_RXQ_OVFL to track dropped packets
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RXQ_OVFL, 1)
	})
	if sockErr != nil {
		return sockErr // Return the most specific error
	}
	if err != nil {
		return err
	}
	return nil
}
