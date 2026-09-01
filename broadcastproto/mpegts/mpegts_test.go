package mpegts

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Comcast/gots/v2/packet"
	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe/broadcastproto/tlv"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
	mio "github.com/eluv-io/common-go/media/io"
	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/log-go"
)

func TestMpegtsPacketProcessorWallClockSegmentation(t *testing.T) {
	// base is the logical "now" for the first datagram of each subtest. Later datagrams pass base.Add(...) to advance
	// time deterministically instead of relying on the real wall clock.
	base := time.Unix(1000, 0)

	t.Run("first datagram opens the initial segment", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := mustTSPacket()
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 1, pp.stats.PacketsWritten.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("does not rotate within the segment length", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := mustTSPacket()
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))
		require.Equal(t, 1, opener.opens)

		// Still inside the 1s segment: same segment, no timed rotation.
		pp.ProcessDatagramPacket(base.Add(500*time.Millisecond), mustDatagramPacket(t, pkt[:]))

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.NumSegments.Load())
		require.EqualValues(t, 0, pp.stats.NumTimedRotate.Load())
	})

	t.Run("rotates once the segment length has elapsed", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := mustTSPacket()
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))
		require.Equal(t, 1, opener.opens)

		// At exactly the segment length the next datagram rolls over to a new segment.
		pp.ProcessDatagramPacket(base.Add(time.Second), mustDatagramPacket(t, pkt[:]))

		require.Equal(t, 2, opener.opens)
		require.EqualValues(t, 2, pp.stats.NumSegments.Load())
		require.EqualValues(t, 1, pp.stats.NumTimedRotate.Load())
	})

	t.Run("rotates on each successive elapsed segment", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := mustTSPacket()
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))
		require.Equal(t, 1, opener.opens)

		// One datagram per elapsed second produces one new segment each.
		for i := 1; i <= 3; i++ {
			pp.ProcessDatagramPacket(base.Add(time.Duration(i)*time.Second), mustDatagramPacket(t, pkt[:]))
		}

		require.Equal(t, 4, opener.opens)
		require.EqualValues(t, 4, pp.stats.NumSegments.Load())
		require.EqualValues(t, 3, pp.stats.NumTimedRotate.Load())
	})
}

// TestMpegtsPacketProcessorContinuityCounterError covers continuity-counter validation, which prior to
// tracker.MediaTracker's adoption had no test coverage at all despite being one of the two core tracking algorithms.
// This logic itself now lives in mpp.tracker (mpegts.TsStreamTracker), surfaced here via ExportedStats.TS.ErrorsCC.
func TestMpegtsPacketProcessorContinuityCounterError(t *testing.T) {
	base := time.Unix(1000, 0)
	opener := &recordingSequentialOpener{}
	pp := newTestMpegtsPacketProcessor(opener)

	pkt := packet.New()
	pkt.SetPID(100)
	pkt.SetContinuityCounter(0)
	pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))
	require.EqualValues(t, 0, statsOf(pp).TS.ErrorsCC, "the first packet on a PID establishes the baseline, not an error")

	// Skip a continuity-counter value: expected 1, actual 5.
	pkt.SetContinuityCounter(5)
	pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))
	require.EqualValues(t, 1, statsOf(pp).TS.ErrorsCC)

	// The tracker's expectation continues from the actually-observed counter, so a correctly incremented follow-up
	// packet is not itself flagged.
	pkt.SetContinuityCounter(6)
	pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))
	require.EqualValues(t, 1, statsOf(pp).TS.ErrorsCC)
}

func TestMpegtsPacketProcessorPcrWrapStat(t *testing.T) {
	base := time.Unix(1000, 0)

	opener := &recordingSequentialOpener{}
	pp := newTestMpegtsPacketProcessor(opener)

	// Seed the pinned PID with a PCR near the top of the counter range. The first PCR is never a wrap.
	high := mustTSPacketWithPCR(t, 100, (PcrMax/4)*3)
	pp.ProcessDatagramPacket(base, mustDatagramPacket(t, high[:]))
	require.EqualValues(t, 0, statsOf(pp).TS.NumWraps)

	// A large backward jump on the pinned PID is a counter wrap.
	low := mustTSPacketWithPCR(t, 100, PcrTs)
	pp.ProcessDatagramPacket(base, mustDatagramPacket(t, low[:]))
	require.EqualValues(t, 1, statsOf(pp).TS.NumWraps)

	// A normal forward advance is not a wrap.
	fwd := mustTSPacketWithPCR(t, 100, PcrTs*2)
	pp.ProcessDatagramPacket(base, mustDatagramPacket(t, fwd[:]))
	require.EqualValues(t, 1, statsOf(pp).TS.NumWraps)
}

// TestMpegtsPacketProcessorPacketsReceivedCountsDatagrams is a regression test for a real bug: PacketsReceived/
// BytesReceived used to count TS packets/TS-only bytes (188-byte units), while PacketsDropped (fed by the channel
// sender, see RegisterPacketsDropped) counts datagrams - so a "Recv/Drop %" report combining the two was comparing
// different units and could never be meaningful. Both now come from mpp.tracker, which counts once per datagram
// (full datagram bytes, including the raw TS bytes here since this processor is non-RTP) like PacketsDropped does.
func TestMpegtsPacketProcessorPacketsReceivedCountsDatagrams(t *testing.T) {
	base := time.Unix(1000, 0)
	opener := &recordingSequentialOpener{}
	pp := newTestMpegtsPacketProcessor(opener)

	// One datagram carrying 3 TS packets must count as 1 received "packet" (datagram), not 3, and its bytes as the
	// full datagram length, not the sum of the individual TS packets (equal here since this is a raw, non-RTP
	// datagram with no header of its own, but sourced independently of the TS-packet loop either way).
	var datagram []byte
	for i := 0; i < 3; i++ {
		pkt := mustTSPacket()
		pkt.SetPID(7)
		datagram = append(datagram, pkt[:]...)
	}
	pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))
	require.EqualValues(t, 1, statsOf(pp).TS.PacketsReceived)
	require.EqualValues(t, len(datagram), statsOf(pp).TS.BytesReceived)

	pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))
	require.EqualValues(t, 2, statsOf(pp).TS.PacketsReceived, "counts once more per datagram, regardless of TS packets within it")
	require.EqualValues(t, 2*len(datagram), statsOf(pp).TS.BytesReceived)
}

// TestMpegtsPacketProcessorDiscardedPackets verifies DiscardedPackets aggregates every condition under which a whole
// datagram is rejected before its TS packets reach mpp.tracker's tsTracker: too small (raw, non-RTP path) and a
// non-188-aligned TS payload (RTP path). Per-packet conditions within an otherwise-processed datagram (CC errors,
// adaptation-field errors) are NOT included - see the comment on DiscardedPackets in ExportedStats.populate.
func TestMpegtsPacketProcessorDiscardedPackets(t *testing.T) {
	base := time.Unix(1000, 0)

	t.Run("too small (raw TS)", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, []byte{0x47, 0x00}))
		require.EqualValues(t, 1, statsOf(pp).TS.DiscardedPackets)
		require.EqualValues(t, 1, statsOf(pp).TS.SmallPacketsDropped)
	})

	t.Run("non-188-aligned TS payload (RTP)", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessorRTP(opener)

		tsPkt := mustTSPacket()
		tsPkt.SetPID(7)
		payload := append(append([]byte{}, tsPkt[:]...), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12)
		datagram := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, payload)

		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))
		require.EqualValues(t, 1, statsOf(pp).TS.DiscardedPackets)
		require.EqualValues(t, 1, statsOf(pp).TS.ErrorsIncompletePackets)
	})

	t.Run("bad RTP version", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessorRTP(opener)

		tsPkt := mustTSPacket()
		datagram := mustRTPDatagram(t, pionrtp.Header{Version: 1, SequenceNumber: 1, Timestamp: 1}, tsPkt[:])

		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))
		require.EqualValues(t, 1, statsOf(pp).TS.DiscardedPackets)
	})

	t.Run("a continuity-counter error is not discarded", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		pkt := packet.New()
		pkt.SetPID(100)
		pkt.SetContinuityCounter(0)
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))
		pkt.SetContinuityCounter(5) // skip: triggers a CC error, but the datagram is still processed
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))

		require.EqualValues(t, 1, statsOf(pp).TS.ErrorsCC)
		require.EqualValues(t, 0, statsOf(pp).TS.DiscardedPackets)
	})
}

func TestMpegtsPacketProcessorStopClosesFinalOutput(t *testing.T) {
	opener := &recordingSequentialOpener{}
	pp := newTestMpegtsPacketProcessor(opener)

	pkt := mustTSPacket()
	pp.ProcessDatagramPacket(time.Unix(1000, 0), mustDatagramPacket(t, pkt[:]))
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
	pp.ProcessDatagramPacket(time.Unix(1000, 0), mustDatagramPacket(t, pkt[:]))

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

// TestMpegtsPacketProcessor_ReportFullStatsLoop_ImmediateAndFinalReport is a regression test for
// reportFullStatsLoop's 5-second ticker: time.NewTicker's first tick only fires after the full interval, and a bare
// select gives no report at all on shutdown - so before the fix, an ingest shorter than 5s never produced a single
// full stats report, and no report ever reflected the stream's final state. Verifies a report arrives immediately
// on StartReportingStats (well before the 5s interval), and one more arrives after Stop().
func TestMpegtsPacketProcessor_ReportFullStatsLoop_ImmediateAndFinalReport(t *testing.T) {
	reports := make(chan struct{}, 8)
	opener := &recordingSequentialOpener{}
	opener.onStat = func(ExportedStats) { reports <- struct{}{} }
	pp := newTestMpegtsPacketProcessor(opener)

	pp.StartReportingStats()
	defer pp.Stop()

	select {
	case <-reports:
	case <-time.After(2 * time.Second):
		t.Fatal("no report arrived immediately on StartReportingStats, well before the 5s ticker interval")
	}

	require.NoError(t, pp.Stop())
	select {
	case <-reports:
	case <-time.After(2 * time.Second):
		t.Fatal("no final report arrived after Stop()")
	}
}

func newTestMpegtsPacketProcessor(opener SequentialOpener) *MpegtsPacketProcessor {
	pp := NewMpegtsPacketProcessor(
		TsConfig{
			SegmentLengthSec: 1,
			Packaging:        transport.RawTs,
		},
		opener,
		1,
	)
	// ExportedStats.populate (via PushStats or statsOf) reads TSStats.PacketsDropped, which is only non-nil once
	// registered - every production caller does this before processing any packets (see custom.go/av_input.go).
	var packetsDropped atomic.Uint64
	pp.RegisterPacketsDropped(&packetsDropped)
	return pp
}

func newTestMpegtsPacketProcessorRTP(opener SequentialOpener) *MpegtsPacketProcessor {
	pp := NewMpegtsPacketProcessor(
		TsConfig{
			SegmentLengthSec: 1,
			Packaging:        transport.RtpTs,
		},
		opener,
		1,
	)
	// PushStats() (triggered on the first RTP packet) reads TSStats.PacketsDropped, which is only non-nil once
	// registered - every production caller does this before processing any packets (see custom.go/av_input.go).
	var packetsDropped atomic.Uint64
	pp.RegisterPacketsDropped(&packetsDropped)
	return pp
}

// mustRTPDatagram marshals an RTP packet with the given header and payload into raw wire bytes.
func mustRTPDatagram(t *testing.T, hdr pionrtp.Header, payload []byte) []byte {
	t.Helper()
	pkt := pionrtp.Packet{Header: hdr, Payload: payload}
	data, err := pkt.Marshal()
	require.NoError(t, err)
	return data
}

// TestMpegtsPacketProcessorConnStats verifies SetConnStatsSource wiring end to end: PushStats surfaces the source's
// SRT stats via ExportedStats.Srt, and omits it entirely (rather than a zero value) when no source is set.
func TestMpegtsPacketProcessorConnStats(t *testing.T) {
	opener := &recordingSequentialOpener{}
	pp := newTestMpegtsPacketProcessor(opener)

	pp.PushStats()
	require.Nil(t, opener.lastStat.Srt, "no connStatsSource set")

	fake := &fakeConnStatsSource{stats: mio.ConnStats{SRT: &mio.SrtConnStats{Version: 5, Encrypted: true}}, ok: true}
	pp.SetConnStatsSource(fake)

	pp.PushStats()
	require.Same(t, fake.stats.SRT, opener.lastStat.Srt)
	require.True(t, fake.lastDetails, "PushStats requests full protocol stats, not just Version/Encrypted")

	fake.ok = false
	pp.PushStats()
	require.Nil(t, opener.lastStat.Srt, "the source no longer reports stats (e.g. disconnected)")
}

// TestMpegtsPacketProcessor_pushStatsInto_DestinationReuseReducesAllocations is a regression test for the
// allocation-reduction goal behind reusing dst across ticks (see reportFullStatsLoop's doc): a caller that reuses
// the same destination across repeated calls must allocate less than one that passes a fresh destination every
// call. mpp.tracker.Snapshot's own per-PID walk (sorting tracked PIDs, unrelated to destination reuse - see
// maputil.SortedKeys) still allocates something on every call regardless of dst reuse, so this asserts a relative
// reduction, not an absolute zero.
func TestMpegtsPacketProcessor_pushStatsInto_DestinationReuseReducesAllocations(t *testing.T) {
	opener := &recordingSequentialOpener{}
	pp := newTestMpegtsPacketProcessor(opener)

	// Multiple PIDs, no PCR, so CopyInto has real nested Streams/HistogramCapture slots to reuse.
	base := time.Unix(1000, 0)
	for pid := 100; pid < 105; pid++ {
		pkt := packet.New()
		pkt.SetPID(pid)
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, (*pkt)[:]))
	}

	var reused ExportedStats
	pp.pushStatsInto(&reused) // warm up: sizes reused.Stream's nested slices/structs to the current PID set

	reusedAllocs := testing.AllocsPerRun(50, func() {
		pp.pushStatsInto(&reused)
	})
	freshAllocs := testing.AllocsPerRun(50, func() {
		var fresh ExportedStats
		pp.pushStatsInto(&fresh)
	})

	log.Info("allocs", "reused", reusedAllocs, "fresh", freshAllocs)
	require.Less(t, reusedAllocs, freshAllocs,
		"reusing dst across calls must allocate less than passing a fresh destination every call - CopyInto's own "+
			"nested Streams/HistogramCapture reuse should not add to Snapshot's unavoidable per-call cost once "+
			"dst's shape is warmed up, but a fresh destination re-allocates all of that every single time")
}

// TestMpegtsPacketProcessor_PushStats_ConcurrentReceiverCopyIsRaceFree is a regression test for the data race that
// Snapshot-based reuse in pushStatsInto would otherwise permit: a destination reused across calls (as
// reportFullStatsLoop's own does, and as this test's writer goroutine mirrors below - see reused) is mutated in
// place by mpp.tracker.Snapshot on every call, and only stays valid for that single call - so a receiver that
// retains an ExportedStats past it (as content-fabric's live-recorder AV_IN_STAT_MPEGTS handler does) must deep-copy
// it via CopyInto at the point of receipt, into its own memory, rather than aliasing avpipe's Stream pointer -
// otherwise a reader goroutine racing the writer's own goroutine would race on *tracker.Stats's fields with no
// shared lock to prevent it (avpipe and content-fabric are separate modules, each with its own, disjoint mutex).
// The writer deliberately calls pushStatsInto with its own persistent, reused destination rather than the exported
// PushStats (which gathers into a fresh, one-off ExportedStats on every call - see PushStats's own doc): a fresh
// destination on every call never actually exercises the in-place-mutation hazard CopyInto exists to guard against
// (no single *tracker.Stats object is ever mutated across two calls), so even a shallow copy in place of CopyInto
// would have passed under -race with PushStats. This drives that reused-destination writer and a receiver simulating
// the live-recorder handler's copy-on-receipt discipline concurrently, under -race.
func TestMpegtsPacketProcessor_PushStats_ConcurrentReceiverCopyIsRaceFree(t *testing.T) {
	var mu sync.Mutex
	owned := &ExportedStats{} // mirrors l.StatusReport.InputStats: the receiver's own, reused destination

	opener := &recordingSequentialOpener{}
	opener.onStat = func(stats ExportedStats) {
		mu.Lock()
		defer mu.Unlock()
		stats.CopyInto(owned)
	}
	pp := newTestMpegtsPacketProcessor(opener)

	// Give the tracker real, multi-PID data to walk on every Snapshot call, so CopyInto has non-trivial nested
	// slices/pointers (Clocks, Ts.Streams, each *HistogramCapture) to detach, not just the zero-value early return.
	base := time.Unix(1000, 0)
	for pid := 100; pid < 105; pid++ {
		pkt := packet.New()
		pkt.SetPID(pid)
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, (*pkt)[:]))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer close(stop)
		var reused ExportedStats // exclusively owned by this goroutine, reused across calls like reportFullStatsLoop's
		for i := 0; i < 300; i++ {
			pp.pushStatsInto(&reused)
		}
	}()
	go func() {
		defer wg.Done()
		reader := &ExportedStats{}
		for {
			mu.Lock()
			owned.CopyInto(reader)
			mu.Unlock()
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	wg.Wait()
}

// TestMpegtsPacketProcessor_PushStats_ConcurrentCallersAreRaceFree is a regression test guarding against reintroducing
// the data race originally reported on avpipe PR #206: PushStats can run concurrently with itself (production callers
// are now reportFullStatsLoop's own ticker plus any manual/test caller of the exported PushStats). Each call gathers
// into its own one-off destination and touches no shared state, so this should be race-free by construction; this test
// exists to catch a future regression, not a currently-open bug. This drives two goroutines both calling PushStats()
// directly, with an opener that reads Stream's fields on receipt. A connStatsSource is wired in too, so the
// Srt-gathering branch of pushStatsInto runs under the same concurrent load.
func TestMpegtsPacketProcessor_PushStats_ConcurrentCallersAreRaceFree(t *testing.T) {
	var reportsMu sync.Mutex
	reports := 0

	opener := &recordingSequentialOpener{}
	opener.onStat = func(stats ExportedStats) {
		if stats.Stream != nil {
			_ = stats.Stream.Packets
			_ = stats.Stream.Errors.CcErrors
		}
		if stats.Srt != nil {
			_ = stats.Srt.Version
		}
		reportsMu.Lock()
		reports++
		reportsMu.Unlock()
	}
	pp := newTestMpegtsPacketProcessor(opener)
	pp.SetConnStatsSource(&fakeConnStatsSource{stats: mio.ConnStats{SRT: &mio.SrtConnStats{Version: 5}}, ok: true})

	// Give the tracker real, multi-PID data to walk on every Snapshot call, so each refresh has non-trivial nested
	// state to mutate/copy, not just the zero-value early return.
	base := time.Unix(1000, 0)
	for pid := 100; pid < 105; pid++ {
		pkt := packet.New()
		pkt.SetPID(pid)
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, (*pkt)[:]))
	}

	const callsPerGoroutine = 300
	var wg sync.WaitGroup
	wg.Add(2)
	for g := 0; g < 2; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < callsPerGoroutine; i++ {
				pp.PushStats()
			}
		}()
	}
	wg.Wait()

	require.Equal(t, 2*callsPerGoroutine, reports, "every PushStats call must have reached the opener")
	require.NotNil(t, opener.lastStat.Stream)
	require.NotNil(t, opener.lastStat.Srt)
}

// fakeConnStatsSource is a test double for mio.ConnStatsSource used to verify that MpegtsPacketProcessor's
// SetConnStatsSource wiring works end to end.
type fakeConnStatsSource struct {
	mu          sync.Mutex
	stats       mio.ConnStats
	ok          bool
	lastDetails bool
}

// ConnStats can be called concurrently by design, so this fake's own bookkeeping needs a lock, just like
// recordingSequentialOpener's.
func (f *fakeConnStatsSource) ConnStats(into *mio.ConnStats, details bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastDetails = details
	if !f.ok {
		return false
	}
	*into = f.stats
	return true
}

// TestMpegtsPacketProcessor_ReportBytesWritten verifies the lightweight stall-detection ping end to end, for both RTP
// and raw TS packaging (content-fabric's CopyModeRawOnly stall check applies independently of packaging - see
// ProcessDatagramPacket's doc): the first datagram triggers one prompt report (independent of the full-stats path - no
// tracker/connStats involvement at all), later datagrams don't trigger another prompt report, and a later
// reportBytesWritten call (simulating a reportBytesWrittenLoop tick) reflects the current, larger BytesWritten value.
func TestMpegtsPacketProcessor_ReportBytesWritten(t *testing.T) {
	base := time.Unix(1000, 0)

	t.Run("RTP", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessorRTP(opener)

		require.Empty(t, opener.bytesWrittenReports, "no datagram written yet")

		tsPkt := mustTSPacket()
		tsPkt.SetPID(7)
		datagram := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 9000}, tsPkt[:])
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))

		require.Len(t, opener.bytesWrittenReports, 1, "the first datagram triggers one prompt ping")
		require.EqualValues(t, pp.stats.BytesWritten.Load(), opener.bytesWrittenReports[0])
		require.Equal(t, len(datagram)+tlv.TLV_HEADER_LEN, int(pp.stats.BytesWritten.Load())) // output is TLV wrapped!

		tsPkt2 := mustTSPacket()
		tsPkt2.SetPID(7)
		datagram2 := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 2, Timestamp: 9010}, tsPkt2[:])
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram2))
		require.Len(t, opener.bytesWrittenReports, 1, "only the first datagram triggers a prompt ping")

		pp.reportBytesWritten() // simulates a reportBytesWrittenLoop tick
		require.Len(t, opener.bytesWrittenReports, 2)
		require.EqualValues(t, pp.stats.BytesWritten.Load(), opener.bytesWrittenReports[1])
		require.Greater(t, opener.bytesWrittenReports[1], opener.bytesWrittenReports[0],
			"BytesWritten grew after the second datagram")
		require.Equal(t, 2*(len(datagram)+tlv.TLV_HEADER_LEN), int(pp.stats.BytesWritten.Load())) // output is TLV wrapped!
	})

	t.Run("raw TS", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessor(opener)

		require.Empty(t, opener.bytesWrittenReports, "no datagram written yet")

		pkt := mustTSPacket()
		pkt.SetPID(7)
		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, pkt[:]))

		require.Len(t, opener.bytesWrittenReports, 1,
			"the first datagram triggers one prompt ping regardless of packaging")
		require.EqualValues(t, pp.stats.BytesWritten.Load(), opener.bytesWrittenReports[0])
		require.Equal(t, len(pkt), int(pp.stats.BytesWritten.Load())) // output is raw TS, so not TLV wrapped!
	})
}

func TestMpegtsPacketProcessorRTP(t *testing.T) {
	base := time.Unix(1000, 0)

	t.Run("decodes a well-formed RTP+TS datagram", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessorRTP(opener)

		tsPkt := mustTSPacket()
		tsPkt.SetPID(7) // non-null, so it's not also flagged as padding
		datagram := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 100, Timestamp: 9000}, tsPkt[:])

		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))

		require.Equal(t, 1, opener.opens)
		require.EqualValues(t, 1, pp.stats.PacketsWritten.Load())
		require.EqualValues(t, 0, statsOf(pp).RTP.BadPackets)
		require.EqualValues(t, 0, statsOf(pp).RTP.LongHeaders)
		require.Len(t, opener.bytesWrittenReports, 1, "the first datagram triggers a prompt reportBytesWritten")
	})

	t.Run("computes header length correctly with CSRC and extension present", func(t *testing.T) {
		// Regression guard for the header-length computation: must not use Header.MarshalSize() (can under-report for
		// extension-bearing packets - see pktpool.RtpPacket.decode's comment), but derive it from where the payload
		// actually landed instead. A wrong offset here would misalign every TS packet, failing CheckErrors().
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessorRTP(opener)

		hdr := pionrtp.Header{
			Version:        2,
			SequenceNumber: 1,
			Timestamp:      1,
			CSRC:           []uint32{0x11111111, 0x22222222},
		}
		require.NoError(t, hdr.SetExtension(1, []byte{0xAA, 0xBB, 0xCC, 0xDD}))

		tsPkt := mustTSPacket()
		tsPkt.SetPID(7)
		datagram := mustRTPDatagram(t, hdr, tsPkt[:])

		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))

		require.EqualValues(t, 0, statsOf(pp).RTP.BadPackets)
		require.EqualValues(t, 1, statsOf(pp).RTP.LongHeaders) // header is longer than the base 12 bytes
		require.EqualValues(t, 1, pp.stats.PacketsWritten.Load())
		require.EqualValues(t, 0, statsOf(pp).TS.BadPackets)
	})

	t.Run("excludes RTP padding from TS packet processing", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessorRTP(opener)

		tsPkt := mustTSPacket()
		tsPkt.SetPID(7)
		hdr := pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1, Padding: true, PaddingSize: 200}
		datagram := mustRTPDatagram(t, hdr, tsPkt[:])

		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))

		require.EqualValues(t, 0, statsOf(pp).RTP.BadPackets)
		require.EqualValues(t, 0, statsOf(pp).TS.ErrorsIncompletePackets)
		require.EqualValues(t, 1, pp.stats.PacketsWritten.Load())
		require.EqualValues(t, 0, statsOf(pp).TS.BadPackets)
	})

	t.Run("flags a non-188-aligned remainder as incomplete instead of silently truncating", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessorRTP(opener)

		tsPkt := mustTSPacket()
		tsPkt.SetPID(7)
		payload := append(append([]byte{}, tsPkt[:]...), []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}...)
		datagram := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, payload)

		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))

		require.EqualValues(t, 1, statsOf(pp).TS.ErrorsIncompletePackets)
		require.Equal(t, 0, opener.opens)
	})

	t.Run("rejects a non-version-2 packet", func(t *testing.T) {
		opener := &recordingSequentialOpener{}
		pp := newTestMpegtsPacketProcessorRTP(opener)

		tsPkt := mustTSPacket()
		datagram := mustRTPDatagram(t, pionrtp.Header{Version: 1, SequenceNumber: 1, Timestamp: 1}, tsPkt[:])

		pp.ProcessDatagramPacket(base, mustDatagramPacket(t, datagram))

		require.EqualValues(t, 1, statsOf(pp).RTP.BadPackets)
		require.Equal(t, 0, opener.opens)
	})
}

// statsOf returns pp's exported stats snapshot, for tests checking fields now sourced from mpp.tracker (see
// ExportedStats.populate) rather than a plain atomic field on pp.stats/pp.rtpStats. It reads mpp.tracker.Stats()
// directly (bypassing PushStats/pushStatsInto) so it always reflects the latest ProcessDatagramPacket call; SRT stats
// are out of scope for these tests, so srt is always nil here - see TestMpegtsPacketProcessorConnStats for that wiring.
func statsOf(pp *MpegtsPacketProcessor) ExportedStats {
	dst := ExportedStats{Stream: pp.tracker.Stats()}
	dst.populate(pp.stats, pp.rtpStats)
	return dst
}

// mustDatagramPacket returns a *pktpool.Packet loaded with data, for tests exercising ProcessDatagramPacket - the
// only entry point now that MpegtsPacketProcessor no longer accepts a plain []byte.
func mustDatagramPacket(t *testing.T, data []byte) *pktpool.Packet {
	t.Helper()
	// outputTlvWrapCap head room matches production pools (NetReader, av_input.go): RtpTs/AtsTs packaging frames its
	// output via Packet.FrameTlv, which needs this reserved space in front of the payload.
	pkt := pktpool.NewPacket(outputTlvWrapCap, len(data))
	require.NoError(t, pkt.From(data))
	return pkt
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
	lastStat ExportedStats
	// onStat, if set, is called with every Stat report in addition to recording it in lastStat - used by tests that
	// need to react to (e.g. copy out of) a report on the same goroutine that produced it.
	onStat func(ExportedStats)
	// statMu guards lastStat/onStat/lastBytesWritten against tests that call PushStats/ReportBytesWritten concurrently
	// from multiple goroutines (e.g. TestMpegtsPacketProcessor_PushStats_ConcurrentCallersAreRaceFree) -
	// Stat/ReportBytesWritten are the opener's own bookkeeping, not the production code under test, so they just need
	// to not be a race in their own right.
	statMu sync.Mutex
	// bytesWrittenReports records every ReportBytesWritten call, in order.
	bytesWrittenReports []uint64
}

func (o *recordingSequentialOpener) OpenNext() (io.WriteCloser, error) {
	o.opens++
	writer := &recordingWriteCloser{closeErr: o.closeErr}
	o.writers = append(o.writers, writer)
	return writer, nil
}

func (o *recordingSequentialOpener) Stat(stat any) error {
	o.statMu.Lock()
	defer o.statMu.Unlock()
	o.lastStat = stat.(ExportedStats)
	if o.onStat != nil {
		o.onStat(o.lastStat)
	}
	return nil
}

func (o *recordingSequentialOpener) ReportStart() error {
	return nil
}

func (o *recordingSequentialOpener) ReportBytesWritten(n uint64) error {
	o.statMu.Lock()
	defer o.statMu.Unlock()
	o.bytesWrittenReports = append(o.bytesWrittenReports, n)
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
