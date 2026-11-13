package stamp

import (
	"sync"
	"testing"
	"time"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ev(seq, pts int64, score string) goavpipe.OverlayEvent {
	return goavpipe.OverlayEvent{TimedEvent: goavpipe.TimedEvent{Seq: seq, Pts: pts, Kind: goavpipe.KindOverlay},
		Slots: map[string]goavpipe.SlotValue{"score": goavpipe.TextValue(score)}}
}

func seqAt(t *testing.T, src goavpipe.EventSource, pts int64) (int64, goavpipe.Status) {
	t.Helper()
	e, status, err := src.At(goavpipe.KindOverlay, pts)
	require.NoError(t, err)
	if e == nil {
		return -1, status
	}
	return e.Timing().Seq, status
}

func TestLogSealAndAt(t *testing.T) {
	l := NewLog(goavpipe.KindOverlay)
	assert.Equal(t, []string{goavpipe.KindOverlay}, l.Kinds())
	var logged []int64
	l.OnEvent(func(e goavpipe.Timed) { logged = append(logged, e.Timing().Seq) })
	src := l.Source(0)

	// Nothing sealed: every ask is not yet
	seq, status := seqAt(t, src, 0)
	assert.Equal(t, goavpipe.StatusNotYet, status)
	assert.Equal(t, int64(-1), seq)

	require.NoError(t, l.Append(ev(1, 1000, "0 - 0")))
	require.NoError(t, l.Append(ev(3, 5000, "2 - 0"))) // placed ahead of time
	require.NoError(t, l.Append(ev(2, 3000, "1 - 0"))) // arrives later, sorts earlier
	assert.Equal(t, []int64{1, 3, 2}, logged, "the hook sees events in append order")
	assert.Error(t, l.Append(ev(2, 3000, "dup")), "same seq and pts refused")

	l.Seal(goavpipe.KindOverlay, 4000)
	seq, status = seqAt(t, src, 500)
	assert.Equal(t, goavpipe.StatusReady, status)
	assert.Equal(t, int64(-1), seq, "sealed, nothing applies yet")
	seq, _ = seqAt(t, src, 1000)
	assert.Equal(t, int64(1), seq)
	seq, _ = seqAt(t, src, 3500)
	assert.Equal(t, int64(2), seq, "the late-arriving event sorted into place")
	seq, status = seqAt(t, src, 4001)
	assert.Equal(t, goavpipe.StatusNotYet, status, "past the seal")
	assert.Equal(t, int64(-1), seq)

	l.Seal(goavpipe.KindOverlay, 3000)
	s, ok := l.SealOf(goavpipe.KindOverlay)
	assert.True(t, ok)
	assert.Equal(t, int64(4000), s, "the seal never moves back")

	l.End(goavpipe.KindOverlay)
	seq, status = seqAt(t, src, 1<<50)
	assert.Equal(t, goavpipe.StatusReady, status)
	assert.Equal(t, int64(3), seq, "ended: the last event applies to the end")

	// Ties share a pts and the higher seq wins
	require.NoError(t, l.Append(ev(4, 5000, "3 - 0")))
	seq, _ = seqAt(t, src, 5000)
	assert.Equal(t, int64(4), seq)
	assert.Equal(t, int64(4), l.Latest(goavpipe.KindOverlay).Timing().Seq)
	assert.Len(t, l.Events(goavpipe.KindOverlay), 4)

	// An unknown kind is ended, not an error
	_, status, err := src.At(goavpipe.KindVerticalCrop, 0)
	require.NoError(t, err)
	assert.Equal(t, goavpipe.StatusEnded, status)

	// A closed view answers nothing more; the log is untouched
	require.NoError(t, src.Close())
	_, _, err = src.At(goavpipe.KindOverlay, 1000)
	assert.Error(t, err)
	seq, _ = seqAt(t, l.Source(0), 1000)
	assert.Equal(t, int64(1), seq)
}

func TestLogSourceOffset(t *testing.T) {
	l := NewLog()
	require.NoError(t, l.Append(ev(1, 100000, "a")))
	require.NoError(t, l.Append(ev(2, 130000, "b")))
	l.Seal(goavpipe.KindOverlay, 140000)
	assert.Equal(t, []string{goavpipe.KindOverlay}, l.Kinds(), "a kind appears with its first event")

	// A job on the part that starts at log pts 120000 asks in its own part-relative pts
	part := l.Source(120000)
	seq, _ := seqAt(t, part, 0)
	assert.Equal(t, int64(1), seq)
	seq, _ = seqAt(t, part, 10000)
	assert.Equal(t, int64(2), seq)
	_, status := seqAt(t, part, 20001)
	assert.Equal(t, goavpipe.StatusNotYet, status)

	// The event comes back on the job's timeline too: the compositor runs a clock and times a fade from the
	// event's pts against the frame's, so an event from before the part has a negative pts
	e, _, err := part.At(goavpipe.KindOverlay, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(-20000), e.Timing().Pts)
	assert.Equal(t, "a", *e.(goavpipe.OverlayEvent).Slots["score"].Text)
	e, _, err = part.At(goavpipe.KindOverlay, 15000)
	require.NoError(t, err)
	assert.Equal(t, int64(10000), e.Timing().Pts)
	assert.LessOrEqual(t, e.Timing().Pts, int64(15000), "At's own contract: the event's pts is at or before the pts asked")

	// The log keeps its own timeline, and a view without an offset hands events out as they are
	assert.Equal(t, int64(130000), l.Latest(goavpipe.KindOverlay).Timing().Pts)
	e, _, err = l.Source(0).At(goavpipe.KindOverlay, 135000)
	require.NoError(t, err)
	assert.Equal(t, int64(130000), e.Timing().Pts)

	// Events by pointer and crop events move the same way
	l2 := NewLog()
	p := ev(1, 100000, "a")
	require.NoError(t, l2.Append(&p))
	require.NoError(t, l2.Append(goavpipe.CropEvent{TimedEvent: goavpipe.TimedEvent{Seq: 1, Pts: 110000, Kind: goavpipe.KindVerticalCrop}, Center: 5000}))
	l2.Seal(goavpipe.KindOverlay, 140000)
	l2.Seal(goavpipe.KindVerticalCrop, 140000)
	view := l2.Source(120000)
	e, _, err = view.At(goavpipe.KindOverlay, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(-20000), e.Timing().Pts)
	assert.Equal(t, int64(100000), p.Pts, "the log's event is not touched")
	e, _, err = view.At(goavpipe.KindVerticalCrop, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(-10000), e.Timing().Pts)
	assert.Equal(t, int32(5000), e.(goavpipe.CropEvent).Center)
}

func TestLogAssets(t *testing.T) {
	l := NewLog(goavpipe.KindOverlay)
	src := l.Source(0)
	_, _, err := src.Asset("h1")
	assert.Error(t, err, "no fetcher: unknown is an error")

	var mu sync.Mutex
	calls := 0
	l.SetAssetFetcher(func(hash string) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		l.PutAsset(hash, []byte("png:"+hash))
	})
	b, status, err := src.Asset("h1")
	require.NoError(t, err)
	assert.Equal(t, goavpipe.StatusNotYet, status)
	assert.Nil(t, b)
	_, status, _ = src.Asset("h1")
	assert.Equal(t, goavpipe.StatusNotYet, status, "fetched once, asked again meanwhile")
	require.Eventually(t, func() bool {
		b, status, err = src.Asset("h1")
		return err == nil && status == goavpipe.StatusReady
	}, time.Second, time.Millisecond)
	assert.Equal(t, []byte("png:h1"), b)
	mu.Lock()
	assert.Equal(t, 1, calls)
	mu.Unlock()

	l.PutAsset("h2", []byte("x"))
	b, status, err = src.Asset("h2")
	require.NoError(t, err)
	assert.Equal(t, goavpipe.StatusReady, status)
	assert.Equal(t, []byte("x"), b)
}
