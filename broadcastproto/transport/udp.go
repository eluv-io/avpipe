package transport

import (
	"io"
	"net"
	"strings"

	elog "github.com/eluv-io/log-go"
)

const UDP_READ_BUFFER_SIZE = 10 * 1024 * 1024

var log = elog.Get("avpipe/broadcastproto/transport")

var _ Transport = (*udpProto)(nil)

// udpProto implements the Transport interface for UDP connections.
type udpProto struct {
	Url string
}

func NewUDPTransport(url string) Transport {
	return &udpProto{Url: url}
}

func (u *udpProto) URL() string {
	return u.Url
}

func (u *udpProto) Handler() string {
	return "udp"
}

func (u *udpProto) Open() (io.ReadCloser, error) {
	addr, err := net.ResolveUDPAddr("udp", u.stripLeadingProto(u.Url))
	if err != nil {
		return nil, err
	}

	var conn *net.UDPConn
	if addr.IP.IsMulticast() {
		conn, err = net.ListenMulticastUDP("udp", nil, addr)
		if err != nil {
			return nil, err
		}
		log.Debug("Listening on UDP multicast address", "addr", addr)
	} else {
		conn, err = net.ListenUDP("udp", addr)
		if err != nil {
			return nil, err
		}
		log.Debug("Listening on UDP address", "addr", addr)
	}

	return conn, nil
}

func (u *udpProto) stripLeadingProto(url string) string {
	s := url
	for _, prefix := range []string{"udp://", "rtp://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	return s
}
