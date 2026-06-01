package avpipe_test

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
)

// postCloseFailOpener allows up to maxCycles sequential open/close pairs, then fails.
// This replicates the content-fabric probe path: the URL table entry supports multiple
// opens (pre-extraction + C probe), but is cleared by InCloser after the C probe closes,
// so any post-probe re-open fails. Set maxCycles=2 to cover pre-extraction and C probe.
type postCloseFailOpener struct {
	url       string
	maxCycles int
	mu        sync.Mutex
	opens     int
	closes    int
}

func (o *postCloseFailOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closes >= o.maxCycles {
		return nil, fmt.Errorf("postCloseFailOpener: re-open after %d cycles (simulates clearReqCtxTables)", o.maxCycles)
	}
	o.opens++
	f, err := os.Open(o.url)
	if err != nil {
		return nil, err
	}
	fi, _ := f.Stat()
	return &trackingCloseHandler{file: f, size: fi.Size(), opener: o}, nil
}

type trackingCloseHandler struct {
	file   *os.File
	size   int64
	opener *postCloseFailOpener
}

func (h *trackingCloseHandler) Read(buf []byte) (int, error) { return h.file.Read(buf) }
func (h *trackingCloseHandler) Seek(off int64, w int) (int64, error) {
	return h.file.Seek(off, w)
}
func (h *trackingCloseHandler) Size() int64 { return h.size }
func (h *trackingCloseHandler) Stat(_ int, _ goavpipe.AVStatType, _ interface{}) error {
	return nil
}
func (h *trackingCloseHandler) Close() error {
	h.opener.mu.Lock()
	h.opener.closes++
	h.opener.mu.Unlock()
	return h.file.Close()
}

// TestProbeTS_NoMp4Info verifies that Probe succeeds for a non-MP4 container (MPEG-TS)
// and that Mp4Info is nil on all streams — extractCodecInfoForProbe fails gracefully when
// mp4.DecodeFile cannot parse the container, leaving the probe result unaffected.
func TestProbeTS_NoMp4Info(t *testing.T) {
	url := "./media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	checkFileExists(t, url)

	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url}, &concurrentOutputOpener{dir: "test_out/probe_ts"})

	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: url, Seekable: true})
	require.NoError(t, err)

	video := probe.StreamByCodecType("video")
	require.NotNil(t, video, "expected a video stream")
	assert.Nil(t, video.Mp4Info, "Mp4Info must be nil for TS container")
}

// TestProbeMVHEVC_VideoLayout verifies that Probe populates Mp4Info.VideoLayout correctly
// for an MV-HEVC source even when the input opener cannot re-open the file after the C
// probe has closed it (as is the case in the content-fabric probe path, where InCloser
// calls clearReqCtxTables before extractCodecInfoForProbe runs).
//
// postCloseFailOpener models this: maxCycles=2 allows the pre-extraction open and the C
// probe open, but fails any further re-open attempt. enhanceStreamInfo must therefore use
// codec infos extracted before the C probe, not after.
func TestProbeMVHEVC_VideoLayout(t *testing.T) {
	url := dovi20TestSource
	checkFileExists(t, url)

	opener := &postCloseFailOpener{url: url, maxCycles: 2}
	goavpipe.InitIOHandler(opener, &concurrentOutputOpener{dir: "test_out/probe_mvhevc"})

	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: url, Seekable: true})
	require.NoError(t, err)

	video := probe.StreamByCodecType("video")
	require.NotNil(t, video, "expected a video stream")
	require.NotNil(t, video.Mp4Info, "Mp4Info must be populated by enhanceStreamInfo")
	assert.Equal(t, goavpipe.VideoLayoutMVHEVC, video.Mp4Info.VideoLayout,
		"VideoLayout must be MVHEVC(10) for MV-HEVC source")
}
