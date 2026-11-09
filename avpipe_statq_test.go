package avpipe

import (
	"sync"
	"testing"
	"time"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ancConsumer is a Stat that takes delay per packet and records what it got.
type ancConsumer struct {
	delay time.Duration
	mu    sync.Mutex
	got   []*goavpipe.AncPacket
}

func (c *ancConsumer) stat(_ int, p *goavpipe.AncPacket) error {
	time.Sleep(c.delay)
	c.mu.Lock()
	c.got = append(c.got, p)
	c.mu.Unlock()
	return nil
}

// TestAncStatQueueLive feeds a live queue far faster than the consumer takes packets: pushes never block, the
// consumer sees packets in order, and the Dropped counts account for every packet it did not see.
func TestAncStatQueueLive(t *testing.T) {
	c := &ancConsumer{delay: time.Millisecond}
	q := newAncStatQueue(true, c.stat)
	const n = 3000
	start := time.Now()
	for i := 0; i < n; i++ {
		q.push(5, &goavpipe.AncPacket{Pts: int64(i)})
	}
	pushTime := time.Since(start)
	q.close()
	assert.Less(t, pushTime, 500*time.Millisecond, "pushes do not wait for the consumer")

	require.NotEmpty(t, c.got)
	assert.Less(t, len(c.got), n, "a 1 ms consumer cannot keep up with a burst of %d", n)
	seen := 0
	var lastPts int64 = -1
	for _, p := range c.got {
		assert.Greater(t, p.Pts, lastPts, "in order")
		assert.Equal(t, int(p.Pts-lastPts-1), p.Dropped, "Dropped is the gap to the previous delivered packet")
		lastPts = p.Pts
		seen += 1 + p.Dropped
	}
	assert.Equal(t, n, seen, "delivered plus dropped is everything pushed")
	assert.Equal(t, int64(n-1), lastPts, "the newest packet is kept, the oldest dropped")
}

// TestAncStatQueueFile: a file input holds the read loop instead of dropping, and close delivers everything.
func TestAncStatQueueFile(t *testing.T) {
	c := &ancConsumer{delay: 200 * time.Microsecond}
	q := newAncStatQueue(false, c.stat)
	const n = ancQueueDepth * 3
	for i := 0; i < n; i++ {
		q.push(5, &goavpipe.AncPacket{Pts: int64(i)})
	}
	q.close()
	require.Len(t, c.got, n)
	for i, p := range c.got {
		assert.Equal(t, int64(i), p.Pts)
		assert.Equal(t, 0, p.Dropped)
	}
}

// TestAncStatQueueCloseStuck: a live consumer that never returns does not hold the close beyond the wait.
func TestAncStatQueueCloseStuck(t *testing.T) {
	block := make(chan struct{})
	q := newAncStatQueue(true, func(int, *goavpipe.AncPacket) error { <-block; return nil })
	q.closeWait = 50 * time.Millisecond
	q.push(5, &goavpipe.AncPacket{})
	q.push(5, &goavpipe.AncPacket{})
	start := time.Now()
	q.close()
	assert.Less(t, time.Since(start), time.Second)
	close(block)
}

func TestIsLiveURL(t *testing.T) {
	assert.True(t, isLiveURL("udp://239.1.1.1:1234"))
	assert.True(t, isLiveURL("srt://host:9000?mode=caller"))
	assert.True(t, isLiveURL("rtmp://localhost/live/x"))
	assert.False(t, isLiveURL("/media/clip.ts"))
	assert.False(t, isLiveURL("pipe:0"))
	assert.False(t, isLiveURL("https://host/clip.mp4"))
}
