package avpipe_test

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/elvxc/cmd"
	"github.com/eluv-io/log-go"
	"github.com/stretchr/testify/assert"
)

const baseOutPath = "test_out"
const debugFrameLevel = true
const h264Codec = "libx264"
const videoBigBuckBunnyPath = "media/bbb_1080p_30fps_60sec.mp4"
const videoRockyPath = "media/rocky.mp4"

type XcTestResult struct {
	mezFile           []string
	timeScale         int
	sampleRate        int
	level             int
	pixelFmt          string
	channelLayoutName string
}

type testStatsInfo struct {
	audioFramesRead         uint64
	videoFramesRead         uint64
	firstKeyFramePTS        uint64
	encodingAudioFrameStats avpipe.EncodingFrameStats
	encodingVideoFrameStats avpipe.EncodingFrameStats
}

var statsInfo testStatsInfo

// Implements avpipe.InputOpener
type fileInputOpener struct {
	t                *testing.T
	url              string
	errorOnOpenInput bool // Generate error in opening input
	errorOnReadInput bool // Generate error in reading input
}

func (fio *fileInputOpener) Open(_ int64, url string) (
	handler avpipe.InputHandler, err error) {

	if fio.errorOnOpenInput {
		return nil, fs.ErrPermission
	}

	var f *os.File
	f, err = os.Open(url)
	assert.NoError(fio.t, err)
	if err != nil {
		return
	}

	fio.url = url
	handler = &fileInput{t: fio.t,
		file:             f,
		errorOnReadInput: fio.errorOnReadInput,
	}
	return
}

// Implements avpipe.InputHandler
type fileInput struct {
	t                *testing.T
	file             *os.File // Input file
	errorOnReadInput bool     // Generate error in reading input
}

func (i *fileInput) Read(buf []byte) (int, error) {
	n, err := i.file.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	if i.errorOnReadInput {
		err = io.ErrNoProgress
		n = -1
	}
	if debugFrameLevel {
		log.Debug("fileInput.Read", "err", err, "n", n)
	}
	return n, err
}

func (i *fileInput) Seek(offset int64, whence int) (int64, error) {
	n, err := i.file.Seek(offset, whence)
	if debugFrameLevel {
		log.Debug("fileInput.Seek", "err", err, "n", n)
	}
	return n, err
}

func (i *fileInput) Close() error {
	err := i.file.Close()
	if debugFrameLevel {
		log.Debug("fileInput.Close", "err", err)
	}
	return err
}

func (i *fileInput) Size() int64 {
	fi, err := i.file.Stat()
	assert.NoError(i.t, err)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (i *fileInput) Stat(statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_IN_STAT_BYTES_READ:
		readOffset := statArgs.(*uint64)
		if debugFrameLevel {
			log.Debug("AVP TEST IN STAT", "STAT read offset", *readOffset)
		}
	case avpipe.AV_IN_STAT_AUDIO_FRAME_READ:
		audioFramesRead := statArgs.(*uint64)
		if debugFrameLevel {
			log.Debug("AVP TEST IN STAT", "audioFramesRead", *audioFramesRead)
		}
		statsInfo.audioFramesRead = *audioFramesRead
	case avpipe.AV_IN_STAT_VIDEO_FRAME_READ:
		videoFramesRead := statArgs.(*uint64)
		if debugFrameLevel {
			log.Debug("AVP TEST IN STAT", "videoFramesRead", *videoFramesRead)
		}
		statsInfo.videoFramesRead = *videoFramesRead
	case avpipe.AV_IN_STAT_DECODING_AUDIO_START_PTS:
		startPTS := statArgs.(*uint64)
		if debugFrameLevel {
			log.Debug("AVP TEST IN STAT", "audio start PTS", *startPTS)
		}
	case avpipe.AV_IN_STAT_DECODING_VIDEO_START_PTS:
		startPTS := statArgs.(*uint64)
		if debugFrameLevel {
			log.Debug("AVP TEST IN STAT", "video start PTS", *startPTS)
		}
	case avpipe.AV_IN_STAT_FIRST_KEYFRAME_PTS:
		keyFramePTS := statArgs.(*uint64)
		if debugFrameLevel {
			log.Debug("AVP TEST IN STAT", "video first keyframe PTS", *keyFramePTS)
		}
		statsInfo.firstKeyFramePTS = *keyFramePTS
	}
	return nil
}

// Implements avpipe.OutputOpener
type fileOutputOpener struct {
	t   *testing.T
	dir string
}

func (oo *fileOutputOpener) Open(_, _ int64, streamIndex, segIndex int,
	pts int64, outType avpipe.AVType) (avpipe.OutputHandler, error) {

	var filename string

	switch outType {
	case avpipe.DASHVideoInit:
		filename = fmt.Sprintf("./%s/vinit-stream%d.m4s", oo.dir, streamIndex)
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/ainit-stream%d.m4s", oo.dir, streamIndex)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", oo.dir)
	case avpipe.DASHVideoSegment:
		filename = fmt.Sprintf("./%s/vchunk-stream%d-%05d.m4s", oo.dir, streamIndex, segIndex)
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/achunk-stream%d-%05d.m4s", oo.dir, streamIndex, segIndex)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./%s/master.m3u8", oo.dir)
	case avpipe.HLSVideoM3U:
		fallthrough
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./%s/media_%d.m3u8", oo.dir, streamIndex)
	case avpipe.AES128Key:
		filename = fmt.Sprintf("./%s/key.bin", oo.dir)
	case avpipe.MP4Segment:
		filename = fmt.Sprintf("./%s/segment-%d.mp4", oo.dir, segIndex)
	case avpipe.FMP4VideoSegment:
		filename = fmt.Sprintf("./%s/vsegment-%d.mp4", oo.dir, segIndex)
	case avpipe.FMP4AudioSegment:
		filename = fmt.Sprintf("./%s/asegment-%d.mp4", oo.dir, segIndex)
	case avpipe.FrameImage:
		filename = fmt.Sprintf("./%s/%d.jpeg", oo.dir, pts)
	}

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	assert.NoError(oo.t, err)
	if err != nil {
		return nil, err
	}

	oh := &fileOutput{
		t:           oo.t,
		url:         filename,
		streamIndex: streamIndex,
		segIndex:    segIndex,
		file:        f}
	return oh, nil
}

// Implements avpipe.OutputOpener
type concurrentOutputOpener struct {
	t   *testing.T
	dir string
}

func (coo *concurrentOutputOpener) Open(h, _ int64, streamIndex, segIndex int,
	pts int64, outType avpipe.AVType) (oh avpipe.OutputHandler, err error) {

	var filename string
	dir := fmt.Sprintf("%s/O%d", coo.dir, h)

	if _, err = os.Stat(dir); os.IsNotExist(err) {
		err = os.Mkdir(dir, 0755)
	}
	assert.NoError(coo.t, err)

	switch outType {
	case avpipe.DASHVideoInit:
		filename = fmt.Sprintf("./%s/vinit-stream%d.m4s", dir, streamIndex)
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/ainit-stream%d.m4s", dir, streamIndex)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", dir)
	case avpipe.DASHVideoSegment:
		filename = fmt.Sprintf("./%s/vchunk-stream%d-%05d.m4s", dir, streamIndex, segIndex)
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/achunk-stream%d-%05d.m4s", dir, streamIndex, segIndex)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./%s/master.m3u8", dir)
	case avpipe.HLSVideoM3U:
		fallthrough
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./%s/media_%d.m3u8", dir, streamIndex)
	case avpipe.AES128Key:
		filename = fmt.Sprintf("./%s/key.bin", dir)
	case avpipe.FrameImage:
		filename = fmt.Sprintf("./%s/%d.jpeg", dir, pts)
	}

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	assert.NoError(coo.t, err)
	if err != nil {
		return
	}

	oh = &fileOutput{
		t:           coo.t,
		url:         filename,
		streamIndex: streamIndex,
		segIndex:    segIndex,
		file:        f}
	return
}

// Implement OutputHandler
type fileOutput struct {
	t           *testing.T
	url         string
	streamIndex int
	segIndex    int
	file        *os.File
}

func (o *fileOutput) Write(buf []byte) (int, error) {
	n, err := o.file.Write(buf)
	if debugFrameLevel {
		log.Debug("fileOutput.Write", "err", err, "n", n, "filename", o.url)
	}
	return n, err
}

func (o *fileOutput) Seek(offset int64, whence int) (int64, error) {
	n, err := o.file.Seek(offset, whence)
	if debugFrameLevel {
		log.Debug("fileOutput.Seek", "err", err, "n", n)
	}
	return n, err
}

func (o *fileOutput) Close() error {
	err := o.file.Close()
	if debugFrameLevel {
		log.Debug("fileOutput.Close", "err", err, "filename", o.url)
	}
	return err
}

func (o fileOutput) Stat(avType avpipe.AVType, statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_OUT_STAT_BYTES_WRITTEN:
		writeOffset := statArgs.(*uint64)
		if debugFrameLevel {
			log.Debug("AVP TEST OUT STAT", "STAT, write offset", *writeOffset)
		}
	case avpipe.AV_OUT_STAT_ENCODING_END_PTS:
		endPTS := statArgs.(*uint64)
		if debugFrameLevel {
			log.Debug("AVP TEST OUT STAT", "STAT, endPTS", *endPTS)
		}
	case avpipe.AV_OUT_STAT_FRAME_WRITTEN:
		encodingStats := statArgs.(*avpipe.EncodingFrameStats)
		if debugFrameLevel {
			log.Debug("AVP TEST OUT STAT", "avType", avType,
				"encodingStats", encodingStats)
		}
		if avType == avpipe.FMP4AudioSegment {
			statsInfo.encodingAudioFrameStats = *encodingStats
		} else {
			statsInfo.encodingVideoFrameStats = *encodingStats
		}
	}

	return nil
}

func TestAudioSeg(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())
	params := &avpipe.XcParams{
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
		XcType:                 avpipe.XcAudio,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	setFastEncodeParams(params, true)
	xcTest(t, outputDir, params, nil, true)
}

func TestVideoSeg(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())
	params := &avpipe.XcParams{
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
		XcType:                 avpipe.XcVideo,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	setFastEncodeParams(params, true)
	xcTest(t, outputDir, params, nil, true)

}

func TestVideoSegDoubleTS(t *testing.T) {
	url := videoBigBuckBunnyPath
	outputDir := path.Join(baseOutPath, fn())
	params := &avpipe.XcParams{
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
		XcType:                 avpipe.XcVideo,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
		VideoTimeBase:          60000,
	}
	setFastEncodeParams(params, true)
	xcTest(t, outputDir, params, nil, true)

}

func TestSingleABRTranscode(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:             avpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)

	params.XcType = avpipe.XcAudio
	params.Ecodec2 = "aac"
	params.NumAudio = -1
	xcTest(t, outputDir, params, nil, false)
}

func TestSingleABRTranscodeByStreamId(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		NumAudio:           -1,
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
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:                avpipe.XcVideo,
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
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	overlayImage, err := ioutil.ReadFile("./media/avpipe.png")
	failNowOnError(t, err)

	params := &avpipe.XcParams{
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
		XcType:               avpipe.XcVideo,
		WatermarkYLoc:        "main_h*0.7",
		WatermarkXLoc:        "main_w/2-overlay_w/2",
		WatermarkOverlay:     string(overlayImage),
		WatermarkOverlayLen:  len(overlayImage),
		WatermarkOverlayType: avpipe.PngImage,
		StreamId:             -1,
		Url:                  url,
		DebugFrameLevel:      debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)
}

func TestV2SingleABRTranscode(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:             avpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)

	params.XcType = avpipe.XcAudio
	params.Ecodec2 = "aac"
	params.NumAudio = 1
	params.AudioIndex[0] = 1
	xcTest(t, outputDir, params, nil, false)
}

func TestV2SingleABRTranscodeIOHandler(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:             avpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil, true)

	params.XcType = avpipe.XcAudio
	params.Ecodec2 = "aac"
	params.NumAudio = 1
	params.AudioIndex[0] = 1
	xcTest(t, outputDir, params, nil, false)
}

func TestV2SingleABRTranscodeCancelling(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, url)

	params := &avpipe.XcParams{
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
		XcType:             avpipe.XcVideo,
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

	params.XcType = avpipe.XcAudio
	params.Ecodec2 = "aac"
	params.NumAudio = 1
	params.AudioIndex[0] = 1
	handleA, err := avpipe.XcInit(params)
	assert.NoError(t, err)
	assert.Greater(t, handleA, int32(0))
	err = avpipe.XcCancel(handleA)
	assert.NoError(t, err)
	err = avpipe.XcRun(handleA)
	assert.Error(t, err)
}

func doTranscode(t *testing.T,
	p *avpipe.XcParams,
	nThreads int,
	outputDir, filename string) {

	avpipe.InitIOHandler(&fileInputOpener{url: filename},
		&concurrentOutputOpener{dir: outputDir})

	done := make(chan struct{})
	for i := 0; i < nThreads; i++ {
		go func(params *avpipe.XcParams) {
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
	if !nvidiaExist() {
		log.Info("Ignoring ", "test", fn())
		return
	}

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	nThreads := 2

	params := &avpipe.XcParams{
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
		XcType:             avpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
	}
	setFastEncodeParams(params, false)
	doTranscode(t, params, nThreads, outputDir, url)
}

// Check nvidia transcoding with weird aspect ratio
func TestNvidiaFmp4SegmentAspectRatio(t *testing.T) {
	if !nvidiaExist() {
		log.Info("Ignoring ", "test", fn())
		return
	}
	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	params := &avpipe.XcParams{
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
		XcType:          avpipe.XcVideo,
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
	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	nThreads := 10

	params := &avpipe.XcParams{
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
		XcType:             avpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	doTranscode(t, params, nThreads, outputDir, url)
}

func TestStartTimeTsWithSkipDecoding(t *testing.T) {
	url := "./media/video-960.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	avpipe.InitUrlIOHandler(url, &fileInputOpener{url: url}, &fileOutputOpener{dir: outputDir})
	boilerXc(t, params)

	files, err := ioutil.ReadDir(outputDir)
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
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	avpipe.InitUrlIOHandler(url, &fileInputOpener{url: url}, &fileOutputOpener{dir: outputDir})
	boilerXc(t, params)

	files, err := ioutil.ReadDir(outputDir)
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
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}
	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioAC3Ts2AC3MezMaker(t *testing.T) {
	url := "./media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcAudio,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 2

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}
	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioAC3Ts2AACMezMaker(t *testing.T) {
	url := "./media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcAudio,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 2

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioMP3Ts2AACMezMaker(t *testing.T) {
	url := "./media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcAudio,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 1

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioDownmix2AACMezMaker(t *testing.T) {
	url := "./media/SIN4_Audio_51-2_120s_CCBYblendercloud.mov"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcAudio,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 6

	xcTestResult := &XcTestResult{
		mezFile:           []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio2MonoTo1Stereo(t *testing.T) {
	url := "./media/gabby_shading_2mono_1080p.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudioJoin,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		NumAudio:            2,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 0
	params.AudioIndex[1] = 1

	xcTestResult := &XcTestResult{
		timeScale:         44100,
		sampleRate:        44100,
		channelLayoutName: "stereo",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio5_1To5_1(t *testing.T) {
	url := "./media/case_1_video_and_5.1_audio.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudio,
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
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio5_1ToStereo(t *testing.T) {
	url := "./media/case_1_video_and_5.1_audio.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudioPan,
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
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioMonoToMono(t *testing.T) {
	url := "./media/case_1_video_and_mono_audio.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudio,
		NumAudio:            1,
		ChannelLayout:       avpipe.ChannelLayout("mono"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 1

	xcTestResult := &XcTestResult{
		timeScale:         22050,
		sampleRate:        22050,
		channelLayoutName: "mono",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudioQuadToQuad(t *testing.T) {
	url := "./media/case_1_video_and_quad_audio.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudio,
		NumAudio:            1,
		ChannelLayout:       avpipe.ChannelLayout("quad"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 1

	xcTestResult := &XcTestResult{
		timeScale:         22050,
		sampleRate:        22050,
		channelLayoutName: "quad",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio6MonoTo5_1(t *testing.T) {
	url := "./media/case_2_video_and_8_mono_audio.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudioMerge,
		NumAudio:            6,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:3][0:4][0:5][0:6][0:7][0:8]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2| c3=c3|c4=c4|c5=c5[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 3
	params.AudioIndex[1] = 4
	params.AudioIndex[2] = 5
	params.AudioIndex[3] = 6
	params.AudioIndex[4] = 7
	params.AudioIndex[5] = 8

	xcTestResult := &XcTestResult{
		timeScale:         44100,
		sampleRate:        44100,
		channelLayoutName: "5.1",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio6MonoUnequalChannelLayoutsTo5_1(t *testing.T) {
	url := "./media/TOS8_Audio_51-2_60s_CCBYblendercloud.mov"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudioMerge,
		NumAudio:            6,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:0][0:1][0:2][0:3][0:4][0:5]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2|c3=c3|c4=c4|c5=c5[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 0
	params.AudioIndex[1] = 1
	params.AudioIndex[2] = 2
	params.AudioIndex[3] = 3
	params.AudioIndex[4] = 4
	params.AudioIndex[5] = 5

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "5.1",
	}
	for i := 1; i < 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio10Channel_s16To6Channel_5_1(t *testing.T) {
	url := "./media/case_3_video_and_10_channel_audio_10sec.mov"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudioPan,
		NumAudio:            1,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:1]pan=5.1|c0=c3|c1=c4|c2=c5|c3=c6|c4=c7|c5=c8[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 0

	xcTestResult := &XcTestResult{
		timeScale:         44100,
		sampleRate:        44100,
		channelLayoutName: "5.1",
	}
	for i := 1; i <= 1; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

func TestAudio2Channel1Stereo(t *testing.T) {
	url := "./media/ELD2_FHD_4_60s_CCBYblendercloud.mov"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudioPan,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		FilterDescriptor:    "[0:1]pan=stereo|c0<c1+0.707*c2|c1<c2+0.707*c1[aout]",
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 1

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}

	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// Transcode audio pan pcm_s24le with 60000 sample rate, into aac 48000 sample rate.
// This is case 1 with audio input sample rate incompatible with AAC
func TestAudioPan2Channel1Stereo_pcm_60000(t *testing.T) {
	url := "./media/Sintel_30s_6ch_pcm_s24le_60000Hz.mov"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudioPan,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		FilterDescriptor:    "[0:6]pan=stereo|c0=c0|c1=c0[aout]",
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 6

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}

	for i := 1; i <= 1; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// Transcode mono pcm_s24le with 60000 sample rate, into aac stereo 48000 sample rate.
// This is case 2 with audio input sample rate incompatible with AAC
func TestAudioMonoToStereo_pcm_60000(t *testing.T) {
	url := "./media/Sintel_30s_6ch_pcm_s24le_60000Hz.mov"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		XcType:              avpipe.XcAudio,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 3

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}

	for i := 1; i <= 1; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// Timebase of BBB0_HD_8_XDCAM_120s_CCBYblendercloud.mxf is 1001/60000
func TestIrregularTsMezMaker_1001_60000(t *testing.T) {
	url := "./media/BBB0_HD_8_XDCAM_120s_CCBYblendercloud.mxf"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         120,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
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
}

// Timebase of Rigify-2min is 1/24
func TestIrregularTsMezMaker_1_24(t *testing.T) {
	url := "./media/Rigify-2min.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcVideo,
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
		xcTestResult.level = 30
	}

	for i := 1; i <= 4; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// Timebase of Rigify-2min is 1/10000
func TestIrregularTsMezMaker_1_10000(t *testing.T) {
	url := "./media/Rigify-2min-10000ts.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcVideo,
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
		xcTestResult.level = 30
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
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, f)

	params := &avpipe.XcParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "30.03",
		Ecodec:            "libx265",
		Dcodec:            "jpeg2000",
		EncHeight:         -1,
		EncWidth:          -1,
		XcType:            avpipe.XcVideo,
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
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:            avpipe.XcVideo,
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
		xcTestResult.level = 30
	}

	xcTest(t, outputDir, params, xcTestResult, true)
}

// Run a mez making session and fail on opening the input.
// This simulates the cases when opening the input fails time to time (for example, opening the cloud object).
func TestMezMakerWithOpenInputError(t *testing.T) {
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:            avpipe.XcVideo,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
	}

	boilerplate(t, outputDir, url)

	fio := &fileInputOpener{t: t, url: url, errorOnOpenInput: true}
	foo := &fileOutputOpener{t: t, dir: outputDir}
	avpipe.InitIOHandler(fio, foo)

	setFastEncodeParams(params, false)
	params.EncHeight = 360 // slow down a bit to allow for the cancel
	params.EncWidth = 640

	handle, err := avpipe.XcInit(params)
	assert.Less(t, handle, int32(0))
	err = avpipe.XcRun(handle)
	assert.Error(t, err)

}

// Run a mez making session and fail on reading from input.
// This simulates the cases when reading the input fails time to time (for example, reading from cloud).
func TestMezMakerWithReadInputError(t *testing.T) {
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:            avpipe.XcVideo,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
	}

	boilerplate(t, outputDir, url)

	fio := &fileInputOpener{t: t, url: url, errorOnReadInput: true}
	foo := &fileOutputOpener{t: t, dir: outputDir}
	avpipe.InitIOHandler(fio, foo)

	setFastEncodeParams(params, false)
	params.EncHeight = 360 // slow down a bit to allow for the cancel
	params.EncWidth = 640

	handle, err := avpipe.XcInit(params)
	assert.Less(t, handle, int32(0))
	err = avpipe.XcRun(handle)
	assert.Error(t, err)

}

// Run a probe and fail on reading from input.
// This simulates the cases when reading the input fails time to time (for example, reading from cloud).
func TestProbeWithReadInputError(t *testing.T) {
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	boilerplate(t, outputDir, url)

	fio := &fileInputOpener{t: t, url: url, errorOnReadInput: true}
	foo := &fileOutputOpener{t: t, dir: outputDir}
	avpipe.InitIOHandler(fio, foo)

	params := &avpipe.XcParams{
		Url:      url,
		Seekable: true,
	}
	probe, err := avpipe.Probe(params)
	assert.Error(t, err)
	assert.Equal(t, (*avpipe.ProbeInfo)(nil), probe)

}

func TestHEVC_H265ABRTranscode(t *testing.T) {
	f := fn()
	if testing.Short() {
		// 403.23s on 2018 MacBook Pro (2.9 GHz 6-Core i9, 32 GB RAM, Radeon Pro 560X 4 GB)
		t.Skip("SKIPPING " + f)
	}
	url := "./media/SIN6_4K_MOS_HEVC_60s.mp4"
	if fileMissing(url, fn()) {
		return
	}

	videoMezDir := path.Join(baseOutPath, f, "VideoMez4H265")
	videoABRDir := path.Join(baseOutPath, f, "VideoABR4H265")

	params := &avpipe.XcParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "30",
		Ecodec:            "libx265",
		Dcodec:            "hevc",
		EncHeight:         -1,
		EncWidth:          -1,
		XcType:            avpipe.XcVideo,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
	}

	setupOutDir(t, videoMezDir)
	xcTest(t, videoMezDir, params, nil, true)

	setupOutDir(t, videoABRDir)
	url = videoMezDir + "/vsegment-1.mp4"
	log.Debug("STARTING video ABR for", "file", url)
	params.XcType = avpipe.XcVideo
	params.Format = "dash"
	params.VideoSegDurationTs = 48000
	params.Url = url
	avpipe.InitUrlIOHandler(url, &fileInputOpener{url: url}, &fileOutputOpener{dir: videoABRDir})
	boilerXc(t, params)

}

func TestAVPipeStats(t *testing.T) {
	url := "./media/Rigify-2min.mp4"
	if fileMissing(url, fn()) {
		return
	}

	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:              avpipe.XcAll,
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

	assert.Equal(t, int64(2880), statsInfo.encodingVideoFrameStats.TotalFramesWritten)
	assert.Equal(t, int64(5625), statsInfo.encodingAudioFrameStats.TotalFramesWritten)
	assert.Equal(t, uint64(0), statsInfo.firstKeyFramePTS)
	// FIXME
	//assert.Equal(t, int64(720), statsInfo.encodingVideoFrameStats.FramesWritten)
	//assert.Equal(t, int64(1406), statsInfo.encodingAudioFrameStats.FramesWritten)
	assert.Equal(t, uint64(5625), statsInfo.audioFramesRead)
	assert.Equal(t, uint64(2880), statsInfo.videoFramesRead)
}

// This unit test is almost a complete test for mez, abr, muxing and probing. It does:
// 1) Creates audio and video mez files
// 2) Creates ABR segments using audio and video mez files in step 1
// 3) Mux the ABR audio and video segments from step 2
// 4) Probes the initial mez file from step 1 and mux output from step 3. The duration has to be equal.
func TestABRMuxing(t *testing.T) {
	f := fn()
	log.Info("STARTING " + f)
	url := "./media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov"
	if fileMissing(url, fn()) {
		return
	}

	videoMezDir := path.Join(baseOutPath, f, "VideoMez4Muxing")
	audioMezDir := path.Join(baseOutPath, f, "AudioMez4Muxing")
	videoABRDir := path.Join(baseOutPath, f, "VideoABR4Muxing")
	videoABRDir2 := path.Join(baseOutPath, f, "VideoABR4Muxing2")
	audioABRDir := path.Join(baseOutPath, f, "AudioABR4Muxing")
	audioABRDir2 := path.Join(baseOutPath, f, "AudioABR4Muxing2")
	muxOutDir := path.Join(baseOutPath, f, "MuxingOutput")

	// Create video mez files
	setupOutDir(t, videoMezDir)
	params := &avpipe.XcParams{
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
		XcType:             avpipe.XcVideo,
		StreamId:           -1,
		NumAudio:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
		ForceKeyInt:        48,
	}
	setFastEncodeParams(params, false)
	avpipe.InitUrlIOHandler(url, &fileInputOpener{url: url}, &fileOutputOpener{dir: videoMezDir})
	boilerXc(t, params)

	log.Debug("STARTING audio mez for muxing", "file", url)
	// Create audio mez files
	setupOutDir(t, audioMezDir)
	params.XcType = avpipe.XcAudio
	params.Ecodec2 = "aac"
	params.ChannelLayout = avpipe.ChannelLayout("stereo")
	avpipe.InitUrlIOHandler(url, &fileInputOpener{url: url}, &fileOutputOpener{dir: audioMezDir})
	boilerXc(t, params)

	// Create video ABR files for the first mez segment
	setupOutDir(t, videoABRDir)
	url = videoMezDir + "/vsegment-1.mp4"
	log.Debug("STARTING video ABR for muxing (first segment)", "file", url)
	params.XcType = avpipe.XcVideo
	params.Format = "dash"
	params.VideoSegDurationTs = 48000
	params.Url = url
	avpipe.InitUrlIOHandler(url, &fileInputOpener{url: url}, &fileOutputOpener{dir: videoABRDir})
	boilerXc(t, params)

	// Create video ABR files for the second mez segment
	setupOutDir(t, videoABRDir2)
	url = videoMezDir + "/vsegment-2.mp4"
	log.Debug("STARTING video ABR for muxing (second segment)", "file", url)
	params.XcType = avpipe.XcVideo
	params.Format = "dash"
	params.VideoSegDurationTs = 48000
	params.Url = url
	params.StartSegmentStr = "16"
	params.StartPts = 721720
	avpipe.InitUrlIOHandler(url, &fileInputOpener{url: url}, &fileOutputOpener{dir: videoABRDir2})
	boilerXc(t, params)

	// Create audio ABR files for the first mez segment
	setupOutDir(t, audioABRDir)
	url = audioMezDir + "/asegment-1.mp4"
	log.Debug("STARTING audio ABR for muxing", "file", url)
	params.XcType = avpipe.XcAudio
	params.Format = "dash"
	params.Ecodec2 = "aac"
	params.AudioSegDurationTs = 96000
	params.Url = url
	params.StartSegmentStr = "1"
	params.StartPts = 0
	avpipe.InitUrlIOHandler(url, &fileInputOpener{url: url}, &fileOutputOpener{dir: audioABRDir})
	boilerXc(t, params)

	// Create audio ABR files for the second mez segment
	setupOutDir(t, audioABRDir2)
	url = audioMezDir + "/asegment-2.mp4"
	log.Debug("STARTING audio ABR for muxing (first segment)", "file", url)
	params.XcType = avpipe.XcAudio
	params.Format = "dash"
	params.Ecodec2 = "aac"
	params.AudioSegDurationTs = 96000
	params.Url = url
	params.StartPts = 1441792
	params.StartSegmentStr = "16"
	avpipe.InitUrlIOHandler(url, &fileInputOpener{url: url}, &fileOutputOpener{dir: audioABRDir2})
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
	log.Debug(f, "muxSpec", string(muxSpec))

	avpipe.InitUrlMuxIOHandler(url, &cmd.AVCmdMuxInputOpener{URL: url}, &cmd.AVCmdMuxOutputOpener{})
	params.Url = url
	err := avpipe.Mux(params)
	failNowOnError(t, err)

	xcTestResult := &XcTestResult{
		mezFile:   []string{fmt.Sprintf("%s/vsegment-1.mp4", videoMezDir)},
		timeScale: 24000,
		level:     31,
		pixelFmt:  "yuv420p",
	}
	// Now probe mez video and output file and become sure both have the same duration
	avpipe.InitIOHandler(&fileInputOpener{url: xcTestResult.mezFile[0]}, &fileOutputOpener{dir: videoMezDir})
	// Now probe the generated files
	videoMezProbeInfo := boilerProbe(t, xcTestResult)

	xcTestResult2 := &XcTestResult{
		mezFile:   []string{fmt.Sprintf("%s/vsegment-2.mp4", videoMezDir)},
		timeScale: 24000,
		level:     31,
		pixelFmt:  "yuv420p",
	}
	// Now probe mez video and output file and become sure both have the same duration
	avpipe.InitIOHandler(&fileInputOpener{url: xcTestResult.mezFile[0]}, &fileOutputOpener{dir: videoMezDir})
	// Now probe the generated files
	videoMezProbeInfo2 := boilerProbe(t, xcTestResult2)

	xcTestResult = &XcTestResult{
		mezFile:   []string{fmt.Sprintf("%s/segment-1.mp4", muxOutDir)},
		timeScale: 24000,
		level:     31,
		pixelFmt:  "yuv420p",
	}

	avpipe.InitIOHandler(&fileInputOpener{url: xcTestResult.mezFile[0]}, &fileOutputOpener{dir: muxOutDir})
	muxOutProbeInfo := boilerProbe(t, xcTestResult)

	assert.Equal(t, true,
		math.Abs(videoMezProbeInfo[0].ContainerInfo.Duration+videoMezProbeInfo2[0].ContainerInfo.Duration-muxOutProbeInfo[0].ContainerInfo.Duration) < 0.05)
}

func TestMarshalParams(t *testing.T) {
	params := &avpipe.XcParams{
		VideoBitrate:       8000000,
		VideoSegDurationTs: 180000,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             avpipe.XcVideo,
	}
	bytes, err := json.Marshal(params)
	assert.NoError(t, err)
	_ = bytes
	// TODO: Add asserts
}

func TestUnmarshalParams(t *testing.T) {
	var params avpipe.XcParams
	bytes := []byte(`{"video_bitrate":8000000,"seg_duration_ts":180000,"seg_duration_fr":50,"enc_height":720,"enc_width":1280,"xc_type":1}`)
	err := json.Unmarshal(bytes, &params)
	assert.NoError(t, err)
	assert.Equal(t, avpipe.XcVideo, int(params.XcType))
	// TODO: More checks
}

func TestProbe(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	avpipe.InitIOHandler(&fileInputOpener{url: url}, &concurrentOutputOpener{dir: "O"})
	xcparams := &avpipe.XcParams{
		Url:      url,
		Seekable: true,
	}
	probe, err := avpipe.Probe(xcparams)
	failNowOnError(t, err)
	assert.Equal(t, 3, len(probe.StreamInfo))

	assert.Equal(t, 27, probe.StreamInfo[0].CodecID)
	assert.Equal(t, "h264", probe.StreamInfo[0].CodecName)
	assert.Equal(t, 100, probe.StreamInfo[0].Profile) // 77 = FF_PROFILE_H264_MAIN
	assert.Equal(t, 41, probe.StreamInfo[0].Level)
	assert.Equal(t, int64(1800), probe.StreamInfo[0].NBFrames)
	assert.Equal(t, int64(1980), probe.StreamInfo[0].StartTime)
	assert.Equal(t, int64(3081772), probe.StreamInfo[0].BitRate)
	assert.Equal(t, 1920, probe.StreamInfo[0].Width)
	assert.Equal(t, 1080, probe.StreamInfo[0].Height)
	assert.Equal(t, int64(30000), probe.StreamInfo[0].TimeBase.Denom().Int64())

	assert.Equal(t, 86017, probe.StreamInfo[1].CodecID)
	assert.Equal(t, "mp3float", probe.StreamInfo[1].CodecName)
	assert.Equal(t, -99, probe.StreamInfo[1].Profile) // 1 = FF_PROFILE_AAC_LOW
	assert.Equal(t, -99, probe.StreamInfo[1].Level)
	assert.Equal(t, int64(2500), probe.StreamInfo[1].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[1].StartTime)
	assert.Equal(t, int64(160000), probe.StreamInfo[1].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[1].Width)
	assert.Equal(t, 0, probe.StreamInfo[1].Height)
	assert.Equal(t, int64(48000), probe.StreamInfo[1].TimeBase.Denom().Int64())

	assert.Equal(t, 86019, probe.StreamInfo[2].CodecID)
	assert.Equal(t, "ac3", probe.StreamInfo[2].CodecName)
	assert.Equal(t, -99, probe.StreamInfo[2].Profile) // 1 = FF_PROFILE_AAC_LOW
	assert.Equal(t, -99, probe.StreamInfo[2].Level)
	assert.Equal(t, int64(1875), probe.StreamInfo[2].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[2].StartTime)
	assert.Equal(t, int64(320000), probe.StreamInfo[2].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[2].Width)
	assert.Equal(t, 0, probe.StreamInfo[2].Height)
	assert.Equal(t, int64(48000), probe.StreamInfo[2].TimeBase.Denom().Int64())
	assert.Equal(t, 6, probe.StreamInfo[2].Channels)
	assert.Equal(t, "5.1(side)", avpipe.ChannelLayoutName(probe.StreamInfo[2].Channels, probe.StreamInfo[2].ChannelLayout))

	// Test StreamInfoAsArray
	a := avpipe.StreamInfoAsArray(probe.StreamInfo)
	assert.Equal(t, "h264", a[0].CodecName)
	assert.Equal(t, "mp3float", a[1].CodecName)
	assert.Equal(t, "ac3", a[2].CodecName)
}

func TestProbeWithData(t *testing.T) {
	url := "./media/TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov"
	if fileMissing(url, fn()) {
		return
	}

	avpipe.InitIOHandler(&fileInputOpener{url: url}, &concurrentOutputOpener{dir: "O"})
	xcparams := &avpipe.XcParams{
		Url:      url,
		Seekable: true,
	}
	probe, err := avpipe.Probe(xcparams)
	failNowOnError(t, err)
	assert.Equal(t, 9, len(probe.StreamInfo))

	assert.Equal(t, 147, probe.StreamInfo[0].CodecID)
	assert.Equal(t, "prores", probe.StreamInfo[0].CodecName)
	assert.Equal(t, 3, probe.StreamInfo[0].Profile) // 3 = FF_PROFILE_MPEG4_MAIN
	assert.Equal(t, -99, probe.StreamInfo[0].Level)
	assert.Equal(t, int64(1439), probe.StreamInfo[0].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[0].StartTime)
	assert.Equal(t, int64(249054569), probe.StreamInfo[0].BitRate)
	assert.Equal(t, 1920, probe.StreamInfo[0].Width)
	assert.Equal(t, 1080, probe.StreamInfo[0].Height)
	assert.Equal(t, int64(24000), probe.StreamInfo[0].TimeBase.Denom().Int64())

	assert.Equal(t, 65548, probe.StreamInfo[1].CodecID)
	assert.Equal(t, "pcm_s24le", probe.StreamInfo[1].CodecName)
	assert.Equal(t, -99, probe.StreamInfo[1].Profile)
	assert.Equal(t, -99, probe.StreamInfo[1].Level)
	assert.Equal(t, int64(2880480), probe.StreamInfo[1].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[1].StartTime)
	assert.Equal(t, int64(1152000), probe.StreamInfo[1].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[1].Width)
	assert.Equal(t, 0, probe.StreamInfo[1].Height)
	assert.Equal(t, int64(48000), probe.StreamInfo[1].TimeBase.Denom().Int64())

	assert.Equal(t, 65548, probe.StreamInfo[2].CodecID)
	assert.Equal(t, "pcm_s24le", probe.StreamInfo[2].CodecName)
	assert.Equal(t, -99, probe.StreamInfo[2].Profile)
	assert.Equal(t, -99, probe.StreamInfo[2].Level)
	assert.Equal(t, int64(2880480), probe.StreamInfo[2].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[2].StartTime)
	assert.Equal(t, int64(1152000), probe.StreamInfo[2].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[2].Width)
	assert.Equal(t, 0, probe.StreamInfo[2].Height)
	assert.Equal(t, int64(48000), probe.StreamInfo[2].TimeBase.Denom().Int64())

	assert.Equal(t, 0, probe.StreamInfo[8].CodecID)
	assert.Equal(t, "", probe.StreamInfo[8].CodecName)
	assert.Equal(t, -99, probe.StreamInfo[8].Profile)
	assert.Equal(t, -99, probe.StreamInfo[8].Level)
	assert.Equal(t, int64(1), probe.StreamInfo[8].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[8].StartTime)
	assert.Equal(t, int64(0), probe.StreamInfo[8].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[8].Width)
	assert.Equal(t, 0, probe.StreamInfo[8].Height)
	assert.Equal(t, int64(24000), probe.StreamInfo[8].TimeBase.Denom().Int64())

	// Test StreamInfoAsArray
	a := avpipe.StreamInfoAsArray(probe.StreamInfo)
	assert.Equal(t, "prores", a[0].CodecName)
	assert.Equal(t, "pcm_s24le", a[1].CodecName)
	assert.Equal(t, "pcm_s24le", a[2].CodecName)
}

func TestExtractImagesInterval(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	outPath := path.Join(baseOutPath, fn())
	params := &avpipe.XcParams{
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
		XcType:                 avpipe.XcExtractImages,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	setFastEncodeParams(params, true)

	xcTest2(t, outPath, params, nil)

	files, err := ioutil.ReadDir(outPath)
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
	if fileMissing(url, fn()) {
		return
	}

	outPath := path.Join(baseOutPath, fn())
	params := &avpipe.XcParams{
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
		XcType:                 avpipe.XcExtractImages,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	params.ExtractImagesTs = []int64{1980, 2980, 72980, 169980, 339980}
	setFastEncodeParams(params, true)

	xcTest2(t, outPath, params, nil)

	files, err := ioutil.ReadDir(outPath)
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
	if fileMissing(url, fn()) {
		return
	}

	outPath := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:                 avpipe.XcExtractImages,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	params.ExtractImagesTs = []int64{1980}
	setFastEncodeParams(params, true)

	xcTest2(t, outPath, params, nil)

	files, err := ioutil.ReadDir(outPath)
	failNowOnError(t, err)
	assert.Equal(t, 1, len(files))
	pts, err := strconv.ParseInt(strings.Split(files[0].Name(), ".")[0], 10, 32)
	assert.NoError(t, err)
	assert.Equal(t, int64(1980), pts)
}

func TestExtractAllImages(t *testing.T) {
	url := videoBigBuckBunnyPath
	if fileMissing(url, fn()) {
		return
	}

	outPath := path.Join(baseOutPath, fn())

	params := &avpipe.XcParams{
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
		XcType:                 avpipe.XcExtractAllImages,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}

	xcTest2(t, outPath, params, nil)

	files, err := ioutil.ReadDir(outPath)
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
		{8, 320, 540, avpipe.XcProfileH264BaseLine},
		{8, 1280, 720, avpipe.XcProfileH264Heigh},
		{8, 1920, 1080, avpipe.XcProfileH264Heigh},
		{8, 3840, 2169, avpipe.XcProfileH264Heigh},
		{10, 3840, 2169, avpipe.XcProfileH264Heigh10},
	}

	for i := 0; i < len(profileParams); i++ {
		profile := avpipe.H264GuessProfile(profileParams[i].bitDepth, profileParams[i].width, profileParams[i].height)
		assert.Equal(t, profileParams[i].expectedProfile, profile)
	}
}

func TestMain(m *testing.M) {
	// call flag.Parse() here if TestMain uses flags
	setupLogging()
	os.Exit(m.Run())
}

func xcTest(t *testing.T, outputDir string, params *avpipe.XcParams, xcTestResult *XcTestResult, isNewTest bool) {
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
		fio := &fileInputOpener{t: t, url: inURL}
		foo := &fileOutputOpener{t: t, dir: outPath}
		avpipe.InitIOHandler(fio, foo)
	}
}

func boilerProbe(t *testing.T, result *XcTestResult) (probeInfoArray []*avpipe.ProbeInfo) {
	if result == nil || len(result.mezFile) == 0 {
		return nil
	}

	for _, mezFile := range result.mezFile {
		xcparams := &avpipe.XcParams{
			Url:      mezFile,
			Seekable: true,
		}
		probeInfo, err := avpipe.Probe(xcparams)
		failNowOnError(t, err)

		si := probeInfo.StreamInfo[0]
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

		if len(result.pixelFmt) > 0 {
			assert.Equal(t, result.pixelFmt, avpipe.GetPixelFormatName(si.PixFmt))
		}

		if len(result.channelLayoutName) > 0 {
			assert.Equal(t, result.channelLayoutName, avpipe.ChannelLayoutName(si.Channels, si.ChannelLayout))
		}
		probeInfoArray = append(probeInfoArray, probeInfo)
	}
	return
}

func boilerXc(t *testing.T, params *avpipe.XcParams) {
	err := avpipe.Xc(params)
	failNowOnError(t, err)
}

func xcTest2(t *testing.T, outputDir string, params *avpipe.XcParams, xcTestResult *XcTestResult) {
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
func boilerXc2(t *testing.T, params *avpipe.XcParams) {
	handle, err := avpipe.XcInit(params)
	failNowOnError(t, err)
	assert.Greater(t, handle, int32(0))
	err = avpipe.XcRun(handle)
	failNowOnError(t, err)
}

func setFastEncodeParams(p *avpipe.XcParams, force bool) bool {
	if !force && !testing.Short() {
		return false
	}

	///// TestMezVideo benchmark
	// 19.4s real  150s user/sys

	// 10s real    62s user/sys
	p.CrfStr = "51"

	// 4.8 real    30s user/sys
	p.Preset = "ultrafast"

	// 8.9s real   12.6 user/sys
	// notes:
	//   * error when height and width are set
	//   * slower in real time despite better user/sys time
	//if runtime.GOOS == "darwin" && p.Ecodec == h264Codec {
	//	p.Ecodec = "h264_videotoolbox" // half the time of libx264
	//}

	// 13s real    62s user/sys
	if p.VideoBitrate > 100000 {
		p.VideoBitrate = 100000
	}

	// 4.3s real   19.1s user/sys
	p.EncHeight = 180
	p.EncWidth = 320

	///// TestMezAudio benchmark
	// 2.78s real  4.29s user/sys

	// 2.09s real  3.81s user/sys
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
			Filename:  "avpipe-test.log",
			LocalTime: true,
			MaxSize:   1000,
		},
	})
	avpipe.SetCLoggers()
}

func fileMissing(url string, test string) bool {
	if !fileExist(url) {
		log.Warn("Skipping, input file missing", "test", test, "file", url)
		return true
	}

	return false
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

func nvidiaExist() bool {
	cmd := exec.Command("nvidia-smi")
	cmd.Stdout = nil
	cmd.Stderr = nil

	err := cmd.Start()
	if err == nil {
		return true
	}

	log.Info("NVIDIA doesn't exist")
	return false
}
