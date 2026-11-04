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

var testStamperSlots = []goavpipe.OverlaySlot{
	{Name: "score", Type: goavpipe.OverlaySlotText},
	{Name: "home", Type: goavpipe.OverlaySlotText},
	{Name: "poss", Type: goavpipe.OverlaySlotImage},
	{Name: "clock", Type: goavpipe.OverlaySlotClock},
}

func TestOverlayStamper(t *testing.T) {
	initial := []goavpipe.OverlayEvent{
		{Seq: 1, Pts: 0, Data: map[string]any{"score": "0 - 0", "home": "HOME"}},
	}
	pr, pw := io.Pipe()
	var logBuf bytes.Buffer
	s := newOverlayStamper(initial, 200, pr, &logBuf, testStamperSlots)
	job := s.handle()

	// First call: nothing new has arrived; through is the frame asked for
	events, through, err := job.EventsThrough(0, 1000)
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, int64(1000), through)

	// A patch arrives: it is stamped delay past the through pts and merged into the state
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
	assert.Equal(t, int64(1500), through)
	assert.Contains(t, logBuf.String(), `"score":"1 - 0"`)

	// A second job that is behind gets the same events and the shared through pts
	job2 := s.handle()
	events, through, err = job2.EventsThrough(0, 500)
	require.NoError(t, err)
	assert.Len(t, events, 2)
	assert.Equal(t, int64(1500), through, "through never goes backwards")

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
	s := newOverlayStamper(nil, 0, src, nil, testStamperSlots)
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

	// Two patches queued before one call are stamped at the same pts, in seq order: the second is in effect
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
	assert.Equal(t, events[0].Pts, events[1].Pts)
}

// TestOverlayStamperSealedOnly checks that a job only receives events at or below the through pts of its call:
// an event stamped delay past the head waits with the stamper until a job asks for a frame past it.
func TestOverlayStamperSealedOnly(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(nil, 2000, pr, nil, testStamperSlots)
	job := s.handle()
	defer job.Close()

	_, err := pw.Write([]byte(`{"data":{"score":"1 - 0"}}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(s.patches) > 0 }, time.Second, time.Millisecond)
	events, through, err := job.EventsThrough(0, 1000) // stamped at 2001, above the seal
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Equal(t, int64(1000), through)

	events, through, err = job.EventsThrough(0, 2000) // still above
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Equal(t, int64(2000), through)

	events, _, err = job.EventsThrough(0, 2001)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, int64(2001), events[0].Pts)
}

func TestOverlayStamperBadPatch(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(nil, 0, pr, nil, testStamperSlots)
	job := s.handle()
	_, _ = pw.Write([]byte("not json\n"))
	<-s.done
	_, _, err := job.EventsThrough(0, 0)
	assert.Error(t, err)
	require.NoError(t, job.Close())
}

// TestOverlayStamperClockCarry checks that a running clock carried into a later snapshot is re-anchored to that
// snapshot's pts, so a score change does not reset the clock, while a clock the patch sets is taken as it is.
func TestOverlayStamperClockCarry(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(nil, 0, pr, nil, testStamperSlots)
	job := s.handle()
	defer job.Close()

	write := func(line string, atLeast int64) []goavpipe.OverlayEvent {
		n := len(s.events)
		_, err := pw.Write([]byte(line + "\n"))
		require.NoError(t, err)
		require.Eventually(t, func() bool { return len(s.patches) > 0 }, time.Second, time.Millisecond)
		events, _, err := job.EventsThrough(int64(n), atLeast)
		require.NoError(t, err)
		require.Len(t, events, 1)
		return events
	}
	clockMs := func(ev goavpipe.OverlayEvent) int64 { // float64 as the patch had it, int64 once carried
		switch n := ev.Data["clock"].(map[string]any)["ms"].(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		}
		t.Fatal("no clock ms")
		return 0
	}

	// A patch is stamped just past the through pts of the previous call, then through moves to atLeast
	ev := write(`{"data":{"clock":{"ms":720000,"running":true}}}`, 1000)
	assert.Equal(t, int64(1), ev[0].Pts)

	ev = write(`{"data":{"score":"1 - 0"}}`, 6000) // stamped at 1001: 1 s after the clock event
	assert.Equal(t, int64(1001), ev[0].Pts)
	assert.Equal(t, int64(719000), clockMs(ev[0]), "the carried clock moved on by the time between the events")
	assert.Equal(t, true, ev[0].Data["clock"].(map[string]any)["running"])

	ev = write(`{"data":{"clock":{"ms":600000,"running":false}}}`, 9000) // stamped at 6001
	assert.Equal(t, int64(600000), clockMs(ev[0]), "a clock the patch sets is not adjusted")

	ev = write(`{"data":{"score":"2 - 0"}}`, 20000) // stamped at 9001
	assert.Equal(t, int64(600000), clockMs(ev[0]), "a stopped clock is carried as it is")
}

// TestOverlayStamperUnknownSlot checks that a patch naming a slot the layout does not have is refused, the way
// the recorder would answer 400, and the following patches still go through.
func TestOverlayStamperUnknownSlot(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(nil, 0, pr, nil, testStamperSlots)
	job := s.handle()
	defer job.Close()

	_, err := pw.Write([]byte(`{"data":{"away":"AWAY","score":"0 - 0"}}` + "\n" + `{"data":{"score":"1 - 0"}}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(s.patches) > 0 }, time.Second, time.Millisecond)
	events, _, err := job.EventsThrough(0, 100)
	require.NoError(t, err)
	require.Len(t, events, 1, "only the valid patch is stamped")
	assert.Equal(t, "1 - 0", events[0].Data["score"])
	assert.NotContains(t, events[0].Data, "away")
}
