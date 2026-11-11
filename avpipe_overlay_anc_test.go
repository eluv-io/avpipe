package avpipe_test

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/eluv-io/avpipe/broadcastproto/anctest"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/goavpipe/stamp"
	"github.com/eluv-io/avpipe/xc"
)

// venueFeed is the venue clock feed of the test clip, message by frame index: teams and a held clock before the
// tip-off at frame 60, a score at 120, the clock stopped at 210 with a correction, hidden text slots never used.
var venueFeed = map[int][]byte{
	5:   anctest.VenueMessage(1, "17", "Home", "Visitor", "HOM", "VIS"),
	6:   anctest.VenueMessage(2, "4", "3", "1", "1st"),
	7:   anctest.VenueMessage(3, "30", "12:00", "", "", "0", "0", "1"),
	8:   anctest.VenueMessage(4, "31", "24", "0", "0", "0"),
	60:  anctest.VenueMessage(5, "30", "12:00", "", "", "0", "0", "0"),
	90:  anctest.VenueMessage(6, "30", "11:59", "", "", "0", "0", "0"),
	120: anctest.VenueMessage(7, "1", "2", "0"),
	150: anctest.VenueMessage(8, "31", "14", "0", "0", "1"),
	210: anctest.VenueMessage(9, "30", "11:54", "", "", "0", "0", "1"),
	240: anctest.VenueMessage(10, "1", "2", "3"),
}

// ancFeedInputOpener opens the TS file and feeds its ancillary stats to a stamper, the way elvxc --overlay-anc and
// the fabric's recorder do. The log is ended when the input closes, so no job waits past the last frame.
type ancFeedInputOpener struct {
	stamper *stamp.ANC
	log     *stamp.Log
	mu      sync.Mutex
	stats   int
}

func (o *ancFeedInputOpener) Open(_ int64, url string) (goavpipe.InputHandler, error) {
	f, err := os.Open(url)
	if err != nil {
		return nil, err
	}
	return &ancFeedInput{ancInput: ancInput{f: f, o: &ancInputOpener{}}, o: o}, nil
}

type ancFeedInput struct {
	ancInput
	o *ancFeedInputOpener
}

func (i *ancFeedInput) Stat(_ int, statType goavpipe.AVStatType, args interface{}) error {
	if statType != goavpipe.AV_IN_STAT_DATA_ANC {
		return nil
	}
	i.o.mu.Lock()
	i.o.stats++
	i.o.mu.Unlock()
	i.o.stamper.Feed(args.(*goavpipe.AncPacket))
	return nil
}

func (i *ancFeedInput) Close() error {
	i.o.log.End(goavpipe.KindOverlay)
	return i.f.Close()
}

// venueOverlayParams is the layout the venue mapping fills: team short names, score, period, and the game clock
// and the shot clock in their venue formats.
func venueOverlayParams(url, font string) *goavpipe.XcParams {
	return &goavpipe.XcParams{
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		VideoBitrate:        1000000,
		Ecodec:              h264Codec,
		EncHeight:           360,
		EncWidth:            640,
		XcType:              goavpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
		Overlay: &goavpipe.OverlayParams{
			Timescale: 1000,
			Slots: []goavpipe.OverlaySlot{
				{Name: "home", Type: goavpipe.OverlaySlotText, X: 0.05, Y: 0.05, Size: 0.08, Font: font, Color: "white"},
				{Name: "away", Type: goavpipe.OverlaySlotText, X: 0.95, Y: 0.05, Size: 0.08, Font: font, Color: "white", Align: "right"},
				{Name: "score", Type: goavpipe.OverlaySlotText, X: 0.5, Y: 0.05, Size: 0.1, Font: font, Color: "yellow", Align: "center"},
				{Name: "period", Type: goavpipe.OverlaySlotText, X: 0.5, Y: 0.16, Size: 0.06, Font: font, Color: "white", Align: "center"},
				{Name: "clock", Type: goavpipe.OverlaySlotClock, X: 0.5, Y: 0.24, Size: 0.08, Font: font, Color: "white", Align: "center", Format: "game"},
				{Name: "shot", Type: goavpipe.OverlaySlotClock, X: 0.5, Y: 0.34, Size: 0.06, Font: font, Color: "red", Align: "center", Format: "shot"},
			},
		},
	}
}

// TestOverlayFromANC runs the in-band path end to end on a generated TS: the venue clock messages ride in the ST
// 2038 stream, the ANC stat feeds a stamper while the job runs, the compositor waits for each frame's seal and
// draws the events. The output must be byte-identical to a second job given the same events up front.
func TestOverlayFromANC(t *testing.T) {
	src := "./media/bbb_1080p_30fps_10sec.mp4"
	checkFileExists(t, src)
	font := overlayTestFont(t)
	outDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outDir)

	tsPath := path.Join(outDir, "venue.ts")
	_, err := anctest.WriteTS(src, tsPath, func(i int, pts int64) []byte {
		h, m, s, f := anctest.TimecodeAt(i, 30)
		pkts := []anc.Packet{{Line: 9, DID: 0x60, SDID: 0x60, UDW: anctest.TimecodeUDW(h, m, s, f, false, 0)}}
		pkts = append(pkts, anctest.VenuePacket(venueFeed[i]...))
		return anc.EncodePES(pkts)
	})
	require.NoError(t, err)

	// Live: the stats feed the stamper, the job reads the log
	liveDir := path.Join(outDir, "live")
	setupOutDir(t, liveDir)
	log := stamp.NewLog(goavpipe.KindOverlay)
	stamper := stamp.NewANC(log, 1000, stamp.DefaultVenueMapping())
	opener := &ancFeedInputOpener{stamper: stamper, log: log}
	goavpipe.InitIOHandler(opener, &xc.FileOutputOpener{Dir: liveDir})
	params := venueOverlayParams(tsPath, font)
	setFastEncodeParams(params, true)
	params.Events = log.Source(0)
	require.NoError(t, avpipe.Xc(params))
	assert.Greater(t, opener.stats, 250, "one stat per frame")

	events := log.Events(goavpipe.KindOverlay)
	require.Len(t, events, len(venueFeed)-1, "every message made an event except the game clock sample that follows the running clock")
	st := stamper.State()
	assert.Equal(t, [2]int{2, 3}, st.Score)
	assert.True(t, st.Stopped)
	assert.Equal(t, 0, stamper.Stats.BadCRC)
	assert.Equal(t, 0, stamper.Stats.Errors)
	first := events[0].(goavpipe.OverlayEvent)
	assert.Equal(t, int64(5*3000*1000/90000), first.Pts, "the teams message at frame 5, in ms")
	clockStart := events[4].(goavpipe.OverlayEvent)
	assert.Equal(t, goavpipe.ClockState{Ms: 720000, Running: true}, *clockStart.Slots["clock"].Clock)
	assert.Equal(t, int64(60*3000*1000/90000), clockStart.Pts)

	// Up front: the same events through a static source give the same bytes
	staticDir := path.Join(outDir, "static")
	setupOutDir(t, staticDir)
	goavpipe.InitIOHandler(&ancInputOpener{}, &xc.FileOutputOpener{Dir: staticDir})
	params = venueOverlayParams(tsPath, font)
	setFastEncodeParams(params, true)
	params.Events = goavpipe.StaticSource(events, nil)
	require.NoError(t, avpipe.Xc(params))
	assertSameMedia(t, liveDir, staticDir)

	// Rendered board: the same feed through a RenderMapping, the pictures made by a fake renderer on its own
	// goroutine and put in the log as assets while the job waits for them
	renderDir := path.Join(outDir, "render")
	setupOutDir(t, renderDir)
	log = stamp.NewLog(goavpipe.KindOverlay)
	log.SetAssetFetcher(func(string) {})
	var renders atomic.Int32
	mapping := stamp.NewRenderMapping("board", "clock", "shot", func(hash string, b stamp.BoardState) {
		renders.Add(1)
		go func() {
			time.Sleep(30 * time.Millisecond) // a Chrome render takes about this long
			log.PutAsset(hash, boardPNG(t, b))
		}()
	})
	stamper = stamp.NewANC(log, 1000, mapping)
	opener = &ancFeedInputOpener{stamper: stamper, log: log}
	goavpipe.InitIOHandler(opener, &xc.FileOutputOpener{Dir: renderDir})
	params = venueOverlayParams(tsPath, font)
	params.Overlay.Slots = []goavpipe.OverlaySlot{
		{Name: "board", Type: goavpipe.OverlaySlotImage, X: 0.02, Y: 0.04, Size: 0.4, H: 0.2223}, // 160 px of 720
		{Name: "clock", Type: goavpipe.OverlaySlotClock, X: 0.22, Y: 0.155, Size: 0.045, Font: font, Color: "yellow", Align: "center", Format: "game"},
		{Name: "shot", Type: goavpipe.OverlaySlotClock, X: 0.395, Y: 0.205, Size: 0.04, Font: font, Color: "red", Align: "right", Format: "shot"},
	}
	setFastEncodeParams(params, true)
	params.Events = log.Source(0)
	require.NoError(t, avpipe.Xc(params))
	assert.GreaterOrEqual(t, renders.Load(), int32(4), "teams, period, two scores")
	renderEvents := log.Events(goavpipe.KindOverlay)
	last := renderEvents[len(renderEvents)-1].(goavpipe.OverlayEvent)
	assert.Equal(t, stamp.BoardStateOf(&st, "").Hash(), last.Slots["board"].Image.Hash)

	// And the rendered run equals a static run with the same events and pictures
	assets := map[string][]byte{}
	for _, e := range renderEvents {
		if ref := e.(goavpipe.OverlayEvent).Slots["board"].Image; ref != nil {
			b, status, err := log.Source(0).Asset(ref.Hash)
			require.NoError(t, err)
			require.Equal(t, goavpipe.StatusReady, status)
			assets[ref.Hash] = b
		}
	}
	renderStaticDir := path.Join(outDir, "render-static")
	setupOutDir(t, renderStaticDir)
	goavpipe.InitIOHandler(&ancInputOpener{}, &xc.FileOutputOpener{Dir: renderStaticDir})
	params.Events = goavpipe.StaticSource(renderEvents, assets)
	require.NoError(t, avpipe.Xc(params))
	assertSameMedia(t, renderDir, renderStaticDir)
}

// boardPNG stands in for the template renderer: a bar whose colour depends on the state, so different boards are
// different pictures.
func boardPNG(t *testing.T, b stamp.BoardState) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(b.Score + b.Period + b.Home))
	img := image.NewNRGBA(image.Rect(0, 0, 512, 128))
	c := color.NRGBA{sum[0], sum[1], sum[2], 200}
	for y := 0; y < 128; y++ {
		for x := 0; x < 512; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}
