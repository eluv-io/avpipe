package transport

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	elog "github.com/eluv-io/log-go"
)

const UDP_READ_BUFFER_SIZE = 10 * 1024 * 1024

const (
	PacketSize = 188
	SyncByte   = 0x47
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

func (u *udpProto) PackagingMode() TsPackagingMode {
	return RawTs
}

func (u *udpProto) Open() (io.ReadCloser, error) {
	liveUrl, err := ParseLiveUrl(u.Url)
	if err != nil {
		return nil, err
	}

	var conn *net.UDPConn
	if liveUrl.Multicast {
		var ifi *net.Interface
		if liveUrl.LocalAddr != nil {
			ifi, err = interfaceByIP(liveUrl.LocalAddr)
			if err != nil {
				return nil, err
			}
		}
		conn, err = net.ListenMulticastUDP("udp", ifi, liveUrl.Group)
		if err != nil {
			return nil, err
		}
		log.Debug("Listening on UDP multicast", "group", liveUrl.Group, "localaddr", liveUrl.LocalAddr, "inteface", ifi)
	} else {
		bindAddr := liveUrl.Addr
		if liveUrl.LocalAddr != nil {
			bindAddr = &net.UDPAddr{
				IP:   liveUrl.LocalAddr,
				Port: liveUrl.Port,
			}
		}
		conn, err = net.ListenUDP("udp", bindAddr)
		if err != nil {
			return nil, err
		}
		log.Debug("Listening on UDP address", "addr", bindAddr)
	}

	u.Conn = conn
	return conn, nil
}

// LiveUrl represents a UDP live stream (unicast or multicast)
type LiveUrl struct {
	Scheme    string
	Host      string       // URL host (may be domain name or IP address)
	Addr      *net.UDPAddr // URL address ()
	Group     *net.UDPAddr
	Multicast bool
	Port      int
	LocalAddr net.IP   // URL bind address if specified via query param 'localaddr'
	Sources   []net.IP // Optional multicast sources (query param 'sources')
	Reuse     bool
}

// ParseLiveUrl parses live stream URLs into their components and resolves host and interfaces.
// Example:
// udp://host-100-10-10-1.contentfabric.io:11001
// udp://232.1.2.3:1234?localaddr=172.16.1.10&sources=10.0.0.5,10.0.0.6&reuse=1
func ParseLiveUrl(urlStr string) (*LiveUrl, error) {
	out := &LiveUrl{}

	u, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	out.Scheme = u.Scheme

	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid host:port in URL (%s): %w", u.Host, err)
	}
	out.Host = host

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port %s: %w", portStr, err)
	}
	out.Port = port

	ip := net.ParseIP(host)
	if ip == nil {
		addrs, lookupErr := net.LookupIP(host)
		if lookupErr != nil {
			return nil, fmt.Errorf("unable to resolve host '%s': %w", host, lookupErr)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("unable to resolve host '%s': no addresses returned", host)
		}
		for _, candidate := range addrs {
			if candidate.To4() != nil {
				ip = candidate
				break
			}
		}
		if ip == nil {
			ip = addrs[0]
		}
	}
	if ip == nil {
		return nil, fmt.Errorf("unable to determine IP for host '%s'", host)
	}
	out.Addr = &net.UDPAddr{IP: ip, Port: out.Port}
	if ip.IsMulticast() {
		out.Multicast = true
		out.Group = &net.UDPAddr{IP: ip, Port: out.Port}
	}

	// Parse query parameters
	q := u.Query()

	if la := q.Get("localaddr"); la != "" {
		laIp := net.ParseIP(la)
		if laIp == nil {
			return nil, fmt.Errorf("localaddr is not a valid IP: %s", la)
		}
		out.LocalAddr = laIp
	}

	if srcs := q.Get("sources"); srcs != "" {
		for _, s := range strings.Split(srcs, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			ip := net.ParseIP(s)
			if ip == nil {
				return nil, fmt.Errorf("invalid source IP: %s", s)
			}
			out.Sources = append(out.Sources, ip)
		}
	}

	if q.Get("reuse") == "1" {
		out.Reuse = true
	}

	return out, nil
}

func interfaceByIP(ip net.IP) (*net.Interface, error) {
	if ip == nil {
		return nil, nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("unable to list interfaces: %w", err)
	}
	for i := range ifaces {
		iface := &ifaces[i]
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ifaceIP net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ifaceIP = v.IP
			case *net.IPAddr:
				ifaceIP = v.IP
			}
			if ifaceIP != nil && ifaceIP.Equal(ip) {
				return iface, nil
			}
		}
	}
	return nil, fmt.Errorf("no interface found with IP %s", ip)
}
