package mpegts

import (
	"io"
	"testing"
	"time"

	"github.com/Comcast/gots/v2/packet"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
)

func TestMpegtsPacketProcessorPCRRotationFallback(t *testing.T) {
	// base is the logical "now" for the first datagram of each subtest. Later datagrams pass base.Add(...) to
	// advance time deterministically, instead of mutating the processor's internal timestamps.
	base := time.Unix(1000, 0)

	// laterThanSegment is comfortably beyond both the segment length and the staleness window (half the segment
	// length) configured by newTestMpegtsPacketProcessor.
	laterThanSegment := base.Add(3 * time.Second)

	t.Run("startup writes without pcr", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithoutPCR(t, 100)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.Equal(t, 0, pp.pcrPid)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 1, pp.stats.PacketsWritten.Load())
	})

	t.Run("no pcr rotates by wall clock", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithoutPCR(t, 100)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())

		second := mustPacketWithoutPCR(t, 100)
		pp.ProcessDatagram(laterThanSegment, second[:])

		require.Equal(t, 2, opener.opens)
		require.Equal(t, 0, pp.pcrPid)
		require.EqualValues(t, 2, pp.stats.NumSegments.Load())
		require.EqualValues(t, 1, pp.stats.NumTimedRotate.Load())
	})

	t.Run("zero pcr initializes processor", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, 0)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.Equal(t, 100, pp.pcrPid)
		require.EqualValues(t, 0, pp.pcr)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 1, pp.stats.PacketsWritten.Load())
	})

	t.Run("first pcr switches from wall clock without immediate rotation", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithoutPCR(t, 100)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.Equal(t, 0, pp.pcrPid)

		second := mustPacketWithPCR(t, 100, PcrTs*100)
		pp.ProcessDatagram(base, second[:])

		require.Equal(t, 1, opener.opens)
		require.Equal(t, 100, pp.pcrPid)
		require.EqualValues(t, PcrTs*100, pp.pcr)
		require.EqualValues(t, PcrTs*100, pp.segStartPcr)
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("pcr advancement rotates", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, 1)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())

		// PCR advances past the segment bounds once the minimum wall-clock margin has elapsed: rotation is media
		// driven, not a timed fallback.
		second := mustPacketWithPCR(t, 100, PcrTs*2)
		pp.ProcessDatagram(base.Add(500*time.Millisecond), second[:])

		require.Equal(t, 2, opener.opens)
		require.EqualValues(t, 2, pp.stats.NumSegments.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("pcr advancement respects minimum wall clock margin", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, 1)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)

		// PCR has advanced a full segment, but barely any wall-clock time has passed (well under the segmentLength/4
		// floor): the rotation is suppressed to avoid producing segments too frequently.
		second := mustPacketWithPCR(t, 100, PcrTs*2)
		pp.ProcessDatagram(base.Add(100*time.Millisecond), second[:])

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())

		// Once the minimum margin has elapsed, the pending PCR advancement triggers the rotation.
		third := mustPacketWithPCR(t, 100, PcrTs*3)
		pp.ProcessDatagram(base.Add(300*time.Millisecond), third[:])

		require.Equal(t, 2, opener.opens)
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("primary rotation stays media driven", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, 1)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())

		// Well past the segment length, but a fresh PCR that has barely advanced keeps rotation media driven: no
		// wall-clock fallback fires.
		second := mustPacketWithPCR(t, 100, 2)
		pp.ProcessDatagram(laterThanSegment, second[:])

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("pcr pid stays pinned", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, 1)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.Equal(t, 100, pp.pcrPid)
		require.EqualValues(t, 1, pp.pcr)

		// A PCR from a different program must not be adopted while the original PID is still fresh.
		second := mustPacketWithPCR(t, 200, PcrTs*2)
		pp.ProcessDatagram(base, second[:])

		require.Equal(t, 1, opener.opens)
		require.Equal(t, 100, pp.pcrPid)
		require.EqualValues(t, 1, pp.pcr)
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("stale pcr pid clears on fallback rotation", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, 1)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.Equal(t, 100, pp.pcrPid)
		require.EqualValues(t, 1, pp.pcr)

		// The pinned PID has gone silent; a PCR from a different program arrives after the staleness window. It is
		// ignored for PCR, so the processor falls back to wall clock and unpins the PID.
		second := mustPacketWithPCR(t, 200, 2)
		pp.ProcessDatagram(laterThanSegment, second[:])

		require.Equal(t, 2, opener.opens)
		require.Equal(t, 0, pp.pcrPid)
		require.EqualValues(t, 1, pp.pcr)
		require.EqualValues(t, 1, pp.stats.NumTimedRotate.Load())

		// With the PID unpinned, the next PCR re-pins onto the new program.
		third := mustPacketWithPCR(t, 200, 2)
		pp.ProcessDatagram(laterThanSegment, third[:])

		require.Equal(t, 2, opener.opens)
		require.Equal(t, 200, pp.pcrPid)
		require.EqualValues(t, 2, pp.pcr)
		require.EqualValues(t, 1, pp.stats.NumTimedRotate.Load())
	})

	t.Run("stagnant pcr falls back to wall clock", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, PcrTs)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.Equal(t, 100, pp.pcrPid)

		// Simulate a frozen source clock: the pinned PID keeps emitting the same PCR value. The repeated value must
		// not refresh pcrLastSeenAt, so staleness triggers a wall-clock rotation.
		second := mustPacketWithPCR(t, 100, PcrTs)
		pp.ProcessDatagram(laterThanSegment, second[:])

		require.Equal(t, 2, opener.opens)
		require.Equal(t, 0, pp.pcrPid)
		require.EqualValues(t, PcrTs, pp.pcr)
		require.EqualValues(t, 1, pp.stats.NumTimedRotate.Load())
	})

	t.Run("backward pcr is ignored", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, PcrTs*100)
		pp.ProcessDatagram(base, first[:])
		require.EqualValues(t, PcrTs*100, pp.pcr)

		// A small backward step (not a wrap) is treated as an unhealthy clock and ignored.
		second := mustPacketWithPCR(t, 100, PcrTs*50)
		pp.ProcessDatagram(base, second[:])
		require.EqualValues(t, PcrTs*100, pp.pcr)
	})

	t.Run("pcr wrap is accepted", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, (PcrMax/4)*3)
		pp.ProcessDatagram(base, first[:])
		require.EqualValues(t, (PcrMax/4)*3, pp.pcr)

		// A large backward jump is a genuine counter wrap and must be accepted as the new media time.
		second := mustPacketWithPCR(t, 100, PcrTs)
		pp.ProcessDatagram(base, second[:])
		require.EqualValues(t, PcrTs, pp.pcr)
	})

	t.Run("stale pid uses wall clock fallback", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		first := mustPacketWithPCR(t, 100, 1)
		pp.ProcessDatagram(base, first[:])

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())

		// The pinned PID has gone silent and a PCR-less datagram arrives after the staleness window: wall clock takes
		// over segmentation.
		second := mustPacketWithoutPCR(t, 100)
		pp.ProcessDatagram(laterThanSegment, second[:])

		require.Equal(t, 2, opener.opens)
		require.EqualValues(t, 2, pp.stats.NumSegments.Load())
		require.EqualValues(t, 1, pp.stats.NumTimedRotate.Load())
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

func mustPacketWithPCR(t *testing.T, pid int, pcr uint64) packet.Packet {
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

func mustPacketWithoutPCR(t *testing.T, pid int) packet.Packet {
	t.Helper()

	pkt := packet.New()
	pkt.SetPID(pid)
	require.NoError(t, pkt.SetAdaptationFieldControl(packet.AdaptationFieldFlag))
	return *pkt
}

type recordingSequentialOpener struct {
	opens int
}

func (o *recordingSequentialOpener) OpenNext() (io.WriteCloser, error) {
	o.opens++
	return &recordingWriteCloser{}, nil
}

func (o *recordingSequentialOpener) Stat(args any) error {
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
