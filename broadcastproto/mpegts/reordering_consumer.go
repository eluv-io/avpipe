package mpegts

import (
	"time"

	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/common-go/media/rtp"
	"github.com/eluv-io/common-go/util/jsonutil"
)

// reorderShutdownFlushBound is the total wall-clock time the shutdown flush allows for draining held packets to
// the inner consumer. See ReorderingConsumer.run. Packets still unsent when it elapses are released instead.
// It is generous relative to MaxWait. A healthy inner consumer drains a full window well within this bound.
// It only matters if the inner consumer is itself stuck.
const reorderShutdownFlushBound = 500 * time.Millisecond

var _ Consumer = (*ReorderingConsumer)(nil)

// ReorderConsumerStats is a snapshot of ReorderingConsumer's counters. It combines the algorithm-level counts from
// rtp.ReorderStats with counters that only exist at the Consumer/pktpool boundary. The algorithm itself has no
// visibility into those.
type ReorderConsumerStats struct {
	rtp.ReorderStats
	// DroppedOnShutdown counts packets released unsent during the bounded shutdown flush. This happens when the
	// inner consumer's channel does not accept them within reorderShutdownFlushBound. Expected to be ~0 in practice.
	DroppedOnShutdown uint64 `json:"dropped_on_shutdown"`
}

// ReorderingConsumer wraps a Consumer to correct short-range RTP sequence-number reordering before packets reach
// it. This prevents ordinary network-level reordering from being misdiagnosed as loss by the inner consumer's own
// packet-ordering logic, e.g. MpegtsPacketProcessor's continuity-counter/gap tracking. It implements Consumer
// itself, so NetReader treats it like any other consumer. No changes to NetReader are required.
//
// Only wrap a consumer receiving transport.RtpTs packaging. That is the only packaging carrying the RTP sequence
// number this correction keys on. RawTs and AtsTs packaging have no per-datagram sequence number to key on.
type ReorderingConsumer struct {
	inner Consumer
	in    chan pktpool.Resource

	buf *rtp.ReorderBuffer[pktpool.Resource]

	reordered         atomic.Uint64
	lostAfterTimeout  atomic.Uint64
	lateDropped       atomic.Uint64
	duplicate         atomic.Uint64
	resyncs           atomic.Uint64
	maxReorderDelta   atomic.Int64
	droppedOnShutdown atomic.Uint64
}

// NewReorderingConsumer creates a ReorderingConsumer wrapping inner. maxWindow, maxWait and maxJump are passed
// straight through to rtp.NewReorderBuffer. See its doc for their meaning and defaults. NewReorderingConsumer
// starts its own goroutine immediately. Call Chan() to get the channel NetReader should send packets to, in place
// of inner.Chan().
func NewReorderingConsumer(inner Consumer, maxWindow int, maxWait time.Duration, maxJump int) *ReorderingConsumer {
	// Construct the buffer first and size r.in off its own MaxWindow(), not the raw maxWindow parameter.
	// rtp.NewReorderBuffer clamps maxWindow, both a non-positive value and an excessively large one, before this
	// point. Sizing the channel from the raw parameter instead would risk an excessive or failing allocation for
	// the same maxWindow the buffer itself was built to clamp away.
	buf := rtp.NewReorderBuffer[pktpool.Resource](maxWindow, maxWait, maxJump)
	r := &ReorderingConsumer{
		inner: inner,
		in:    make(chan pktpool.Resource, 2*buf.MaxWindow()),
		buf:   buf,
	}
	go r.run()
	return r
}

// reorderingConsumerFor decides whether inner needs reordering correction and returns the Consumer NetReader
// should actually use in its place. Only transport.RtpTs packaging carries the per-datagram RTP sequence number
// this correction keys on, and even then it is opt-in via cfg.Enabled. If both hold, it wraps inner in a new
// ReorderingConsumer, wires it as inner's reorder-stats source, and returns the wrapper. Otherwise it returns
// inner unchanged, warning if cfg.Enabled asked for correction that this packaging cannot support. fd and url are
// only used for logging. custom.go and bypass.go both call this, so their wiring can never drift apart.
func reorderingConsumerFor(
	inner *MpegTsConsumer, packaging transport.TsPackagingMode, cfg goavpipe.ReorderBufferConfig,
	fd int64, url string,
) Consumer {
	if packaging != transport.RtpTs || !cfg.Enabled {
		if cfg.Enabled {
			goavpipe.Log.Warn("reorder_buffer.enabled is set but packaging does not support it; reordering "+
				"correction will not be applied",
				"fd", fd,
				"url", url,
				"packaging", packaging,
			)
		}
		return inner
	}

	cfg = cfg.ApplyDefaults()
	goavpipe.Log.Debug("enabling packet reordering buffer",
		"fd", fd,
		"url", url,
		"config", jsonutil.Stringer(cfg),
	)
	reorderingConsumer := NewReorderingConsumer(inner, cfg.MaxWindow, cfg.MaxWait.Duration(), cfg.MaxJump)
	inner.pp.SetReorderStatsSource(reorderingConsumer)
	return reorderingConsumer
}

func (r *ReorderingConsumer) Name() string { return r.inner.Name() }

func (r *ReorderingConsumer) Chan() chan<- pktpool.Resource { return r.in }

// PacketDropped forwards to the inner consumer's own counter. This is the one drop counter for this consumer.
// NetReader calls it directly when its own non-blocking send into Chan() (r.in) is full. admit also calls it,
// for a late-arriving packet dropped inside the buffer itself. See admit's doc. Both call sites feed the same
// counter, so anything watching it sees every drop in this pipeline.
func (r *ReorderingConsumer) PacketDropped() { r.inner.PacketDropped() }

// Stats returns a snapshot of this consumer's reorder-correction counters.
func (r *ReorderingConsumer) Stats() ReorderConsumerStats {
	return ReorderConsumerStats{
		ReorderStats: rtp.ReorderStats{
			Reordered:        r.reordered.Load(),
			MaxReorderDelta:  r.maxReorderDelta.Load(),
			LostAfterTimeout: r.lostAfterTimeout.Load(),
			LateDropped:      r.lateDropped.Load(),
			Duplicate:        r.duplicate.Load(),
			Resyncs:          r.resyncs.Load(),
			CurrentOccupancy: 0, // a gauge, meaningful only inside run's own goroutine, so not exposed across goroutines
		},
		DroppedOnShutdown: r.droppedOnShutdown.Load(),
	}
}

// run is the consumer's single goroutine. It owns r.buf, the one timer, and every decision about what to forward
// to r.inner.Chan(). It is the only goroutine that ever touches r.buf. This satisfies the buffer's own
// no-concurrency contract without any locking here.
func (r *ReorderingConsumer) run() {
	var timer *time.Timer
	var timerC <-chan time.Time
	scratch := make([]pktpool.Resource, 0, r.buf.MaxWindow()+1)

	resetTimer := func() {
		deadline, ok := r.buf.Deadline()
		if !ok {
			if timer != nil {
				timer.Stop()
			}
			timerC = nil
			return
		}
		wait := time.Until(deadline)
		if wait < 0 {
			wait = 0
		}
		if timer == nil {
			timer = time.NewTimer(wait)
			timerC = timer.C
			return
		}
		timer.Stop()
		select {
		case <-timer.C:
		default:
		}
		timer.Reset(wait)
	}

	for {
		select {
		case res, ok := <-r.in:
			if !ok {
				r.shutdown(timer)
				return
			}
			r.admit(res, scratch[:0])
			resetTimer()

		case <-timerC:
			emitted := r.buf.Expire(time.Now(), scratch[:0])
			for _, item := range emitted {
				r.forward(item)
			}
			r.syncStats()
			resetTimer()
		}
	}
}

// admit decodes the RTP sequence number and pushes the packet into the reorder buffer, forwarding whatever becomes
// releasable as a result.
func (r *ReorderingConsumer) admit(res pktpool.Resource, scratch []pktpool.Resource) {
	rtpLayer, err := res.T.Rtp()
	if err != nil {
		// The RTP header is malformed, so there is nothing to key on. Forward the packet as-is instead of letting
		// a bad packet wedge the window. The inner consumer's own validation will flag it.
		r.forward(res)
		return
	}
	seq := rtpLayer.Packet().Header.SequenceNumber
	emitted, dropped := r.buf.Push(time.Now(), seq, res, scratch)
	for _, item := range emitted {
		r.forward(item)
	}
	if dropped {
		// res arrived after its gap had already resolved, so Push dropped it. Release it here so the pooled
		// resource is not leaked. Count it through PacketDropped() too, the same counter every other drop in
		// this pipeline goes through. See PacketDropped's doc. This keeps the drop visible to whatever is
		// watching that counter.
		res.Release()
		r.PacketDropped()
	}
	r.syncStats()
}

// syncStats copies the buffer's current stats into this consumer's atomics. Stats() may be called concurrently,
// from the stats-reporting goroutine. This lets it reflect up-to-date counts without touching r.buf itself from
// outside run's goroutine.
func (r *ReorderingConsumer) syncStats() {
	s := r.buf.Stats()
	r.reordered.Store(s.Reordered)
	r.lostAfterTimeout.Store(s.LostAfterTimeout)
	r.lateDropped.Store(s.LateDropped)
	r.duplicate.Store(s.Duplicate)
	r.resyncs.Store(s.Resyncs)
	r.maxReorderDelta.Store(s.MaxReorderDelta)
}

// forward sends res to the inner consumer's channel. Outside shutdown this is a plain, unconditional blocking
// send. That is deliberate, not an oversight. forward is only ever called with an item Push has already decided
// to release, so it never drops anything itself and needs no counter of its own.
//
// A blocked send here would simply stall run's loop. That in turn fills r.in, which is exactly what makes
// NetReader's own non-blocking-send-and-count path take over. See PacketDropped's doc. forward introduces no
// additional, uncounted drop site. admit's late-arrival path does drop, but that is documented and counted there.
func (r *ReorderingConsumer) forward(res pktpool.Resource) {
	r.inner.Chan() <- res
}

// shutdown drains whatever the buffer still holds, in sequence order, and forwards it to the inner consumer.
// Each send is bounded by one shared deadline for the whole flush, not a per-item timeout. A per-item timeout
// would starve later items in the flush. shutdown then closes the inner consumer's channel, propagating shutdown
// to it exactly as NetReader would have done directly, before this wrapper existed.
func (r *ReorderingConsumer) shutdown(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
	flushed := r.buf.Flush(make([]pktpool.Resource, 0, r.buf.MaxWindow()))
	r.syncStats()
	if len(flushed) > 0 {
		deadline := time.Now().Add(reorderShutdownFlushBound)
		for _, res := range flushed {
			r.flushForward(res, deadline)
		}
	}
	close(r.inner.Chan())
}

// flushForward sends res to the inner consumer, but only until deadline. If the inner consumer's channel does not
// accept it in time, flushForward releases res unsent and counts DroppedOnShutdown. Every pktpool.Resource must be
// accounted for exactly once, either forwarded or released.
func (r *ReorderingConsumer) flushForward(res pktpool.Resource, deadline time.Time) {
	select {
	case r.inner.Chan() <- res:
	case <-time.After(time.Until(deadline)):
		res.Release()
		r.droppedOnShutdown.Add(1)
	}
}
