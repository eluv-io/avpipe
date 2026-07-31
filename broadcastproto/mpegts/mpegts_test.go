package mpegts

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Comcast/gots/v2/packet"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
)

func TestMpegtsPacketProcessorWallClockSegmentation(t *testing.T) {
	// base is the logical "now" for the first datagram of each subtest. Later datagrams pass base.Add(...) to advance
	// time deterministically instead of relying on the real wall clock.
	base := time.Unix(1000, 0)

	t.Run("first datagram opens the initial segment", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := mustTSPacket()
		pp.ProcessDatagram(base, pkt[:])

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 1, pp.stats.PacketsWritten.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("does not rotate within the segment length", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := mustTSPacket()
		pp.ProcessDatagram(base, pkt[:])
		require.Equal(t, 1, opener.opens)

		// Still inside the 1s segment: same segment, no timed rotation.
		pp.ProcessDatagram(base.Add(500*time.Millisecond), pkt[:])

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("rotates once the segment length has elapsed", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := mustTSPacket()
		pp.ProcessDatagram(base, pkt[:])
		require.Equal(t, 1, opener.opens)

		// At exactly the segment length the next datagram rolls over to a new segment.
		pp.ProcessDatagram(base.Add(time.Second), pkt[:])

		require.Equal(t, 2, opener.opens)
		require.EqualValues(t, 2, pp.stats.NumSegments.Load())
		require.EqualValues(t, 1, pp.stats.NumTimedRotate.Load())
	})

	t.Run("rotates on each successive elapsed segment", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := mustTSPacket()
		pp.ProcessDatagram(base, pkt[:])
		require.Equal(t, 1, opener.opens)

		// One datagram per elapsed second produces one new segment each.
		for i := 1; i <= 3; i++ {
			pp.ProcessDatagram(base.Add(time.Duration(i)*time.Second), pkt[:])
		}

		require.Equal(t, 4, opener.opens)
		require.EqualValues(t, 4, pp.stats.NumSegments.Load())
		require.EqualValues(t, 3, pp.stats.NumTimedRotate.Load())
	})
}

func TestMpegtsPacketProcessorPcrPidPinning(t *testing.T) {
	// PCR is stats-only here, but multi-program streams carry an independent PCR per program, so tracking must pin to
	// a single PID to keep FirstPCR/LastPCR meaningful.
	base := time.Unix(1000, 0)

	opener := &recordingSequentialOpener{}
	pp := newTestMpegtsPacketProcessor(opener)

	// The first PCR-bearing PID pins PCR tracking.
	pinned := mustTSPacketWithPCR(t, 100, PcrTs)
	pp.ProcessDatagram(base, pinned[:])
	require.Equal(t, 100, pp.pcrPid)
	require.EqualValues(t, PcrTs, pp.stats.FirstPCR.Load())
	require.EqualValues(t, PcrTs, pp.stats.LastPCR.Load())

	// A PCR from a different program is ignored: the pinned PID and LastPCR are unchanged.
	otherProgram := mustTSPacketWithPCR(t, 200, PcrTs*5)
	pp.ProcessDatagram(base, otherProgram[:])
	require.Equal(t, 100, pp.pcrPid)
	require.EqualValues(t, PcrTs, pp.stats.LastPCR.Load())

	// A later PCR from the pinned program updates LastPCR.
	pinnedLater := mustTSPacketWithPCR(t, 100, PcrTs*2)
	pp.ProcessDatagram(base, pinnedLater[:])
	require.Equal(t, 100, pp.pcrPid)
	require.EqualValues(t, PcrTs*2, pp.stats.LastPCR.Load())
}

func TestMpegtsPacketProcessorPcrWrapStat(t *testing.T) {
	base := time.Unix(1000, 0)

	opener := &recordingSequentialOpener{}
	pp := newTestMpegtsPacketProcessor(opener)

	// Seed the pinned PID with a PCR near the top of the counter range. The first PCR is never a wrap.
	high := mustTSPacketWithPCR(t, 100, (PcrMax/4)*3)
	pp.ProcessDatagram(base, high[:])
	require.EqualValues(t, 0, pp.stats.NumWraps.Load())

	// A large backward jump on the pinned PID is a counter wrap.
	low := mustTSPacketWithPCR(t, 100, PcrTs)
	pp.ProcessDatagram(base, low[:])
	require.EqualValues(t, 1, pp.stats.NumWraps.Load())
	require.EqualValues(t, PcrTs, pp.stats.LastPCR.Load())

	// A normal forward advance is not a wrap.
	fwd := mustTSPacketWithPCR(t, 100, PcrTs*2)
	pp.ProcessDatagram(base, fwd[:])
	require.EqualValues(t, 1, pp.stats.NumWraps.Load())
}

func TestMpegtsPacketProcessorStopClosesFinalOutput(t *testing.T) {
	opener := &recordingSequentialOpener{}
	pp := newTestMpegtsPacketProcessor(opener)

	pkt := mustTSPacket()
	pp.ProcessDatagram(time.Unix(1000, 0), pkt[:])
	require.Len(t, opener.writers, 1)

	require.NoError(t, pp.Stop())
	require.Equal(t, 1, opener.writers[0].closes)

	// Stop is safe to call repeatedly and does not close the output twice.
	require.NoError(t, pp.Stop())
	require.Equal(t, 1, opener.writers[0].closes)
}

func TestMpegtsPacketProcessorStopReturnsFinalOutputCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	opener := &recordingSequentialOpener{closeErr: closeErr}
	pp := newTestMpegtsPacketProcessor(opener)

	pkt := mustTSPacket()
	pp.ProcessDatagram(time.Unix(1000, 0), pkt[:])

	require.ErrorIs(t, pp.Stop(), closeErr)
	// The original close result remains available without retrying Close.
	require.ErrorIs(t, pp.Stop(), closeErr)
	require.Equal(t, 1, opener.writers[0].closes)
}

func TestRemoveTsPadding(t *testing.T) {
	t.Run("strips a well-formed padding packet", func(t *testing.T) {
		pkt := mustPaddingPacket()
		datagram := append([]byte{}, pkt[:]...)

		res, stripped, faulty := RemoveTsPadding(datagram, 0)

		require.Equal(t, 1, stripped)
		require.Equal(t, 0, faulty)
		require.Len(t, res, 4) // header preserved, 184-byte payload stripped
		require.Equal(t, pkt[:4], res[:4])
	})

	t.Run("leaves a non-null packet untouched", func(t *testing.T) {
		pkt := mustTSPacket()
		pkt.SetPID(100) // mustTSPacket defaults to the null PID; make this a regular, non-null packet
		datagram := append([]byte{}, pkt[:]...)

		res, stripped, faulty := RemoveTsPadding(datagram, 0)

		require.Equal(t, 0, stripped)
		require.Equal(t, 0, faulty)
		require.Equal(t, datagram, res)
	})

	t.Run("counts faulty when a null packet's payload is not all padding", func(t *testing.T) {
		pkt := mustPaddingPacket()
		pkt[100] = 0x00 // corrupt one payload byte so it's not genuine padding
		datagram := append([]byte{}, pkt[:]...)

		res, stripped, faulty := RemoveTsPadding(datagram, 0)

		require.Equal(t, 0, stripped)
		require.Equal(t, 1, faulty)
		require.Equal(t, datagram, res) // left untouched, not stripped
	})

	t.Run("does not touch a malformed packet even if it claims to be null", func(t *testing.T) {
		// Regression test: the condition used to be CheckErrors() != nil && IsNull(), which only ever matched
		// malformed packets - well-formed padding packets (CheckErrors() == nil) were never stripped at all.
		pkt := mustPaddingPacket()
		pkt[0] = 0x00 // corrupt the sync byte: CheckErrors() != nil
		datagram := append([]byte{}, pkt[:]...)

		res, stripped, faulty := RemoveTsPadding(datagram, 0)

		require.Equal(t, 0, stripped)
		require.Equal(t, 0, faulty)
		require.Equal(t, datagram, res)
	})

	t.Run("strips padding packets among other packets in the same datagram", func(t *testing.T) {
		regular1 := mustTSPacket()
		regular1.SetPID(7) // mustTSPacket defaults to the null PID; make this a regular, non-null packet
		padding := mustPaddingPacket()
		regular2 := mustTSPacket()
		regular2.SetPID(42) // distinguish from regular1 so we can tell them apart after stripping

		datagram := append([]byte{}, regular1[:]...)
		datagram = append(datagram, padding[:]...)
		datagram = append(datagram, regular2[:]...)

		res, stripped, faulty := RemoveTsPadding(datagram, 0)

		require.Equal(t, 1, stripped)
		require.Equal(t, 0, faulty)
		require.Len(t, res, 188+4+188) // regular1 + stripped padding header + regular2

		gotRegular1 := toTSPacket(res[0:188])
		require.Equal(t, regular1.PID(), gotRegular1.PID())

		gotPaddingHeader := res[188:192]
		require.Equal(t, padding[:4], gotPaddingHeader)

		gotRegular2 := toTSPacket(res[192:380])
		require.Equal(t, 42, gotRegular2.PID())
	})

	t.Run("respects the RTP header offset", func(t *testing.T) {
		rtpHeader := make([]byte, 12)
		padding := mustPaddingPacket()
		datagram := append(append([]byte{}, rtpHeader...), padding[:]...)

		res, stripped, faulty := RemoveTsPadding(datagram, 12)

		require.Equal(t, 1, stripped)
		require.Equal(t, 0, faulty)
		require.Len(t, res, 12+4)
		require.Equal(t, rtpHeader, res[:12])
	})
}

func newTestMpegtsPacketProcessor(opener SequentialOpener) *MpegtsPacketProcessor {
	return NewMpegtsPacketProcessor(
		TsConfig{
			SegmentLengthSec: 1,
			Packaging:        transport.RawTs,
		},
		opener,
		1,
	)
}

// mustTSPacket returns a valid (null) TS packet usable as a single-packet datagram.
func mustTSPacket() packet.Packet {
	return *packet.New()
}

// mustPaddingPacket returns a well-formed TS null/padding packet: null PID, valid sync byte and flags, and a
// payload fully filled with 0xFF, as real TS padding packets are.
func mustPaddingPacket() packet.Packet {
	pkt := *packet.New() // defaults to the null PID
	for i := 4; i < len(pkt); i++ {
		pkt[i] = 0xFF
	}
	return pkt
}

// mustTSPacketWithPCR returns a TS packet on the given PID carrying the given PCR value.
func mustTSPacketWithPCR(t *testing.T, pid int, pcr uint64) packet.Packet {
	t.Helper()
	pkt := packet.New()
	pkt.SetPID(pid)
	require.NoError(t, pkt.SetAdaptationFieldControl(packet.AdaptationFieldFlag))
	af, err := pkt.AdaptationField()
	require.NoError(t, err)
	require.NoError(t, af.SetHasPCR(true))
	require.NoError(t, af.SetPCR(pcr))
	return *pkt
}

type recordingSequentialOpener struct {
	opens    int
	closeErr error
	writers  []*recordingWriteCloser
}

func (o *recordingSequentialOpener) OpenNext() (io.WriteCloser, error) {
	o.opens++
	writer := &recordingWriteCloser{closeErr: o.closeErr}
	o.writers = append(o.writers, writer)
	return writer, nil
}

func (o *recordingSequentialOpener) Stat(_ any) error {
	return nil
}

func (o *recordingSequentialOpener) ReportStart() error {
	return nil
}

type recordingWriteCloser struct {
	closes   int
	closeErr error
}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *recordingWriteCloser) Close() error {
	w.closes++
	return w.closeErr
}
