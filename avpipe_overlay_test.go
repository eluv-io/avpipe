package avpipe_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
)

// overlayTestImages makes the image library in the test: a translucent dark bar for the background and two
// solid squares for a possession marker.
func overlayTestImages(t *testing.T) map[string][]byte {
	t.Helper()
	solid := func(w, h int, c color.NRGBA) []byte {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.SetNRGBA(x, y, c)
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	return map[string][]byte{
		"bg":    solid(400, 100, color.NRGBA{0, 0, 0, 160}),
		"red":   solid(60, 60, color.NRGBA{220, 30, 30, 255}),
		"green": solid(60, 60, color.NRGBA{30, 200, 30, 255}),
	}
}

// overlayTestFont returns a bold TrueType font present on the host, or skips the test.
func overlayTestFont(t *testing.T) string {
	t.Helper()
	for _, f := range []string{
		"/System/Library/Fonts/Supplemental/Arial Bold.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
		"/usr/share/fonts/dejavu/DejaVuSans-Bold.ttf",
	} {
		if fileExist(f) {
			return f
		}
	}
	t.Skip("no test font found")
	return ""
}

// overlayTextParams transcodes the first 4 s of the input with a background image, a possession marker, two
// text slots and a clock: the clock counts down from 2:00 and stops at 2 s when the score and the marker change,
// and the overlay is hidden from 3 s.
func overlayTextParams(url, font string, images map[string][]byte) *goavpipe.XcParams {
	return &goavpipe.XcParams{
		Format:             "hls",
		StartTimeTs:        0,
		StartPts:           0,
		DurationTs:         120000,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		VideoSegDurationTs: 60000,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
		Overlay: &goavpipe.OverlayParams{
			Timescale: 30000,
			Images:    images,
			Slots: []goavpipe.OverlaySlot{
				{Name: "bg", Type: goavpipe.OverlaySlotImage, X: 0.03, Y: 0.03, Size: 0.94, Default: "bg"},
				{Name: "poss", Type: goavpipe.OverlaySlotImage, X: 0.36, Y: 0.06, Size: 0.05},
				{Name: "score", Type: goavpipe.OverlaySlotText, X: 0.5, Y: 0.05, Size: 0.12, Font: font, Color: "white", Align: "center"},
				{Name: "home", Type: goavpipe.OverlaySlotText, X: 0.05, Y: 0.05, Size: 0.08, Font: font, Color: "yellow"},
				{Name: "clock", Type: goavpipe.OverlaySlotClock, X: 0.95, Y: 0.05, Size: 0.08, Font: font, Color: "white", Align: "right"},
			},
			Events: []goavpipe.OverlayEvent{
				{Seq: 1, Pts: 0, Data: map[string]any{"score": "0 - 0", "home": "HOME", "poss": "red",
					"clock": map[string]any{"ms": 120000, "running": true}}},
				{Seq: 2, Pts: 60000, Data: map[string]any{"score": "1 - 0", "home": "HOME", "poss": "green",
					"clock": map[string]any{"ms": 118000, "running": false}}},
				{Seq: 3, Pts: 90000, Hidden: true, Data: map[string]any{"score": "1 - 0", "home": "HOME", "poss": "green",
					"clock": map[string]any{"ms": 118000, "running": false}}},
			},
		},
	}
}

func TestOverlayTextSlots(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)

	params := overlayTextParams(url, font, overlayTestImages(t))
	setFastEncodeParams(params, true)
	xcTest(t, path.Join(baseOutPath, fn()), params, nil, true)
}

// windowSource hands events out a window of media time at a time, after a short wait, the way a source at the
// live edge would: each call covers atLeast + window.
type windowSource struct {
	events []goavpipe.OverlayEvent
	window int64
	err    error // returned by every call when set
	calls  atomic.Int32
	closed atomic.Bool
}

func (s *windowSource) EventsThrough(afterSeq, atLeast int64) ([]goavpipe.OverlayEvent, int64, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, 0, s.err
	}
	time.Sleep(2 * time.Millisecond)
	through := atLeast + s.window
	var out []goavpipe.OverlayEvent
	for _, e := range s.events {
		if e.Seq > afterSeq && e.Pts <= through {
			out = append(out, e)
		}
	}
	return out, through, nil
}

func (s *windowSource) Close() error {
	s.closed.Store(true)
	return nil
}

// readDirFiles returns the regular files of dir by name.
func readDirFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(path.Join(dir, e.Name()))
		require.NoError(t, err)
		files[e.Name()] = b
	}
	return files
}

// TestOverlayEventSource checks that events fetched piecemeal through a source while the transcode runs give the
// same output as the same events given up front.
func TestOverlayEventSource(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)
	images := overlayTestImages(t)

	refDir := path.Join(baseOutPath, fn(), "upfront")
	ref := overlayTextParams(url, font, images)
	setFastEncodeParams(ref, true)
	xcTest(t, refDir, ref, nil, true)

	srcDir := path.Join(baseOutPath, fn(), "source")
	params := overlayTextParams(url, font, images)
	setFastEncodeParams(params, true)
	source := &windowSource{events: params.Overlay.Events, window: 15000} // half a second of media per call
	params.Overlay.Events = nil
	params.Overlay.ThroughPts = 0
	params.Overlay.Source = source
	xcTest(t, srcDir, params, nil, true)

	assert.Greater(t, source.calls.Load(), int32(3), "the source should have been asked several times")
	assert.True(t, source.closed.Load(), "the source should be closed when Xc returns")

	refFiles := readDirFiles(t, refDir)
	srcFiles := readDirFiles(t, srcDir)
	require.Equal(t, len(refFiles), len(srcFiles))
	for name, b := range refFiles {
		assert.True(t, bytes.Equal(b, srcFiles[name]), "output %s differs", name)
	}
}

// TestOverlayEventSourceError checks that a failing source fails the transcode with EAV_OVERLAY.
func TestOverlayEventSourceError(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)

	outDir := path.Join(baseOutPath, fn())
	params := overlayTextParams(url, font, overlayTestImages(t))
	setFastEncodeParams(params, true)
	source := &windowSource{err: errors.New("source down")}
	params.Overlay.Events = nil
	params.Overlay.Source = source

	boilerplate(t, outDir, url)
	err := avpipe.Xc(params)
	require.Error(t, err)
	assert.True(t, errors.Is(err, avpipe.EAV_OVERLAY), "got %v", err)
	assert.True(t, source.closed.Load())
}
