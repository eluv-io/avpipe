package avpipe_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
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
	// green is taller than the slot it goes into, so it is fitted by height, and than red, so a rect that
	// depended on the images known at start would differ between jobs
	return map[string][]byte{
		"bg":    solid(400, 100, color.NRGBA{0, 0, 0, 160}),
		"red":   solid(60, 60, color.NRGBA{220, 30, 30, 255}),
		"green": solid(60, 120, color.NRGBA{30, 200, 30, 255}),
		"half":  solid(60, 60, color.NRGBA{255, 255, 255, 128}), // half-transparent white, for the blend test
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

func overlayEvent(seq, pts int64, hidden bool, slots map[string]goavpipe.SlotValue) goavpipe.OverlayEvent {
	return goavpipe.OverlayEvent{TimedEvent: goavpipe.TimedEvent{Seq: seq, Pts: pts, Kind: goavpipe.KindOverlay},
		Hidden: hidden, Slots: slots}
}

func overlayTimed(events []goavpipe.OverlayEvent) []goavpipe.Timed {
	out := make([]goavpipe.Timed, len(events))
	for i := range events {
		out[i] = events[i]
	}
	return out
}

// overlayTextEvents: the clock counts down from 2:00 and stops at 2 s when the score and the marker change, and
// the overlay is hidden from 3 s. The timescale is the clip's 1/30000.
func overlayTextEvents() []goavpipe.OverlayEvent {
	return []goavpipe.OverlayEvent{
		overlayEvent(1, 0, false, map[string]goavpipe.SlotValue{"score": goavpipe.TextValue("0 - 0"),
			"home": goavpipe.TextValue("HOME"), "poss": goavpipe.ImageValue("red"),
			"clock": goavpipe.ClockValue(120000, true)}),
		overlayEvent(2, 60000, false, map[string]goavpipe.SlotValue{"score": goavpipe.TextValue("1 - 0"),
			"home": goavpipe.TextValue("HOME"), "poss": goavpipe.ImageValue("green"),
			"clock": goavpipe.ClockValue(118000, false)}),
		overlayEvent(3, 90000, true, map[string]goavpipe.SlotValue{"score": goavpipe.TextValue("1 - 0"),
			"home": goavpipe.TextValue("HOME"), "poss": goavpipe.ImageValue("green"),
			"clock": goavpipe.ClockValue(118000, false)}),
	}
}

// overlayTextParams transcodes the first 4 s of the input with a background image, a possession marker, two
// text slots and a clock, the events coming from a static source.
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
				{Name: "bg", Type: goavpipe.OverlaySlotImage, X: 0.03, Y: 0.03, Size: 0.94, H: 0.42, Default: "bg"},
				{Name: "poss", Type: goavpipe.OverlaySlotImage, X: 0.36, Y: 0.06, Size: 0.05, H: 0.089, Fade: 1000},
				{Name: "score", Type: goavpipe.OverlaySlotText, X: 0.5, Y: 0.05, Size: 0.12, Font: font, Color: "white", Align: "center"},
				{Name: "home", Type: goavpipe.OverlaySlotText, X: 0.05, Y: 0.05, Size: 0.08, Font: font, Color: "yellow"},
				{Name: "clock", Type: goavpipe.OverlaySlotClock, X: 0.95, Y: 0.05, Size: 0.08, Font: font, Color: "white", Align: "right"},
			},
		},
		Events: goavpipe.StaticSource(overlayTimed(overlayTextEvents()), nil),
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

// sealedSource behaves like a source at the live edge: events are sealed up to a pts that advances by window
// every time a frame past it asks, so the first ask for such a frame is "not yet" and the next succeeds. Assets
// are served from a map. err, when set, fails every call.
type sealedSource struct {
	mu     sync.Mutex
	events []goavpipe.OverlayEvent
	assets map[string][]byte
	seal   int64
	window int64
	err    error
	stuck  bool // never seals: every ask past seal is not yet
	calls  atomic.Int32
	notYet atomic.Int32
	closed atomic.Bool
}

func (s *sealedSource) Kinds() []string { return []string{goavpipe.KindOverlay} }

func (s *sealedSource) At(kind string, pts int64) (goavpipe.Timed, goavpipe.Status, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, goavpipe.StatusReady, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pts > s.seal {
		s.notYet.Add(1)
		if !s.stuck {
			s.seal += s.window
		}
		return nil, goavpipe.StatusNotYet, nil
	}
	var latest *goavpipe.OverlayEvent
	for i := range s.events {
		if s.events[i].Pts <= pts {
			latest = &s.events[i]
		}
	}
	if latest == nil {
		return nil, goavpipe.StatusReady, nil
	}
	return *latest, goavpipe.StatusReady, nil
}

func (s *sealedSource) Asset(hash string) ([]byte, goavpipe.Status, error) {
	b, ok := s.assets[hash]
	if !ok {
		return nil, goavpipe.StatusReady, errors.New("unknown asset " + hash)
	}
	return b, goavpipe.StatusReady, nil
}

func (s *sealedSource) Close() error {
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

// TestOverlayEventSource checks that events that become sealed while the transcode runs, with frames waiting for
// them, give the same output as the same events known up front.
func TestOverlayEventSource(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)
	images := overlayTestImages(t)

	refDir := path.Join(baseOutPath, fn(), "upfront")
	ref := overlayTextParams(url, font, images)
	setFastEncodeParams(ref, true)
	xcTest(t, refDir, ref, nil, true)

	srcDir := path.Join(baseOutPath, fn(), "sealed")
	params := overlayTextParams(url, font, images)
	setFastEncodeParams(params, true)
	source := &sealedSource{events: overlayTextEvents(), window: 15000} // half a second of media per seal
	params.Events = source
	xcTest(t, srcDir, params, nil, true)

	assert.Greater(t, source.notYet.Load(), int32(3), "frames waited for the seal several times")
	assert.True(t, source.closed.Load(), "the source is closed when Xc returns")
	assertSameMedia(t, refDir, srcDir)
}

// overlayHashEvents is overlayTextEvents with the possession marker named by hash, and the assets to go with it.
func overlayHashEvents(images map[string][]byte) ([]goavpipe.OverlayEvent, map[string][]byte) {
	events := overlayTextEvents()
	events[0].Slots["poss"] = goavpipe.ImageHashValue("hash-red")
	events[1].Slots["poss"] = goavpipe.ImageHashValue("hash-green")
	events[2].Slots["poss"] = goavpipe.ImageHashValue("hash-green")
	return events, map[string][]byte{"hash-red": images["red"], "hash-green": images["green"]}
}

// TestOverlayHashImages checks that images fetched by hash from the source, up front or at the live edge, give the
// same output as the same images named from the library.
func TestOverlayHashImages(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)
	images := overlayTestImages(t)
	library := map[string][]byte{"bg": images["bg"]}

	refDir := path.Join(baseOutPath, fn(), "library")
	ref := overlayTextParams(url, font, images)
	setFastEncodeParams(ref, true)
	xcTest(t, refDir, ref, nil, true)

	staticDir := path.Join(baseOutPath, fn(), "static")
	params := overlayTextParams(url, font, library)
	setFastEncodeParams(params, true)
	events, assets := overlayHashEvents(images)
	params.Events = goavpipe.StaticSource(overlayTimed(events), assets)
	xcTest(t, staticDir, params, nil, true)

	srcDir := path.Join(baseOutPath, fn(), "sealed")
	params = overlayTextParams(url, font, library)
	setFastEncodeParams(params, true)
	source := &sealedSource{events: events, assets: assets, window: 15000}
	params.Events = source
	xcTest(t, srcDir, params, nil, true)
	assert.Greater(t, source.notYet.Load(), int32(3))

	assertSameMedia(t, refDir, staticDir)
	assertSameMedia(t, refDir, srcDir)
}

// overlayExtractParams extracts single frames of the clip (mjpeg, native size unless the fast params shrink it)
// with the given overlay and events.
func overlayExtractParams(url string, overlay *goavpipe.OverlayParams, events []goavpipe.OverlayEvent,
	pts ...int64) *goavpipe.XcParams {
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
		Overlay:                overlay,
	}
	if overlay != nil {
		params.Events = goavpipe.StaticSource(overlayTimed(events), nil, goavpipe.KindOverlay)
	}
	params.ExtractImagesTs = pts
	setFastEncodeParams(params, true)
	return params
}

// The clip's video time base is 1/30000 with the first frame at 1980, which is also the overlay timescale
const (
	overlayFrame15  = 1980 + 45*1000  // 1.5 s: 46980
	overlayFrame25  = 1980 + 75*1000  // 2.5 s: 76980
	overlayFrame12s = 1980 + 360*1000 // 12 s: 361980, scenery rather than the white title card
)

// overlayMarker reads the extracted frame for pts and samples a point inside the possession marker: the slot rect
// is at x 0.36, y 0.06, 0.05 of the width by 0.089 of the height; red fills it, the taller green is fitted to its
// height and covers the left half (the fast encode params shrink the output, so it is measured from the image).
func overlayMarker(t *testing.T, outPath string, pts int64) color.RGBA {
	t.Helper()
	files, err := os.ReadDir(outPath)
	require.NoError(t, err)
	for _, f := range files {
		// Files are named by the output pts; with pts < 0 take the only frame extracted, whatever its name
		if pts >= 0 && !strings.HasPrefix(f.Name(), strconv.FormatInt(pts, 10)+".") {
			continue
		}
		fh, err := os.Open(path.Join(outPath, f.Name()))
		require.NoError(t, err)
		defer fh.Close()
		img, _, err := image.Decode(fh)
		require.NoError(t, err)
		w, h := float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
		return color.RGBAModel.Convert(img.At(int(0.36*w+0.012*w), int(0.06*h+0.044*h))).(color.RGBA)
	}
	t.Fatalf("no image for pts %d", pts)
	return color.RGBA{}
}

// TestOverlayFadeFrames extracts frames before and halfway through the possession marker's 1 s cross-fade from
// red to green (the event is at 2 s) and checks the marker is red first and a mix of both in the middle, and that
// the result does not depend on the encoder time base.
func TestOverlayFadeFrames(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)
	images := overlayTestImages(t)

	outPath := path.Join(baseOutPath, fn(), "default")
	overlay := overlayTextParams(url, font, images).Overlay
	params := overlayExtractParams(url, overlay, overlayTextEvents(), overlayFrame15, overlayFrame25)
	xcTest2(t, outPath, params, nil)
	before := overlayMarker(t, outPath, overlayFrame15)
	mid := overlayMarker(t, outPath, overlayFrame25)
	t.Logf("marker before %v, mid-fade %v", before, mid)
	assert.Greater(t, int(before.R), 150, "red before the fade")
	assert.Less(t, int(before.G), 80)
	assert.Less(t, int(mid.R), int(before.R)-40, "red fading out halfway")
	assert.Greater(t, int(mid.G), int(before.G)+40, "green fading in halfway")

	// Transition timing comes from the overlay timescale, not the encoder time base
	tbPath := path.Join(baseOutPath, fn(), "tb")
	// Extract pts are in the output time base: 2.5 s is 2566 at 1/1000 (the clip starts at 66 ms)
	params = overlayExtractParams(url, overlayTextParams(url, font, images).Overlay, overlayTextEvents(), 2566)
	params.VideoTimeBase = 1000
	xcTest2(t, tbPath, params, nil)
	midTb := overlayMarker(t, tbPath, -1) // named by its pts in the 1/1000 output base
	assert.InDelta(t, int(mid.R), int(midTb.R), 3, "mid-fade with a coarse encoder time base: %v vs %v", mid, midTb)
	assert.InDelta(t, int(mid.G), int(midTb.G), 3)
}

// TestOverlayAlphaBlend checks the blend: a half-transparent white marker over the video reads as the average of
// white and the pixel underneath, so straight alpha is handled right end to end (blit, canvas conversion, filter).
func TestOverlayAlphaBlend(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)
	images := overlayTestImages(t)

	plainPath := path.Join(baseOutPath, fn(), "plain")
	xcTest2(t, plainPath, overlayExtractParams(url, nil, nil, overlayFrame12s), nil)
	under := overlayMarker(t, plainPath, overlayFrame12s)

	overlay := overlayTextParams(url, font, images).Overlay
	overlay.Slots[0].Default = "" // no background bar under the marker: the video is what is underneath
	events := []goavpipe.OverlayEvent{overlayEvent(1, 0, false,
		map[string]goavpipe.SlotValue{"poss": goavpipe.ImageValue("half")})}
	overPath := path.Join(baseOutPath, fn(), "half")
	xcTest2(t, overPath, overlayExtractParams(url, overlay, events, overlayFrame12s), nil)
	over := overlayMarker(t, overPath, overlayFrame12s)

	t.Logf("under %v, with half-white %v", under, over)
	for _, c := range []struct {
		name string
		u, o uint8
	}{{"R", under.R, over.R}, {"G", under.G, over.G}, {"B", under.B, over.B}} {
		want := (int(c.u) + 255) / 2
		assert.InDelta(t, want, int(c.o), 8, "%s: expected about (%d+255)/2", c.name, c.u)
	}
}

// TestOverlayBadEvents checks that an event that does not fit the layout, or an asset that is not a PNG, fails the
// job with EAV_OVERLAY, and that the layout itself is checked before any output.
func TestOverlayBadEvents(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)
	images := overlayTestImages(t)

	run := func(name string, change func(p *goavpipe.XcParams)) error {
		outDir := path.Join(baseOutPath, fn(), name)
		params := overlayTextParams(url, font, images)
		setFastEncodeParams(params, true)
		change(params)
		boilerplate(t, outDir, url)
		return avpipe.Xc(params)
	}

	err := run("unknown-slot", func(p *goavpipe.XcParams) {
		events := overlayTextEvents()
		events[0].Slots["away"] = goavpipe.TextValue("AWAY")
		p.Events = goavpipe.StaticSource(overlayTimed(events), nil)
	})
	assert.True(t, errors.Is(err, avpipe.EAV_OVERLAY), "unknown slot: got %v", err)

	err = run("bad-asset", func(p *goavpipe.XcParams) {
		events, assets := overlayHashEvents(images)
		assets["hash-red"] = []byte("not a png")
		p.Events = goavpipe.StaticSource(overlayTimed(events), assets)
	})
	assert.True(t, errors.Is(err, avpipe.EAV_OVERLAY), "bad asset: got %v", err)

	err = run("missing-asset", func(p *goavpipe.XcParams) {
		events, _ := overlayHashEvents(images)
		p.Events = goavpipe.StaticSource(overlayTimed(events), nil)
	})
	assert.True(t, errors.Is(err, avpipe.EAV_OVERLAY), "missing asset: got %v", err)

	err = run("bad-library-image", func(p *goavpipe.XcParams) {
		p.Overlay.Images = map[string][]byte{"bg": []byte("not a png")}
	})
	assert.True(t, errors.Is(err, avpipe.EAV_PARAM), "bad library image: got %v", err)

	err = run("no-source", func(p *goavpipe.XcParams) { p.Events = nil })
	assert.True(t, errors.Is(err, avpipe.EAV_PARAM), "no source: got %v", err)

	err = run("wrong-kind", func(p *goavpipe.XcParams) {
		p.Events = goavpipe.StaticSource(nil, nil, goavpipe.KindVerticalCrop)
	})
	assert.True(t, errors.Is(err, avpipe.EAV_PARAM), "source without the overlay kind: got %v", err)
}

// TestOverlayEventSourceError checks that a failing source fails the transcode with EAV_OVERLAY.
func TestOverlayEventSourceError(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)

	outDir := path.Join(baseOutPath, fn())
	params := overlayTextParams(url, font, overlayTestImages(t))
	setFastEncodeParams(params, true)
	source := &sealedSource{err: errors.New("source down")}
	params.Events = source

	boilerplate(t, outDir, url)
	err := avpipe.Xc(params)
	require.Error(t, err)
	assert.True(t, errors.Is(err, avpipe.EAV_OVERLAY), "got %v", err)
	assert.True(t, source.closed.Load())
}

// TestOverlayTimeout: a source that never seals fails the job after WaitMs with the default policy, and with
// OnTimeout hold the job runs through with the event it has.
func TestOverlayTimeout(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)
	images := overlayTestImages(t)

	failDir := path.Join(baseOutPath, fn(), "fail")
	params := overlayTextParams(url, font, images)
	setFastEncodeParams(params, true)
	params.Overlay.WaitMs = 300
	// Sealed through the first event only: the frame after 1 s waits for the second and gives up
	source := &sealedSource{events: overlayTextEvents(), seal: 30000, stuck: true}
	params.Events = source
	boilerplate(t, failDir, url)
	start := time.Now()
	err := avpipe.Xc(params)
	assert.True(t, errors.Is(err, avpipe.EAV_OVERLAY), "got %v", err)
	assert.Less(t, time.Since(start), 10*time.Second, "gave up after the wait budget")
	assert.Greater(t, source.notYet.Load(), int32(5), "asked again in slices while waiting")

	holdDir := path.Join(baseOutPath, fn(), "hold")
	params = overlayTextParams(url, font, images)
	setFastEncodeParams(params, true)
	params.Overlay.WaitMs = 300
	params.Overlay.OnTimeout = goavpipe.OverlayTimeoutHold
	source = &sealedSource{events: overlayTextEvents(), seal: 30000, stuck: true}
	params.Events = source
	xcTest(t, holdDir, params, nil, true)
	assert.True(t, source.closed.Load())
}

// TestOverlayCancelWhileWaiting: XcCancel ends a job that is waiting for an event, with EAV_CANCELLED, well before
// the wait budget.
func TestOverlayCancelWhileWaiting(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	font := overlayTestFont(t)

	outDir := path.Join(baseOutPath, fn())
	params := overlayTextParams(url, font, overlayTestImages(t))
	setFastEncodeParams(params, true)
	params.Overlay.WaitMs = 60000
	source := &sealedSource{events: overlayTextEvents(), seal: 30000, stuck: true}
	params.Events = source
	boilerplate(t, outDir, url)

	handle, err := avpipe.XcInit(params)
	require.NoError(t, err)
	go func() {
		time.Sleep(500 * time.Millisecond)
		assert.NoError(t, avpipe.XcCancel(handle))
	}()
	start := time.Now()
	err = avpipe.XcRun(handle)
	assert.True(t, errors.Is(err, avpipe.EAV_CANCELLED), "got %v", err)
	assert.Less(t, time.Since(start), 10*time.Second)
	require.NoError(t, avpipe.XcFini(handle))
	assert.True(t, source.closed.Load())
}
