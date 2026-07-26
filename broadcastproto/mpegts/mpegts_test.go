package mpegts

import (
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

		pkt := mustTSPacket(t)
		pp.ProcessDatagram(base, pkt[:])

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 1, pp.stats.PacketsWritten.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("does not rotate within the segment length", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := mustTSPacket(t)
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

		pkt := mustTSPacket(t)
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

		pkt := mustTSPacket(t)
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
func mustTSPacket(t *testing.T) packet.Packet {
	t.Helper()
	return *packet.New()
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
	opens int
}

func (o *recordingSequentialOpener) OpenNext() (io.WriteCloser, error) {
	o.opens++
	return &recordingWriteCloser{}, nil
}

func (o *recordingSequentialOpener) Stat(_ any) error {
	return nil
}

func (o *recordingSequentialOpener) ReportStart() error {
	return nil
}

type recordingWriteCloser struct{}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *recordingWriteCloser) Close() error {
	return nil
}
