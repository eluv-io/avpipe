package avpipe_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"os"
	"path"
	"strconv"
	"strings"
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
				{Name: "poss", Type: goavpipe.OverlaySlotImage, X: 0.36, Y: 0.06, Size: 0.05, Fade: 1000},
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

// assertSameMedia checks that two output directories hold the same init and media segments. Playlists are left
// out: they carry the wall-clock time the job started.
func assertSameMedia(t *testing.T, refDir, dir string) {
	t.Helper()
	refFiles := readDirFiles(t, refDir)
	files := readDirFiles(t, dir)
	require.Equal(t, len(refFiles), len(files))
	for name, b := range refFiles {
		if path.Ext(name) == ".m3u8" {
			continue
		}
		assert.True(t, bytes.Equal(b, files[name]), "%s: output %s differs", path.Base(dir), name)
	}
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
	assertSameMedia(t, refDir, srcDir)
}

// overlayInlineParams is overlayTextParams with the possession marker delivered as PNG bytes in the events, in
// both forms a value can take, and a library that holds only the background.
func overlayInlineParams(url, font string, images map[string][]byte) *goavpipe.XcParams {
	params := overlayTextParams(url, font, map[string][]byte{"bg": images["bg"]})
	events := params.Overlay.Events
	events[0].Data["poss"] = images["red"]
	events[1].Data["poss"] = map[string]any{"png": base64.StdEncoding.EncodeToString(images["green"])}
	events[2].Data["poss"] = map[string]any{"png": base64.StdEncoding.EncodeToString(images["green"])}
	return params
}

// TestOverlayInlineImages checks that pictures delivered with the events, up front or through a source while the
// transcode runs, give the same output as the same pictures named from the library.
func TestOverlayInlineImages(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)
	images := overlayTestImages(t)

	refDir := path.Join(baseOutPath, fn(), "library")
	ref := overlayTextParams(url, font, images)
	setFastEncodeParams(ref, true)
	xcTest(t, refDir, ref, nil, true)

	inlineDir := path.Join(baseOutPath, fn(), "inline")
	params := overlayInlineParams(url, font, images)
	setFastEncodeParams(params, true)
	xcTest(t, inlineDir, params, nil, true)

	srcDir := path.Join(baseOutPath, fn(), "source")
	params = overlayInlineParams(url, font, images)
	setFastEncodeParams(params, true)
	source := &windowSource{events: params.Overlay.Events, window: 15000}
	params.Overlay.Events = nil
	params.Overlay.ThroughPts = 0
	params.Overlay.Source = source
	xcTest(t, srcDir, params, nil, true)
	assert.Greater(t, source.calls.Load(), int32(3))

	assertSameMedia(t, refDir, inlineDir)
	assertSameMedia(t, refDir, srcDir)
}

// TestOverlayFadeFrames extracts frames before and halfway through the possession marker's 1 s cross-fade from
// red to green (the event is at 2 s) and checks the marker is red first and a mix of both in the middle.
func TestOverlayFadeFrames(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)

	// The clip's video time base is 1/30000 with the first frame at 1980, which is also the overlay timescale
	const frame1 = 1980 + 45*1000 // 1.5 s: 46980
	const frame2 = 1980 + 75*1000 // 2.5 s: 76980
	outPath := path.Join(baseOutPath, fn())
	params := &goavpipe.XcParams{
		Format:                 "image2",
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
		Ecodec:                 "mjpeg",
		Ecodec2:                "aac",
		EncHeight:              -1,
		EncWidth:               -1,
		ExtractImageIntervalTs: -1,
		GPUIndex:               -1,
		SampleRate:             -1,
		SegDuration:            "30",
		StartFragmentIndex:     1,
		StartSegmentStr:        "1",
		StreamId:               -1,
		SyncAudioToStreamId:    -1,
		VideoBitrate:           -1,
		VideoSegDurationTs:     -1,
		XcType:                 goavpipe.XcExtractImages,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
		Overlay:                overlayTextParams(url, font, overlayTestImages(t)).Overlay,
	}
	params.ExtractImagesTs = []int64{frame1, frame2}
	setFastEncodeParams(params, true)
	xcTest2(t, outPath, params, nil)

	// The marker's centre: the slot is at x 0.36, y 0.06, a square 0.05 of the width (the fast encode params
	// shrink the output, so it is measured from the image)
	marker := func(pts int64) color.RGBA {
		files, err := os.ReadDir(outPath)
		require.NoError(t, err)
		for _, f := range files {
			if !strings.HasPrefix(f.Name(), strconv.FormatInt(pts, 10)+".") {
				continue
			}
			fh, err := os.Open(path.Join(outPath, f.Name()))
			require.NoError(t, err)
			defer fh.Close()
			img, _, err := image.Decode(fh)
			require.NoError(t, err)
			w, h := float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
			return color.RGBAModel.Convert(img.At(int(0.36*w+0.025*w), int(0.06*h+0.025*w))).(color.RGBA)
		}
		t.Fatalf("no image for pts %d", pts)
		return color.RGBA{}
	}
	before := marker(frame1)
	mid := marker(frame2)
	t.Logf("marker before %v, mid-fade %v", before, mid)
	assert.Greater(t, int(before.R), 150, "red before the fade")
	assert.Less(t, int(before.G), 80)
	assert.Less(t, int(mid.R), int(before.R)-40, "red fading out halfway")
	assert.Greater(t, int(mid.G), int(before.G)+40, "green fading in halfway")
}

// TestOverlayInlineImageBad checks that a picture that is not a PNG is rejected before any output.
func TestOverlayInlineImageBad(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)

	outDir := path.Join(baseOutPath, fn())
	params := overlayInlineParams(url, font, overlayTestImages(t))
	params.Overlay.Events[0].Data["poss"] = []byte("not a png")
	setFastEncodeParams(params, true)

	boilerplate(t, outDir, url)
	err := avpipe.Xc(params)
	require.Error(t, err)
	assert.True(t, errors.Is(err, avpipe.EAV_PARAM), "got %v", err)
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
