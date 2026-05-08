package transport

import (
	"net"
	"strings"
	"testing"
)

func TestResolveUDPAddr(t *testing.T) {
	addrs := []string{
		"udp://239.255.255.11:1234", // has udp prefix
		"239.255.255.11:1234",       // no udp prefix
	}

	for _, addr := range addrs {
		host := strings.TrimPrefix(addr, "udp://")
		_, err := net.ResolveUDPAddr("udp", host)
		if err != nil {
			t.Errorf("Failed to resolve address %s: %v", addr, err)
		}
	}
}
