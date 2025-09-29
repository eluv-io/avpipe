package transport

import (
	"net"
	"testing"
)

func TestResolveUDPAddr(t *testing.T) {
	addrs := []string{
		"udp://239.255.255.11:1234", // has udp prefix
		"239.255.255.11:1234",       // no udp prefix
	}

	for _, addr := range addrs {
		_, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			t.Errorf("Failed to resolve address %s: %v", addr, err)
		}
	}
}
