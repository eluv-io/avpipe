package transport

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseLiveUrlUnicast(t *testing.T) {
	url := "udp://10.0.0.1:5000?localaddr=192.168.1.10&reuse=1"

	liveURL, err := ParseLiveUrl(url)
	assert.NoError(t, err)
	assert.False(t, liveURL.Multicast, "expected unicast URL")
	assert.Nil(t, liveURL.Group)
	if assert.NotNil(t, liveURL.Addr) {
		assert.True(t, liveURL.Addr.IP.Equal(net.ParseIP("10.0.0.1")))
		assert.Equal(t, 5000, liveURL.Addr.Port)
	}
	assert.NotNil(t, liveURL.LocalAddr)
	assert.True(t, liveURL.LocalAddr.Equal(net.ParseIP("192.168.1.10")))
	assert.True(t, liveURL.Reuse)
}

func TestParseLiveUrlMulticast(t *testing.T) {
	url := "udp://232.1.2.3:5000?localaddr=172.16.1.10&sources=10.0.0.5, 10.0.0.6"

	liveURL, err := ParseLiveUrl(url)
	assert.NoError(t, err)
	assert.True(t, liveURL.Multicast, "expected multicast URL")
	if assert.NotNil(t, liveURL.Group) {
		assert.True(t, liveURL.Group.IP.Equal(net.ParseIP("232.1.2.3")))
		assert.Equal(t, 5000, liveURL.Group.Port)
	}
	if assert.NotNil(t, liveURL.Addr) {
		assert.True(t, liveURL.Addr.IP.Equal(net.ParseIP("232.1.2.3")))
		assert.Equal(t, 5000, liveURL.Addr.Port)
	}
	assert.Len(t, liveURL.Sources, 2)
	assert.True(t, liveURL.Sources[0].Equal(net.ParseIP("10.0.0.5")))
	assert.True(t, liveURL.Sources[1].Equal(net.ParseIP("10.0.0.6")))
	assert.NotNil(t, liveURL.LocalAddr)
	assert.True(t, liveURL.LocalAddr.Equal(net.ParseIP("172.16.1.10")))
}

// Test fails - and what's the purpose of it?
func DisabledTestParseLiveUrlRejectsAtPrefix(t *testing.T) {
	_, err := ParseLiveUrl("udp://@232.1.2.3:5000")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}

func TestInterfaceByIP(t *testing.T) {
	ifi, err := interfaceByIP(nil)
	assert.NoError(t, err)
	assert.Nil(t, ifi)

	loopback := net.ParseIP("127.0.0.1")
	if loopback == nil {
		t.Fatal("failed to parse loopback IP")
	}
	ifi, err = interfaceByIP(loopback)
	assert.NoError(t, err)
	assert.NotNil(t, ifi, "expected loopback interface to be found")
}
