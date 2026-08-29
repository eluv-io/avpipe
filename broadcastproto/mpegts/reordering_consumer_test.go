package mpegts

import (
	"math"
	"testing"
	"time"

	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/pktpool"
)

// newRtpPool creates a packet pool sized like production pools (see mustDatagramPacket).
func newRtpPool() *pktpool.Pool {
	return pktpool.NewPacketPool(outputTlvWrapCap, 2048)
}

// borrowRtpResource borrows a packet from pool and loads it with a well-formed single-TS-packet RTP-TS datagram
// carrying the given sequence number.
func borrowRtpResource(t *testing.T, pool *pktpool.Pool, seq uint16) pktpool.Resource {
	t.Helper()
	tsPkt := mustTSPacket()
	datagram := mustRTPDatagram(t, pionrtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq)}, tsPkt[:])
	res := pool.Borrow()
	require.NoError(t, res.T.From(datagram))
	return res
}

// sendNonBlocking mimics NetReader.readLoop's own admission into a consumer's channel: a non-blocking send,
// falling back to PacketDropped() (and releasing the resource) if the channel is full. Used so backpressure tests
// exercise the exact same drop path production goes through, without needing a real NetReader.
func sendNonBlocking(c Consumer, res pktpool.Resource) (sent bool) {
	select {
	case c.Chan() <- res:
		return true
	default:
		res.Release()
		c.PacketDropped()
		return false
	}
}

// TestReorderingConsumer_PassThrough verifies that packets already in order pass straight through, unbuffered.
func TestReorderingConsumer_PassThrough(t *testing.T) {
	defer goleak.VerifyNone(t)
	pool := newRtpPool()
	inner := &testConsumer{pktChan: make(chan pktpool.Resource, 10)}
	r := NewReorderingConsumer(inner, 8, 20*time.Millisecond, 0)

	for _, seq := range []uint16{1, 2, 3} {
		r.Chan() <- borrowRtpResource(t, pool, seq)
	}

	for _, want := range []uint16{1, 2, 3} {
		res := <-inner.pktChan
		rtpLayer, err := res.T.Rtp()
		require.NoError(t, err)
		require.EqualValues(t, want, rtpLayer.Packet().Header.SequenceNumber)
		res.Release()
	}

	close(r.Chan())
	_, ok := <-inner.pktChan
	require.False(t, ok, "inner channel must be closed once the decorator shuts down")

	stats := r.Stats()
	require.Zero(t, stats.Reordered)
	require.Zero(t, stats.LostAfterTimeout)
	require.EqualValues(t, 0, pool.Stats().Borrowed-pool.Stats().Returned, "every borrowed resource must be released")
}

// TestReorderingConsumer_OneReorderCorrected verifies that a single out-of-order pair is corrected before reaching
// the inner consumer.
func TestReorderingConsumer_OneReorderCorrected(t *testing.T) {
	defer goleak.VerifyNone(t)
	pool := newRtpPool()
	inner := &testConsumer{pktChan: make(chan pktpool.Resource, 10)}
	r := NewReorderingConsumer(inner, 8, 20*time.Millisecond, 0)

	r.Chan() <- borrowRtpResource(t, pool, 1)
	r.Chan() <- borrowRtpResource(t, pool, 3)
	r.Chan() <- borrowRtpResource(t, pool, 2)

	for _, want := range []uint16{1, 2, 3} {
		res := <-inner.pktChan
		rtpLayer, err := res.T.Rtp()
		require.NoError(t, err)
		require.EqualValues(t, want, rtpLayer.Packet().Header.SequenceNumber, "must arrive in corrected order")
		res.Release()
	}

	// Give the stats-sync inside run() a moment to catch up (it runs after the forward loop, in the same goroutine
	// iteration that released these packets to inner.pktChan, so by the time we've drained inner.pktChan the stats
	// update for that same Push call has already happened - no sleep needed).
	require.EqualValues(t, 1, r.Stats().Reordered)

	close(r.Chan())
	_, ok := <-inner.pktChan
	require.False(t, ok)
	require.EqualValues(t, 0, pool.Stats().Borrowed-pool.Stats().Returned)
}

// TestReorderingConsumer_Backpressure verifies that when the inner consumer's channel is never drained, the drop
// still surfaces through the exact same PacketDropped() path NetReader would exercise directly - the decorator
// introduces no second, invisible drop point.
func TestReorderingConsumer_Backpressure(t *testing.T) {
	defer goleak.VerifyNone(t)
	pool := newRtpPool()
	// inner's channel has capacity 1 and is never drained by the test, so it fills almost immediately once the
	// decorator starts forwarding into it.
	inner := &testConsumer{pktChan: make(chan pktpool.Resource, 1)}
	// maxWindow=1 keeps r.in small (capacity 2) so backpressure on r.in itself is reachable quickly too.
	r := NewReorderingConsumer(inner, 1, time.Hour, 0)

	sent := 0
	dropped := 0
	for seq := uint16(1); seq <= 20; seq++ {
		res := borrowRtpResource(t, pool, seq)
		if sendNonBlocking(r, res) {
			sent++
		} else {
			dropped++
		}
	}
	require.Positive(t, dropped, "sending in-order packets fast enough must eventually hit backpressure and drop")
	require.EqualValues(t, dropped, inner.pktDropped.Load(),
		"drops must be counted via PacketDropped(), forwarded straight to the inner consumer's own counter")

	// Drain what did get through so the goroutine can finish forwarding and the test can shut down cleanly.
	close(r.Chan())
	for res := range inner.pktChan {
		res.Release()
	}
	require.EqualValues(t, 0, pool.Stats().Borrowed-pool.Stats().Returned)
}

// TestReorderingConsumer_ShutdownMidWindow verifies that closing Chan() while packets are held flushes the
// remainder to the inner consumer in order, then closes the inner consumer's channel, with every borrowed resource
// accounted for (no leak).
func TestReorderingConsumer_ShutdownMidWindow(t *testing.T) {
	defer goleak.VerifyNone(t)
	pool := newRtpPool()
	inner := &testConsumer{pktChan: make(chan pktpool.Resource, 10)}
	r := NewReorderingConsumer(inner, 8, time.Hour, 0) // maxWait large: shutdown flush, not a timeout, must release these

	r.Chan() <- borrowRtpResource(t, pool, 1) // released immediately by run()
	res1 := <-inner.pktChan
	rtpLayer, err := res1.T.Rtp()
	require.NoError(t, err)
	require.EqualValues(t, 1, rtpLayer.Packet().Header.SequenceNumber)
	res1.Release()

	// 3 and 4 arrive ahead of the still-missing 2 and stay held in the window. r.in is a buffered channel, so these
	// sends complete immediately regardless of whether run() has processed them yet; closing it below is still race
	// -free, because a close on a buffered channel does not discard values already in the buffer - run()'s receive
	// loop drains 3 and 4 (admitting them into the window) before it ever observes the channel as closed.
	r.Chan() <- borrowRtpResource(t, pool, 3)
	r.Chan() <- borrowRtpResource(t, pool, 4)

	close(r.Chan())

	var got []uint16
	for res := range inner.pktChan {
		rtpLayer, err := res.T.Rtp()
		require.NoError(t, err)
		got = append(got, rtpLayer.Packet().Header.SequenceNumber)
		res.Release()
	}
	require.Equal(t, []uint16{3, 4}, got, "the held remainder must flush in ascending order on shutdown")
	require.EqualValues(t, 0, pool.Stats().Borrowed-pool.Stats().Returned)
}

// TestReorderingConsumer_LateDroppedReleasesResource verifies that a packet arriving already behind nextExpected -
// too late to place in order - has its pooled resource released, not leaked, and is counted through the inner
// consumer's own PacketDropped(), the same counter every other drop in this pipeline goes through. ReorderBuffer.
// Push silently omits a late-dropped item from its own emitted slice (it is neither forwarded nor held for later),
// so admit must notice and handle it itself.
func TestReorderingConsumer_LateDroppedReleasesResource(t *testing.T) {
	defer goleak.VerifyNone(t)
	pool := newRtpPool()
	inner := &testConsumer{pktChan: make(chan pktpool.Resource, 10)}
	r := NewReorderingConsumer(inner, 4, time.Hour, 0)

	r.Chan() <- borrowRtpResource(t, pool, 10) // establishes nextExpected=11, released immediately
	res := <-inner.pktChan
	res.Release()

	r.Chan() <- borrowRtpResource(t, pool, 5) // far behind nextExpected: late-dropped, never forwarded

	// A subsequent in-order packet, once observed on inner.pktChan, guarantees run() has already finished processing
	// (and releasing) packet 5 first - the single run() goroutine processes admit() calls strictly in the order
	// packets arrived on r.Chan().
	r.Chan() <- borrowRtpResource(t, pool, 11)
	res = <-inner.pktChan
	res.Release()

	require.EqualValues(t, 1, r.Stats().LateDropped)
	require.EqualValues(t, 1, inner.pktDropped.Load(),
		"a late-dropped packet must be counted through PacketDropped(), not just ReorderStats.LateDropped")

	close(r.Chan())
	_, ok := <-inner.pktChan
	require.False(t, ok)
	require.EqualValues(t, 0, pool.Stats().Borrowed-pool.Stats().Returned,
		"the late-dropped packet's resource must be released, not leaked")
}

// TestReorderingConsumer_DuplicateHeldReleasesResource verifies that a packet duplicating one already held has its
// pooled resource released, not leaked, and is counted through both ReorderStats.Duplicate and PacketDropped() -
// mirroring TestReorderingConsumer_LateDroppedReleasesResource for the duplicate-admission path in ReorderBuffer.
// Push instead of the late-arrival one.
func TestReorderingConsumer_DuplicateHeldReleasesResource(t *testing.T) {
	defer goleak.VerifyNone(t)
	pool := newRtpPool()
	inner := &testConsumer{pktChan: make(chan pktpool.Resource, 10)}
	r := NewReorderingConsumer(inner, 4, time.Hour, 0)

	r.Chan() <- borrowRtpResource(t, pool, 10) // establishes nextExpected=11, released immediately
	res := <-inner.pktChan
	res.Release()

	r.Chan() <- borrowRtpResource(t, pool, 13) // held, waiting on 11 and 12
	r.Chan() <- borrowRtpResource(t, pool, 13) // duplicate of the held packet: must be dropped, not swapped in

	// Fill the gap. Once these are observed on inner.pktChan, run() has finished processing (and releasing) the
	// duplicate first - the single run() goroutine processes admit() calls strictly in the order packets arrived.
	r.Chan() <- borrowRtpResource(t, pool, 11)
	r.Chan() <- borrowRtpResource(t, pool, 12)
	for i := 0; i < 3; i++ { // 11, 12, then the held 13 cascades out behind them
		res = <-inner.pktChan
		res.Release()
	}

	require.EqualValues(t, 1, r.Stats().Duplicate)
	require.EqualValues(t, 1, inner.pktDropped.Load(),
		"a duplicate must be counted through PacketDropped(), not just ReorderStats.Duplicate")

	close(r.Chan())
	_, ok := <-inner.pktChan
	require.False(t, ok)
	require.EqualValues(t, 0, pool.Stats().Borrowed-pool.Stats().Returned,
		"the duplicate's resource must be released, not leaked")
}

// TestNewReorderingConsumer_ChannelSizedFromClampedWindow verifies that r.in is sized from the buffer's own,
// already-clamped MaxWindow(), not the raw maxWindow parameter. An absurdly large maxWindow must not reach
// make(chan ...) unclamped, which would risk an excessive or failing channel allocation.
func TestNewReorderingConsumer_ChannelSizedFromClampedWindow(t *testing.T) {
	inner := &testConsumer{pktChan: make(chan pktpool.Resource, 1)}

	var r *ReorderingConsumer
	require.NotPanics(t, func() {
		r = NewReorderingConsumer(inner, math.MaxInt64/4+1000, time.Hour, 0)
	})

	require.LessOrEqual(t, cap(r.in), 2*32768,
		"r.in must be sized from the buffer's clamped MaxWindow(), not the raw maxWindow parameter")

	close(r.Chan())
}

// newTestMpegTsConsumer builds an MpegTsConsumer around a real MpegtsPacketProcessor with RtpTs packaging, so
// reorderingConsumerFor's SetReorderStatsSource wiring has somewhere real to attach to.
func newTestMpegTsConsumer() *MpegTsConsumer {
	return &MpegTsConsumer{
		pktChan: make(chan pktpool.Resource, 10),
		pp:      newTestMpegtsPacketProcessorRTP(&recordingSequentialOpener{}),
	}
}

// TestReorderingConsumerFor_Enabled verifies that RtpTs packaging plus an enabled config makes
// reorderingConsumerFor wrap inner in a ReorderingConsumer and wire it as inner.pp's reorder-stats source - this is
// the one condition under which correction actually applies.
func TestReorderingConsumerFor_Enabled(t *testing.T) {
	inner := newTestMpegTsConsumer()

	consumer := reorderingConsumerFor(inner, transport.RtpTs, goavpipe.ReorderBufferConfig{Enabled: true}, 1, "url")

	rc, ok := consumer.(*ReorderingConsumer)
	require.True(t, ok, "must wrap inner in a ReorderingConsumer")
	require.Same(t, rc, inner.pp.reorderStats, "must wire itself as inner.pp's reorder-stats source")

	close(rc.Chan())
}

// TestReorderingConsumerFor_PackagingMismatch verifies that a config asking for correction under packaging that
// carries no per-datagram sequence number (anything but RtpTs) is left unapplied: inner is returned unchanged,
// unwrapped, and untouched.
func TestReorderingConsumerFor_PackagingMismatch(t *testing.T) {
	inner := newTestMpegTsConsumer()

	consumer := reorderingConsumerFor(inner, transport.RawTs, goavpipe.ReorderBufferConfig{Enabled: true}, 1, "url")

	require.Same(t, inner, consumer, "must return inner unchanged when packaging cannot support correction")
	require.Nil(t, inner.pp.reorderStats, "must not wire a reorder-stats source when correction is not applied")
}

// TestReorderingConsumerFor_Disabled verifies that RtpTs packaging alone, without cfg.Enabled, does not apply
// correction - it is opt-in, not automatic whenever the packaging supports it.
func TestReorderingConsumerFor_Disabled(t *testing.T) {
	inner := newTestMpegTsConsumer()

	consumer := reorderingConsumerFor(inner, transport.RtpTs, goavpipe.ReorderBufferConfig{Enabled: false}, 1, "url")

	require.Same(t, inner, consumer, "must return inner unchanged when disabled")
	require.Nil(t, inner.pp.reorderStats)
}
