package cmd

import (
	"bytes"
	"encoding/base64"
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
			{"name": "bg", "type": "image", "x": 0, "y": 0, "size": 0.5, "h": 0.2, "default": "bg"},
			{"name": "score", "type": "text", "x": 0.5, "y": 0.1, "size": 0.1, "font": "font.ttf"},
			{"name": "clock", "type": "clock", "x": 0.9, "y": 0.1, "size": 0.1, "font": "font.ttf", "format": "game"},
			{"name": "period", "type": "text", "x": 0.1, "y": 0.1, "size": 0.1, "font": "Arial:bold"}
		],
		"events": [
			{"t": 0, "slots": {"score": "0 - 0", "clock": {"clock": {"ms": 720000, "running": true}}, "bg": "bg"}},
			{"t": 2.5, "data": {"score": {"text": "1 - 0"}, "bg": null, "clock": {"ms": 5000, "running": true, "dir": "up"}}},
			{"seq": 10, "pts": 4000, "hidden": true, "slots": {}}
		]
	}`)

	ov, events, spec, err := loadOverlaySpec(filepath.Join(dir, "spec.json"))
	require.NoError(t, err)
	assert.Equal(t, int64(500), spec.DelayMs)
	assert.Equal(t, int64(overlayTimescale), ov.Timescale)
	assert.NotEmpty(t, ov.Images["bg"])
	assert.Equal(t, filepath.Join(dir, "font.ttf"), ov.Slots[1].Font)
	assert.Equal(t, "Arial:bold", ov.Slots[3].Font, "not a file: a fontconfig pattern, passed on as it is")
	require.Len(t, events, 3)
	assert.Equal(t, []int64{1, 2, 10}, []int64{events[0].Seq, events[1].Seq, events[2].Seq})
	assert.Equal(t, []int64{0, 2500, 4000}, []int64{events[0].Pts, events[1].Pts, events[2].Pts})
	assert.Equal(t, goavpipe.KindOverlay, events[0].Kind)
	assert.Equal(t, "0 - 0", *events[0].Slots["score"].Text, "a plain string is text")
	assert.Equal(t, int64(720000), events[0].Slots["clock"].Clock.Ms)
	assert.Equal(t, "bg", events[0].Slots["bg"].Image.Name, "a plain string on an image slot is a library name")
	assert.Equal(t, "1 - 0", *events[1].Slots["score"].Text, "data is the older spelling of slots")
	assert.True(t, events[1].Slots["bg"].Hide, "null hides")
	assert.Equal(t, goavpipe.ClockState{Ms: 5000, Running: true, Up: true}, *events[1].Slots["clock"].Clock,
		"the bare clock object with dir")
	assert.True(t, events[2].Hidden)
}

// TestLoadOverlaySpecEmbeddedImage: an image entry that is the PNG itself in base64, the form a layout in the
// fabric's recording config has, loads as the same bytes as the file does.
func TestLoadOverlaySpecEmbeddedImage(t *testing.T) {
	dir := writeOverlaySpecDir(t, `{"images": {"bg": "bg.png"}, "slots": []}`)
	fromFile, _, _, err := loadOverlaySpec(filepath.Join(dir, "spec.json"))
	require.NoError(t, err)

	embedded := `{"images": {"bg": "` + base64.StdEncoding.EncodeToString(fromFile.Images["bg"]) + `"}, "slots": []}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.json"), []byte(embedded), 0644))
	ov, _, _, err := loadOverlaySpec(filepath.Join(dir, "spec.json"))
	require.NoError(t, err)
	assert.Equal(t, fromFile.Images["bg"], ov.Images["bg"])
}

func TestLoadOverlaySpecErrors(t *testing.T) {
	notPNG := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 2000))
	for name, spec := range map[string]string{
		"missing image": `{"images": {"bg": "nope.png"}, "slots": []}`,
		"not a png":     `{"images": {"bg": "` + notPNG + `"}, "slots": []}`,
		"missing font":  `{"slots": [{"name": "a", "type": "text", "font": "nope.ttf"}]}`,
		"missing path":  `{"slots": [{"name": "a", "type": "text", "font": "fonts/nope"}]}`,
		"t and pts":     `{"slots": [], "events": [{"t": 1, "pts": 1000}]}`,
		"unsorted":      `{"slots": [], "events": [{"t": 2}, {"t": 1}]}`,
		"unknown slot":  `{"slots": [{"name": "a", "type": "text", "font": "font.ttf"}], "events": [{"t": 0, "slots": {"b": "x"}}]}`,
		"wrong type":    `{"slots": [{"name": "a", "type": "text", "font": "font.ttf"}], "events": [{"t": 0, "slots": {"a": {"clock": {"ms": 1}}}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeOverlaySpecDir(t, spec)
			_, _, _, err := loadOverlaySpec(filepath.Join(dir, "spec.json"))
			assert.Error(t, err)
		})
	}
}

var testStamperLayout = &goavpipe.OverlayParams{
	Slots: []goavpipe.OverlaySlot{
		{Name: "score", Type: goavpipe.OverlaySlotText},
		{Name: "home", Type: goavpipe.OverlaySlotText},
		{Name: "poss", Type: goavpipe.OverlaySlotImage},
		{Name: "clock", Type: goavpipe.OverlaySlotClock},
	},
	Images: map[string][]byte{"red": {1}},
}

// at asks the source for the overlay event at pts and returns it, or nil.
func at(t *testing.T, src goavpipe.EventSource, pts int64) *goavpipe.OverlayEvent {
	t.Helper()
	ev, status, err := src.At(goavpipe.KindOverlay, pts)
	require.NoError(t, err)
	require.Equal(t, goavpipe.StatusReady, status)
	if ev == nil {
		return nil
	}
	oe := ev.(goavpipe.OverlayEvent)
	return &oe
}

func TestOverlayStamper(t *testing.T) {
	initial := []goavpipe.OverlayEvent{{TimedEvent: goavpipe.TimedEvent{Seq: 1, Kind: goavpipe.KindOverlay},
		Slots: map[string]goavpipe.SlotValue{"score": goavpipe.TextValue("0 - 0"), "home": goavpipe.TextValue("HOME")}}}
	pr, pw := io.Pipe()
	var logBuf bytes.Buffer
	s := newOverlayStamper(testStamperLayout, initial, 200, pr, &logBuf)
	job := s.handle()
	assert.Equal(t, []string{goavpipe.KindOverlay}, job.Kinds())

	// First ask: nothing new has arrived; the initial event applies and through is the frame asked for
	ev := at(t, job, 1000)
	require.NotNil(t, ev)
	assert.Equal(t, int64(1), ev.Seq)
	assert.Equal(t, int64(1000), s.through)

	// A patch arrives: it is stamped delay past the through pts and merged into the state
	_, err := pw.Write([]byte(`{"slots": {"score": "1 - 0"}}` + "\n"))
	require.NoError(t, err)
	queued := func() bool { return len(s.patches) > 0 }
	require.Eventually(t, queued, time.Second, time.Millisecond, "the reader should queue the patch")
	ev = at(t, job, 1500)
	require.NotNil(t, ev)
	assert.Equal(t, int64(2), ev.Seq)
	assert.Equal(t, int64(1201), ev.Pts)
	assert.Equal(t, "1 - 0", *ev.Slots["score"].Text)
	assert.Equal(t, "HOME", *ev.Slots["home"].Text, "the patch merges into the last state")
	assert.Equal(t, goavpipe.KindOverlay, ev.Kind)
	assert.Contains(t, logBuf.String(), `"score":{"text":"1 - 0"}`)

	// A second job that is behind sees the event in effect at its frame; the shared through pts stays ahead
	job2 := s.handle()
	ev = at(t, job2, 500)
	require.NotNil(t, ev)
	assert.Equal(t, int64(1), ev.Seq)
	assert.Equal(t, int64(1500), s.through, "through never goes backwards")

	// Hidden is part of the state too
	_, err = pw.Write([]byte(`{"hidden": true}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, queued, time.Second, time.Millisecond)
	ev = at(t, job, 1800)
	require.NotNil(t, ev)
	assert.True(t, ev.Hidden)
	assert.Equal(t, "1 - 0", *ev.Slots["score"].Text)

	// Another kind is not this source's business
	_, status, err := job.At(goavpipe.KindVerticalCrop, 0)
	require.NoError(t, err)
	assert.Equal(t, goavpipe.StatusEnded, status)

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
	s := newOverlayStamper(testStamperLayout, nil, 0, src, nil)
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

	assert.Nil(t, at(t, job, 500), "nothing stamped yet")
	write(`{"data":{"score":"1 - 0"}}`)
	ev := at(t, job, 1000)
	require.NotNil(t, ev)
	assert.Equal(t, int64(501), ev.Pts, "stamped just past the through pts of the previous ask")
	assert.Equal(t, "1 - 0", *ev.Slots["score"].Text)

	write(`{"slots":{"score":"1 - 1","poss":{"image":{"name":"red"}}}}`)
	ev = at(t, job, 2000)
	require.NotNil(t, ev)
	assert.Equal(t, int64(2), ev.Seq)
	assert.Equal(t, int64(1001), ev.Pts)
	assert.Equal(t, "1 - 1", *ev.Slots["score"].Text)
	assert.Equal(t, "red", ev.Slots["poss"].Image.Name)

	// Two patches queued before one ask are stamped at the same pts, in seq order: the second is in effect
	write(`{"data":{"score":"2 - 1"}}`)
	_, err = w.Write([]byte(`{"hidden":true}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(s.patches) == 2 }, time.Second, time.Millisecond)
	ev = at(t, job, 3000)
	require.NotNil(t, ev)
	assert.Equal(t, int64(4), ev.Seq)
	assert.True(t, ev.Hidden)
	assert.Equal(t, "2 - 1", *ev.Slots["score"].Text)
	require.Len(t, s.events, 4)
	assert.Equal(t, s.events[2].Pts, s.events[3].Pts)
	assert.False(t, s.events[2].Hidden)
}

// TestOverlayStamperSealedOnly checks that a job only receives events at or below the pts it asks for: an event
// stamped delay past the head waits with the stamper until a job asks for a frame past it.
func TestOverlayStamperSealedOnly(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(testStamperLayout, nil, 2000, pr, nil)
	job := s.handle()
	defer job.Close()

	_, err := pw.Write([]byte(`{"slots":{"score":"1 - 0"}}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(s.patches) > 0 }, time.Second, time.Millisecond)
	assert.Nil(t, at(t, job, 1000), "stamped at 2001, above the frame")
	assert.Nil(t, at(t, job, 2000), "still above")
	ev := at(t, job, 2001)
	require.NotNil(t, ev)
	assert.Equal(t, int64(2001), ev.Pts)
}

func TestOverlayStamperBadPatch(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(testStamperLayout, nil, 0, pr, nil)
	job := s.handle()
	_, _ = pw.Write([]byte("not json\n"))
	<-s.done
	_, _, err := job.At(goavpipe.KindOverlay, 0)
	assert.Error(t, err)
	require.NoError(t, job.Close())
}

// TestOverlayStamperClockCarry checks that a running clock carried into a later snapshot is re-anchored to that
// snapshot's pts, so a score change does not reset the clock, while a clock the patch sets is taken as it is.
func TestOverlayStamperClockCarry(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(testStamperLayout, nil, 0, pr, nil)
	job := s.handle()
	defer job.Close()

	write := func(line string, pts int64) *goavpipe.OverlayEvent {
		_, err := pw.Write([]byte(line + "\n"))
		require.NoError(t, err)
		require.Eventually(t, func() bool { return len(s.patches) > 0 }, time.Second, time.Millisecond)
		ev := at(t, job, pts)
		require.NotNil(t, ev)
		return ev
	}

	// A patch is stamped just past the through pts of the previous ask, then through moves to the frame
	ev := write(`{"slots":{"clock":{"clock":{"ms":720000,"running":true}}}}`, 1000)
	assert.Equal(t, int64(1), ev.Pts)

	ev = write(`{"slots":{"score":"1 - 0"}}`, 6000) // stamped at 1001: 1 s after the clock event
	assert.Equal(t, int64(1001), ev.Pts)
	assert.Equal(t, int64(719000), ev.Slots["clock"].Clock.Ms, "the carried clock moved on by the time between the events")
	assert.True(t, ev.Slots["clock"].Clock.Running)

	ev = write(`{"slots":{"clock":{"clock":{"ms":600000,"running":false}}}}`, 9000) // stamped at 6001
	assert.Equal(t, int64(600000), ev.Slots["clock"].Clock.Ms, "a clock the patch sets is not adjusted")

	ev = write(`{"slots":{"score":"2 - 0"}}`, 20000) // stamped at 9001
	assert.Equal(t, int64(600000), ev.Slots["clock"].Clock.Ms, "a stopped clock is carried as it is")
}

// TestOverlayStamperRefused checks that a patch naming a slot the layout does not have, or a value of the wrong
// type, is refused the way the recorder would answer 400, and the following patches still go through.
func TestOverlayStamperRefused(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(testStamperLayout, nil, 0, pr, nil)
	job := s.handle()
	defer job.Close()

	_, err := pw.Write([]byte(`{"slots":{"away":"AWAY","score":"0 - 0"}}` + "\n" +
		`{"slots":{"poss":"not an image"}}` + "\n" +
		`{"slots":{"score":"1 - 0"}}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(s.patches) >= 2 }, time.Second, time.Millisecond)
	ev := at(t, job, 100)
	require.NotNil(t, ev)
	require.Len(t, s.events, 1, "only the valid patch is stamped")
	assert.Equal(t, "1 - 0", *ev.Slots["score"].Text)
	assert.NotContains(t, ev.Slots, "away")
}

// TestOverlayStamperShortForms checks the forms the demo helpers send: an image by plain name, a bare clock object
// and null to hide.
func TestOverlayStamperShortForms(t *testing.T) {
	pr, pw := io.Pipe()
	s := newOverlayStamper(testStamperLayout, nil, 0, pr, nil)
	job := s.handle()
	defer job.Close()

	_, err := pw.Write([]byte(`{"data":{"poss":"red","clock":{"ms":5000,"running":true},"home":"HOME"}}` + "\n" +
		`{"data":{"home":null}}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(s.patches) == 2 }, time.Second, time.Millisecond)
	ev := at(t, job, 100)
	require.NotNil(t, ev)
	assert.Equal(t, int64(2), ev.Seq)
	assert.Equal(t, "red", ev.Slots["poss"].Image.Name)
	assert.Equal(t, goavpipe.ClockState{Ms: 5000, Running: true}, *ev.Slots["clock"].Clock)
	assert.True(t, ev.Slots["home"].Hide)
}

// TestOverlayStamperPicture checks that a picture delivered with a patch becomes an asset the job fetches by hash.
func TestOverlayStamperPicture(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 4, 2))))
	pic := buf.Bytes()

	pr, pw := io.Pipe()
	s := newOverlayStamper(testStamperLayout, nil, 0, pr, nil)
	job := s.handle()
	defer job.Close()

	_, err := pw.Write([]byte(`{"slots":{"poss":{"png":"` + base64.StdEncoding.EncodeToString(pic) + `"}}}` + "\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(s.patches) > 0 }, time.Second, time.Millisecond)
	ev := at(t, job, 100)
	require.NotNil(t, ev)
	ref := ev.Slots["poss"].Image
	require.NotNil(t, ref)
	assert.Empty(t, ref.Name)
	assert.Len(t, ref.Hash, 64)
	b, status, err := job.Asset(ref.Hash)
	require.NoError(t, err)
	assert.Equal(t, goavpipe.StatusReady, status)
	assert.Equal(t, pic, b)
	_, _, err = job.Asset("nope")
	assert.Error(t, err)
}
