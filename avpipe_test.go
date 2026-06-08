package avpipe_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"math"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	mp4bits "github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/elvxc/cmd"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/eluv-io/avpipe/internal/testutil"
	"github.com/eluv-io/avpipe/mp4e"
	"github.com/eluv-io/avpipe/mp4e/mvhevc"
	"github.com/eluv-io/avpipe/xc"
	"github.com/eluv-io/log-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastEncode enables reduced-quality encoding (320x180, ultrafast preset) for speed.
// Defaults to true when -short is active; use -fast=false to override.
var fastEncode bool

func init() {
	flag.BoolVar(&fastEncode, "fast", false, "use reduced-quality encoding for speed (default true under -short)")
}

func flagExplicitlySet(name string) bool {
	var found bool
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

const baseOutPath = "test_out"
const debugFrameLevel = false
const h264Codec = "libx264"
const h265Codec = "libx265"
const videoBigBuckBunnyPath = "media/bbb_1080p_30fps_60sec.mp4"
const videoBigBuckBunny3AudioPath = "media/caminandes_llamigos_1080p_4audios.mp4"
const audioDolbyAtmosPath = "media/Audio_ID_720p_50fps_h264_6ch_640kbps_ddp_joc.mp4"
const dovi81TestSource = "./media/040_Escape_Frame_0_48_HD_P3D65_24Fps_v1_4444_dv81.mp4"
const dovi20TestSource = "./media/sample_dv20.mp4"
const dovi20BFramesCTSTestSource = "./media/sample_dv20_2560x1440_bframes.mp4"

// HDR10 test settings
const (
	hdr10TestSource     = "./media/hdr10-plus-injected.mp4"
	hdr10TestDurationTs = int64(35 * 24000) // 35s @ 1/24000 timebase = 1 full mez seg + fragment
	hdr10MasterDisplay  = "G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,10)"
	hdr10MaxCLL         = "1000,400"
)

// enableNvenc enables tests on NVIDIA GPU
const enableNvenc = false

type XcTestResult struct {
	mezFile           []string
	timeScale         int
	sampleRate        int
	profile           string
	level             int
	pixelFmt          string
	channelLayoutName string
}

var statsInfo xc.IOStats

// concurrentOutputOpener creates per-handle subdirectories for concurrent transcoding tests.
type concurrentOutputOpener struct {
	dir string
}

func (coo *concurrentOutputOpener) Open(h, _ int64, streamIndex, segIndex int,
	pts int64, outType goavpipe.AVType) (goavpipe.OutputHandler, error) {

	dir := fmt.Sprintf("%s/O%d", coo.dir, h)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err = os.Mkdir(dir, 0755); err != nil {
			return nil, err
		}
	}

	oo := &xc.FileOutputOpener{Dir: dir}
	return oo.Open(h, 0, streamIndex, segIndex, pts, outType)
}

func TestAudioSeg(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	params := &goavpipe.XcParams{
		BypassTranscoding:      false,
		Format:                 "fmp4-segment",
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
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
		XcType:                 goavpipe.XcAudio,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	xcTest(t, outputDir, params, nil, true)
}

func TestVideoSeg(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	params := &goavpipe.XcParams{
		BypassTranscoding:      false,
		Format:                 "fmp4-segment",
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
		Ecodec:                 "libx264",
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
		ForceKeyInt:            60,
		XcType:                 goavpipe.XcVideo,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	setFastEncodeParams(params, true)
	xcTest(t, outputDir, params, nil, true)

}

func TestProResBT709BadFrameColor(t *testing.T) {
	url := "./media/prores_bt709_bad_frame_color.mov"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	params := &goavpipe.XcParams{
		BypassTranscoding:      false,
		Format:                 "fmp4-segment",
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
		Ecodec:                 h264Codec,
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
		ForceKeyInt:            48,
		XcType:                 goavpipe.XcVideo,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	setFastEncodeParams(params, true)

	xcTestResult := &XcTestResult{
		mezFile:  []string{fmt.Sprintf("%s/vsegment-1.mp4", outputDir)},
		pixelFmt: "yuv420p",
	}
	xcTest(t, outputDir, params, xcTestResult, true)

	probeInfo, err := avpipe.Probe(&goavpipe.XcParams{
		Url:      xcTestResult.mezFile[0],
		Seekable: true,
	})
	failNowOnError(t, err)
	requireVideoStreamColor(t, probeInfo, "bt709", "bt709", "bt709")
}

func TestVideoSegWithRotate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	params := &goavpipe.XcParams{
		BypassTranscoding:      false,
		Format:                 "fmp4-segment",
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
		Ecodec:                 "libx264",
		EncHeight:              -1,
		EncWidth:               -1,
		ExtractImageIntervalTs: -1,
		GPUIndex:               -1,
		SampleRate:             -1,
		StartFragmentIndex:     1,
		StartSegmentStr:        "1",
		StreamId:               -1,
		SyncAudioToStreamId:    -1,
		VideoBitrate:           -1,
		VideoSegDurationTs:     900000,
		ForceKeyInt:            60,
		XcType:                 goavpipe.XcVideo,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
		Rotate:                 90,
	}
	xcTest(t, outputDir, params, nil, true)

}

func TestVideoSegDoubleTS(t *testing.T) {
	url := videoBigBuckBunnyPath
	outputDir := path.Join(baseOutPath, fn())
	params := &goavpipe.XcParams{
		BypassTranscoding:      false,
		Format:                 "fmp4-segment",
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
		Ecodec:                 "libx264",
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
		ForceKeyInt:            60,
		XcType:                 goavpipe.XcVideo,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
		VideoTimeBase:          60000,
	}
	setFastEncodeParams(params, true)
	xcTest(t, outputDir, params, nil, true)

}

func TestSingleABRTranscode(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 60000,
		AudioSegDurationTs: 96000,
		Ecodec:             h264Codec,
		Ecodec2:            "aac",
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)

	params.XcType = goavpipe.XcAudio
	params.Ecodec2 = "aac"
	xcTest(t, outputDir, params, nil, false)
}

func TestSingleABRTranscodeByStreamId(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 60000,
		AudioSegDurationTs: 96000,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		StreamId:           1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)

	params.StreamId = 2
	params.Ecodec2 = "aac"
	xcTest(t, outputDir, params, nil, false)
}

func TestSingleABRTranscodeWithWatermark(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:     false,
		Format:                "hls",
		StartTimeTs:           0,
		DurationTs:            -1,
		StartSegmentStr:       "1",
		VideoBitrate:          2560000,
		AudioBitrate:          64000,
		SampleRate:            44100,
		VideoSegDurationTs:    60000,
		Ecodec:                h264Codec,
		EncHeight:             720,
		EncWidth:              1280,
		XcType:                goavpipe.XcVideo,
		WatermarkText:         "This is avpipe text watermarking",
		WatermarkYLoc:         "H*0.5",
		WatermarkXLoc:         "W/2",
		WatermarkRelativeSize: 0.05,
		WatermarkFontColor:    "black",
		WatermarkShadow:       true,
		WatermarkShadowColor:  "white",
		StreamId:              -1,
		Url:                   url,
		DebugFrameLevel:       debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)
}

func TestSingleABRTranscodeWithOverlayWatermark(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	overlayImage, err := os.ReadFile("./media/avpipe.png")
	failNowOnError(t, err)

	params := &goavpipe.XcParams{
		BypassTranscoding:    false,
		Format:               "hls",
		StartTimeTs:          0,
		DurationTs:           -1,
		StartSegmentStr:      "1",
		VideoBitrate:         2560000,
		AudioBitrate:         64000,
		SampleRate:           44100,
		VideoSegDurationTs:   60000,
		Ecodec:               h264Codec,
		EncHeight:            720,
		EncWidth:             1280,
		XcType:               goavpipe.XcVideo,
		WatermarkYLoc:        "main_h*0.7",
		WatermarkXLoc:        "main_w/2-overlay_w/2",
		WatermarkOverlay:     string(overlayImage),
		WatermarkOverlayLen:  len(overlayImage),
		WatermarkOverlayType: goavpipe.PngImage,
		StreamId:             -1,
		Url:                  url,
		DebugFrameLevel:      debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)
}

func TestV2SingleABRTranscode(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 60000,
		AudioSegDurationTs: 96000,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)

	params.XcType = goavpipe.XcAudio
	params.Ecodec2 = "aac"
	params.AudioIndex = []int32{1}
	xcTest(t, outputDir, params, nil, false)
}

func TestV2SingleABRTranscodeIOHandler(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 60000,
		AudioSegDurationTs: 96000,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)

	params.XcType = goavpipe.XcAudio
	params.Ecodec2 = "aac"
	params.AudioIndex = []int32{1}
	xcTest(t, outputDir, params, nil, false)
}

func TestV2SingleABRTranscodeCancelling(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, url)

	params := &goavpipe.XcParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 60000,
		AudioSegDurationTs: 96000,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	params.EncHeight = 360 // slow down a bit to allow for the cancel
	params.EncWidth = 640

	handle, err := avpipe.XcInit(params)
	failNowOnError(t, err)
	assert.Greater(t, handle, int32(0))
	go func(handle int32) {
		// Wait for 2 sec the transcoding starts, then cancel it.
		time.Sleep(2 * time.Second)
		err := avpipe.XcCancel(handle)
		assert.NoError(t, err)
	}(handle)
	err2 := avpipe.XcRun(handle)
	assert.Error(t, err2)

	params.XcType = goavpipe.XcAudio
	params.Ecodec2 = "aac"
	params.AudioIndex = []int32{1}
	handleA, err := avpipe.XcInit(params)
	assert.NoError(t, err)
	assert.Greater(t, handleA, int32(0))
	err = avpipe.XcCancel(handleA)
	assert.NoError(t, err)
	err = avpipe.XcRun(handleA)
	assert.Error(t, err)
}

func doTranscode(t *testing.T,
	p *goavpipe.XcParams,
	nThreads int,
	outputDir, filename string) {

	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: filename},
		&concurrentOutputOpener{dir: outputDir})

	done := make(chan struct{})
	for i := 0; i < nThreads; i++ {
		go func(params *goavpipe.XcParams) {
			err := avpipe.Xc(params)
			done <- struct{}{} // Signal the main goroutine
			if err != nil {
				failNowOnError(t, err)
			}
		}(p)
	}

	for i := 0; i < nThreads; i++ {
		<-done // Wait for background goroutines to finish
	}
}

func TestNvidiaABRTranscode(t *testing.T) {
	if !testutil.NvidiaExist() {
		log.Info("Ignoring ", "test", fn())
		return
	}

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	nThreads := 2

	params := &goavpipe.XcParams{
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 60000,
		AudioSegDurationTs: 96000,
		Ecodec:             "h264_nvenc",
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
	}
	setFastEncodeParams(params, false)
	doTranscode(t, params, nThreads, outputDir, url)
}

// Check nvidia transcoding with weird aspect ratio
func TestNvidiaFmp4SegmentAspectRatio(t *testing.T) {
	if !testutil.NvidiaExist() {
		log.Info("Ignoring ", "test", fn())
		return
	}
	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	params := &goavpipe.XcParams{
		Format:          "fmp4-segment",
		StartTimeTs:     0,
		DurationTs:      -1,
		StartSegmentStr: "1",
		VideoBitrate:    2560000,
		AudioBitrate:    64000,
		SampleRate:      44100,
		SegDuration:     "30",
		Ecodec:          "h264_nvenc",
		EncHeight:       642,
		EncWidth:        1532,
		XcType:          goavpipe.XcVideo,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}
	setFastEncodeParams(params, false)

	xcTestResult := &XcTestResult{
		mezFile:  []string{fmt.Sprintf("%s/vsegment-1.mp4", outputDir)},
		level:    32,
		pixelFmt: "yuv420p",
	}
	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestConcurrentABRTranscode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	nThreads := 10

	params := &goavpipe.XcParams{
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 60000,
		AudioSegDurationTs: 96000,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	doTranscode(t, params, nThreads, outputDir, url)
}

func TestSettingProfileLevel(t *testing.T) {
	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	params := &goavpipe.XcParams{
		Format:             "fmp4-segment",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		VideoSegDurationTs: 900000,
		AudioSegDurationTs: 1428480,
		Ecodec:             h264Codec,
		EncHeight:          480,
		EncWidth:           720,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
		Profile:            "high",
		Level:              51,
	}
	xcTestResult := &XcTestResult{
		mezFile:  []string{fmt.Sprintf("%s/vsegment-1.mp4", outputDir)},
		level:    51,
		profile:  "High",
		pixelFmt: "yuv420p",
	}
	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestStartTimeTsWithSkipDecoding(t *testing.T) {
	url := "./media/video-960.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "dash",
		StartTimeTs:         180000,
		StartPts:            900000,
		DurationTs:          720000,
		StartSegmentStr:     "19",
		VideoSegDurationTs:  60000,
		SkipDecoding:        true,
		StartFragmentIndex:  1081,
		ForceKeyInt:         60,
		SegDuration:         "30",
		Ecodec:              h264Codec,
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: outputDir})
	boilerXc(t, params)

	files, err := os.ReadDir(outputDir)
	assert.NoError(t, err)
	assert.Equal(t, 14, len(files))

	// Check the ABR segment is within the expected chunks
	// Starting chunk is "vchunk-stream0-00019.m4s" and ending chunk is "vchunk-stream0-00030.m4s".
	for i := 0; i < len(files); i++ {
		if files[i].Name() == "vchunk-stream0-00031.m4s" ||
			files[i].Name() == "vchunk-stream0-00018.m4s" {
			assert.Error(t, fmt.Errorf("failed skip decoding"))
		}
	}
}

func TestStartTimeTsWithoutSkipDecoding(t *testing.T) {
	url := "./media/video-960.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "dash",
		StartTimeTs:         180000,
		StartPts:            900000,
		DurationTs:          720000,
		StartSegmentStr:     "19",
		VideoSegDurationTs:  60000,
		SkipDecoding:        false,
		StartFragmentIndex:  1081,
		ForceKeyInt:         60,
		SegDuration:         "30",
		Ecodec:              h264Codec,
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: outputDir})
	boilerXc(t, params)

	files, err := os.ReadDir(outputDir)
	assert.NoError(t, err)
	assert.Equal(t, 14, len(files))

	// Check the ABR segment is within the expected chunks
	// Starting chunk is "vchunk-stream0-00019.m4s" and ending chunk is "vchunk-stream0-00030.m4s".
	for i := 0; i < len(files); i++ {
		if files[i].Name() == "vchunk-stream0-00031.m4s" ||
			files[i].Name() == "vchunk-stream0-00018.m4s" {
			assert.Error(t, fmt.Errorf("failed skip decoding"))
		}
	}
}

func TestAudioAAC2AACMezMaker(t *testing.T) {
	url := "./media/bbb-audio-stereo-2min.aac"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec:              "aac",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment0-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}
	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioAC3Ts2AC3MezMaker(t *testing.T) {
	url := "./media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "ac3",
		Dcodec2:             "ac3",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{2}

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment0-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}
	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioAC3Ts2AACMezMaker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := "./media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "ac3",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{2}

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment0-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioMP3Ts2AACMezMaker(t *testing.T) {
	url := "./media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "mp3",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{1}

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment0-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioDownmix2AACMezMaker(t *testing.T) {
	url := "./media/SIN4_Audio_51-2_120s_CCBYblendercloud.mov"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "pcm_s24le",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcAudio,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{6}

	xcTestResult := &XcTestResult{
		mezFile:           []string{fmt.Sprintf("%s/asegment0-1.mp4", outputDir)},
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio2MonoTo1Stereo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := "./media/gabby_shading_2mono_1080p.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudioJoin,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{0, 1}

	xcTestResult := &XcTestResult{
		timeScale:         44100,
		sampleRate:        44100,
		channelLayoutName: "stereo",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio2MonoUnknownLayoutToStereo(t *testing.T) {
	url := "./media/sample_episode_pcm_audio_35s.mov"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30.08",
		Ecodec2:             "aac",
		Dcodec2:             "",
		AudioBitrate:        128000,
		XcType:              goavpipe.XcAudioJoin,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{0, 1}

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio5_1To5_1(t *testing.T) {
	url := "./media/case_1_video_and_5.1_audio.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		timeScale:         44100,
		sampleRate:        44100,
		channelLayoutName: "5.1",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio5_1ToStereo(t *testing.T) {
	url := "./media/case_1_video_and_5.1_audio.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudioPan,
		FilterDescriptor:    "[0:1]pan=stereo|c0<c0+c4+0.707*c2|c1<c1+c5+0.707*c2[aout]",
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		timeScale:         44100,
		sampleRate:        44100,
		channelLayoutName: "stereo",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioMonoToMono(t *testing.T) {
	url := "./media/case_1_video_and_mono_audio.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudio,
		ChannelLayout:       avpipe.ChannelLayout("mono"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{1}

	xcTestResult := &XcTestResult{
		timeScale:         22050,
		sampleRate:        22050,
		channelLayoutName: "mono",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioQuadToQuad(t *testing.T) {
	url := "./media/case_1_video_and_quad_audio.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudio,
		ChannelLayout:       avpipe.ChannelLayout("quad"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{1}

	xcTestResult := &XcTestResult{
		timeScale:         22050,
		sampleRate:        22050,
		channelLayoutName: "quad",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio6MonoTo5_1(t *testing.T) {
	url := "./media/case_2_video_and_8_mono_audio.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudioMerge,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:3][0:4][0:5][0:6][0:7][0:8]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2| c3=c3|c4=c4|c5=c5[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{3, 4, 5, 6, 7, 8}

	xcTestResult := &XcTestResult{
		timeScale:         44100,
		sampleRate:        44100,
		channelLayoutName: "5.1",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio6MonoUnequalChannelLayoutsTo5_1(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := "./media/TOS8_Audio_51-2_60s_CCBYblendercloud.mov"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudioMerge,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:0][0:1][0:2][0:3][0:4][0:5]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2|c3=c3|c4=c4|c5=c5[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{0, 1, 2, 3, 4, 5}

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "5.1",
	}
	for i := 1; i < 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio6MonoMovPcmTo5_1(t *testing.T) {
	url := "./media/sample_6mono_fr_5_1_audio_35s.mov"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudioMerge,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:0][0:1][0:2][0:3][0:4][0:5]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2|c3=c3|c4=c4|c5=c5[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{0, 1, 2, 3, 4, 5}

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "5.1",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio10Channel_s16To6Channel_5_1(t *testing.T) {
	url := "./media/case_3_video_and_10_channel_audio_10sec.mov"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudioPan,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:1]pan=5.1|c0=c3|c1=c4|c2=c5|c3=c6|c4=c7|c5=c8[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{0}

	xcTestResult := &XcTestResult{
		timeScale:         44100,
		sampleRate:        44100,
		channelLayoutName: "5.1",
	}
	for i := 1; i <= 1; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio2Channel1Stereo(t *testing.T) {
	url := "./media/ELD2_FHD_4_60s_CCBYblendercloud.mov"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudioPan,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		FilterDescriptor:    "[0:1]pan=stereo|c0<c1+0.707*c2|c1<c2+0.707*c1[aout]",
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{1}

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}

	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// Transcode audio pan pcm_s24le with 60000 sample rate, into aac 48000 sample rate.
// This is case 1 with audio input sample rate incompatible with AAC
func TestAudioPan2Channel1Stereo_pcm_60000(t *testing.T) {
	url := "./media/Sintel_30s_6ch_pcm_s24le_60000Hz.mov"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudioPan,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		FilterDescriptor:    "[0:6]pan=stereo|c0=c0|c1=c0[aout]",
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{6}

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}

	for i := 1; i <= 1; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// Transcode mono pcm_s24le with 60000 sample rate, into aac stereo 48000 sample rate.
// This is case 2 with audio input sample rate incompatible with AAC
func TestAudioMonoToStereo_pcm_60000(t *testing.T) {
	url := "./media/Sintel_30s_6ch_pcm_s24le_60000Hz.mov"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              goavpipe.XcAudio,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{3}

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}

	for i := 1; i <= 1; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment0-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestMultiAudioXc(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := videoBigBuckBunny3AudioPath

	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		VideoSegDurationTs:  294912, // 24 frames * 512 ticks = 1 sec at 24fps/12288 timescale
		AudioSegDurationTs:  1428480,
		Ecodec:              h264Codec,
		Dcodec:              "",
		Ecodec2:             "aac",
		EncHeight:           720,
		EncWidth:            1280,
		XcType:              goavpipe.XcAll,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	params.AudioIndex = []int32{1, 2, 3}

	xcTestResult := &XcTestResult{
		timeScale: 12288,
		pixelFmt:  "yuv420p",
	}

	for i := 1; i <= 4; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// TestAudioAtmosBypass verifies that a Dolby Atmos input triggers bypass
// automatically via is_dolby_atmos() in prepare_audio_encoder (the
// PENDING(SS) WIP hack at avpipe_xc.c:1522). No BypassTranscoding or Ecodec2
// is set — the hack mutates params at runtime. If the hack is removed, this
// test should be updated to expect failure rather than success.
func TestAudioAtmosBypass(t *testing.T) {
	url := audioDolbyAtmosPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, url)

	params := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	boilerXc(t, params)

	// Probe the output mezzanine: EAC-3 must be preserved (not re-encoded to AAC).
	// Re-use the global IO handler set by boilerplate — FileInputOpener.Open opens
	// whatever URL is passed to it, so no need to reset it for the probe step.
	mezFile := fmt.Sprintf("%s/asegment0-1.mp4", outputDir)
	xcparams := &goavpipe.XcParams{Url: mezFile, Seekable: true}
	probe, err := avpipe.Probe(xcparams)
	failNowOnError(t, err)

	audioStream := probe.StreamByCodecType("audio")
	require.NotNil(t, audioStream, "expected an audio stream in the mezzanine output")
	assert.Equal(t, "eac3", audioStream.CodecName, "Atmos stream must be bypassed as EAC-3, not re-encoded")
	assert.True(t, audioStream.DolbyAtmos, "expected DolbyAtmos flag to be set in bypass output")
	assert.Equal(t, int64(640000), audioStream.BitRate, "bit rate must be preserved by bypass")
	assert.Equal(t, 6, audioStream.Channels, "channel count must be preserved by bypass")
	assert.Equal(t, "5.1(side)", audioStream.ChannelLayoutName, "channel layout must be preserved by bypass")
	require.NotNil(t, audioStream.MP4, "MP4 must be present for MP4 EAC-3 stream")
	require.NotNil(t, audioStream.MP4.EC3, "MP4.EC3 must be present for Dolby Atmos stream")
	assert.True(t, audioStream.MP4.EC3.JOC, "EC3.JOC must be true for Dolby Atmos")
}

// TestAudioAtmosBypassExplicit verifies EAC-3 passthrough using BypassTranscoding=true,
// which routes audio packets through do_bypass() in avpipe_xc.c without depending on
// the is_dolby_atmos() auto-bypass heuristic. BypassTranscoding is a global flag; for
// XcAudio there is no video stream, so it acts as audio-only bypass.
func TestAudioAtmosBypassExplicit(t *testing.T) {
	url := audioDolbyAtmosPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, url)

	params := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcAudio,
		BypassTranscoding:   true,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	boilerXc(t, params)

	mezFile := fmt.Sprintf("%s/asegment0-1.mp4", outputDir)
	xcparams := &goavpipe.XcParams{Url: mezFile, Seekable: true}
	probe, err := avpipe.Probe(xcparams)
	failNowOnError(t, err)

	audioStream := probe.StreamByCodecType("audio")
	require.NotNil(t, audioStream, "expected an audio stream in the mezzanine output")
	assert.Equal(t, "eac3", audioStream.CodecName, "audio must be copied as EAC-3, not re-encoded")
	assert.True(t, audioStream.DolbyAtmos, "DolbyAtmos flag must survive passthrough")
	assert.Equal(t, int64(640000), audioStream.BitRate, "bit rate must be preserved by bypass")
	assert.Equal(t, 6, audioStream.Channels, "channel count must be preserved by bypass")
	assert.Equal(t, "5.1(side)", audioStream.ChannelLayoutName, "channel layout must be preserved by bypass")
	require.NotNil(t, audioStream.MP4, "MP4 must be present for MP4 EAC-3 stream")
	require.NotNil(t, audioStream.MP4.EC3, "MP4.EC3 must be present for Dolby Atmos stream")
	assert.True(t, audioStream.MP4.EC3.JOC, "EC3.JOC must be true for Dolby Atmos")
}

// Timebase of BBB0_HD_8_XDCAM_120s_CCBYblendercloud.mxf is 1001/60000 - in this case the mp4 muxer changes timebase to 1/60000
// Test both with and without explicit video_time_base to verify both pre and post encoding timebase adjustment.
func TestIrregularTsMezMaker_1001_60000(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := "./media/BBB0_HD_8_XDCAM_120s_CCBYblendercloud.mxf"
	checkFileExists(t, url)
	const expectedSegDurationTs int64 = 1801800

	tests := []struct {
		name          string
		videoTimeBase int // 0 = not specified (decoder default 1001/60000)
	}{
		{"no_timebase", 0},
		{"timebase_60000", 60000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outputDir := path.Join(baseOutPath, fn(), tc.name)

			params := &goavpipe.XcParams{
				BypassTranscoding:   false,
				Format:              "fmp4-segment",
				StartTimeTs:         0,
				DurationTs:          -1,
				StartSegmentStr:     "1",
				SegDuration:         "30.03",
				Ecodec:              h264Codec,
				Dcodec:              "",
				EncHeight:           720,
				EncWidth:            1280,
				XcType:              goavpipe.XcVideo,
				StreamId:            -1,
				SyncAudioToStreamId: -1,
				ForceKeyInt:         120,
				Url:                 url,
				DebugFrameLevel:     debugFrameLevel,
				VideoTimeBase:       tc.videoTimeBase,
			}
			setFastEncodeParams(params, false)
			xcTestResult := &XcTestResult{
				timeScale: 60000,
				pixelFmt:  "yuv420p",
			}

			for i := 1; i <= 4; i++ {
				xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i))
			}

			xcTest(t, outputDir, params, xcTestResult, true)

			// Verify the first 3 segments are exactly 1801800 ts (30.03s) long
			for i := 0; i < 3; i++ {
				xcparams := &goavpipe.XcParams{
					Url:      xcTestResult.mezFile[i],
					Seekable: true,
				}
				probeInfo, err := avpipe.Probe(xcparams)
				failNowOnError(t, err)
				si := probeInfo.Streams[0]
				assert.Equal(t, expectedSegDurationTs, si.DurationTs,
					"segment %d duration_ts mismatch (timebase=%s)", i+1, si.TimeBase.RatString())
			}
		})
	}
}

// Timebase of Rigify-2min is 1/24
func TestIrregularTsMezMaker_1_24(t *testing.T) {
	url := "./media/Rigify-2min.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec:              h264Codec,
		Dcodec:              "",
		EncHeight:           720,
		EncWidth:            1280,
		XcType:              goavpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		timeScale: 12288,
		level:     31,
		pixelFmt:  "yuv420p",
	}

	if setFastEncodeParams(params, false) {
		xcTestResult.level = 0 // fast mode reduces resolution; level depends on resolution
	}

	for i := 1; i <= 4; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// Timebase of Rigify-2min is 1/10000
func TestIrregularTsMezMaker_1_10000(t *testing.T) {
	url := "./media/Rigify-2min-10000ts.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec:              h264Codec,
		Dcodec:              "",
		EncHeight:           720,
		EncWidth:            1280,
		XcType:              goavpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		timeScale: 10000,
		level:     31,
		pixelFmt:  "yuv420p",
	}

	if setFastEncodeParams(params, false) {
		xcTestResult.level = 0 // fast mode reduces resolution; level depends on resolution
	}

	for i := 1; i <= 4; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestMXF_H265MezMaker(t *testing.T) {
	f := fn()
	if testing.Short() {
		// 558.20s on 2018 MacBook Pro (2.9 GHz 6-Core i9, 32 GB RAM, Radeon Pro 560X 4 GB)
		t.Skip("SKIPPING " + f)
	}
	url := "./media/SIN5_4K_MOS_J2K_60s_CCBYblendercloud.mxf"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, f)

	params := &goavpipe.XcParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "30.03",
		Ecodec:            h265Codec,
		Dcodec:            "jpeg2000",
		EncHeight:         -1,
		EncWidth:          -1,
		XcType:            goavpipe.XcVideo,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
		ForceKeyInt:       48,
	}

	xcTestResult := &XcTestResult{
		mezFile:   []string{fmt.Sprintf("%s/vsegment-1.mp4", outputDir)},
		timeScale: 24000,
		level:     150,
		pixelFmt:  "yuv420p",
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestHEVC_H264MezMaker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "15.03",
		Ecodec:            h264Codec,
		Dcodec:            "hevc",
		EncHeight:         -1,
		EncWidth:          -1,
		XcType:            goavpipe.XcVideo,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		mezFile:   []string{fmt.Sprintf("%s/vsegment-1.mp4", outputDir)},
		timeScale: 24000,
		level:     51,
		pixelFmt:  "yuv420p",
	}

	if setFastEncodeParams(params, false) {
		xcTestResult.level = 0 // fast mode reduces resolution; level depends on resolution
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// Run a mez making session and fail on opening the input.
// This simulates the cases when opening the input fails time to time (for example, opening the cloud object).
func TestMezMakerWithOpenInputError(t *testing.T) {
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "15.03",
		Ecodec:            h264Codec,
		Dcodec:            "hevc",
		EncHeight:         -1,
		EncWidth:          -1,
		XcType:            goavpipe.XcVideo,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
	}

	boilerplate(t, outputDir, url)

	fio := &xc.FileInputOpener{URL: url, ErrorOnOpen: true}
	foo := &xc.FileOutputOpener{Dir: outputDir}
	goavpipe.InitIOHandler(fio, foo)

	setFastEncodeParams(params, false)
	params.EncHeight = 360 // slow down a bit to allow for the cancel
	params.EncWidth = 640

	handle, err := avpipe.XcInit(params)
	assert.Greater(t, handle, int32(0))
	failNowOnError(t, err)
	err = avpipe.XcRun(handle)
	assert.Error(t, err)

}

// Run a mez making session and fail on reading from input.
// This simulates the cases when reading the input fails time to time (for example, reading from cloud).
func TestMezMakerWithReadInputError(t *testing.T) {
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "15.03",
		Ecodec:            h264Codec,
		Dcodec:            "hevc",
		EncHeight:         -1,
		EncWidth:          -1,
		XcType:            goavpipe.XcVideo,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
	}

	boilerplate(t, outputDir, url)

	fio := &xc.FileInputOpener{URL: url, ErrorOnRead: true}
	foo := &xc.FileOutputOpener{Dir: outputDir}
	goavpipe.InitIOHandler(fio, foo)

	setFastEncodeParams(params, false)
	params.EncHeight = 360 // slow down a bit to allow for the cancel
	params.EncWidth = 640

	handle, err := avpipe.XcInit(params)
	assert.Greater(t, handle, int32(0))
	failNowOnError(t, err)
	err = avpipe.XcRun(handle)
	assert.Error(t, err)

}

// Run a probe and fail on reading from input.
// This simulates the cases when reading the input fails time to time (for example, reading from cloud).
func TestProbeWithReadInputError(t *testing.T) {
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	boilerplate(t, outputDir, url)

	fio := &xc.FileInputOpener{URL: url, ErrorOnRead: true}
	foo := &xc.FileOutputOpener{Dir: outputDir}
	goavpipe.InitIOHandler(fio, foo)

	params := &goavpipe.XcParams{
		Url:      url,
		Seekable: true,
	}
	probe, err := avpipe.Probe(params)
	assert.Error(t, err)
	assert.Equal(t, (*goavpipe.ProbeInfo)(nil), probe)

}

func TestHEVC_H265ABRTranscode(t *testing.T) {
	f := fn()
	if testing.Short() {
		// 403.23s on 2018 MacBook Pro (2.9 GHz 6-Core i9, 32 GB RAM, Radeon Pro 560X 4 GB)
		t.Skip("SKIPPING " + f)
	}
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	checkFileExists(t, url)

	videoMezDir := path.Join(baseOutPath, f, "VideoMez4H265")
	videoABRDir := path.Join(baseOutPath, f, "VideoABR4H265")

	params := &goavpipe.XcParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "30",
		Ecodec:            h265Codec,
		Dcodec:            "hevc",
		EncHeight:         -1,
		EncWidth:          -1,
		XcType:            goavpipe.XcVideo,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
	}

	setupOutDir(t, videoMezDir)
	xcTest(t, videoMezDir, params, nil, true)

	setupOutDir(t, videoABRDir)
	url = videoMezDir + "/vsegment-1.mp4"
	log.Debug("STARTING video ABR for", "file", url)
	params.XcType = goavpipe.XcVideo
	params.Format = "dash"
	params.VideoSegDurationTs = 48000
	params.Url = url
	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: videoABRDir})
	boilerXc(t, params)

}

// TestMezBypassWithBframesAndCts tests mez bypass and ABR bypass using a DolbyVision MV-HEVC source with dense B-frame GOPs.
// The source is 24fps with IDR frames every 48 frames and each 30s
// The generated mez parts begin with an IDR frame that has a non-zero CTS (PTS 1536 in this case) due to
// the bframe structure which makes the IDR frame the 4th frame in presentation order.
// This test covers the dashenc.c fix to force 'frag_discont' and 'use_editlist=0' for DASH output:
// - without the fix the first frame of every DASH segment has the wrong PTS and parts 2
// - with the fix the segments have proper PTS (effectively copied from the source)
func TestMezBypassWithBframesAndCts(t *testing.T) {
	const (
		mezPartTimeScale     = int64(12288)
		mezPartDurationTs    = int64(30) * mezPartTimeScale
		abrSegmentDurationTs = int64(2) * mezPartTimeScale
	)

	checkFileExists(t, dovi20BFramesCTSTestSource)

	testDir := path.Join(baseOutPath, fn())
	mezDir := path.Join(testDir, "Mez")
	mezParts := runBFramesCTSMezBypass(t, dovi20BFramesCTSTestSource, mezDir, mezPartTimeScale)

	tests := []struct {
		name               string
		url                string
		startPts           int64
		startSegmentStr    string
		startFragmentIndex int32
		expectedChunks     int
	}{
		{
			name:               "p1",
			url:                mezParts[0],
			startPts:           0,
			startSegmentStr:    "1",
			startFragmentIndex: 1,
			expectedChunks:     15,
		},
		{
			name:               "p2",
			url:                mezParts[1],
			startPts:           mezPartDurationTs,
			startSegmentStr:    "16",
			startFragmentIndex: 16,
			expectedChunks:     15,
		},
		{
			name:               "p3",
			url:                mezParts[2],
			startPts:           2 * mezPartDurationTs,
			startSegmentStr:    "31",
			startFragmentIndex: 31,
			expectedChunks:     15,
		},
		{
			name:               "p4",
			url:                mezParts[3],
			startPts:           3 * mezPartDurationTs,
			startSegmentStr:    "46",
			startFragmentIndex: 46,
			expectedChunks:     0,
		},
	}

	var previousResult abrBypassDashPTSResult
	var havePreviousResult bool
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result := runABRBypassDashPTS(t, tt.url, tt.startPts, tt.startSegmentStr, tt.startFragmentIndex, abrSegmentDurationTs, tt.expectedChunks)
			if havePreviousResult {
				assertABRBypassPartsContiguous(t, previousResult, result)
			}
			previousResult = result
			havePreviousResult = true
		})
	}
}

func runBFramesCTSMezBypass(t *testing.T, source, mezDir string, mezPartTimeScale int64) []string {
	t.Helper()

	params := &goavpipe.XcParams{
		Url:                 source,
		BypassTranscoding:   true,
		Format:              "fmp4-segment",
		DurationTs:          -1,
		StartPts:            0,
		StartSegmentStr:     "1",
		VideoBitrate:        30000000,
		VideoSegDurationTs:  -1,
		SegDuration:         "30.0000",
		ForceKeyInt:         48,
		Ecodec:              h265Codec, // ignored for bypass, but avpipe still wants an encoder name
		Dcodec:              "hevc",
		GPUIndex:            -1,
		EncHeight:           1440,
		EncWidth:            2560,
		XcType:              goavpipe.XcVideo,
		Seekable:            true,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		VideoTimeBase:       24,
		VideoLayout:         int32(goavpipe.VideoLayoutMVHEVC),
		DebugFrameLevel:     debugFrameLevel,
	}

	boilerplate(t, mezDir, source)
	boilerXc(t, params)

	mezParts, err := filepath.Glob(path.Join(mezDir, "vsegment-*.mp4"))
	require.NoError(t, err)
	sort.Strings(mezParts)
	require.Len(t, mezParts, 4, "expected 30s, 30s, 30s and partial 18s mez video parts")

	expectedFirstCTS := int32(3 * (mezPartTimeScale / 24))
	for _, part := range mezParts {
		assertMezPartStartsWithIDRCTS(t, part, expectedFirstCTS)
	}
	return mezParts
}

func assertMezPartStartsWithIDRCTS(t *testing.T, part string, expectedFirstCTS int32) {
	t.Helper()

	frames, _ := sourceMP4FrameTimings(t, part)
	require.NotEmpty(t, frames, "generated mez part has no video frames: %s", part)

	first := frames[0]
	require.True(t, first.sync, "%s first sample should be sync/IDR", part)
	require.Equal(t, "I", first.frameType, "%s first sample frame type", part)
	require.Equal(t, uint64(0), first.dts, "%s first sample DTS", part)
	require.Equal(t, int64(expectedFirstCTS), first.pts, "%s first sample PTS", part)
	require.Equal(t, expectedFirstCTS, first.composition, "%s first sample CTS/composition offset", part)
}

type abrBypassDashPTSResult struct {
	name         string
	url          string
	startPts     int64
	sourceFrames []dashFrameTiming
	outputFrames []dashFrameTiming
}

func runABRBypassDashPTS(
	t *testing.T,
	url string,
	startPts int64,
	startSegmentStr string,
	startFragmentIndex int32,
	abrSegmentDurationTs int64,
	expectedChunks int,
) abrBypassDashPTSResult {
	t.Helper()

	checkFileExists(t, url)

	dashDir := path.Join(baseOutPath, strings.NewReplacer("/", "_").Replace(t.Name()), "DASH")
	params := &goavpipe.XcParams{
		Url:                url,
		BypassTranscoding:  true,
		Format:             "dash",
		DurationTs:         -1,
		StartPts:           startPts,
		StartSegmentStr:    startSegmentStr,
		VideoBitrate:       30000000,
		VideoSegDurationTs: abrSegmentDurationTs,
		StartFragmentIndex: startFragmentIndex,
		ForceKeyInt:        48,
		Ecodec:             h265Codec, // ignored for bypass, but avpipe still wants an encoder name
		Dcodec:             "hevc",
		GPUIndex:           -1,
		EncHeight:          1440,
		EncWidth:           2560,
		XcType:             goavpipe.XcVideo,
		Seekable:           true,
		StreamId:           -1,
		VideoLayout:        int32(goavpipe.VideoLayoutMVHEVC),
		DebugFrameLevel:    debugFrameLevel,
	}

	setupOutDir(t, dashDir)
	goavpipe.InitUrlIOHandler(url,
		&xc.FileInputOpener{URL: url},
		&xc.FileOutputOpener{Dir: dashDir})
	boilerXc(t, params)

	dashVideoInit(t, dashDir)
	chunks, err := filepath.Glob(path.Join(dashDir, "vchunk-stream0-*.m4s"))
	require.NoError(t, err)
	sort.Strings(chunks)
	if expectedChunks > 0 {
		require.Len(t, chunks, expectedChunks, "%s should produce %d DASH video chunks", url, expectedChunks)
	} else {
		require.NotEmpty(t, chunks, "%s should produce at least one DASH video chunk", url)
	}

	sourceFrames, sourceBFrameStats := sourceMP4FrameTimings(t, url)
	outputFrames := assertDashChunksPTSFrameByFrame(t, chunks)
	assertDashMPDSegmentTimelineStart(t, dashDir, sourceFrames, startPts)
	printSourceOutputPTSComparison(sourceFrames, outputFrames, sourceBFrameStats, startPts)
	assertSourceOutputFrameTimings(t, sourceFrames, outputFrames, startPts)
	return abrBypassDashPTSResult{
		name:         t.Name(),
		url:          url,
		startPts:     startPts,
		sourceFrames: sourceFrames,
		outputFrames: outputFrames,
	}
}

// runHEVCHDR10MezAndABR is the test body for HDR10 mez + ABR with configurable encoder
// Validates: pix_fmt, profile, and colr/mdcv/clli through:
//
//	source -> mez (HEVC, full-res)
//	mez    -> ABR bypass (no re-encode)
//	mez    -> ABR re-encode (720p)
func runHEVCHDR10MezAndABR(t *testing.T, ecodec string) {
	f := fn()
	if testing.Short() {
		t.Skip("SKIPPING " + f + " (fast mode)")
	}
	checkFileExists(t, hdr10TestSource)

	mezDir := path.Join(baseOutPath, f, "Mez")
	bypassDir := path.Join(baseOutPath, f, "ABRBypass")
	abrDir := path.Join(baseOutPath, f, "ABR720")

	// Stage 1: source → mez (no bitdepth and profile specified)
	mezParams := &goavpipe.XcParams{
		Url:               hdr10TestSource,
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		StartPts:          0,
		DurationTs:        hdr10TestDurationTs,
		StartSegmentStr:   "1",
		SegDuration:       "30",
		Ecodec:            ecodec,
		Dcodec:            "hevc",
		EncHeight:         -1, // preserve 2160
		EncWidth:          -1, // preserve 3840
		XcType:            goavpipe.XcVideo,
		StreamId:          -1,
		MasterDisplay:     hdr10MasterDisplay,
		MaxCLL:            hdr10MaxCLL,
		DebugFrameLevel:   debugFrameLevel,
	}

	setupOutDir(t, mezDir)
	xcTest(t, mezDir, mezParams, nil, true)

	mezExpected := expectedHDR10{
		PixFmt:           "yuv420p10le",
		ProfileName:      "Main 10",
		ColorPrimaries:   "bt2020",
		ColorTransfer:    "smpte2084",
		ColorSpace:       "bt2020nc",
		ColorRange:       "tv",
		Width:            3840,
		Height:           2160,
		MasteringDisplay: hdr10MasterDisplay,
		MaxCLL:           hdr10MaxCLL,
	}
	assertHDR10(t, path.Join(mezDir, "vsegment-1.mp4"), mezExpected)
	assertHDR10(t, path.Join(mezDir, "vsegment-2.mp4"), mezExpected)

	mezSeg := path.Join(mezDir, "vsegment-1.mp4")

	// Stage 2a: mez → ABR bypass
	{
		bypassParams := *mezParams
		bypassParams.Url = mezSeg
		bypassParams.Format = "dash"
		bypassParams.BypassTranscoding = true
		bypassParams.VideoSegDurationTs = 48000
		bypassParams.DurationTs = -1

		setupOutDir(t, bypassDir)
		goavpipe.InitUrlIOHandler(mezSeg,
			&xc.FileInputOpener{URL: mezSeg},
			&xc.FileOutputOpener{Dir: bypassDir})
		boilerXc(t, &bypassParams)

		assertHDR10(t, dashVideoInit(t, bypassDir), mezExpected)
	}

	// Stage 2b: mez → ABR
	{
		scaledExpected := mezExpected
		scaledExpected.Width = 0 // computed from aspect ratio (16:9 → 1280)
		scaledExpected.Height = 720

		abrParams := *mezParams
		abrParams.Url = mezSeg
		abrParams.Format = "dash"
		abrParams.VideoSegDurationTs = 48000
		abrParams.EncHeight = 720
		abrParams.EncWidth = -1
		abrParams.DurationTs = -1

		setupOutDir(t, abrDir)
		goavpipe.InitUrlIOHandler(mezSeg,
			&xc.FileInputOpener{URL: mezSeg},
			&xc.FileOutputOpener{Dir: abrDir})
		boilerXc(t, &abrParams)

		assertHDR10(t, dashVideoInit(t, abrDir), scaledExpected)
	}
}

// mvhevcCase represents parameters for a single MVHEVC source
type mvhevcCase struct {
	src                  string // path to source MV-HEVC mp4
	width, height        uint16 // per-view resolution
	expectBaseProfileIDC int    // hvcC base layer: 1=Main (SDR), 2=Main 10 (HDR)
	expectLevelIDC       int    // expected HEVC level_idc (150=L5.1, 156=L5.2, ...)
	expectDOVIProfile    int    // expected Dolby Vision profile
	expectHDR10          bool
	expectMdcv           bool
	expectClli           bool
}

// TestMVHEVC_MezAndABRBypass exercises source → mez → ABR for MV-HEVC content for 'bypass'
func TestMVHEVC_MezAndABRBypass(t *testing.T) {
	cases := map[string]mvhevcCase{
		"SDR_4K": {
			src:                  "./media/sample_mvhevc_4k.mp4",
			width:                3840,
			height:               2160,
			expectBaseProfileIDC: 1, // Main
			expectLevelIDC:       150,
		},
		"SDR_720p": {
			src:                  "./media/tos_720_mvhevc_sdr.mp4",
			width:                1280,
			height:               720,
			expectBaseProfileIDC: 1, // Main
			expectLevelIDC:       93,
		},
		"HDR_1440p": {
			src:                  "./media/sample_mvhevc_2560x1440@8.50.mp4",
			width:                2560,
			height:               1440,
			expectBaseProfileIDC: 2, // Main 10
			expectLevelIDC:       156,
			expectHDR10:          true,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			runMVHEVCMezAndABRBypass(t, c)
		})
	}
}

// TestDOVI20_MVHEVC_MezAndABRBypass verifies Dolby Vision MV-HEVC bypass.
func TestDOVI20_MVHEVC_MezAndABRBypass(t *testing.T) {
	runMVHEVCMezAndABRBypass(t, mvhevcCase{
		src:                  "./media/3d_test_reel_2024_dovi_mvhevc.mp4",
		width:                3840,
		height:               2160,
		expectBaseProfileIDC: 2, // Main 10
		expectLevelIDC:       183,
		expectDOVIProfile:    avdesc.DOVIProfileMVHEVC,
	})
}

func runMVHEVCMezAndABRBypass(t *testing.T, c mvhevcCase) {
	f := fn() + "/" + t.Name()
	checkFileExists(t, c.src)

	mezDir := path.Join(baseOutPath, f, "Mez")
	abrDir := path.Join(baseOutPath, f, "ABRBypass")

	// Sanity-check the source has the boxes we expect repackagers to drop —
	// if it doesn't, there's nothing useful for this test to verify.
	srcIns, err := mvhevc.Inspect(c.src)
	failNowOnError(t, err)
	assert.True(t, srcIns.HasMultiLayerVPS, "source not multi-layer MV-HEVC")
	assert.True(t, srcIns.HasOinfSgpd, "source missing oinf")
	assert.True(t, srcIns.HasLinfSgpd, "source missing linf")
	assert.True(t, srcIns.HasTrgrCstg, "source missing trgr/cstg")
	if c.expectDOVIProfile > 0 {
		assertDOVIMVHEVCProfile(t, c.src, c.expectDOVIProfile)
	}

	// Stage 1: source → mez (bypass repackage to fmp4-segment).
	// Ecodec must be a real encoder libavformat can find even in bypass —
	// avpipe still allocates an encoder context. libx265 is the natural pick
	// for HEVC input regardless of bit depth (libx265 handles both 8 and 10).
	mezParams := &goavpipe.XcParams{
		Url:               c.src,
		BypassTranscoding: true,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		StartPts:          0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "30",
		Ecodec:            "libx265",
		Dcodec:            "hevc",
		XcType:            goavpipe.XcVideo,
		StreamId:          -1,
		VideoLayout:       int32(goavpipe.VideoLayoutMVHEVC),
		DebugFrameLevel:   debugFrameLevel,
	}
	setupOutDir(t, mezDir)
	xcTest(t, mezDir, mezParams, nil, true)

	mezSeg := path.Join(mezDir, "vsegment-1.mp4")
	// fmp4-segment output preserves the sample-entry children (lhvC/vexu/hfov)
	// since the mov muxer copies the visual sample entry opaquely.
	assertMVHEVCStructure(t, mezSeg, c)

	// Stage 2: mez → ABR (bypass only for MVHEVC)
	abrParams := *mezParams
	abrParams.Url = mezSeg
	abrParams.Format = "dash"
	abrParams.VideoSegDurationTs = 48000
	abrParams.DurationTs = -1
	abrParams.BypassTranscoding = true
	if !abrParams.BypassTranscoding {
		t.Fatal("ABR params must be bypass for MV-HEVC")
	}

	setupOutDir(t, abrDir)
	goavpipe.InitUrlIOHandler(mezSeg,
		&xc.FileInputOpener{URL: mezSeg},
		&xc.FileOutputOpener{Dir: abrDir})
	boilerXc(t, &abrParams)

	// DASH init segment:
	// - oinf/linf/trgr restored by the in-stream patcher
	// - lhvC restored by dashenc.c fix
	assertMVHEVCStructure(t, dashVideoInit(t, abrDir), c)
}

// assertMVHEVCStructure verifies all MV-HEVC metadata
func assertMVHEVCStructure(t *testing.T, mp4Path string, c mvhevcCase) {
	t.Helper()
	ins, err := mvhevc.Inspect(mp4Path)
	failNowOnError(t, err)
	if !assert.True(t, ins.HasVideoTrak, "no video trak in %s", mp4Path) {
		return
	}
	assert.True(t, ins.HasHvcC, "missing hvcC in %s", mp4Path)
	assert.True(t, ins.HasMultiLayerVPS, "VPS not multi-layer in %s", mp4Path)
	assert.True(t, ins.HasOinfSgpd, "missing oinf sgpd in %s", mp4Path)
	assert.True(t, ins.HasLinfSgpd, "missing linf sgpd in %s", mp4Path)
	assert.True(t, ins.HasTrgrCstg, "missing trgr/cstg in %s", mp4Path)
	assert.True(t, ins.HasLhvC, "missing lhvC in %s", mp4Path)
	if c.width > 0 {
		assert.Equal(t, c.width, ins.Width, "width in %s", mp4Path)
	}
	if c.height > 0 {
		assert.Equal(t, c.height, ins.Height, "height in %s", mp4Path)
	}

	// Profile/level come from the base-layer SPS — SDR (Main) from HDR (Main 10)
	mp4f, err := os.Open(mp4Path)
	if assert.NoError(t, err, "open %s", mp4Path) {
		defer func() { _ = mp4f.Close() }()
		infos, err := mp4e.ExtractCodecInfo(mp4f)
		if assert.NoError(t, err, "ExtractCodecInfo on %s", mp4Path) && len(infos) > 0 {
			assert.Equal(t, c.expectBaseProfileIDC, infos[0].ProfileIDC, "base profile_idc in %s", mp4Path)
			assert.Equal(t, c.expectLevelIDC, infos[0].Level, "level_idc in %s", mp4Path)
			if c.expectDOVIProfile > 0 {
				assertDOVIMVHEVCCodecInfo(t, infos[0], c.expectDOVIProfile, mp4Path)
			}
		}
	}

	if c.expectHDR10 {
		// HDR10 basics: BT.2020 primaries + SMPTE 2084 PQ transfer + BT.2020-NCL matrix + 10-bit.
		assert.Equal(t, byte(10), ins.BitDepthLuma, "expected 10-bit luma in %s", mp4Path)
		assert.Equal(t, byte(10), ins.BitDepthChroma, "expected 10-bit chroma in %s", mp4Path)
		assert.Equal(t, byte(9), ins.ColourPrimaries, "expected BT.2020 colour primaries in %s", mp4Path)
		assert.Equal(t, byte(16), ins.TransferCharacteristics, "expected SMPTE 2084 (PQ) transfer in %s", mp4Path)
		assert.Equal(t, byte(9), ins.MatrixCoefficients, "expected BT.2020-NCL matrix in %s", mp4Path)
	}
	if c.expectMdcv {
		assert.True(t, ins.HasMdcv, "missing mdcv (mastering display) in %s", mp4Path)
	}
	if c.expectClli {
		assert.True(t, ins.HasClli, "missing clli (content light level) in %s", mp4Path)
	}
}

func assertDOVIMVHEVCProfile(t *testing.T, mp4Path string, expectProfile int) {
	t.Helper()
	f, err := os.Open(mp4Path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	infos, err := mp4e.ExtractCodecInfo(f)
	require.NoError(t, err)
	require.NotEmpty(t, infos, "no codec info in %s", mp4Path)
	assertDOVIMVHEVCCodecInfo(t, infos[0], expectProfile, mp4Path)
}

func assertDOVIMVHEVCCodecInfo(t *testing.T, info *mp4e.CodecInfo, expectProfile int, mp4Path string) {
	t.Helper()
	require.NotNil(t, info, "nil codec info for %s", mp4Path)
	assert.Equal(t, "hvc1", info.CodecTagString, "codec tag in %s", mp4Path)
	assert.Equal(t, mp4e.Mp4VideoLayoutMVHEVC, info.VideoLayout, "video layout in %s", mp4Path)
	assert.Equal(t, 6, info.EnhancementProfileIDC, "MV-HEVC enhancement profile_idc in %s", mp4Path)
	require.NotNil(t, info.DOVI, "Dolby Vision configuration missing from %s", mp4Path)
	assert.Equal(t, expectProfile, info.DOVI.Profile, "DOVI profile in %s", mp4Path)
	assert.Equal(t, "dvh1", info.DOVI.FourCC, "DOVI fourcc in %s", mp4Path)
	assert.True(t, info.DOVI.RPUPresent, "DOVI RPU flag in %s", mp4Path)
	assert.True(t, info.DOVI.BLPresent, "DOVI BL flag in %s", mp4Path)
}

// TestHEVC_HDR10_MezAndABR creates a mez from source and ABR rungs from mez,
// using libx265 (CPU). Validates: pix_fmt, profile, and colr/mdcv/clli.
func TestHEVC_HDR10_MezAndABR(t *testing.T) {
	runHEVCHDR10MezAndABR(t, h265Codec)
}

// TestHEVC_HDR10_MezAndABR_Nvenc creates a mez from souce and ABR rungs from mez,
// sing nvenc (GPU).
// Requires enableNvenc
func TestHEVC_HDR10_MezAndABR_Nvenc(t *testing.T) {
	if !enableNvenc {
		t.Skip("enableNvenc=false; skipping hevc_nvenc HDR10 test")
	}
	runHEVCHDR10MezAndABR(t, "hevc_nvenc")
}

// assertDOVI81 opens the MP4 at mp4Path, parses it with mp4e.ExtractCodecInfo,
// and asserts that a dvvC box is present with the hvc1 codec tag (required for dvh1
// manifest signalling and Apple HLS compatibility) and Profile=8, Level=1, BLSignalCompatibilityID=1.
func assertDOVI81(t *testing.T, mp4Path string) {
	t.Helper()
	f, err := os.Open(mp4Path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	infos, err := mp4e.ExtractCodecInfo(f)
	require.NoError(t, err)
	var info *mp4e.CodecInfo
	for _, ci := range infos {
		if ci.DOVI != nil {
			info = ci
			break
		}
	}
	require.NotNil(t, info, "dvvC box missing from %s", mp4Path)
	assert.Equal(t, "hvc1", info.CodecTagString, "codec tag must be hvc1 (not hev1) in %s", mp4Path)
	dovi := info.DOVI
	assert.Equal(t, 8, dovi.Profile, "DOVI.Profile in %s", mp4Path)
	assert.Equal(t, 1, dovi.Level, "DOVI.Level in %s", mp4Path)
	assert.Equal(t, 1, dovi.BLSignalCompatibilityID, "DOVI.BLSignalCompatibilityID in %s", mp4Path)
}

// assertDOVI20 opens the MP4 at mp4Path and asserts that a Dolby Vision
// configuration box (dvcC, per spec) is present with Profile=20, CCID=0, FourCC=dvh1,
// and that the stream is MV-HEVC.
func assertDOVI20(t *testing.T, mp4Path string) {
	t.Helper()
	f, err := os.Open(mp4Path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	infos, err := mp4e.ExtractCodecInfo(f)
	require.NoError(t, err)
	var info *mp4e.CodecInfo
	for _, ci := range infos {
		if ci.DOVI != nil {
			info = ci
			break
		}
	}
	require.NotNil(t, info, "dvcC/dvvC box missing from %s", mp4Path)
	assert.Equal(t, mp4e.Mp4VideoLayoutMVHEVC, info.VideoLayout, "must be MV-HEVC in %s", mp4Path)
	dovi := info.DOVI
	assert.Equal(t, 20, dovi.Profile, "DOVI.Profile in %s", mp4Path)
	assert.Equal(t, 0, dovi.BLSignalCompatibilityID, "DOVI.BLSignalCompatibilityID in %s", mp4Path)
	assert.Equal(t, "dvh1", dovi.FourCC, "DOVI.FourCC in %s", mp4Path)
	// Profile 20 must use dvcC per Dolby Vision ISOBMFF spec v2.7.1.
	assert.Equal(t, "dvcC", dovi.BoxType, "DV config box type in %s", mp4Path)
}

// TestDOVI81_MezAndDASH verifies that the dvvC box (Dolby Vision configuration)
// survives the two-stage bypass pipeline used in production:
//
//	source → mez (fmp4-segment, bypass)
//	mez    → DASH init segment (bypass)
//
// Asserts that mp4e.ExtractCodecInfo on the DASH init segment returns DOVI != nil
// with the hvc1 codec tag preserved (required for dvh1 manifest signaling) and
// Profile=8, Level=1, BLSignalCompatibilityID=1.
func TestDOVI81_MezAndDASH(t *testing.T) {
	checkFileExists(t, dovi81TestSource)

	mezDir := path.Join(baseOutPath, fn(), "Mez")
	dashDir := path.Join(baseOutPath, fn(), "DASH")

	// Stage 1: source → mez (fmp4-segment, bypass)
	// Params mirror what xcMezVideoParams() produces for an h264-default ABR profile
	// with bypass_mode=true: Ecodec comes from defaultCodecs(), Dcodec is unset,
	// and dimensions come from the rung spec.
	mezParams := goavpipe.XcParams{
		Url:                 dovi81TestSource,
		BypassTranscoding:   true,
		Format:              "fmp4-segment",
		DurationTs:          -1,
		StartSegmentStr:     "1",
		VideoBitrate:        4500000,
		VideoSegDurationTs:  -1,
		SegDuration:         "30.0000",
		ForceKeyInt:         48,
		Ecodec:              "libx264", // ignored for bypass
		GPUIndex:            -1,
		EncHeight:           720,
		EncWidth:            1280,
		XcType:              goavpipe.XcVideo,
		Seekable:            true,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		VideoTimeBase:       24,
		BitDepth:            8,
		DebugFrameLevel:     debugFrameLevel,
	}

	boilerplate(t, mezDir, dovi81TestSource)
	boilerXc(t, &mezParams)

	// Stage 2: mez → DASH (bypass)
	mezSeg := path.Join(mezDir, "vsegment-1.mp4")
	dashParams := goavpipe.XcParams{
		Url:                mezSeg,
		BypassTranscoding:  true,
		Format:             "dash",
		DurationTs:         25088,
		StartSegmentStr:    "1",
		VideoBitrate:       4500000,
		VideoSegDurationTs: 24576,
		StartFragmentIndex: 1,
		ForceKeyInt:        48,
		Ecodec:             h265Codec, // ignored for bypass
		GPUIndex:           -1,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		DebugFrameLevel:    debugFrameLevel,
	}

	setupOutDir(t, dashDir)
	goavpipe.InitUrlIOHandler(mezSeg,
		&xc.FileInputOpener{URL: mezSeg},
		&xc.FileOutputOpener{Dir: dashDir})
	boilerXc(t, &dashParams)

	assertDOVI81(t, dashVideoInit(t, dashDir))
}

// TestDOVI20_MezAndDASH verifies that the dvcC box (Dolby Vision Profile 20 configuration)
// survives the two-stage bypass pipeline used in production:
//
//	source → mez (fmp4-segment, bypass, VideoLayout=MV-HEVC)
//	mez    → DASH init segment (bypass, VideoLayout=MV-HEVC)
//
// The MV-HEVC output wrapper (wrapMvhevcOutputHandler / StreamPatcher) intercepts
// segment bytes and, via fixVideoTrak / fixDVBoxType, renames the DV config box from
// dvwC (FFmpeg bug for profile 20) to dvcC (per Dolby Vision ISOBMFF spec v2.7.1),
// and injects oinf/linf sample-group descriptors and trgr/cstg track-group box
// stripped by FFmpeg bypass.
//
// Asserts dvcC present at both stages (mez sanity-check + final DASH init segment)
// with Profile=20, BLSignalCompatibilityID=0, FourCC=dvh1, BoxType=dvcC, and
// VideoLayout=MVHEVC.
func TestDOVI20_MezAndDASH(t *testing.T) {
	checkFileExists(t, dovi20TestSource)

	mezDir := path.Join(baseOutPath, fn(), "Mez")
	dashDir := path.Join(baseOutPath, fn(), "DASH")

	// Stage 1: source → mez (fmp4-segment, bypass)
	// Mirrors xcMezVideoParams() for a bypass_mode=true ABR profile with VideoLayout=MV-HEVC.
	mezParams := goavpipe.XcParams{
		Url:                 dovi20TestSource,
		BypassTranscoding:   true,
		Format:              "fmp4-segment",
		DurationTs:          -1,
		StartSegmentStr:     "1",
		VideoBitrate:        30000000,
		VideoSegDurationTs:  -1,
		SegDuration:         "30.0000",
		ForceKeyInt:         48,
		Ecodec:              h265Codec, // ignored for bypass
		GPUIndex:            -1,
		EncHeight:           2160,
		EncWidth:            3840,
		XcType:              goavpipe.XcVideo,
		Seekable:            true,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		VideoTimeBase:       24,
		VideoLayout:         int32(goavpipe.VideoLayoutMVHEVC),
		DebugFrameLevel:     debugFrameLevel,
	}

	boilerplate(t, mezDir, dovi20TestSource)
	boilerXc(t, &mezParams)

	mezSeg := path.Join(mezDir, "vsegment-1.mp4")
	assertDOVI20(t, mezSeg) // sanity-check: dvcC must survive stage 1

	// Stage 2: mez → DASH init segment (bypass)
	// VideoLayout=MV-HEVC triggers the MV-HEVC output wrapper which calls
	// injectDVProfile20Box when FFmpeg strips the dvcC from the DASH output.
	dashParams := goavpipe.XcParams{
		Url:                mezSeg,
		BypassTranscoding:  true,
		Format:             "dash",
		DurationTs:         61440,
		StartSegmentStr:    "1",
		VideoBitrate:       30000000,
		VideoSegDurationTs: 24576,
		StartFragmentIndex: 1,
		ForceKeyInt:        48,
		Ecodec:             h265Codec, // ignored for bypass
		GPUIndex:           -1,
		EncHeight:          2160,
		EncWidth:           3840,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		VideoLayout:        int32(goavpipe.VideoLayoutMVHEVC),
		DebugFrameLevel:    debugFrameLevel,
	}

	setupOutDir(t, dashDir)
	goavpipe.InitUrlIOHandler(mezSeg,
		&xc.FileInputOpener{URL: mezSeg},
		&xc.FileOutputOpener{Dir: dashDir})
	boilerXc(t, &dashParams)

	assertDOVI20(t, dashVideoInit(t, dashDir))
}

func TestAVPipeStats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := "./media/Rigify-2min.mp4"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec:              h264Codec,
		Dcodec:              "",
		Ecodec2:             "aac",
		EncHeight:           720,
		EncWidth:            1280,
		XcType:              goavpipe.XcAll,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	setFastEncodeParams(params, false)

	xcTestResult := &XcTestResult{
		timeScale: 12288,
		pixelFmt:  "yuv420p",
	}

	for i := 1; i <= 4; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)

	assert.Equal(t, int64(2880), statsInfo.EncodingVideoFrameStats.TotalFramesWritten)
	assert.Equal(t, int64(5625), statsInfo.EncodingAudioFrameStats.TotalFramesWritten)
	assert.Equal(t, uint64(0), statsInfo.FirstKeyFramePTS)
	// FIXME
	//assert.Equal(t, int64(720), statsInfo.EncodingVideoFrameStats.FramesWritten)
	//assert.Equal(t, int64(1406), statsInfo.EncodingAudioFrameStats.FramesWritten)
	assert.Equal(t, uint64(5625), statsInfo.AudioFramesRead)
	assert.Equal(t, uint64(2880), statsInfo.VideoFramesRead)
}

// This unit test is almost a complete test for mez, abr, muxing and probing. It does:
// 1) Creates audio and video mez files
// 2) Creates ABR segments using audio and video mez files in step 1
// 3) Mux the ABR audio and video segments from step 2
// 4) Probes the initial mez file from step 1 and mux output from step 3. The duration has to be equal.
func TestABRMuxing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	f := fn()
	log.Info("STARTING " + f)
	url := "./media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov"
	checkFileExists(t, url)

	videoMezDir := path.Join(baseOutPath, f, "VideoMez4Muxing")
	audioMezDir := path.Join(baseOutPath, f, "AudioMez4Muxing")
	videoABRDir := path.Join(baseOutPath, f, "VideoABR4Muxing")
	videoABRDir2 := path.Join(baseOutPath, f, "VideoABR4Mugooglexing2")
	audioABRDir := path.Join(baseOutPath, f, "AudioABR4Muxing")
	audioABRDir2 := path.Join(baseOutPath, f, "AudioABR4Muxing2")
	muxOutDir := path.Join(baseOutPath, f, "MuxingOutput")

	// Create video mez files
	setupOutDir(t, videoMezDir)
	params := &goavpipe.XcParams{
		BypassTranscoding:  false,
		Format:             "fmp4-segment",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       128000,
		SampleRate:         48000,
		VideoSegDurationTs: 720000,
		AudioSegDurationTs: 1440000,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
		ForceKeyInt:        48,
		Profile:            "high",
		Level:              31,
	}
	setFastEncodeParams(params, false)
	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: videoMezDir})
	boilerXc(t, params)

	log.Debug("STARTING audio mez for muxing", "file", url)
	// Create audio mez files
	setupOutDir(t, audioMezDir)
	params.XcType = goavpipe.XcAudio
	params.Ecodec2 = "aac"
	params.ChannelLayout = avpipe.ChannelLayout("stereo")
	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: audioMezDir})
	boilerXc(t, params)

	// Create video ABR files for the first mez segment
	setupOutDir(t, videoABRDir)
	url = videoMezDir + "/vsegment-1.mp4"
	log.Debug("STARTING video ABR for muxing (first segment)", "file", url)
	params.XcType = goavpipe.XcVideo
	params.Format = "dash"
	params.VideoSegDurationTs = 48000
	params.Url = url
	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: videoABRDir})
	boilerXc(t, params)

	// Create video ABR files for the second mez segment
	setupOutDir(t, videoABRDir2)
	url = videoMezDir + "/vsegment-2.mp4"
	log.Debug("STARTING video ABR for muxing (second segment)", "file", url)
	params.XcType = goavpipe.XcVideo
	params.Format = "dash"
	params.VideoSegDurationTs = 48000
	params.Url = url
	params.StartSegmentStr = "16"
	params.StartPts = 721720
	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: videoABRDir2})
	boilerXc(t, params)

	// Create audio ABR files for the first mez segment
	setupOutDir(t, audioABRDir)
	url = audioMezDir + "/asegment0-1.mp4"
	log.Debug("STARTING audio ABR for muxing", "file", url)
	params.XcType = goavpipe.XcAudio
	params.Format = "dash"
	params.Ecodec2 = "aac"
	params.AudioSegDurationTs = 96000
	params.Url = url
	params.StartSegmentStr = "1"
	params.StartPts = 0
	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: audioABRDir})
	boilerXc(t, params)

	// Create audio ABR files for the second mez segment
	setupOutDir(t, audioABRDir2)
	url = audioMezDir + "/asegment0-2.mp4"
	log.Debug("STARTING audio ABR for muxing (first segment)", "file", url)
	params.XcType = goavpipe.XcAudio
	params.Format = "dash"
	params.Ecodec2 = "aac"
	params.AudioSegDurationTs = 96000
	params.Url = url
	params.StartPts = 1441792
	params.StartSegmentStr = "16"
	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: audioABRDir2})
	boilerXc(t, params)

	// Create playable file by muxing audio/video segments
	setupOutDir(t, muxOutDir)
	muxSpec := "abr-mux\n"
	muxSpec += "audio,1," + audioABRDir + "/ainit-stream0.m4s\n"
	for i := 1; i <= 15; i++ {
		muxSpec += fmt.Sprintf("%s%s%s%02d%s\n", "audio,1,", audioABRDir, "/achunk-stream0-000", i, ".m4s")
	}
	for i := 16; i <= 30; i++ {
		muxSpec += fmt.Sprintf("%s%s%s%02d%s\n", "audio,1,", audioABRDir2, "/achunk-stream0-000", i, ".m4s")
	}

	muxSpec += "video,1," + videoABRDir + "/vinit-stream0.m4s\n"
	for i := 1; i <= 15; i++ {
		muxSpec += fmt.Sprintf("%s%s%s%02d%s\n", "video,1,", videoABRDir, "/vchunk-stream0-000", i, ".m4s")
	}
	for i := 16; i <= 30; i++ {
		muxSpec += fmt.Sprintf("%s%s%s%02d%s\n", "video,1,", videoABRDir2, "/vchunk-stream0-000", i, ".m4s")
	}
	url = muxOutDir + "/segment-1.mp4"
	params.MuxingSpec = muxSpec
	log.Debug(f, "muxSpec", muxSpec)

	goavpipe.InitUrlMuxIOHandler(url, &cmd.AVCmdMuxInputOpener{URL: url}, &cmd.AVCmdMuxOutputOpener{})
	defer goavpipe.RemoveUrlMuxIOHandler(url)
	params.Url = url
	params.Timecode = "01:00:00:00"
	err := avpipe.Mux(params)
	failNowOnError(t, err)

	xcTestResult := &XcTestResult{
		mezFile:   []string{fmt.Sprintf("%s/vsegment-1.mp4", videoMezDir)},
		timeScale: 24000,
		pixelFmt:  "yuv420p",
	}
	if !testing.Short() {
		xcTestResult.level = 31
	}
	// Now probe mez video and output file and become sure both have the same duration
	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: xcTestResult.mezFile[0]}, &xc.FileOutputOpener{Dir: videoMezDir})
	// Now probe the generated files
	videoMezProbeInfo := boilerProbe(t, xcTestResult)

	xcTestResult2 := &XcTestResult{
		mezFile:   []string{fmt.Sprintf("%s/vsegment-2.mp4", videoMezDir)},
		timeScale: 24000,
		pixelFmt:  "yuv420p",
	}
	if !testing.Short() {
		xcTestResult2.level = 31
	}
	// Now probe mez video and output file and become sure both have the same duration
	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: xcTestResult.mezFile[0]}, &xc.FileOutputOpener{Dir: videoMezDir})
	// Now probe the generated files
	videoMezProbeInfo2 := boilerProbe(t, xcTestResult2)

	xcTestResult = &XcTestResult{
		mezFile:   []string{fmt.Sprintf("%s/segment-1.mp4", muxOutDir)},
		timeScale: 24000,
		pixelFmt:  "yuv420p",
	}
	if !testing.Short() {
		xcTestResult.level = 31
	}

	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: xcTestResult.mezFile[0]}, &xc.FileOutputOpener{Dir: muxOutDir})
	muxOutProbeInfo := boilerProbe(t, xcTestResult)

	assert.Equal(t, true,
		math.Abs(videoMezProbeInfo[0].Format.Duration+videoMezProbeInfo2[0].Format.Duration-muxOutProbeInfo[0].Format.Duration) < 0.05)
}

func TestMarshalParams(t *testing.T) {
	params := &goavpipe.XcParams{
		VideoBitrate:       8000000,
		VideoSegDurationTs: 180000,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
	}
	bytes, err := json.Marshal(params)
	assert.NoError(t, err)
	_ = bytes
	// TODO: Add asserts
}

func TestUnmarshalParams(t *testing.T) {
	var params goavpipe.XcParams
	bytes := []byte(`{"video_bitrate":8000000,"seg_duration_ts":180000,"seg_duration_fr":50,"enc_height":720,"enc_width":1280,"xc_type":1}`)
	err := json.Unmarshal(bytes, &params)
	assert.NoError(t, err)
	assert.Equal(t, int(goavpipe.XcVideo), int(params.XcType), "XcVideo type expected")

	// TODO: More checks
}

func TestUnmarshalParamsNumAudioBackwardsCompat(t *testing.T) {
	var params goavpipe.XcParams
	bytesWithNAudio := []byte(`{"video_bitrate":8000000,"seg_duration_ts":180000,"seg_duration_fr":50,"enc_height":720,"enc_width":1280,"xc_type":1,"audio_index":[0,0,0,0,0,0,0,0],"n_audio":1}`)
	err := json.Unmarshal(bytesWithNAudio, &params)
	assert.NoError(t, err)
	assert.Equal(t, len(params.AudioIndex), 1)

	bytesNoNAudio := []byte(`{"video_bitrate":8000000,"seg_duration_ts":180000,"seg_duration_fr":50,"enc_height":720,"enc_width":1280,"xc_type":1,"audio_index":[0,0,0,0,0,0,0,0]}`)
	err = json.Unmarshal(bytesNoNAudio, &params)
	assert.NoError(t, err)
	assert.Equal(t, len(params.AudioIndex), 8)
}

func TestProbe(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url}, &concurrentOutputOpener{dir: "O"})
	xcparams := &goavpipe.XcParams{
		Url:      url,
		Seekable: true,
	}
	probe, err := avpipe.Probe(xcparams)
	failNowOnError(t, err)
	assert.Equal(t, 3, len(probe.Streams))

	assert.Equal(t, 27, probe.Streams[0].CodecID)
	assert.Equal(t, "h264", probe.Streams[0].CodecName)
	assert.Equal(t, 100, probe.Streams[0].Profile) // 77 = AV_PROFILE_H264_MAIN
	assert.Equal(t, 41, probe.Streams[0].Level)
	assert.Equal(t, int64(1800), probe.Streams[0].NBFrames)
	assert.Equal(t, int64(1980), probe.Streams[0].StartTime)
	assert.Equal(t, int64(3081772), probe.Streams[0].BitRate)
	assert.Equal(t, 1920, probe.Streams[0].Width)
	assert.Equal(t, 1080, probe.Streams[0].Height)
	assert.Equal(t, int64(30000), probe.Streams[0].TimeBase.Denom().Int64())

	assert.Equal(t, 86017, probe.Streams[1].CodecID)
	assert.Equal(t, "mp3float", probe.Streams[1].CodecName)
	assert.Equal(t, 0, probe.Streams[1].Profile)
	assert.Equal(t, 0, probe.Streams[1].Level)
	assert.Equal(t, int64(2500), probe.Streams[1].NBFrames)
	assert.Equal(t, int64(0), probe.Streams[1].StartTime)
	assert.Equal(t, int64(160000), probe.Streams[1].BitRate)
	assert.Equal(t, 0, probe.Streams[1].Width)
	assert.Equal(t, 0, probe.Streams[1].Height)
	assert.Equal(t, int64(48000), probe.Streams[1].TimeBase.Denom().Int64())

	assert.Equal(t, 86019, probe.Streams[2].CodecID)
	assert.Equal(t, "ac3", probe.Streams[2].CodecName)
	assert.Equal(t, 0, probe.Streams[2].Profile)
	assert.Equal(t, 0, probe.Streams[2].Level)
	assert.Equal(t, int64(1875), probe.Streams[2].NBFrames)
	assert.Equal(t, int64(0), probe.Streams[2].StartTime)
	assert.Equal(t, int64(320000), probe.Streams[2].BitRate)
	assert.Equal(t, 0, probe.Streams[2].Width)
	assert.Equal(t, 0, probe.Streams[2].Height)
	assert.Equal(t, int64(48000), probe.Streams[2].TimeBase.Denom().Int64())
	assert.Equal(t, 6, probe.Streams[2].Channels)

	// Verify CodecTagString (4CC from container)
	assert.Equal(t, "avc1", probe.Streams[0].CodecTagString)
	assert.Equal(t, "mp4a", probe.Streams[1].CodecTagString)
	assert.Equal(t, "ac-3", probe.Streams[2].CodecTagString)

	// Verify pre-populated string fields (populated by Probe(), not recomputed here)
	assert.Equal(t, "High", probe.Streams[0].ProfileName)
	assert.Equal(t, "5.1(side)", probe.Streams[2].ChannelLayoutName)

	// Test StreamInfoAsArray
	a := goavpipe.StreamInfoAsArray(probe.Streams)
	assert.Equal(t, "h264", a[0].CodecName)
	assert.Equal(t, "mp3float", a[1].CodecName)
	assert.Equal(t, "ac3", a[2].CodecName)
}

func TestProbeWithData(t *testing.T) {
	url := "./media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov"
	checkFileExists(t, url)

	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url}, &concurrentOutputOpener{dir: "O"})
	xcparams := &goavpipe.XcParams{
		Url:      url,
		Seekable: true,
	}
	probe, err := avpipe.Probe(xcparams)
	failNowOnError(t, err)
	assert.Equal(t, 9, len(probe.Streams))

	assert.Equal(t, 147, probe.Streams[0].CodecID)
	assert.Equal(t, "prores", probe.Streams[0].CodecName)
	assert.Equal(t, 3, probe.Streams[0].Profile) // 3 = AV_PROFILE_MPEG4_MAIN
	assert.Equal(t, 0, probe.Streams[0].Level)
	assert.Equal(t, int64(1439), probe.Streams[0].NBFrames)
	assert.Equal(t, int64(0), probe.Streams[0].StartTime)
	assert.Equal(t, int64(249054569), probe.Streams[0].BitRate)
	assert.Equal(t, 1920, probe.Streams[0].Width)
	assert.Equal(t, 1080, probe.Streams[0].Height)
	assert.Equal(t, int64(24000), probe.Streams[0].TimeBase.Denom().Int64())

	assert.Equal(t, 65548, probe.Streams[1].CodecID)
	assert.Equal(t, "pcm_s24le", probe.Streams[1].CodecName)
	assert.Equal(t, 0, probe.Streams[1].Profile)
	assert.Equal(t, 0, probe.Streams[1].Level)
	assert.Equal(t, int64(2880480), probe.Streams[1].NBFrames)
	assert.Equal(t, int64(0), probe.Streams[1].StartTime)
	assert.Equal(t, int64(1152000), probe.Streams[1].BitRate)
	assert.Equal(t, 0, probe.Streams[1].Width)
	assert.Equal(t, 0, probe.Streams[1].Height)
	assert.Equal(t, int64(48000), probe.Streams[1].TimeBase.Denom().Int64())

	assert.Equal(t, 65548, probe.Streams[2].CodecID)
	assert.Equal(t, "pcm_s24le", probe.Streams[2].CodecName)
	assert.Equal(t, 0, probe.Streams[2].Profile)
	assert.Equal(t, 0, probe.Streams[2].Level)
	assert.Equal(t, int64(2880480), probe.Streams[2].NBFrames)
	assert.Equal(t, int64(0), probe.Streams[2].StartTime)
	assert.Equal(t, int64(1152000), probe.Streams[2].BitRate)
	assert.Equal(t, 0, probe.Streams[2].Width)
	assert.Equal(t, 0, probe.Streams[2].Height)
	assert.Equal(t, int64(48000), probe.Streams[2].TimeBase.Denom().Int64())

	assert.Equal(t, 0, probe.Streams[8].CodecID)
	assert.Equal(t, "", probe.Streams[8].CodecName)
	assert.Equal(t, 0, probe.Streams[8].Profile)
	assert.Equal(t, 0, probe.Streams[8].Level)
	assert.Equal(t, int64(1), probe.Streams[8].NBFrames)
	assert.Equal(t, int64(0), probe.Streams[8].StartTime)
	assert.Equal(t, int64(0), probe.Streams[8].BitRate)
	assert.Equal(t, 0, probe.Streams[8].Width)
	assert.Equal(t, 0, probe.Streams[8].Height)
	assert.Equal(t, int64(24000), probe.Streams[8].TimeBase.Denom().Int64())

	// Test StreamInfoAsArray
	a := goavpipe.StreamInfoAsArray(probe.Streams)
	assert.Equal(t, "prores", a[0].CodecName)
	assert.Equal(t, "pcm_s24le", a[1].CodecName)
	assert.Equal(t, "pcm_s24le", a[2].CodecName)
}

func TestExtractImagesInterval(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

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
	}
	setFastEncodeParams(params, true)

	xcTest2(t, outPath, params, nil)

	files, err := os.ReadDir(outPath)
	failNowOnError(t, err)
	assert.Equal(t, 6, len(files))
	var sum int
	for _, f := range files {
		pts, err2 := strconv.ParseInt(strings.Split(f.Name(), ".")[0], 10, 32)
		assert.NoError(t, err2)
		sum += int(pts)
	}
	assert.Equal(t, 4511880, sum)
}

func TestExtractImagesList(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

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
	}
	params.ExtractImagesTs = []int64{1980, 2980, 72980, 169980, 339980}
	setFastEncodeParams(params, true)

	xcTest2(t, outPath, params, nil)

	files, err := os.ReadDir(outPath)
	failNowOnError(t, err)
	assert.Equal(t, 5, len(files))
	var sum int
	for _, f := range files {
		pts, err2 := strconv.ParseInt(strings.Split(f.Name(), ".")[0], 10, 32)
		assert.NoError(t, err2)
		sum += int(pts)
	}
	assert.Equal(t, 1980+2980+72980+169980+339980, sum)
}

// Should exit after extracting the first frame
func TestExtractImagesListFast(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

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
	}
	params.ExtractImagesTs = []int64{1980}
	setFastEncodeParams(params, true)

	xcTest2(t, outPath, params, nil)

	files, err := os.ReadDir(outPath)
	failNowOnError(t, err)
	assert.Equal(t, 1, len(files))
	pts, err := strconv.ParseInt(strings.Split(files[0].Name(), ".")[0], 10, 32)
	assert.NoError(t, err)
	assert.Equal(t, int64(1980), pts)
}

func TestExtractAllImages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)

	outPath := path.Join(baseOutPath, fn())

	params := &goavpipe.XcParams{
		Format:                 "image2",
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
		Ecodec:                 "mjpeg",
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
		XcType:                 goavpipe.XcExtractAllImages,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}

	xcTest2(t, outPath, params, nil)

	files, err := os.ReadDir(outPath)
	failNowOnError(t, err)
	assert.Equal(t, 1800, len(files))
	pts, err := strconv.ParseInt(strings.Split(files[0].Name(), ".")[0], 10, 32)
	assert.NoError(t, err)
	assert.Equal(t, int64(1000980), pts)
}

type LevelParams struct {
	profile       int
	bitrate       int64
	framerate     int
	width         int
	height        int
	expectedLevel int
}

func TestLevel(t *testing.T) {
	levelParams := []LevelParams{
		{100, 7498181, 30, 3840, 2160, 51},
		{100, 4001453, 60, 1920, 1080, 42},
		{100, 3081772, 30, 1920, 1080, 40},
		{100, 8173343, 24, 2048, 858, 40},
		{77, 921161, 25, 2048, 864, 40},
		{100, 207245, 25, 1532, 642, 32},
		{100, 1530884, 30, 1280, 720, 31},
		{100, 409778, 24, 720, 302, 30},
		{100, 348377, 24, 640, 268, 21},
		{100, 324552, 30, 480, 272, 21},
		{100, 115986, 24, 320, 144, 12},
	}

	for i := 0; i < len(levelParams); i++ {
		level := avpipe.H264GuessLevel(levelParams[i].profile, levelParams[i].bitrate, levelParams[i].framerate, levelParams[i].width, levelParams[i].height)
		assert.Equal(t, levelParams[i].expectedLevel, level)
	}
}

type ProfileParams struct {
	bitDepth        int
	width           int
	height          int
	expectedProfile int
}

func TestProfile(t *testing.T) {
	profileParams := []ProfileParams{
		{8, 320, 540, goavpipe.XcProfileH264BaseLine},
		{8, 1280, 720, goavpipe.XcProfileH264Heigh},
		{8, 1920, 1080, goavpipe.XcProfileH264Heigh},
		{8, 3840, 2169, goavpipe.XcProfileH264Heigh},
		{10, 3840, 2169, goavpipe.XcProfileH264Heigh10},
	}

	for i := 0; i < len(profileParams); i++ {
		profile := avpipe.H264GuessProfile(profileParams[i].bitDepth, profileParams[i].width, profileParams[i].height)
		assert.Equal(t, profileParams[i].expectedProfile, profile)
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	if !flagExplicitlySet("fast") {
		fastEncode = testing.Short()
	}
	setupLogging()
	os.Exit(m.Run())
}

func xcTest(t *testing.T, outputDir string, params *goavpipe.XcParams, xcTestResult *XcTestResult, isNewTest bool) {
	if isNewTest {
		boilerplate(t, outputDir, params.Url)
	}
	boilerXc(t, params)
	boilerProbe(t, xcTestResult)
}

func boilerplate(t *testing.T, outPath, inURL string) {

	log.Info("STARTING " + outPath)
	setupOutDir(t, outPath)

	if len(inURL) > 0 {
		fio := &xc.FileInputOpener{URL: inURL, Stats: &statsInfo}
		foo := &xc.FileOutputOpener{Dir: outPath, Stats: &statsInfo}
		goavpipe.InitIOHandler(fio, foo)
	}
}

func boilerProbe(t *testing.T, result *XcTestResult) (probeInfoArray []*goavpipe.ProbeInfo) {
	if result == nil || len(result.mezFile) == 0 {
		return nil
	}

	for _, mezFile := range result.mezFile {
		xcparams := &goavpipe.XcParams{
			Url:      mezFile,
			Seekable: true,
		}
		probeInfo, err := avpipe.Probe(xcparams)
		failNowOnError(t, err)

		si := probeInfo.Streams[0]
		if result.timeScale > 0 {
			tb := *si.TimeBase.Denom()
			assert.Equal(t, 0, tb.Cmp(big.NewInt(int64(result.timeScale))), si.TimeBase)
		}
		if result.sampleRate > 0 {
			assert.Equal(t, result.sampleRate, si.SampleRate)
		}

		if result.level > 0 {
			assert.Equal(t, result.level, si.Level)
		}

		if len(result.profile) > 0 {
			assert.Equal(t, result.profile, si.ProfileName)
		}

		if len(result.pixelFmt) > 0 && si.PixFmt != nil {
			assert.Equal(t, result.pixelFmt, avpipe.GetPixelFormatName(*si.PixFmt))
		}

		if len(result.channelLayoutName) > 0 {
			assert.Equal(t, result.channelLayoutName, si.ChannelLayoutName)
		}
		probeInfoArray = append(probeInfoArray, probeInfo)
	}
	return
}

func boilerXc(t *testing.T, params *goavpipe.XcParams) {
	err := avpipe.Xc(params)
	failNowOnError(t, err)
}

func xcTest2(t *testing.T, outputDir string, params *goavpipe.XcParams, xcTestResult *XcTestResult) {
	boilerplate(t, outputDir, params.Url)
	boilerXc2(t, params)
	boilerProbe(t, xcTestResult)
}

// This test uses the following new APIs
// - to obtain a handle of running session:
//   - XcInit()
//
// - to run the tx session
//   - XcRun()
func boilerXc2(t *testing.T, params *goavpipe.XcParams) {
	handle, err := avpipe.XcInit(params)
	failNowOnError(t, err)
	assert.Greater(t, handle, int32(0))
	err = avpipe.XcRun(handle)
	failNowOnError(t, err)
}

func setFastEncodeParams(p *goavpipe.XcParams, always bool) bool {
	if !always && !fastEncode {
		return false
	}

	p.CrfStr = "51"
	p.Preset = "ultrafast"
	p.EncHeight = 180
	p.EncWidth = 320

	// ultrafast disables CABAC which is required for High profile; clear these
	// so the encoder picks a profile/level consistent with 320x180/ultrafast.
	p.Profile = ""
	p.Level = 0

	// needs testing
	//if runtime.GOOS == "darwin" {
	//	switch p.Ecodec {
	//	case h264Codec:
	//		p.Ecodec = "h264_videotoolbox"
	//	case h265Codec:
	//		p.Ecodec = "hevc_videotoolbox"
	//	}
	//	if p.Ecodec2 == "aac" {
	//		p.Ecodec2 = "aac_at"
	//	}
	//}

	if p.VideoBitrate > 100000 {
		p.VideoBitrate = 100000
	}
	if p.AudioBitrate > 32000 {
		p.AudioBitrate = 32000
	}

	return true
}

func failNowOnError(t *testing.T, err error) {
	if err != nil {
		assert.NoError(t, err)
		t.FailNow()
	}
}

// fn returns the caller's function name, e.g. pkg.Foo
func fn() (fname string) {
	fname = "unknown"
	if pc, _, _, ok := runtime.Caller(1); ok {
		if f := runtime.FuncForPC(pc); f != nil {
			fname = path.Base(f.Name())
		}
	}
	return
}

func removeDirContents(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() {
		e := d.Close()
		log.Error("error closing dir", e, "dir", dir)
	}()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}
	return nil
}

func setupLogging() {
	log.SetDefault(&log.Config{
		Level:   "debug",
		Handler: "text",
		File: &log.LumberjackConfig{
			Filename:  "test_out/avpipe-test.log",
			LocalTime: true,
			MaxSize:   1000,
		},
	})
	avpipe.SetCLoggers()
}

func checkFileExists(t *testing.T, url string) {
	t.Helper()
	if !fileExist(url) {
		t.Skipf("input file missing: %s", url)
	}
}

func fileExist(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func setupOutDir(t *testing.T, dir string) {
	var err error
	if _, err = os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(dir, 0755)
		}
	} else {
		err = removeDirContents(dir)
	}
	failNowOnError(t, err)
}

// expectedHDR10 stores expected HDR10 specific info
type expectedHDR10 struct {
	PixFmt           string // e.g. "yuv420p10le"
	ProfileName      string // e.g. "Main 10"
	ColorPrimaries   string // e.g. "bt2020"
	ColorTransfer    string // e.g. "smpte2084"
	ColorSpace       string // e.g. "bt2020nc"
	ColorRange       string // e.g. "tv"
	Width, Height    int    // 0 = don't check
	MasteringDisplay string // x265 format "G(...)B(...)R(...)WP(...)L(...)"; "*" = require non-empty
	MaxCLL           string // "<MaxCLL>,<MaxFALL>"; "*" = require non-empty.
}

func requireVideoStreamColor(
	t *testing.T,
	probe *goavpipe.ProbeInfo,
	colorPrimaries,
	colorTransfer,
	colorSpace string) {
	t.Helper()

	var si *goavpipe.StreamInfo
	for i := range probe.Streams {
		if probe.Streams[i].CodecType == "video" {
			si = &probe.Streams[i]
			break
		}
	}
	if !assert.NotNil(t, si, "no video stream in probe result") {
		return
	}

	assert.Equal(t, colorPrimaries, si.ColorPrimaries)
	assert.Equal(t, colorTransfer, si.ColorTransfer)
	assert.Equal(t, colorSpace, si.ColorSpace)
}

// assertHDR10 probes and asserts the mp4 video stream matches the expected.
func assertHDR10(t *testing.T, mp4 string, want expectedHDR10) {
	t.Helper()
	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: mp4, Seekable: true})
	failNowOnError(t, err)

	si := probe.StreamByCodecType("video")
	require.NotNil(t, si, "no video stream in %s", mp4)

	if want.PixFmt != "" && si.PixFmt != nil {
		assert.Equal(t, want.PixFmt, avpipe.GetPixelFormatName(*si.PixFmt), "pix_fmt in %s", mp4)
	}
	if want.ProfileName != "" {
		assert.Equal(t, want.ProfileName, avpipe.GetProfileName(si.CodecID, si.Profile), "profile in %s", mp4)
	}
	if want.ColorPrimaries != "" {
		assert.Equal(t, want.ColorPrimaries, si.ColorPrimaries, "color_primaries in %s", mp4)
	}
	if want.ColorTransfer != "" {
		assert.Equal(t, want.ColorTransfer, si.ColorTransfer, "color_transfer in %s", mp4)
	}
	if want.ColorSpace != "" {
		assert.Equal(t, want.ColorSpace, si.ColorSpace, "color_space in %s", mp4)
	}
	if want.ColorRange != "" {
		assert.Equal(t, want.ColorRange, si.ColorRange, "color_range in %s", mp4)
	}
	if want.Width > 0 {
		assert.Equal(t, want.Width, si.Width, "width in %s", mp4)
	}
	if want.Height > 0 {
		assert.Equal(t, want.Height, si.Height, "height in %s", mp4)
	}
	if want.MasteringDisplay == "*" {
		assert.NotEmpty(t, si.MasteringDisplay, "Mastering display side data missing in %s", mp4)
	} else if want.MasteringDisplay != "" {
		assert.Equal(t, want.MasteringDisplay, si.MasteringDisplay, "mastering_display in %s", mp4)
	}
	if want.MaxCLL == "*" {
		assert.NotEmpty(t, si.MaxCLL, "Content light level side data missing in %s", mp4)
	} else if want.MaxCLL != "" {
		assert.Equal(t, want.MaxCLL, si.MaxCLL, "max_cll in %s", mp4)
	}
}

type dashFrameTiming struct {
	chunk       string
	decodeIdx   int
	frameType   string
	dts         uint64
	pts         int64
	duration    uint32
	composition int32
	sync        bool
	newSegment  bool
}

type sourceBFrameStats struct {
	maxCompositionOffset          int32
	maxCompositionOffsetFrame     int
	maxCompositionOffsetFrameDur  uint32
	maxPresentationDisplacement   int
	maxPresentationDisplaceFrame  int
	maxPresentationDisplacePTS    int64
	maxPresentationDisplaceDTS    uint64
	maxPresentationDisplaceTarget int
}

func assertDashChunksPTSFrameByFrame(t *testing.T, chunks []string) []dashFrameTiming {
	t.Helper()

	var (
		havePrevDtsEnd        bool
		prevDtsEnd            uint64
		havePrevPtsEnd        bool
		prevPtsEnd            int64
		totalFrames           int
		allFrames             []dashFrameTiming
		continuityMismatchCnt int
		continuityMismatches  []string
	)
	recordContinuityMismatch := func(format string, args ...interface{}) {
		continuityMismatchCnt++
		if len(continuityMismatches) < 20 {
			continuityMismatches = append(continuityMismatches, fmt.Sprintf(format, args...))
		}
	}

	for _, chunk := range chunks {
		samples := dashChunkFullSamples(t, chunk)
		require.NotEmpty(t, samples, "no video samples in %s", chunk)

		if havePrevDtsEnd && prevDtsEnd != samples[0].DecodeTime {
			recordContinuityMismatch("DTS gap at start of %s expected %d got %d",
				filepath.Base(chunk), prevDtsEnd, samples[0].DecodeTime)
		}

		frames := make([]dashFrameTiming, 0, len(samples))
		for i, sample := range samples {
			require.Greater(t, sample.Dur, uint32(0),
				"zero sample duration in %s decode frame %d", filepath.Base(chunk), i)
			if i > 0 {
				expectedDTS := samples[i-1].DecodeTime + uint64(samples[i-1].Dur)
				if expectedDTS != sample.DecodeTime {
					recordContinuityMismatch("DTS gap in %s decode frame %d expected %d got %d",
						filepath.Base(chunk), i, expectedDTS, sample.DecodeTime)
				}
			}

			frame := dashFrameTiming{
				chunk:       chunk,
				decodeIdx:   i,
				frameType:   hevcFrameType(sample),
				dts:         sample.DecodeTime,
				pts:         sample.PresentationTime(),
				duration:    sample.Dur,
				composition: sample.CompositionTimeOffset,
				sync:        sample.IsSync(),
				newSegment:  i == 0,
			}
			frames = append(frames, frame)
			allFrames = append(allFrames, frame)
		}

		sort.SliceStable(frames, func(i, j int) bool {
			if frames[i].pts == frames[j].pts {
				return frames[i].decodeIdx < frames[j].decodeIdx
			}
			return frames[i].pts < frames[j].pts
		})

		for i, frame := range frames {
			if i == 0 {
				if havePrevPtsEnd && prevPtsEnd != frame.pts {
					recordContinuityMismatch("PTS gap/overlap at start of %s expected %d got %d",
						filepath.Base(chunk), prevPtsEnd, frame.pts)
				}
				continue
			}

			prev := frames[i-1]
			expectedPTS := prev.pts + int64(prev.duration)
			if expectedPTS != frame.pts {
				recordContinuityMismatch("PTS gap/overlap in %s presentation frame %d expected %d got %d (decode frame %d follows decode frame %d)",
					filepath.Base(chunk), i, expectedPTS, frame.pts, frame.decodeIdx, prev.decodeIdx)
			}
		}

		lastDecodeSample := samples[len(samples)-1]
		prevDtsEnd = lastDecodeSample.DecodeTime + uint64(lastDecodeSample.Dur)
		havePrevDtsEnd = true

		lastPresentationFrame := frames[len(frames)-1]
		prevPtsEnd = lastPresentationFrame.pts + int64(lastPresentationFrame.duration)
		havePrevPtsEnd = true
		totalFrames += len(samples)
	}

	t.Logf("checked %d DASH chunks and %d video frames for frame-by-frame PTS continuity",
		len(chunks), totalFrames)
	if continuityMismatchCnt > 0 {
		t.Errorf("DASH chunk DTS/PTS continuity mismatches: %d\n%s",
			continuityMismatchCnt, strings.Join(continuityMismatches, "\n"))
	}
	return allFrames
}

func dashChunkFullSamples(t *testing.T, chunk string) []mp4.FullSample {
	t.Helper()

	f, err := os.Open(chunk)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	parsed, err := mp4.DecodeFile(f)
	require.NoError(t, err, "failed to parse DASH chunk %s", chunk)

	var samples []mp4.FullSample
	for _, segment := range parsed.Segments {
		for _, fragment := range segment.Fragments {
			fullSamples, err := fragment.GetFullSamples(nil)
			require.NoError(t, err, "failed to read samples from %s", chunk)
			samples = append(samples, fullSamples...)
		}
	}
	return samples
}

func sourceMP4FrameTimings(t *testing.T, source string) ([]dashFrameTiming, sourceBFrameStats) {
	t.Helper()

	f, err := os.Open(source)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	parsed, err := mp4.DecodeFile(f)
	require.NoError(t, err, "failed to parse source MP4 %s", source)
	require.NotNil(t, parsed.Moov, "source MP4 missing moov: %s", source)

	var videoTrak *mp4.TrakBox
	for _, trak := range parsed.Moov.Traks {
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil && trak.Mdia.Hdlr.HandlerType == "vide" {
			videoTrak = trak
			break
		}
	}
	require.NotNil(t, videoTrak, "source MP4 missing video track: %s", source)
	require.NotNil(t, videoTrak.Mdia.Minf, "source video track missing minf")
	require.NotNil(t, videoTrak.Mdia.Minf.Stbl, "source video track missing stbl")
	stbl := videoTrak.Mdia.Minf.Stbl
	require.NotNil(t, stbl.Stts, "source video track missing stts")

	nrSamples := sttsSampleCount(stbl.Stts)
	require.Zero(t, nrSamples, "ABR bypass source should be a fragmented mez part: %s", source)
	frames := fragmentedMP4FrameTimings(t, parsed, source)
	return frames, calculateSourceBFrameStats(frames)
}

func fragmentedMP4FrameTimings(t *testing.T, parsed *mp4.File, source string) []dashFrameTiming {
	t.Helper()

	var frames []dashFrameTiming
	for _, segment := range parsed.Segments {
		for _, fragment := range segment.Fragments {
			fullSamples, err := fragment.GetFullSamples(nil)
			require.NoError(t, err, "failed to read source fragment samples from %s", source)
			for _, sample := range fullSamples {
				frames = append(frames, dashFrameTiming{
					decodeIdx:   len(frames),
					frameType:   hevcFrameType(sample),
					dts:         sample.DecodeTime,
					pts:         sample.PresentationTime(),
					duration:    sample.Dur,
					composition: sample.CompositionTimeOffset,
					sync:        sample.IsSync(),
				})
			}
		}
	}
	require.NotEmpty(t, frames, "source MP4 had no stts samples and no fragment samples: %s", source)
	return frames
}

func calculateSourceBFrameStats(frames []dashFrameTiming) sourceBFrameStats {
	var stats sourceBFrameStats
	for _, frame := range frames {
		if frame.composition > stats.maxCompositionOffset {
			stats.maxCompositionOffset = frame.composition
			stats.maxCompositionOffsetFrame = frame.decodeIdx
			stats.maxCompositionOffsetFrameDur = frame.duration
		}
	}

	presentationOrder := append([]dashFrameTiming(nil), frames...)
	sort.SliceStable(presentationOrder, func(i, j int) bool {
		if presentationOrder[i].pts == presentationOrder[j].pts {
			return presentationOrder[i].decodeIdx < presentationOrder[j].decodeIdx
		}
		return presentationOrder[i].pts < presentationOrder[j].pts
	})
	for presentationIdx, frame := range presentationOrder {
		displacement := absInt(frame.decodeIdx - presentationIdx)
		if displacement > stats.maxPresentationDisplacement {
			stats.maxPresentationDisplacement = displacement
			stats.maxPresentationDisplaceFrame = frame.decodeIdx
			stats.maxPresentationDisplaceTarget = presentationIdx
			stats.maxPresentationDisplacePTS = frame.pts
			stats.maxPresentationDisplaceDTS = frame.dts
		}
	}

	return stats
}

type abrBypassContentFrame struct {
	chunk     string
	decodeIdx int
	frameType string
	dts       int64
	pts       int64
	duration  uint32
}

func assertABRBypassPartsContiguous(t *testing.T, previous, current abrBypassDashPTSResult) {
	t.Helper()

	require.NotEmpty(t, previous.outputFrames, "%s output frames", previous.name)
	require.NotEmpty(t, current.outputFrames, "%s output frames", current.name)

	previousLastChunk := previous.outputFrames[len(previous.outputFrames)-1].chunk
	currentFirstChunk := current.outputFrames[0].chunk
	previousSegmentFrames := abrBypassContentSegmentFrames(t, previous, previousLastChunk)
	currentSegmentFrames := abrBypassContentSegmentFrames(t, current, currentFirstChunk)

	previousLastDecode := previousSegmentFrames[len(previousSegmentFrames)-1]
	currentFirstDecode := currentSegmentFrames[0]
	previousDecodeEnd := previousLastDecode.dts + int64(previousLastDecode.duration)
	require.Equal(t, previousDecodeEnd, currentFirstDecode.dts,
		"%s last segment %s -> %s first segment %s DTS continuity (prev dts/dur=%d/%d next dts=%d)",
		previous.name, filepath.Base(previousLastChunk),
		current.name, filepath.Base(currentFirstChunk),
		previousLastDecode.dts, previousLastDecode.duration, currentFirstDecode.dts)

	previousPresentationFrames := abrBypassPresentationOrder(previousSegmentFrames)
	currentPresentationFrames := abrBypassPresentationOrder(currentSegmentFrames)
	previousLastPresentation := previousPresentationFrames[len(previousPresentationFrames)-1]
	currentFirstPresentation := currentPresentationFrames[0]
	previousPresentationEnd := previousLastPresentation.pts + int64(previousLastPresentation.duration)
	require.Equal(t, previousPresentationEnd, currentFirstPresentation.pts,
		"%s last segment %s -> %s first segment %s PTS continuity (prev pts/dur=%d/%d next pts=%d)",
		previous.name, filepath.Base(previousLastChunk),
		current.name, filepath.Base(currentFirstChunk),
		previousLastPresentation.pts, previousLastPresentation.duration, currentFirstPresentation.pts)
}

func abrBypassContentSegmentFrames(t *testing.T, result abrBypassDashPTSResult, chunk string) []abrBypassContentFrame {
	t.Helper()

	dtsOffset, ptsOffset := sourceOutputTimelineOffset(t, result.sourceFrames, result.outputFrames)
	frames := make([]abrBypassContentFrame, 0)
	for _, frame := range result.outputFrames {
		if frame.chunk != chunk {
			continue
		}
		frames = append(frames, abrBypassContentFrame{
			chunk:     frame.chunk,
			decodeIdx: frame.decodeIdx,
			frameType: frame.frameType,
			dts:       int64(frame.dts) - dtsOffset + result.startPts,
			pts:       frame.pts - ptsOffset + result.startPts,
			duration:  frame.duration,
		})
	}
	require.NotEmpty(t, frames, "%s missing output frames for chunk %s", result.name, filepath.Base(chunk))
	return frames
}

func abrBypassPresentationOrder(frames []abrBypassContentFrame) []abrBypassContentFrame {
	presentationOrder := append([]abrBypassContentFrame(nil), frames...)
	sort.SliceStable(presentationOrder, func(i, j int) bool {
		if presentationOrder[i].pts == presentationOrder[j].pts {
			return presentationOrder[i].decodeIdx < presentationOrder[j].decodeIdx
		}
		return presentationOrder[i].pts < presentationOrder[j].pts
	})
	return presentationOrder
}

func assertSourceOutputFrameTimings(t *testing.T, sourceFrames, outputFrames []dashFrameTiming, startPts int64) {
	t.Helper()

	require.Equal(t, len(sourceFrames), len(outputFrames), "source/output frame count")
	dtsOffset, ptsOffset := sourceOutputTimelineOffset(t, sourceFrames, outputFrames)
	require.Equal(t, dtsOffset, ptsOffset,
		"output DTS/PTS should have the same offset from the source timeline")
	require.True(t, dtsOffset == 0 || dtsOffset == startPts,
		"output timeline offset should be either chunk-local (0) or start_pts (%d), got %d", startPts, dtsOffset)

	var (
		mismatchCount int
		mismatches    []string
	)
	for i := range sourceFrames {
		expectedDTS := int64(sourceFrames[i].dts) + dtsOffset
		expectedPTS := sourceFrames[i].pts + ptsOffset
		gotDTS := int64(outputFrames[i].dts)
		gotPTS := outputFrames[i].pts
		if expectedDTS == gotDTS && expectedPTS == gotPTS {
			continue
		}
		mismatchCount++
		if len(mismatches) < 20 {
			mismatches = append(mismatches,
				fmt.Sprintf("frame %d expected %d/%d got %d/%d", i, expectedDTS, expectedPTS, gotDTS, gotPTS))
		}
	}
	if mismatchCount > 0 {
		t.Fatalf("source/output DTS/PTS mismatches with start_pts=%d output_offset=%d/%d: %d\n%s",
			startPts, dtsOffset, ptsOffset, mismatchCount, strings.Join(mismatches, "\n"))
	}
}

func printSourceOutputPTSComparison(sourceFrames, outputFrames []dashFrameTiming, stats sourceBFrameStats, startPts int64) {
	var b strings.Builder
	dtsOffset, ptsOffset := sourceOutputTimelineOffset(nil, sourceFrames, outputFrames)

	fmt.Fprintf(&b, "\nsource/output DTS/PTS comparison\n")
	fmt.Fprintf(&b, "requested start_pts: %d\n", startPts)
	fmt.Fprintf(&b, "observed output offset from source: dts=%d pts=%d\n", dtsOffset, ptsOffset)
	fmt.Fprintf(&b, "content timeline column: source + start_pts\n")
	fmt.Fprintf(&b, "source max B-frame composition offset: %d ticks", stats.maxCompositionOffset)
	if stats.maxCompositionOffsetFrameDur > 0 {
		offsetFrames := float64(stats.maxCompositionOffset) / float64(stats.maxCompositionOffsetFrameDur)
		fmt.Fprintf(&b, " (%.2f frames)", offsetFrames)
	}
	fmt.Fprintf(&b, " at source decode_frame=%d\n", stats.maxCompositionOffsetFrame)
	fmt.Fprintf(&b, "source max decode/presentation displacement: %d frames at source decode_frame=%d presentation_frame=%d dts=%d pts=%d\n",
		stats.maxPresentationDisplacement,
		stats.maxPresentationDisplaceFrame,
		stats.maxPresentationDisplaceTarget,
		stats.maxPresentationDisplaceDTS,
		stats.maxPresentationDisplacePTS)
	fmt.Fprintf(&b, "%6s  %4s  %18s  %18s  %18s  %-7s  %s\n",
		"frame", "type", "source dts/pts", "content dts/pts", "output dts/pts", "output", "diff")

	rowCount := len(sourceFrames)
	if len(outputFrames) > rowCount {
		rowCount = len(outputFrames)
	}
	for i := 0; i < rowCount; i++ {
		sourceTiming := "-"
		expectedTiming := "-"
		outputTiming := "-"
		frameType := "?"
		outputMarker := ""
		diff := "-"

		switch {
		case i >= len(sourceFrames):
			output := outputFrames[i]
			outputTiming = fmt.Sprintf("%d/%d", output.dts, output.pts)
			frameType = output.frameType
			if output.newSegment {
				outputMarker = "new seg"
			}
			diff = "missing_source"
		case i >= len(outputFrames):
			source := sourceFrames[i]
			sourceTiming = fmt.Sprintf("%d/%d", source.dts, source.pts)
			expectedTiming = fmt.Sprintf("%d/%d", int64(source.dts)+startPts, source.pts+startPts)
			frameType = source.frameType
			diff = "missing_output"
		default:
			source := sourceFrames[i]
			output := outputFrames[i]
			sourceTiming = fmt.Sprintf("%d/%d", source.dts, source.pts)
			contentDTS := int64(source.dts) + startPts
			contentPTS := source.pts + startPts
			expectedDTS := int64(source.dts) + dtsOffset
			expectedPTS := source.pts + ptsOffset
			expectedTiming = fmt.Sprintf("%d/%d", contentDTS, contentPTS)
			outputTiming = fmt.Sprintf("%d/%d", output.dts, output.pts)
			frameType = source.frameType
			if frameType == "?" {
				frameType = output.frameType
			}
			if output.newSegment {
				outputMarker = "new seg"
			}
			var differences []string
			if expectedDTS != int64(output.dts) {
				differences = append(differences, "DTS")
			}
			if expectedPTS != output.pts {
				differences = append(differences, "PTS")
			}
			if len(differences) > 0 {
				diff = strings.Join(differences, ",")
			}
		}

		fmt.Fprintf(&b, "%6d  %4s  %18s  %18s  %18s  %-7s  %s\n",
			i, frameType, sourceTiming, expectedTiming, outputTiming, outputMarker, diff)
	}

	fmt.Print(b.String())
}

func sourceOutputTimelineOffset(t *testing.T, sourceFrames, outputFrames []dashFrameTiming) (int64, int64) {
	if t != nil {
		t.Helper()
		require.NotEmpty(t, sourceFrames, "source frame timings")
		require.NotEmpty(t, outputFrames, "output frame timings")
	} else if len(sourceFrames) == 0 || len(outputFrames) == 0 {
		return 0, 0
	}
	return int64(outputFrames[0].dts) - int64(sourceFrames[0].dts),
		outputFrames[0].pts - sourceFrames[0].pts
}

type dashMPD struct {
	Periods []dashPeriod `xml:"Period"`
}

type dashPeriod struct {
	AdaptationSets []dashAdaptationSet `xml:"AdaptationSet"`
}

type dashAdaptationSet struct {
	Representations []dashRepresentation `xml:"Representation"`
}

type dashRepresentation struct {
	SegmentTemplate dashSegmentTemplate `xml:"SegmentTemplate"`
}

type dashSegmentTemplate struct {
	SegmentTimeline dashSegmentTimeline `xml:"SegmentTimeline"`
}

type dashSegmentTimeline struct {
	Entries []dashSegmentTimelineEntry `xml:"S"`
}

type dashSegmentTimelineEntry struct {
	T int64 `xml:"t,attr"`
}

func assertDashMPDSegmentTimelineStart(t *testing.T, dashDir string, sourceFrames []dashFrameTiming, startPts int64) {
	t.Helper()

	require.NotEmpty(t, sourceFrames, "source frame timings")
	mpdPath := path.Join(dashDir, "dash.mpd")
	f, err := os.Open(mpdPath)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	var mpd dashMPD
	require.NoError(t, xml.NewDecoder(f).Decode(&mpd), "failed to parse %s", mpdPath)
	require.NotEmpty(t, mpd.Periods, "%s missing Period", mpdPath)
	require.NotEmpty(t, mpd.Periods[0].AdaptationSets, "%s missing AdaptationSet", mpdPath)
	require.NotEmpty(t, mpd.Periods[0].AdaptationSets[0].Representations, "%s missing Representation", mpdPath)
	entries := mpd.Periods[0].AdaptationSets[0].Representations[0].SegmentTemplate.SegmentTimeline.Entries
	require.NotEmpty(t, entries, "%s missing SegmentTimeline entries", mpdPath)

	expectedStart := sourceFrames[0].pts + startPts
	localStart := sourceFrames[0].pts
	require.True(t, entries[0].T == localStart || entries[0].T == expectedStart,
		"MPD SegmentTimeline start should be either local (%d) or source+start_pts (%d), got %d",
		localStart, expectedStart, entries[0].T)
}

func hevcFrameType(sample mp4.FullSample) string {
	frameType := hevcFrameTypeFromSample(sample.Data)
	if frameType != "?" {
		return frameType
	}
	if sample.IsSync() {
		return "I"
	}
	return "?"
}

func hevcFrameTypeFromSample(data []byte) string {
	nalusByLayer := hevc.SplitNalusByLayerID(data, 4)
	if len(nalusByLayer) == 0 {
		return "?"
	}

	if frameType := hevcFrameTypeFromNalus(nalusByLayer[0]); frameType != "?" {
		return frameType
	}

	layerIDs := make([]int, 0, len(nalusByLayer))
	for layerID := range nalusByLayer {
		if layerID != 0 {
			layerIDs = append(layerIDs, int(layerID))
		}
	}
	sort.Ints(layerIDs)
	for _, layerID := range layerIDs {
		if frameType := hevcFrameTypeFromNalus(nalusByLayer[byte(layerID)]); frameType != "?" {
			return frameType
		}
	}
	return "?"
}

func hevcFrameTypeFromNalus(nalus [][]byte) string {
	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		naluInfo := hevc.ParseNaluHeader(nalu[:2])
		if !hevc.IsVideoNaluType(naluInfo.Type) {
			continue
		}
		if naluInfo.Type >= hevc.NALU_BLA_W_LP && naluInfo.Type <= hevc.NALU_IRAP_VCL23 {
			return "I"
		}

		sliceType, ok := hevcSliceType(nalu, naluInfo.Type)
		if !ok {
			continue
		}
		switch sliceType {
		case hevc.SLICE_B:
			return "B"
		case hevc.SLICE_P:
			return "P"
		case hevc.SLICE_I:
			return "I"
		default:
			return "?"
		}
	}
	return "?"
}

func hevcSliceType(nalu []byte, naluType hevc.NaluType) (hevc.SliceType, bool) {
	br := mp4bits.NewEBSPReader(bytes.NewReader(nalu))

	br.Read(16)
	firstSliceSegmentInPicFlag := br.ReadFlag()

	if naluType >= hevc.NALU_BLA_W_LP && naluType <= hevc.NALU_IRAP_VCL23 {
		br.ReadFlag()
	}

	br.ReadExpGolomb()
	if br.AccError() != nil || !firstSliceSegmentInPicFlag {
		return 0, false
	}

	sliceType := hevc.SliceType(br.ReadExpGolomb())
	if br.AccError() != nil {
		return 0, false
	}
	return sliceType, true
}

func sttsSampleCount(stts *mp4.SttsBox) uint32 {
	var total uint32
	for _, sampleCount := range stts.SampleCount {
		total += sampleCount
	}
	return total
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// dashVideoInit returns the path to the video init segment in the output directory
func dashVideoInit(t *testing.T, dashDir string) string {
	t.Helper()
	init := path.Join(dashDir, "vinit-stream0.m4s")
	if !fileExist(init) {
		t.Fatalf("DASH init segment missing: %s", init)
	}
	return init
}
