package transport

import (
	"io"
	"testing"

	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	mio "github.com/eluv-io/common-go/media/io"
	"github.com/eluv-io/common-go/media/pktpool"
)

func TestRtpDecapsulatorStripsRTPHeader(t *testing.T) {
	tsPkt := mustTestTSPacket(7)
	datagram := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, tsPkt)
	dec := newTestRtpDecapsulator(&fakeReadCloser{data: datagram})

	buf := make([]byte, 1500)
	n, err := dec.Read(buf)

	require.NoError(t, err)
	require.Equal(t, tsPkt, buf[:n])
}

func TestRtpDecapsulatorExcludesRTPPadding(t *testing.T) {
	tsPkt := mustTestTSPacket(7)
	hdr := pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1, Padding: true, PaddingSize: 200}
	datagram := mustRTPDatagram(t, hdr, tsPkt)
	dec := newTestRtpDecapsulator(&fakeReadCloser{data: datagram})

	buf := make([]byte, 1500)
	n, err := dec.Read(buf)

	require.NoError(t, err)
	require.Equal(t, tsPkt, buf[:n])
}

func TestRtpDecapsulatorDropsADatagramThatIsNotRTP(t *testing.T) {
	tsPkt := mustTestTSPacket(42)
	good := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 2, Timestamp: 2}, tsPkt)
	for _, bad := range [][]byte{
		{0x01, 0x02, 0x03}, // too short to be a valid RTP header
		mustRTPDatagram(t, pionrtp.Header{Version: 1, SequenceNumber: 1, Timestamp: 1}, mustTestTSPacket(7)),
		mustTestTSPacket(7), // plain MPEG-TS
	} {
		dec := newTestRtpDecapsulator(&datagramsReadCloser{datagrams: [][]byte{bad, good}})

		buf := make([]byte, 1500)
		n, err := dec.Read(buf)
		require.NoError(t, err)
		require.Equal(t, tsPkt, buf[:n], "the datagram that is not RTP is dropped, the next one delivered")
	}
}

func TestRtpDecapsulatorFailsASourceThatDoesNotSendRTP(t *testing.T) {
	tsPkt := mustTestTSPacket(7)
	dec := newTestRtpDecapsulator(&datagramsReadCloser{datagrams: [][]byte{tsPkt, tsPkt, tsPkt}})
	dec.guard.max = 3

	buf := make([]byte, 1500)
	_, err := dec.Read(buf)
	require.ErrorIs(t, err, ErrNotRtp)
}

func TestRtpDecapsulatorReturnsTheErrorOfADatagramThatIsNotRTP(t *testing.T) {
	// A read error that comes with a datagram that is not RTP is returned rather than retried, so that the end of the
	// stream is not lost.
	dec := newTestRtpDecapsulator(&fakeReadCloser{data: mustTestTSPacket(7), err: io.EOF})

	buf := make([]byte, 1500)
	_, err := dec.Read(buf)
	require.ErrorIs(t, err, io.EOF)
}

func TestRtpDecapsulatorPreservesUnderlyingEOF(t *testing.T) {
	// Regression test: the original implementation shadowed the named return err with the header-parse error,
	// silently swallowing a same-call io.EOF from the underlying reader whenever n > 0 and parsing succeeded - a
	// legal, common io.Reader pattern for a stream's final read.
	tsPkt := mustTestTSPacket(7)
	datagram := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, tsPkt)
	dec := newTestRtpDecapsulator(&fakeReadCloser{data: datagram, err: io.EOF})

	buf := make([]byte, 1500)
	n, err := dec.Read(buf)

	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, tsPkt, buf[:n])
}

func TestRtpDecapsulatorStripsAcrossRepeatedReads(t *testing.T) {
	// The scratch packet used for stripping is reused across reads via repeated From() calls; confirm this actually
	// works for more than one datagram, not just the first.
	fake := &fakeReadCloser{}
	dec := newTestRtpDecapsulator(fake)

	for i, pid := range []int{7, 42, 100} {
		tsPkt := mustTestTSPacket(pid)
		fake.data = mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: uint16(i), Timestamp: uint32(i)}, tsPkt)
		fake.read = false

		buf := make([]byte, 1500)
		n, err := dec.Read(buf)
		require.NoError(t, err)
		require.Equal(t, tsPkt, buf[:n], "datagram %d", i)
	}
}

// TestRtpDecapsulator_ConnStats_Passthrough verifies that stripping the RTP layer doesn't break the mio.StatsReporter
// chain: the wrapped reader's stats (e.g. an SRT connection's) come through unchanged.
func TestRtpDecapsulator_ConnStats_Passthrough(t *testing.T) {
	fake := &fakeStatsReporter{stats: mio.ConnStats{RemoteAddr: "1.2.3.4:5678"}}
	dec := newTestRtpDecapsulator(fake)

	var stats mio.ConnStats
	dec.ConnStats(&stats, true)
	require.Equal(t, "1.2.3.4:5678", stats.RemoteAddr)
	require.True(t, fake.lastDetails)
}

// TestRtpDecapsulator_ConnStats_NonReporter verifies a zero ConnStats (not a panic) when the wrapped reader doesn't
// implement mio.StatsReporter (e.g. a plain UDP socket).
func TestRtpDecapsulator_ConnStats_NonReporter(t *testing.T) {
	dec := newTestRtpDecapsulator(&fakeReadCloser{})
	var stats mio.ConnStats
	dec.ConnStats(&stats, true)
	require.Zero(t, stats)
}

// fakeStatsReporter is a minimal io.ReadCloser that also implements mio.StatsReporter.
type fakeStatsReporter struct {
	stats       mio.ConnStats
	lastDetails bool
}

func (*fakeStatsReporter) Read([]byte) (int, error) { return 0, io.EOF }
func (*fakeStatsReporter) Close() error             { return nil }
func (f *fakeStatsReporter) ConnStats(into *mio.ConnStats, details bool) {
	f.lastDetails = details
	*into = f.stats
}

func newTestRtpDecapsulator(reader io.ReadCloser) *RtpDecapsulator {
	return &RtpDecapsulator{reader: reader, pkt: pktpool.NewPacket(0, maxUDPPacketSize)}
}

// fakeReadCloser returns data (and err, if set) from a single Read call, then io.EOF on any subsequent call.
type fakeReadCloser struct {
	data []byte
	err  error
	read bool
}

func (f *fakeReadCloser) Read(p []byte) (int, error) {
	if f.read {
		return 0, io.EOF
	}
	f.read = true
	n := copy(p, f.data)
	if f.err != nil {
		return n, f.err
	}
	return n, nil
}

func (f *fakeReadCloser) Close() error {
	return nil
}

// datagramsReadCloser returns one of its datagrams per Read call, in order, then io.EOF.
type datagramsReadCloser struct {
	datagrams [][]byte
}

func (d *datagramsReadCloser) Read(p []byte) (int, error) {
	if len(d.datagrams) == 0 {
		return 0, io.EOF
	}
	n := copy(p, d.datagrams[0])
	d.datagrams = d.datagrams[1:]
	return n, nil
}

func (d *datagramsReadCloser) Close() error {
	return nil
}
