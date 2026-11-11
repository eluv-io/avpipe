package avpipe_test

import (
	"path"
	"sort"
	"sync"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
)

// frameStatOpener writes like xc.FileOutputOpener and keeps every frame-written stat.
type frameStatOpener struct {
	xc.FileOutputOpener
	mu    sync.Mutex
	stats []avpipe.EncodingFrameStats
}

func (o *frameStatOpener) Open(h, fd int64, streamIndex, segIndex int, pts int64,
	outType goavpipe.AVType) (goavpipe.OutputHandler, error) {

	w, err := o.FileOutputOpener.Open(h, fd, streamIndex, segIndex, pts, outType)
	if err != nil {
		return nil, err
	}
	return &frameStatOutput{OutputHandler: w, o: o}, nil
}

type frameStatOutput struct {
	goavpipe.OutputHandler
	o *frameStatOpener
}

func (w *frameStatOutput) Stat(streamIndex int, avType goavpipe.AVType, statType goavpipe.AVStatType,
	args interface{}) error {

	// a video-only transcode: every frame-written stat is the video stream's
	if statType == goavpipe.AV_OUT_STAT_FRAME_WRITTEN {
		w.o.mu.Lock()
		w.o.stats = append(w.o.stats, *args.(*avpipe.EncodingFrameStats))
		w.o.mu.Unlock()
	}
	return w.OutputHandler.Stat(streamIndex, avType, statType, args)
}

// Every encoded frame reports its muxed pts and the output time base, so a consumer can keep the written head
// (the overlay log's seal) from the stats alone.
func TestFrameWrittenLastPts(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	outPath := path.Join(baseOutPath, fn())
	setupOutDir(t, outPath)

	foo := &frameStatOpener{FileOutputOpener: xc.FileOutputOpener{Dir: outPath}}
	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url}, foo)

	params := &goavpipe.XcParams{
		Format:             "hls",
		DurationTs:         120000, // 4 s of the 30 fps, 1/30000 clip
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		VideoSegDurationTs: 60000,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
	}
	setFastEncodeParams(params, true)
	if err := avpipe.Xc(params); err != nil {
		t.Fatal(err)
	}

	foo.mu.Lock()
	stats := foo.stats
	foo.mu.Unlock()
	if len(stats) < 2 {
		t.Fatalf("frame-written stats: got %d", len(stats))
	}
	last := stats[len(stats)-1]
	if last.TotalFramesWritten < 119 || last.TotalFramesWritten > 121 {
		t.Fatalf("TotalFramesWritten=%d, want about 120 frames", last.TotalFramesWritten)
	}
	// One stat per frame, in order. The first frame's stat fires before the muxer has opened a segment file, so
	// it reaches no output handler: the stats start at frame 2.
	if stats[0].TotalFramesWritten > 2 {
		t.Fatalf("first stat is frame %d", stats[0].TotalFramesWritten)
	}
	pts := make([]int64, len(stats))
	for i, s := range stats {
		if s.TotalFramesWritten != stats[0].TotalFramesWritten+int64(i) {
			t.Fatalf("stat %d is frame %d, want %d", i, s.TotalFramesWritten, stats[0].TotalFramesWritten+int64(i))
		}
		if s.TimeBaseNum <= 0 || s.TimeBaseDen <= 0 || s.TimeBaseNum != last.TimeBaseNum || s.TimeBaseDen != last.TimeBaseDen {
			t.Fatalf("stat %d: time base %d/%d, want one positive time base throughout (%d/%d)",
				i, s.TimeBaseNum, s.TimeBaseDen, last.TimeBaseNum, last.TimeBaseDen)
		}
		pts[i] = s.LastPts
	}
	// ultrafast encodes without B-frames, so the pts arrive in order; sorting keeps the check true for a
	// preset that reorders
	sort.Slice(pts, func(i, j int) bool { return pts[i] < pts[j] })
	d := pts[1] - pts[0]
	if d <= 0 {
		t.Fatalf("frame duration %d from pts %v", d, pts[:2])
	}
	for i := 1; i < len(pts); i++ {
		if pts[i]-pts[i-1] != d {
			t.Fatalf("pts[%d]-pts[%d] = %d, want the frame duration %d", i, i-1, pts[i]-pts[i-1], d)
		}
	}
	// the reported frames span the clip's 4 s, in the reported time base, to within the frames not reported
	seconds := float64(pts[len(pts)-1]-pts[0]+d) * float64(last.TimeBaseNum) / float64(last.TimeBaseDen)
	if seconds < 3.9 || seconds > 4.05 {
		t.Errorf("written span %.4f s in time base %d/%d, want 4 s", seconds, last.TimeBaseNum, last.TimeBaseDen)
	}
}

// A passthrough reports the pts of every copied packet too. The muxer takes the packet, so the pts has to be read
// before the write.
func TestFrameWrittenLastPtsBypass(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	outPath := path.Join(baseOutPath, fn())
	setupOutDir(t, outPath)

	foo := &frameStatOpener{FileOutputOpener: xc.FileOutputOpener{Dir: outPath}}
	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url}, foo)

	params := &goavpipe.XcParams{
		BypassTranscoding:  true,
		Format:             "hls",
		DurationTs:         120000, // 4 s of the 30 fps, 1/30000 clip
		StartSegmentStr:    "1",
		VideoSegDurationTs: 60000,
		Ecodec:             h264Codec,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
	}
	if err := avpipe.Xc(params); err != nil {
		t.Fatal(err)
	}

	foo.mu.Lock()
	stats := foo.stats
	foo.mu.Unlock()
	if len(stats) < 100 {
		t.Fatalf("frame-written stats: got %d, want about 120", len(stats))
	}
	// Packets are copied in decode order, so the pts are sorted before they are compared
	pts := make([]int64, len(stats))
	for i, s := range stats {
		if s.LastPts < 0 {
			t.Fatalf("stat %d (frame %d): LastPts=%d, want the packet's pts", i, s.TotalFramesWritten, s.LastPts)
		}
		if s.TimeBaseNum <= 0 || s.TimeBaseDen <= 0 {
			t.Fatalf("stat %d: time base %d/%d", i, s.TimeBaseNum, s.TimeBaseDen)
		}
		pts[i] = s.LastPts
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i] < pts[j] })
	d := pts[1] - pts[0]
	if d <= 0 {
		t.Fatalf("frame duration %d from pts %v", d, pts[:2])
	}
	for i := 1; i < len(pts); i++ {
		if pts[i]-pts[i-1] != d {
			t.Fatalf("pts[%d]-pts[%d] = %d, want the frame duration %d", i, i-1, pts[i]-pts[i-1], d)
		}
	}
}
