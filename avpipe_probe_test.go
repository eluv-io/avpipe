package avpipe_test

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/eluv-io/avpipe/mp4e"
	"github.com/eluv-io/avpipe/xc"
	"github.com/eluv-io/log-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/jsonc"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
)

func TestProbeDolbyAtmos(t *testing.T) {
	url := audioDolbyAtmosPath
	checkFileExists(t, url)

	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url}, &concurrentOutputOpener{dir: "test_out/dolby_atmos"})
	xcparams := &goavpipe.XcParams{
		Url:      url,
		Seekable: true,
	}
	probe, err := avpipe.Probe(xcparams)
	failNowOnError(t, err)

	got, err := json.Marshal(probe)
	failNowOnError(t, err)

	want, err := os.ReadFile("testdata/avprobe_dolby_atmos.jsonc")
	failNowOnError(t, err)

	assert.JSONEq(t, string(jsonc.ToJSON(want)), string(got))
	//println(string(got))
}

func TestProbeDOVI81Golden(t *testing.T) {
	url := dovi81TestSource
	checkFileExists(t, url)

	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url}, &concurrentOutputOpener{dir: "test_out/dv81"})
	xcparams := &goavpipe.XcParams{
		Url:      url,
		Seekable: true,
	}
	probe, err := avpipe.Probe(xcparams)
	failNowOnError(t, err)

	got, err := json.Marshal(probe)
	failNowOnError(t, err)

	want, err := os.ReadFile("testdata/avprobe_dovi_81.jsonc")
	failNowOnError(t, err)

	assert.JSONEq(t, string(jsonc.ToJSON(want)), string(got))
	//println(string(got))
}

// TestProbeDOVI81_MatchesExtractCodecInfo verifies that the DOVI fields
// returned by avpipe.Probe() (read from AV_PKT_DATA_DOVI_CONF coded side data
// via FFmpeg) agree with those returned by mp4e.ExtractCodecInfo() (parsed
// directly from the dvvC/dvcC MP4 box). Both paths must produce the same result
// for a given Dolby Vision Profile 8.1 file.
func TestProbeDOVI81_MatchesExtractCodecInfo(t *testing.T) {
	url := dovi81TestSource
	checkFileExists(t, url)

	// Probe via FFmpeg CGO path
	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url}, &concurrentOutputOpener{dir: "test_out/dv81"})
	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: url, Seekable: true})
	require.NoError(t, err)

	probeVideo := probe.StreamByCodecType("video")
	require.NotNil(t, probeVideo, "Probe: expected a video stream")
	probeDOVI := probeVideo.DOVI
	require.NotNil(t, probeDOVI, "Probe: expected DOVI on video stream")

	// Extract via pure-Go MP4 box parser
	f, err := os.Open(url)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	infos, err := mp4e.ExtractCodecInfo(f)
	require.NoError(t, err)
	var boxDOVI *avdesc.DOVIInfo
	for _, info := range infos {
		if info.DOVI != nil {
			boxDOVI = info.DOVI
			break
		}
	}
	require.NotNil(t, boxDOVI, "ExtractCodecInfo: expected DOVI in codec info")

	// Both parsers must agree on the numeric fields
	assert.Equal(t, boxDOVI.VersionMajor, probeDOVI.VersionMajor)
	assert.Equal(t, boxDOVI.VersionMinor, probeDOVI.VersionMinor)
	assert.Equal(t, boxDOVI.Profile, probeDOVI.Profile)
	assert.Equal(t, boxDOVI.Level, probeDOVI.Level)
	assert.Equal(t, boxDOVI.RPUPresent, probeDOVI.RPUPresent)
	assert.Equal(t, boxDOVI.ELPresent, probeDOVI.ELPresent)
	assert.Equal(t, boxDOVI.BLPresent, probeDOVI.BLPresent)
	assert.Equal(t, boxDOVI.BLSignalCompatibilityID, probeDOVI.BLSignalCompatibilityID)

	// BoxType is only available from the MP4 box path; FourCC is now derived
	// from CodecTagString in the probe path and must match the mp4e result.
	assert.Empty(t, probeDOVI.BoxType, "StreamInfo.DOVI: BoxType must be empty (side-data path)")
	assert.Equal(t, boxDOVI.FourCC, probeDOVI.FourCC, "StreamInfo.DOVI: FourCC must match mp4e result")

	require.NotNil(t, probeVideo.MP4, "MP4 must be populated")
	mp4DOVI := probeVideo.MP4.DOVI
	require.NotNil(t, mp4DOVI, "MP4.DOVI must be set for Dolby Vision file")
	assert.Equal(t, boxDOVI.BoxType, mp4DOVI.BoxType, "MP4.DOVI.BoxType must match mp4e result")
	assert.Equal(t, boxDOVI.FourCC, mp4DOVI.FourCC, "MP4.DOVI.FourCC must match mp4e result")
}

// postCloseFailOpener allows multiple concurrent opens but fails if Open is called after
// any handle has been closed. This replicates the content-fabric probe path: the URL table
// entry supports multiple simultaneous opens (pre-extraction plus C probe), but is cleared by
// InCloser after the C probe closes, so any post-probe re-open fails.
type postCloseFailOpener struct {
	url    string
	mu     sync.Mutex
	opens  int
	closes int
}

func (o *postCloseFailOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closes > 0 {
		log.Debug("postCloseFailOpener: rejecting re-open after close", "fd", fd, "url", url, "opens", o.opens, "closes", o.closes)
		return nil, fmt.Errorf("postCloseFailOpener: re-open after close (simulates clearReqCtxTables)")
	}
	o.opens++
	log.Debug("postCloseFailOpener: open", "fd", fd, "url", url, "opens", o.opens)
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
func (h *trackingCloseHandler) Stat(_ int, _ goavpipe.AVStatType, _ any) error {
	return nil
}
func (h *trackingCloseHandler) Close() error {
	h.opener.mu.Lock()
	h.opener.closes++
	closes := h.opener.closes
	h.opener.mu.Unlock()
	log.Debug("trackingCloseHandler: close", "file", h.file.Name(), "closes", closes)
	return h.file.Close()
}

// TestProbeMVHEVC_VideoLayout verifies that Probe populates MP4.VideoLayout correctly
// for an MV-HEVC source even when the input opener cannot re-open the file after the C
// probe has closed it (as is the case in the content-fabric probe path, where InCloser
// calls clearReqCtxTables after the C probe closes).
//
// postCloseFailOpener models this: multiple simultaneous opens are allowed (pre-extraction
// and C probe can both hold handles), but any open attempt after a close fails.
// enhanceStreamInfo must therefore use codec infos extracted before the C probe, not after.
func TestProbeMVHEVC_VideoLayout(t *testing.T) {
	url := dovi20TestSource
	checkFileExists(t, url)

	opener := &postCloseFailOpener{url: url}
	goavpipe.InitIOHandler(opener, &concurrentOutputOpener{dir: "test_out/probe_mvhevc"})

	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: url, Seekable: true})
	require.NoError(t, err)
	assert.Equal(t, 2, opener.opens, "expected pre-extraction open and C probe open")
	assert.Equal(t, 2, opener.closes, "expected both handles closed: C probe via InCloser, pre-extraction explicitly")

	video := probe.StreamByCodecType("video")
	require.NotNil(t, video, "expected a video stream")
	require.NotNil(t, video.MP4, "MP4 must be populated by enhanceStreamInfo")
	assert.Equal(t, goavpipe.VideoLayoutMVHEVC, video.MP4.VideoLayout,
		"VideoLayout must be MVHEVC(10) for MV-HEVC source")
}

// TestProbeTS_NoMP4Info verifies that Probe succeeds for a non-MP4 container (MPEG-TS)
// and that MP4Info is nil on all streams — extractCodecInfoForProbe fails gracefully when
// mp4.DecodeFile cannot parse the container, leaving the probe result unaffected.
func TestProbeTS_NoMP4Info(t *testing.T) {
	url := "./media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	checkFileExists(t, url)

	opener := &postCloseFailOpener{url: url}
	goavpipe.InitIOHandler(opener, &concurrentOutputOpener{dir: "test_out/probe_ts"})

	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: url, Seekable: true})
	require.NoError(t, err)
	assert.Equal(t, 2, opener.opens, "expected pre-extraction open and C probe open")
	assert.Equal(t, 2, opener.closes, "expected both handles closed: C probe via InCloser, pre-extraction explicitly")

	video := probe.StreamByCodecType("video")
	require.NotNil(t, video, "expected a video stream")
	assert.Nil(t, video.MP4, "MP4 must be nil for TS container")
}

// TestProbeUndecodableAudioStream verifies that Probe succeeds for a file with a stream that
// ffmpeg can demux but has no decoder for. The clip is cut from an iPhone spatial-video capture
// (video re-encoded to 90x160 H.264): a stereo AAC track plus a 4-channel Apple Positional Audio
// Codec ('apac') track, which ffmpeg demuxes with codec_id AV_CODEC_ID_NONE. Probing used to fail
// with EAV_CODEC_PARAM on such files. The undecodable track is still listed, in its ffmpeg stream
// position, so that clients mapping probe positions to stream indexes are not shifted; it is reported
// like a data stream (no codec id/name) with the container tag and the audio parameters the demuxer
// found.
func TestProbeUndecodableAudioStream(t *testing.T) {
	url := apacTestSource
	checkFileExists(t, url)

	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url}, &concurrentOutputOpener{dir: "test_out/probe_apac"})

	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: url, Seekable: true})
	require.NoError(t, err)
	require.Equal(t, 3, len(probe.Streams))

	assert.Equal(t, 0, probe.Streams[0].StreamIndex)
	assert.Equal(t, "video", probe.Streams[0].CodecType)
	assert.Equal(t, "h264", probe.Streams[0].CodecName)

	aac := probe.Streams[1]
	assert.Equal(t, 1, aac.StreamIndex)
	assert.Equal(t, "audio", aac.CodecType)
	assert.Equal(t, "aac", aac.CodecName)
	assert.Equal(t, "mp4a", aac.CodecTagString)
	assert.Equal(t, 2, aac.Channels)
	assert.Equal(t, "stereo", aac.ChannelLayoutName)

	apac := probe.Streams[2]
	assert.Equal(t, 2, apac.StreamIndex)
	assert.Equal(t, "audio", apac.CodecType)
	assert.Equal(t, 0, apac.CodecID)
	assert.Equal(t, "", apac.CodecName)
	assert.Equal(t, "apac", apac.CodecTagString)
	assert.Equal(t, 48000, apac.SampleRate)
	assert.Equal(t, 4, apac.Channels)
	assert.Equal(t, "4.0", apac.ChannelLayoutName)
	assert.Equal(t, int64(48000), apac.TimeBase.Denom().Int64())
	assert.Greater(t, apac.NBFrames, int64(0))
	assert.Greater(t, apac.DurationTs, int64(0))
}
