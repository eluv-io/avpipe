package cmd

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe/goavpipe"
)

func writeOverlaySpecDir(t *testing.T, spec string) string {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 4, 2))))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bg.png"), buf.Bytes(), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "font.ttf"), []byte("not a font"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.json"), []byte(spec), 0644))
	return dir
}

func TestLoadOverlaySpec(t *testing.T) {
	dir := writeOverlaySpecDir(t, `{
		"delay_ms": 500,
		"images": {"bg": "bg.png"},
		"slots": [
			{"name": "bg", "type": "image", "x": 0, "y": 0, "size": 0.5, "default": "bg"},
			{"name": "score", "type": "text", "x": 0.5, "y": 0.1, "size": 0.1, "font": "font.ttf"}
		],
		"events": [
			{"t": 0, "data": {"score": "0 - 0"}},
			{"t": 2.5, "data": {"score": "1 - 0"}},
			{"seq": 10, "pts": 4000, "hidden": true, "data": {}}
		]
	}`)

	ov, spec, err := loadOverlaySpec(filepath.Join(dir, "spec.json"))
	require.NoError(t, err)
	assert.Equal(t, int64(500), spec.DelayMs)
	assert.Equal(t, int64(overlayTimescale), ov.Timescale)
	assert.Len(t, ov.Images["bg"], 0+len(ov.Images["bg"]))
	assert.NotEmpty(t, ov.Images["bg"])
	assert.Equal(t, filepath.Join(dir, "font.ttf"), ov.Slots[1].Font)
	require.Len(t, ov.Events, 3)
	assert.Equal(t, []int64{1, 2, 10}, []int64{ov.Events[0].Seq, ov.Events[1].Seq, ov.Events[2].Seq})
	assert.Equal(t, []int64{0, 2500, 4000}, []int64{ov.Events[0].Pts, ov.Events[1].Pts, ov.Events[2].Pts})
	assert.True(t, ov.Events[2].Hidden)
}

func TestLoadOverlaySpecErrors(t *testing.T) {
	for name, spec := range map[string]string{
		"missing image": `{"images": {"bg": "nope.png"}, "slots": []}`,
		"missing font":  `{"slots": [{"name": "a", "type": "text", "font": "nope.ttf"}]}`,
		"t and pts":     `{"slots": [], "events": [{"t": 1, "pts": 1000}]}`,
		"unsorted":      `{"slots": [], "events": [{"t": 2}, {"t": 1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeOverlaySpecDir(t, spec)
			_, _, err := loadOverlaySpec(filepath.Join(dir, "spec.json"))
			assert.Error(t, err)
		})
	}
}

func TestOverlayStamper(t *testing.T) {
	initial := []goavpipe.OverlayEvent{
		{Seq: 1, Pts: 0, Data: map[string]any{"score": "0 - 0", "home": "HOME"}},
	}
	pr, pw := io.Pipe()
	var logBuf bytes.Buffer
	s := newOverlayStamper(initial, 200, pr, &logBuf)
	job := s.handle()

	// First call: nothing new has arrived; through runs delay ahead of the frame
	events, through, err := job.EventsThrough(0, 1000)
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, int64(1200), through)

	// A patch arrives: it is stamped just past the through pts and merged into the state
	_, err = pw.Write([]byte(`{"data": {"score": "1 - 0"}}` + "\n"))
	require.NoError(t, err)
	queued := func() bool { return len(s.patches) > 0 }
	require.Eventually(t, queued, time.Second, time.Millisecond, "the reader should queue the patch")
	events, through, err = job.EventsThrough(1, 1500)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, int64(2), events[0].Seq)
	assert.Equal(t, int64(1201), events[0].Pts)
	assert.Equal(t, "1 - 0", events[0].Data["score"])
	assert.Equal(t, "HOME", events[0].Data["home"], "the patch merges into the last state")
	assert.Equal(t, int64(1700), through)
	assert.Contains(t, logBuf.String(), `"score":"1 - 0"`)

	// A second job that is behind gets the same events and the shared through pts
	job2 := s.handle()
	events, through, err = job2.EventsThrough(0, 500)
	require.NoError(t, err)
	assert.Len(t, events, 2)
	assert.Equal(t, int64(1700), through, "through never goes backwards")

	// Hidden is part of the state too
	_, err = pw.Write([]byte(`{"hidden": true}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, queued, time.Second, time.Millisecond)
	events, _, err = job.EventsThrough(2, 1800)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.True(t, events[0].Hidden)
	assert.Equal(t, "1 - 0", events[0].Data["score"])

	// The source closes with the last handle
	require.NoError(t, job.Close())
	_, err = pw.Write([]byte("\n"))
	require.NoError(t, err, "still open: job2 holds a handle")
	require.NoError(t, job2.Close())
	_, err = pw.Write([]byte("\n"))
	assert.Error(t, err, "closed with the last handle")
}

// TestOverlayStamperFifo drives the stamper through a real FIFO, the way the demo does: patches written at
// different times are stamped in order with their own data.
func TestOverlayStamperFifo(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "ov.fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0600))
	src, err := openOverlayEvents(fifo)
	require.NoError(t, err)
	s := newOverlayStamper(nil, 0, src, nil)
	job := s.handle()
	defer job.Close()

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer w.Close()

	write := func(line string) {
		_, err := w.Write([]byte(line + "\n"))
		require.NoError(t, err)
		require.Eventually(t, func() bool { return len(s.patches) > 0 }, time.Second, time.Millisecond)
	}

	write(`{"data":{"score":"1 - 0"}}`)
	events, _, err := job.EventsThrough(0, 1000)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "1 - 0", events[0].Data["score"])

	write(`{"data":{"score":"1 - 1","poss":"red"}}`)
	events, _, err = job.EventsThrough(1, 2000)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, int64(2), events[0].Seq)
	assert.Equal(t, int64(1001), events[0].Pts)
	assert.Equal(t, "1 - 1", events[0].Data["score"])
	assert.Equal(t, "red", events[0].Data["poss"])

	// Two patches queued before one call are stamped in order, one pts apart
	write(`{"data":{"score":"2 - 1"}}`)
	_, err = w.Write([]byte(`{"hidden":true}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(s.patches) == 2 }, time.Second, time.Millisecond)
	events, _, err = job.EventsThrough(2, 3000)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "2 - 1", events[0].Data["score"])
	assert.False(t, events[0].Hidden)
	assert.Equal(t, "2 - 1", events[1].Data["score"])
	assert.True(t, events[1].Hidden)
	assert.Equal(t, events[0].Pts+1, events[1].Pts)
}

func TestOverlayStamperBadPatch(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(nil, 0, pr, nil)
	job := s.handle()
	_, _ = pw.Write([]byte("not json\n"))
	<-s.done
	_, _, err := job.EventsThrough(0, 0)
	assert.Error(t, err)
	require.NoError(t, job.Close())
}
