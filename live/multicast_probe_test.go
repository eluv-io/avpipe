package live

import (
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/ipv4"
)

// TestProbeTSMulticast is the end-to-end test for UDP multicast with an
// empty input_cfg.
// udp:// exercises avpipe's legacy C UDP reader
// rtp:// exercises the libavformat path (which further rewrites the URL to udp://)
func TestProbeTSMulticast(t *testing.T) {
	setupLogging()

	for i, scheme := range []string{"udp", "rtp"} {
		t.Run(scheme, func(t *testing.T) {
			port := freeUDPPort(t)
			group := net.ParseIP(fmt.Sprintf("239.255.43.%d", i+1))
			url := fmt.Sprintf("%s://%s:%d?localaddr=127.0.0.1", scheme, group, port)

			stop := startLoopbackMulticastTS(t, scheme, &net.UDPAddr{IP: group, Port: port})
			defer stop()

			params := &goavpipe.XcParams{
				Seekable:          false,
				XcType:            goavpipe.Xcprobe,
				StreamId:          -1,
				Url:               url,
				ConnectionTimeout: 5,
			}
			putReqCtxByURL(url, &testCtx{url: url})
			goavpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

			probeInfo, err := probeWithRetry(t, params, 2)
			requireProbe(t, probeInfo, err, 2)
		})
	}
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func loopbackInterface(t *testing.T) *net.Interface {
	t.Helper()
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for i := range ifaces {
		addrs, err := ifaces[i].Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.Equal(net.ParseIP("127.0.0.1")) {
				return &ifaces[i]
			}
		}
	}
	t.Fatal("loopback interface not found")
	return nil
}

func startLoopbackMulticastTS(t *testing.T, scheme string, group *net.UDPAddr) func() {
	t.Helper()

	source, err := os.Open("../media/BBB4_HD_51_AVC_120s_CCBYblendercloud.ts")
	require.NoError(t, err)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	packetConn := ipv4.NewPacketConn(conn)
	require.NoError(t, packetConn.SetMulticastInterface(loopbackInterface(t)))
	require.NoError(t, packetConn.SetMulticastLoopback(true))

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer source.Close()
		defer conn.Close()

		payload := make([]byte, 7*188)
		var sequence uint16
		for {
			select {
			case <-stop:
				return
			default:
			}

			if _, err := io.ReadFull(source, payload); err != nil {
				if _, seekErr := source.Seek(0, io.SeekStart); seekErr != nil {
					return
				}
				continue
			}

			packet := payload
			if scheme == "rtp" {
				rtpPacket := rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    33,
						SequenceNumber: sequence,
						Timestamp:      uint32(sequence) * 3600,
					},
					Payload: payload,
				}
				marshaled, marshalErr := rtpPacket.Marshal()
				if marshalErr != nil {
					return
				}
				packet = marshaled
				sequence++
			}

			if _, err := packetConn.WriteTo(packet, nil, group); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}
