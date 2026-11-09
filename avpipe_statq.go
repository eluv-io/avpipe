package avpipe

import (
	"strings"
	"time"

	"github.com/eluv-io/avpipe/goavpipe"
)

// ancQueueDepth is how many ANC packets wait for the consumer: about four seconds at 60 frames a second.
const ancQueueDepth = 256

// ancCloseWait bounds how long closing a live input waits for a consumer that is still inside Stat.
const ancCloseWait = 2 * time.Second

// ancStatQueue takes AV_IN_STAT_DATA_ANC off the demux thread. The C read loop raises the stat once per frame and
// waits for the Go call to return, so a slow consumer would stall demuxing and, on a live input, make the UDP
// reader drop transport packets. The queue returns at once and one goroutine delivers the packets to the
// consumer's Stat in order. When the queue is full a live input drops the oldest packet, and the next delivered
// packet carries the count in AncPacket.Dropped; a file input blocks instead, since there is no real time to keep
// up with. push and close are called from the demux thread only.
type ancStatQueue struct {
	ch        chan ancItem
	drop      bool // live input: drop the oldest when full
	closeWait time.Duration
	n         int64 // packets pushed, the sequence the drainer counts drops from
	done      chan struct{}
}

type ancItem struct {
	n           int64
	streamIndex int
	pkt         *goavpipe.AncPacket
}

// newAncStatQueue starts the delivery goroutine; stat is the consumer's Stat.
func newAncStatQueue(live bool, stat func(streamIndex int, pkt *goavpipe.AncPacket) error) *ancStatQueue {
	q := &ancStatQueue{
		ch:        make(chan ancItem, ancQueueDepth),
		drop:      live,
		closeWait: ancCloseWait,
		done:      make(chan struct{}),
	}
	go q.drain(stat)
	return q
}

func (q *ancStatQueue) drain(stat func(int, *goavpipe.AncPacket) error) {
	defer close(q.done)
	var last int64
	for it := range q.ch {
		it.pkt.Dropped = int(it.n - last - 1)
		last = it.n
		if err := stat(it.streamIndex, it.pkt); err != nil {
			goavpipe.Log.Warn("ANC stat consumer failed", "err", err, "pts", it.pkt.Pts)
		}
	}
}

// push queues one packet. On a live input with the queue full the oldest queued packet is dropped.
func (q *ancStatQueue) push(streamIndex int, pkt *goavpipe.AncPacket) {
	q.n++
	it := ancItem{n: q.n, streamIndex: streamIndex, pkt: pkt}
	if !q.drop {
		q.ch <- it
		return
	}
	select {
	case q.ch <- it:
		return
	default:
	}
	// Full: make room by dropping the oldest. The demux thread is the only sender, so the retry cannot fail.
	select {
	case <-q.ch:
	default:
	}
	select {
	case q.ch <- it:
	default:
	}
}

// close ends the queue and waits for the queued packets to be delivered. A live input waits at most closeWait for
// a consumer that is stuck inside Stat, since the job is ending anyway.
func (q *ancStatQueue) close() {
	close(q.ch)
	if !q.drop {
		<-q.done
		return
	}
	select {
	case <-q.done:
	case <-time.After(q.closeWait):
		goavpipe.Log.Warn("ANC stat consumer still busy at close, packets left undelivered")
	}
}

// isLiveURL reports whether url is a live input, whose ANC stats are dropped rather than held when the consumer
// falls behind.
func isLiveURL(url string) bool {
	for _, p := range []string{"udp://", "srt://", "rtp://", "rtmp://", "rtmps://"} {
		if strings.HasPrefix(url, p) {
			return true
		}
	}
	return false
}
