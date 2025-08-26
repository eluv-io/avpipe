package transport

import (
	"fmt"
	"io"
	"net"
	"strings"

	elog "github.com/eluv-io/log-go"
)

const UDP_READ_BUFFER_SIZE = 10 * 1024 * 1024

const (
	PacketSize = 188
	SyncByte   = 0x47
	outSock    = "/tmp/elv_sock_jxs"
)

var log = elog.Get("avpipe/broadcastproto/transport")

var _ Transport = (*udpProto)(nil)

// udpProto implements the Transport interface for UDP connections.
type udpProto struct {
	Url string

	Conn *net.UDPConn
}

func NewUDPTransport(url string) Transport {
	log.Debug("Creating new UDP transport", "url", url)
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
		fmt.Println("Listening on UDP multicast address", "addr", addr)

	} else {
		conn, err = net.ListenUDP("udp", addr)
		if err != nil {
			return nil, err
		}
		log.Debug("Listening on UDP address", "addr", addr)
		fmt.Println("Listening on UDP address", "addr", addr)
	}

	u.Conn = conn
	return conn, nil
}

func (u *udpProto) stripLeadingProto(url string) string {
	s := url
	for _, prefix := range []string{"udp://", "rtp://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	return s
}
