package transport

import (
	"fmt"
	"io"
	"net"
	"testing"

	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/common-go/util/testutil"
)

func TestRtpHandlerStripsRTPHeader(t *testing.T) {
	rc, port := openRTPHandler(t, RawTs) // RawTs strips: rtpHandler must deliver the bare TS payload
	defer rc.Close()

	tsPkt := mustTestTSPacket(7)
	sendUDPDatagram(t, port, mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, tsPkt))

	buf := make([]byte, 1500)
	n, err := rc.Read(buf)
	require.NoError(t, err)
	require.Equal(t, tsPkt, buf[:n])
}

func TestRtpHandlerExcludesRTPPadding(t *testing.T) {
	rc, port := openRTPHandler(t, RawTs)
	defer rc.Close()

	tsPkt := mustTestTSPacket(7)
	hdr := pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1, Padding: true, PaddingSize: 200}
	sendUDPDatagram(t, port, mustRTPDatagram(t, hdr, tsPkt))

	buf := make([]byte, 1500)
	n, err := rc.Read(buf)
	require.NoError(t, err)
	require.Equal(t, tsPkt, buf[:n], "padding must not be delivered as part of the stripped payload")
}

func TestRtpHandlerDropsADatagramThatIsNotRTP(t *testing.T) {
	for _, bad := range [][]byte{
		{0x01, 0x02, 0x03}, // too short to be a valid RTP header
		mustRTPDatagram(t, pionrtp.Header{Version: 1, SequenceNumber: 1, Timestamp: 1}, mustTestTSPacket(7)),
		mustTestTSPacket(7), // plain MPEG-TS
	} {
		rc, port := openRTPHandler(t, RawTs)

		tsPkt := mustTestTSPacket(42)
		sendUDPDatagram(t, port, bad)
		sendUDPDatagram(t, port, mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 2, Timestamp: 2}, tsPkt))

		buf := make([]byte, 1500)
		n, err := rc.Read(buf)
		require.NoError(t, err)
		require.Equal(t, tsPkt, buf[:n], "the datagram that is not RTP is dropped, the next one delivered")
		require.NoError(t, rc.Close())
	}
}

func TestRtpHandlerFailsASourceThatDoesNotSendRTP(t *testing.T) {
	for _, packaging := range []TsPackagingMode{RawTs, AtsTs, RtpTs} {
		t.Run(string(packaging), func(t *testing.T) {
			rc, port := openRTPHandler(t, packaging)
			defer rc.Close()
			rc.(*rtpHandler).guard.max = 3

			tsPkt := mustTestTSPacket(7)
			for range 3 {
				sendUDPDatagram(t, port, tsPkt)
			}

			buf := make([]byte, 1500)
			if packaging == RtpTs {
				// RtpTs passes on what it receives, so that the stream's statistics count it, until the guard trips.
				for range 2 {
					n, err := rc.Read(buf)
					require.NoError(t, err)
					require.Equal(t, tsPkt, buf[:n])
				}
			}
			_, err := rc.Read(buf)
			require.ErrorIs(t, err, ErrNotRtp)
		})
	}
}

func TestRtpHandlerResetsTheGuardOnRTP(t *testing.T) {
	rc, port := openRTPHandler(t, RawTs)
	defer rc.Close()
	rc.(*rtpHandler).guard.max = 3

	tsPktNoRtp := mustTestTSPacket(7)
	tsPkt := mustTestTSPacket(7)
	for i := range 3 {
		sendUDPDatagram(t, port, tsPktNoRtp)
		sendUDPDatagram(t, port, tsPktNoRtp)
		sendUDPDatagram(t, port, mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: uint16(i)}, tsPkt))

		buf := make([]byte, 1500)
		n, err := rc.Read(buf)
		require.NoError(t, err, "two datagrams that are not RTP stay below the limit, round %d", i)
		require.Equal(t, tsPkt, buf[:n])
	}
}

// TestRtpStrippingAllocatesNothing checks that stripping the RTP header of a datagram, per datagram on the ingest path,
// allocates nothing in either the RTP handler or the RTP decapsulator.
func TestRtpStrippingAllocatesNothing(t *testing.T) {
	datagram := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, mustTestTSPacket(7))

	h := newTestStripHandler()
	require.Zero(t, testing.AllocsPerRun(100, func() {
		h.bufEnd = copy(h.buf, datagram)
		_, _ = h.prepare()
	}), "rtpHandler")

	fake := &fakeReadCloser{data: datagram}
	dec := newTestRtpDecapsulator(fake)
	buf := make([]byte, 1500)
	require.Zero(t, testing.AllocsPerRun(100, func() {
		fake.read = false
		_, _ = dec.Read(buf)
	}), "RtpDecapsulator")
}

func TestRtpHandlerRtpTsPassthroughDoesNotStrip(t *testing.T) {
	rc, port := openRTPHandler(t, RtpTs) // RtpTs retains RTP framing: no stripping should happen
	defer rc.Close()

	tsPkt := mustTestTSPacket(7)
	datagram := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, tsPkt)
	sendUDPDatagram(t, port, datagram)

	buf := make([]byte, 1500)
	n, err := rc.Read(buf)
	require.NoError(t, err)
	require.Equal(t, datagram, buf[:n])
}

func TestRtpHandlerStripsAcrossRepeatedReads(t *testing.T) {
	// The scratch packet used for stripping is reused across reads via repeated From() calls; confirm this actually
	// works for more than one datagram, not just the first.
	rc, port := openRTPHandler(t, RawTs)
	defer rc.Close()

	for i, pid := range []int{7, 42, 100} {
		tsPkt := mustTestTSPacket(pid)
		sendUDPDatagram(t, port, mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: uint16(i), Timestamp: uint32(i)}, tsPkt))

		buf := make([]byte, 1500)
		n, err := rc.Read(buf)
		require.NoError(t, err)
		require.Equal(t, tsPkt, buf[:n], "datagram %d", i)
	}
}

// newTestStripHandler returns an rtpHandler that strips the RTP header (RawTs) and has no socket, for driving prepare
// directly on a datagram copied into its buf.
func newTestStripHandler() *rtpHandler {
	return &rtpHandler{buf: make([]byte, maxUDPPacketSize), Mode: RawTs, stripPkt: pktpool.NewPacket(0, maxUDPPacketSize)}
}

// openRTPHandler opens an RTP transport listening on a free loopback port with the given output packaging, returning
// the resulting io.ReadCloser (an *rtpHandler) and the port to send test datagrams to.
func openRTPHandler(t *testing.T, packaging TsPackagingMode) (io.ReadCloser, int) {
	t.Helper()
	port, err := testutil.FreePort()
	require.NoError(t, err)

	tp := NewRTPTransport(fmt.Sprintf("udp://127.0.0.1:%d", port), packaging)
	rc, err := tp.Open()
	require.NoError(t, err)

	return rc, port
}

// sendUDPDatagram sends data as a single UDP datagram to 127.0.0.1:port.
func sendUDPDatagram(t *testing.T, port int, data []byte) {
	t.Helper()
	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write(data)
	require.NoError(t, err)
}

// mustRTPDatagram marshals an RTP packet with the given header and payload into raw wire bytes.
func mustRTPDatagram(t *testing.T, hdr pionrtp.Header, payload []byte) []byte {
	t.Helper()
	pkt := pionrtp.Packet{Header: hdr, Payload: payload}
	data, err := pkt.Marshal()
	require.NoError(t, err)
	return data
}

// mustTestTSPacket returns a well-formed, single 188-byte TS packet (valid sync byte, given PID, no payload content
// beyond zero-fill) usable as an RTP payload.
func mustTestTSPacket(pid int) []byte {
	pkt := make([]byte, 188)
	pkt[0] = 0x47 // sync byte
	pkt[1] = byte(pid >> 8 & 0x1f)
	pkt[2] = byte(pid)
	return pkt
}
