package mpegts

import (
	"encoding/binary"
	"testing"
	"time"

	pionrtp "github.com/pion/rtp"
	"go.uber.org/atomic"

	"github.com/eluv-io/common-go/media/pktpool"
)

// Baseline (median of 5 runs, via benchstat)
//
// go test ./broadcastproto/mpegts/... -run '^$' -bench 'BenchmarkDiscardConsumer|BenchmarkReorderingConsumer' -benchmem -count=5 > /tmp/bench.txt
// benchstat /tmp/bench.txt
//
// goos: darwin
// goarch: arm64
// pkg: github.com/eluv-io/avpipe/broadcastproto/mpegts
// cpu: Apple M4 Max
//                                  │ /tmp/bench.txt │
//                                  │     sec/op      │
// DiscardConsumer_InOrder-14                293.6n ± ∞ ¹
// ReorderingConsumer_InOrder-14             366.2n ± ∞ ¹
// DiscardConsumer_Reordered-14              593.5n ± ∞ ¹
// ReorderingConsumer_Reordered-14           749.8n ± ∞ ¹
// geomean                                   467.7n
// ¹ need >= 6 samples for confidence interval at level 0.95
//
//                                  │ /tmp/bench.txt │
//                                  │      B/op       │
// DiscardConsumer_InOrder-14                 0.000 ± ∞ ¹
// ReorderingConsumer_InOrder-14              1.000 ± ∞ ¹
// DiscardConsumer_Reordered-14               1.000 ± ∞ ¹
// ReorderingConsumer_Reordered-14            2.000 ± ∞ ¹
// ¹ need >= 6 samples for confidence interval at level 0.95
//
//                                  │ /tmp/bench.txt │
//                                  │    allocs/op    │
// DiscardConsumer_InOrder-14                 0.000 ± ∞ ¹
// ReorderingConsumer_InOrder-14              0.000 ± ∞ ¹
// DiscardConsumer_Reordered-14               0.000 ± ∞ ¹
// ReorderingConsumer_Reordered-14            0.000 ± ∞ ¹
// ¹ need >= 6 samples for confidence interval at level 0.95
//
// B/op is nonzero here even though allocs/op is 0. Both are MemStats delta / b.N using integer division. b.N is in
// the millions, so a few hundred allocations round down to 0 for the count. Each of those allocations is a full
// pktpool.Packet buffer, large enough that the byte total still rounds up to 1-2 bytes/op.
//
// A memory profile (-memprofilerate=1) traces these bytes to pktpool.NewPacket, called from Pool.Borrow when
// sync.Pool's local cache is empty. Neither ReorderBuffer nor ReorderingConsumer allocates on their own hot path.
// The cache goes empty more often the more pooled resources are concurrently in flight: ReorderingConsumer adds a
// goroutine hop between borrow and release, and the Reordered pattern holds one packet per swapped pair until its
// partner arrives. Both delay release, so more resources are checked out from the pool at once, and B/op rises
// accordingly: DiscardConsumer_InOrder (no extra hop, no held packet) has none of this pressure, each of
// ReorderingConsumer_InOrder and DiscardConsumer_Reordered has one source of it, and
// ReorderingConsumer_Reordered has both.

// discardConsumer is a Consumer that releases every packet sent to it, on its own goroutine, doing nothing else -
// a zero-processing baseline for measuring what ReorderingConsumer adds on top of a bare Consumer. It also counts
// PacketDropped() calls and closes done once its channel is closed and drained, so benchmarks can verify afterward
// that nothing was silently dropped or left unprocessed.
type discardConsumer struct {
	pktChan chan pktpool.Resource
	dropped atomic.Uint64
	done    chan struct{}
}

func newDiscardConsumer(chanCap int) *discardConsumer {
	d := &discardConsumer{pktChan: make(chan pktpool.Resource, chanCap), done: make(chan struct{})}
	go func() {
		defer close(d.done)
		for res := range d.pktChan {
			res.Release()
		}
	}()
	return d
}

func (d *discardConsumer) Name() string                  { return "discard" }
func (d *discardConsumer) Chan() chan<- pktpool.Resource { return d.pktChan }
func (d *discardConsumer) PacketDropped()                { d.dropped.Add(1) }

// benchRtpDatagramTemplate marshals a single well-formed RTP-TS datagram (seq 0) usable as a mutable template: the
// sequence number lives at a fixed offset (RFC 3550), so benchmarks can stamp in a new value per iteration with
// binary.BigEndian.PutUint16 instead of re-marshaling a packet on every iteration.
func benchRtpDatagramTemplate(b *testing.B) []byte {
	b.Helper()
	tsPkt := mustTSPacket()
	pkt := pionrtp.Packet{
		Header:  pionrtp.Header{Version: 2, SequenceNumber: 0, Timestamp: 0},
		Payload: tsPkt[:],
	}
	data, err := pkt.Marshal()
	if err != nil {
		b.Fatal(err)
	}
	return data
}

// sendBenchPacket patches seq into datagram's RTP sequence-number field, borrows a fresh pooled resource loaded
// with the result, and sends it on ch - the same per-packet cost every benchmark in this file is built from, so
// comparisons between them isolate the receiving consumer's own overhead.
func sendBenchPacket(b *testing.B, pool *pktpool.Pool, datagram []byte, seq uint16, ch chan<- pktpool.Resource) {
	b.Helper()
	binary.BigEndian.PutUint16(datagram[2:4], seq)
	res := pool.Borrow()
	if err := res.T.From(datagram); err != nil {
		b.Fatal(err)
	}
	ch <- res
}

// verifyNoDropsOrLeaks closes inner's channel, waits for its drain goroutine to finish, then fails the benchmark if
// any packet was dropped or any pooled resource was never released. A benchmark that silently drops packets instead
// of processing them would report a misleadingly fast number, so this turns that failure mode into a hard error
// instead of a number nobody double-checks.
func verifyNoDropsOrLeaks(b *testing.B, pool *pktpool.Pool, inner *discardConsumer) {
	b.Helper()
	<-inner.done
	if dropped := inner.dropped.Load(); dropped != 0 {
		b.Fatalf("%d packets dropped", dropped)
	}
	if leaked := pool.Stats().Borrowed - pool.Stats().Returned; leaked != 0 {
		b.Fatalf("%d resources leaked", leaked)
	}
}

// BenchmarkDiscardConsumer_InOrder measures the cost of sending a steady stream of in-order packets straight to a
// bare Consumer - the floor BenchmarkReorderingConsumer_InOrder is measured against.
func BenchmarkDiscardConsumer_InOrder(b *testing.B) {
	pool := pktpool.NewPacketPool(outputTlvWrapCap, 2048)
	datagram := benchRtpDatagramTemplate(b)
	c := newDiscardConsumer(1024)

	b.ReportAllocs()
	b.ResetTimer()

	seq := uint16(0)
	for b.Loop() {
		sendBenchPacket(b, pool, datagram, seq, c.pktChan)
		seq++
	}

	b.StopTimer()
	close(c.pktChan)
	verifyNoDropsOrLeaks(b, pool, c)
}

// BenchmarkReorderingConsumer_InOrder measures the same in-order stream through a ReorderingConsumer wrapping a
// discardConsumer, isolating the decorator's own overhead (RTP header parse, ReorderBuffer's fast path, the extra
// channel hop) from whatever the inner consumer does. Every packet already arrives in order, the common case, so
// the buffer never actually holds anything.
func BenchmarkReorderingConsumer_InOrder(b *testing.B) {
	pool := pktpool.NewPacketPool(outputTlvWrapCap, 2048)
	datagram := benchRtpDatagramTemplate(b)
	inner := newDiscardConsumer(1024)
	r := NewReorderingConsumer(inner, 32, 20*time.Millisecond, 0)

	b.ReportAllocs()
	b.ResetTimer()

	seq := uint16(0)
	for b.Loop() {
		sendBenchPacket(b, pool, datagram, seq, r.Chan())
		seq++
	}

	b.StopTimer()
	close(r.Chan())
	verifyNoDropsOrLeaks(b, pool, inner)
	if stats := r.Stats(); stats.LostAfterTimeout != 0 || stats.LateDropped != 0 || stats.DroppedOnShutdown != 0 {
		b.Fatalf("unexpected loss: %+v", stats)
	}
}

// BenchmarkDiscardConsumer_Reordered measures the same swapped-pair stream as BenchmarkReorderingConsumer_Reordered
// (seq n+1 arrives before seq n, for every pair) sent straight to a bare Consumer, which does not care about order -
// the floor that benchmark is measured against.
func BenchmarkDiscardConsumer_Reordered(b *testing.B) {
	pool := pktpool.NewPacketPool(outputTlvWrapCap, 2048)
	datagram := benchRtpDatagramTemplate(b)
	c := newDiscardConsumer(1024)

	b.ReportAllocs()
	b.ResetTimer()

	seq := uint16(0)
	for b.Loop() {
		sendBenchPacket(b, pool, datagram, seq+1, c.pktChan)
		sendBenchPacket(b, pool, datagram, seq, c.pktChan)
		seq += 2
	}

	b.StopTimer()
	close(c.pktChan)
	verifyNoDropsOrLeaks(b, pool, c)
}

// BenchmarkReorderingConsumer_Reordered measures a steady stream of single-pair swaps (seq n+1 arrives before seq n,
// for every pair) through ReorderingConsumer wrapping a discardConsumer - the buffer's actual reason to exist,
// mirroring rtp.BenchmarkReorderBuffer_OutOfOrderRecovery in common-go but exercised through the full Consumer
// decorator (channel hop, RTP header parse, timer bookkeeping) rather than calling Push directly.
func BenchmarkReorderingConsumer_Reordered(b *testing.B) {
	pool := pktpool.NewPacketPool(outputTlvWrapCap, 2048)
	datagram := benchRtpDatagramTemplate(b)
	inner := newDiscardConsumer(1024)
	r := NewReorderingConsumer(inner, 32, 20*time.Millisecond, 0)

	// Prime the buffer with a single packet before timing starts: ReorderBuffer treats the very first packet it
	// ever sees as the reference point and releases it unconditionally, regardless of its sequence number. Without
	// this, the timed loop's first "seq" packet would arrive already behind that reference point and count as a
	// spurious late drop instead of exercising the swap pattern every other iteration does. The channel preserves
	// send order, so this is guaranteed to be processed before anything sent below - no extra synchronization needed.
	sendBenchPacket(b, pool, datagram, 0, r.Chan())

	b.ReportAllocs()
	b.ResetTimer()

	seq := uint16(1)
	for b.Loop() {
		// Push seq+1 first (held), then seq (fills the gap and cascades seq+1 out) - one corrected swap per
		// iteration, same pattern as common-go's BenchmarkReorderBuffer_OutOfOrderRecovery.
		sendBenchPacket(b, pool, datagram, seq+1, r.Chan())
		sendBenchPacket(b, pool, datagram, seq, r.Chan())
		seq += 2
	}

	b.StopTimer()
	close(r.Chan())
	verifyNoDropsOrLeaks(b, pool, inner)
	stats := r.Stats()
	if stats.LostAfterTimeout != 0 || stats.LateDropped != 0 || stats.DroppedOnShutdown != 0 {
		b.Fatalf("unexpected loss: %+v", stats)
	}
	if stats.Reordered == 0 {
		b.Fatal("expected every swapped pair to be corrected, but Reordered stat is 0")
	}
}
