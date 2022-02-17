package avpipe_test

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eluv-io/log-go"
	"github.com/qluvio/avpipe"
	"github.com/qluvio/avpipe/avcmd/cmd"
	"github.com/stretchr/testify/assert"
)

const baseOutPath = "test_out"
const debugFrameLevel = false
const h264Codec = "libx264"
const videoErstePath = "media/ErsteChristmas.mp4"
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
	encodingAudioFrameStats avpipe.EncodingFrameStats
	encodingVideoFrameStats avpipe.EncodingFrameStats
}

var statsInfo testStatsInfo

// Implements avpipe.InputOpener
type fileInputOpener struct {
	t   *testing.T
	url string
}

func (fio *fileInputOpener) Open(_ int64, url string) (
	handler avpipe.InputHandler, err error) {

	var f *os.File
	f, err = os.Open(url)
	assert.NoError(fio.t, err)
	if err != nil {
		return
	}

	fio.url = url
	handler = &fileInput{t: fio.t, file: f}
	return
}

// Implements avpipe.InputHandler
type fileInput struct {
	t    *testing.T
	file *os.File // Input file
}

func (i *fileInput) Read(buf []byte) (int, error) {
	n, err := i.file.Read(buf)
	if err == io.EOF {
		return 0, nil
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
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/init-stream%d.mp4", oo.dir, streamIndex)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", oo.dir)
	case avpipe.DASHVideoSegment:
		fallthrough
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/chunk-stream%d-%05d.mp4", oo.dir, streamIndex, segIndex)
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
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/init-stream%d.mp4", dir, streamIndex)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", dir)
	case avpipe.DASHVideoSegment:
		fallthrough
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/chunk-stream%d-%05d.mp4", dir, streamIndex, segIndex)
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
		log.Debug("fileOutput.Write", "err", err, "n", n)
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
		log.Debug("fileOutput.Close", "err", err)
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
	url := videoErstePath
	outputDir := path.Join(baseOutPath, fn())
	params := &avpipe.TxParams{
		BypassTranscoding:      false,
		Format:                 "fmp4-segment",
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
		Ecodec:                 "libx264",
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
		TxType:                 avpipe.TxAudio,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	setFastEncodeParams(params, true)
	xcTest(t, outputDir, params, nil)
}

func TestVideoSeg(t *testing.T) {
	url := videoErstePath
	outputDir := path.Join(baseOutPath, fn())
	params := &avpipe.TxParams{
		BypassTranscoding:      false,
		Format:                 "fmp4-segment",
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
		Ecodec:                 "libx264",
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
		TxType:                 avpipe.TxVideo,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	setFastEncodeParams(params, true)
	xcTest(t, outputDir, params, nil)

}

func TestSingleABRTranscode(t *testing.T) {
	filename := videoErstePath
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
		Url:                filename,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil)

	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"
	params.NumAudio = -1
	xcTest(t, outputDir, params, nil)
}

func TestSingleABRTranscodeByStreamId(t *testing.T) {
	filename := videoErstePath
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		StreamId:           1,
		NumAudio:           -1,
		Url:                filename,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil)

	params.StreamId = 2
	params.Ecodec2 = "aac"
	xcTest(t, outputDir, params, nil)
}

func TestSingleABRTranscodeWithWatermark(t *testing.T) {
	filename := videoErstePath
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:     false,
		Format:                "hls",
		StartTimeTs:           0,
		DurationTs:            -1,
		StartSegmentStr:       "1",
		VideoBitrate:          2560000,
		AudioBitrate:          64000,
		SampleRate:            44100,
		VideoSegDurationTs:    1001 * 60,
		Ecodec:                h264Codec,
		EncHeight:             720,
		EncWidth:              1280,
		TxType:                avpipe.TxVideo,
		WatermarkText:         "This is avpipe watermarking",
		WatermarkYLoc:         "H*0.5",
		WatermarkXLoc:         "W/2",
		WatermarkRelativeSize: 0.05,
		WatermarkFontColor:    "black",
		WatermarkShadow:       true,
		WatermarkShadowColor:  "white",
		StreamId:              -1,
		Url:                   filename,
		DebugFrameLevel:       debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil)
}

func TestSingleABRTranscodeWithOverlayWatermark(t *testing.T) {
	filename := videoErstePath
	outputDir := path.Join(baseOutPath, fn())

	overlayImage, err := ioutil.ReadFile("./media/fox_watermark.png")
	failNowOnError(t, err)

	params := &avpipe.TxParams{
		BypassTranscoding:    false,
		Format:               "hls",
		StartTimeTs:          0,
		DurationTs:           -1,
		StartSegmentStr:      "1",
		VideoBitrate:         2560000,
		AudioBitrate:         64000,
		SampleRate:           44100,
		VideoSegDurationTs:   1001 * 60,
		Ecodec:               h264Codec,
		EncHeight:            720,
		EncWidth:             1280,
		TxType:               avpipe.TxVideo,
		WatermarkYLoc:        "main_h*0.7",
		WatermarkXLoc:        "main_w/2-overlay_w/2",
		WatermarkOverlay:     string(overlayImage),
		WatermarkOverlayLen:  len(overlayImage),
		WatermarkOverlayType: avpipe.PngImage,
		StreamId:             -1,
		Url:                  filename,
		DebugFrameLevel:      debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil)
}

func TestV2SingleABRTranscode(t *testing.T) {
	filename := videoErstePath
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
		Url:                filename,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil)

	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"
	params.NumAudio = 1
	params.AudioIndex[0] = 1
	xcTest(t, outputDir, params, nil)
}

func TestV2SingleABRTranscodeIOHandler(t *testing.T) {
	filename := videoErstePath
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
		Url:                filename,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	xcTest(t, outputDir, params, nil)

	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"
	params.NumAudio = 1
	params.AudioIndex[0] = 1
	xcTest(t, outputDir, params, nil)
}

func TestV2SingleABRTranscodeCancelling(t *testing.T) {
	filename := videoErstePath
	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, filename)

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
		Url:                filename,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	params.EncHeight = 360 // slow down a bit to allow for the cancel
	params.EncWidth = 640

	handle, err := avpipe.TxInit(params)
	failNowOnError(t, err)
	assert.Greater(t, handle, int32(0))
	go func(handle int32) {
		// Wait for 2 sec the transcoding starts, then cancel it.
		time.Sleep(2 * time.Second)
		err := avpipe.TxCancel(handle)
		assert.NoError(t, err)
	}(handle)
	err2 := avpipe.TxRun(handle)
	assert.Error(t, err2)

	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"
	params.NumAudio = 1
	params.AudioIndex[0] = 1
	handleA, err := avpipe.TxInit(params)
	assert.NoError(t, err)
	assert.Greater(t, handleA, int32(0))
	err = avpipe.TxCancel(handleA)
	assert.NoError(t, err)
	err = avpipe.TxRun(handleA)
	assert.Error(t, err)
}

func doTranscode(t *testing.T, p *avpipe.TxParams, nThreads int, outputDir,
	filename string, reportFailure string) {

	avpipe.InitIOHandler(&fileInputOpener{url: filename},
		&concurrentOutputOpener{dir: outputDir})

	done := make(chan struct{})
	for i := 0; i < nThreads; i++ {
		go func(params *avpipe.TxParams) {
			err := avpipe.Tx(params)
			done <- struct{}{} // Signal the main goroutine
			if err != nil && reportFailure == "" {
				failNowOnError(t, err)
			} else if err != nil {
				fmt.Printf("Ignoring error: %s\n", reportFailure)
				log.Error("doTranscode failed", err)
			}
		}(p)
	}

	for i := 0; i < nThreads; i++ {
		<-done // Wait for background goroutines to finish
	}
}

func TestNvidiaABRTranscode(t *testing.T) {
	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")
	filename := videoRockyPath
	nThreads := 10

	params := &avpipe.TxParams{
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             "h264_nvenc",
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
		Url:                filename,
	}
	setFastEncodeParams(params, false)
	doTranscode(t, params, nThreads, outputDir, filename, "H264_NVIDIA encoder might not be enabled or hardware might not be available")
}

func TestConcurrentABRTranscode(t *testing.T) {
	outputDir := path.Join(baseOutPath, fn())
	boilerplate(t, outputDir, "")
	filename := videoRockyPath
	nThreads := 10

	params := &avpipe.TxParams{
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		VideoSegDurationTs: 1001 * 60,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
		Url:                filename,
		DebugFrameLevel:    debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	doTranscode(t, params, nThreads, outputDir, filename, "")
}

func TestAudioAAC2AACMezMaker(t *testing.T) {
	filename := "./media/bond-seg1.aac"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:              avpipe.TxAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}
	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudioAC3Ts2AC3MezMaker(t *testing.T) {
	filename := "./media/FS1-19-10-15-2-min.ts"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:              avpipe.TxAudio,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 0

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}
	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudioAC3Ts2AACMezMaker(t *testing.T) {
	filename := "./media/FS1-19-10-15-2-min.ts"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:              avpipe.TxAudio,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 0

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudioMP2Ts2AACMezMaker(t *testing.T) {
	filename := "./media/FS1-19-10-15-2-min.ts"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "mp2",
		Dcodec2:             "mp2",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		TxType:              avpipe.TxAudio,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 1

	xcTestResult := &XcTestResult{
		mezFile:    []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:  48000,
		sampleRate: 48000,
	}

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudioDownmix2AACMezMaker(t *testing.T) {
	filename := "./media/BOND23-CLIP-downmix-2min.mov"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:              avpipe.TxAudio,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 6

	xcTestResult := &XcTestResult{
		mezFile:           []string{fmt.Sprintf("%s/asegment-1.mp4", outputDir)},
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudio2MonoTo1Stereo(t *testing.T) {
	filename := "./media/AGAIG-clip-2mono.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		TxType:              avpipe.TxAudioJoin,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		NumAudio:            2,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex[0] = 1
	params.AudioIndex[1] = 2

	xcTestResult := &XcTestResult{
		timeScale:         48000,
		sampleRate:        48000,
		channelLayoutName: "stereo",
	}
	for i := 1; i <= 2; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudio5_1To5_1(t *testing.T) {
	filename := "./media/case_1_video_and_5.1_audio.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		TxType:              avpipe.TxAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudio5_1ToStereo(t *testing.T) {
	filename := "./media/case_1_video_and_5.1_audio.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		TxType:              avpipe.TxAudioPan,
		FilterDescriptor:    "[0:1]pan=stereo|c0<c0+c4+0.707*c2|c1<c1+c5+0.707*c2[aout]",
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudioMonoToMono(t *testing.T) {
	filename := "./media/case_1_video_and_mono_audio.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		TxType:              avpipe.TxAudio,
		NumAudio:            1,
		ChannelLayout:       avpipe.ChannelLayout("mono"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudioQuadToQuad(t *testing.T) {
	filename := "./media/case_1_video_and_quad_audio.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		TxType:              avpipe.TxAudio,
		NumAudio:            1,
		ChannelLayout:       avpipe.ChannelLayout("quad"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudio6MonoTo5_1(t *testing.T) {
	filename := "./media/case_2_video_and_8_mono_audio.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		TxType:              avpipe.TxAudioMerge,
		NumAudio:            6,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:3][0:4][0:5][0:6][0:7][0:8]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2| c3=c3|c4=c4|c5=c5[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudio6MonoUnequalChannelLayoutsTo5_1(t *testing.T) {
	filename := "./media/cmbyn_th-2348159_aud-comp-30sec.mov"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		TxType:              avpipe.TxAudioMerge,
		NumAudio:            6,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:0][0:1][0:2][0:3][0:4][0:5]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2|c3=c3|c4=c4|c5=c5[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudio10Channel_s16To6Channel_5_1(t *testing.T) {
	filename := "./media/case_3_video_and_10_channel_audio_10sec.mov"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		TxType:              avpipe.TxAudioPan,
		NumAudio:            1,
		ChannelLayout:       avpipe.ChannelLayout("5.1"),
		FilterDescriptor:    "[0:1]pan=5.1|c0=c3|c1=c4|c2=c5|c3=c6|c4=c7|c5=c8[aout]",
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

func TestAudio2Channel1Stereo(t *testing.T) {
	filename := "./media/multichannel_audio_clip.mov"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec2:             "",
		TxType:              avpipe.TxAudioPan,
		ChannelLayout:       avpipe.ChannelLayout("stereo"),
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		FilterDescriptor:    "[0:1]pan=stereo|c0<c1+0.707*c2|c1<c2+0.707*c1[aout]",
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

// Timebase of BOB923HL_clip_timebase_1001_60000.MXF is 1001/60000
func TestIrregularTsMezMaker_1001_60000(t *testing.T) {
	filename := "./media/BOB923HL_clip_timebase_1001_60000.MXF"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:              avpipe.TxVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         120,
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

// Timebase of Rigify-2min is 1/24
func TestIrregularTsMezMaker_1_24(t *testing.T) {
	filename := "./media/Rigify-2min.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:              avpipe.TxVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
		Url:                 filename,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		timeScale: 12288,
		level:     42,
		pixelFmt:  "yuv420p",
	}

	if setFastEncodeParams(params, false) {
		xcTestResult.level = 30
	}

	for i := 1; i <= 4; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult)
}

// Timebase of Rigify-2min is 1/10000
func TestIrregularTsMezMaker_1_10000(t *testing.T) {
	filename := "./media/Rigify-2min-10000ts.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:              avpipe.TxVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
		Url:                 filename,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		timeScale: 10000,
		level:     42,
		pixelFmt:  "yuv420p",
	}

	if setFastEncodeParams(params, false) {
		xcTestResult.level = 30
	}

	for i := 1; i <= 4; i++ {
		xcTestResult.mezFile = append(xcTestResult.mezFile, fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i))
	}

	xcTest(t, outputDir, params, xcTestResult)
}

func TestMXF_H265MezMaker(t *testing.T) {
	f := fn()
	if testing.Short() {
		// 558.20s on 2018 MacBook Pro (2.9 GHz 6-Core i9, 32 GB RAM, Radeon Pro 560X 4 GB)
		t.Skip("SKIPPING " + f)
	}
	filename := "./media/across_the_universe_4k_clip_60sec.mxf"
	outputDir := path.Join(baseOutPath, f)

	params := &avpipe.TxParams{
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
		TxType:            avpipe.TxVideo,
		StreamId:          -1,
		Url:               filename,
		DebugFrameLevel:   debugFrameLevel,
	}

	xcTestResult := &XcTestResult{
		mezFile:   []string{fmt.Sprintf("%s/vsegment-1.mp4", outputDir)},
		timeScale: 24000,
		level:     150,
		pixelFmt:  "yuv420p",
	}

	xcTest(t, outputDir, params, xcTestResult)
}

func TestHEVC_H264MezMaker(t *testing.T) {
	filename := "./media/across_the_universe_4k_clip_30sec.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:            avpipe.TxVideo,
		StreamId:          -1,
		Url:               filename,
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

	xcTest(t, outputDir, params, xcTestResult)
}

func TestHEVC_H265ABRTranscode(t *testing.T) {
	f := fn()
	if testing.Short() {
		// 403.23s on 2018 MacBook Pro (2.9 GHz 6-Core i9, 32 GB RAM, Radeon Pro 560X 4 GB)
		t.Skip("SKIPPING " + f)
	}
	filename := "./media/across_the_universe_4k_clip_30sec.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
		BypassTranscoding: false,
		Format:            "dash",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "15.03",
		Ecodec:            "libx265",
		Dcodec:            "hevc",
		EncHeight:         -1,
		EncWidth:          -1,
		TxType:            avpipe.TxVideo,
		StreamId:          -1,
		Url:               filename,
		DebugFrameLevel:   debugFrameLevel,
	}

	xcTest(t, outputDir, params, nil)
}

func TestAVPipeStats(t *testing.T) {
	filename := "./media/Rigify-2min.mp4"
	outputDir := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:              avpipe.TxAll,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
		Url:                 filename,
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

	xcTest(t, outputDir, params, xcTestResult)

	assert.Equal(t, int64(2880), statsInfo.encodingVideoFrameStats.TotalFramesWritten)
	assert.Equal(t, int64(5625), statsInfo.encodingAudioFrameStats.TotalFramesWritten)
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
	filename := "./media/creed_1_min.mov"

	videoMezDir := path.Join(baseOutPath, f, "VideoMez4Muxing")
	audioMezDir := path.Join(baseOutPath, f, "AudioMez4Muxing")
	videoABRDir := path.Join(baseOutPath, f, "VideoABR4Muxing")
	audioABRDir := path.Join(baseOutPath, f, "AudioABR4Muxing")
	muxOutDir := path.Join(baseOutPath, f, "MuxingOutput")

	// Create video mez files
	setupOutDir(t, videoMezDir)
	params := &avpipe.TxParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		VideoBitrate:      2560000,
		AudioBitrate:      128000,
		SampleRate:        44100,
		SegDuration:       "30.03",
		Ecodec:            h264Codec,
		EncHeight:         720,
		EncWidth:          1280,
		TxType:            avpipe.TxVideo,
		StreamId:          -1,
		NumAudio:          -1,
		Url:               filename,
		DebugFrameLevel:   debugFrameLevel,
	}
	setFastEncodeParams(params, false)
	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: videoMezDir})
	boilerTx(t, params)

	log.Debug("STARTING audio mez for muxing", "file", filename)
	// Create audio mez files
	setupOutDir(t, audioMezDir)
	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"
	params.ChannelLayout = avpipe.ChannelLayout("stereo")
	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: audioMezDir})
	boilerTx(t, params)

	// Create video ABR files
	setupOutDir(t, videoABRDir)
	filename = videoMezDir + "/vsegment-1.mp4"
	log.Debug("STARTING video ABR for muxing", "file", filename)
	params.TxType = avpipe.TxVideo
	params.Format = "dash"
	params.VideoSegDurationTs = 48000
	params.Url = filename
	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: videoABRDir})
	boilerTx(t, params)

	// Create audio ABR files
	setupOutDir(t, audioABRDir)
	filename = audioMezDir + "/asegment-1.mp4"
	log.Debug("STARTING audio ABR for muxing", "file", filename)
	params.TxType = avpipe.TxAudio
	params.Format = "dash"
	params.Ecodec2 = "aac"
	params.AudioSegDurationTs = 96000
	params.Url = filename
	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: audioABRDir})
	boilerTx(t, params)

	// Create playable file by muxing audio/video segments
	setupOutDir(t, muxOutDir)
	muxSpec := "abr-mux\n"
	muxSpec += "audio,1," + audioABRDir + "/init-stream0.mp4\n"
	for i := 1; i <= 15; i++ {
		muxSpec += fmt.Sprintf("%s%s%s%02d%s\n", "audio,1,", audioABRDir, "/chunk-stream0-000", i, ".mp4")
	}
	muxSpec += "video,1," + videoABRDir + "/init-stream0.mp4\n"
	for i := 1; i <= 15; i++ {
		muxSpec += fmt.Sprintf("%s%s%s%02d%s\n", "video,1,", videoABRDir, "/chunk-stream0-000", i, ".mp4")
	}
	filename = muxOutDir + "/segment-1.mp4"
	params.MuxingSpec = muxSpec
	log.Debug(f, "muxSpec", string(muxSpec))

	avpipe.InitUrlMuxIOHandler(filename, &cmd.AVCmdMuxInputOpener{URL: filename}, &cmd.AVCmdMuxOutputOpener{})
	params.Url = filename
	err := avpipe.Mux(params)
	failNowOnError(t, err)

	xcTestResult := &XcTestResult{}
	// Now probe mez video and output file and become sure both have the same duration
	xcTestResult.mezFile = []string{fmt.Sprintf("%s/vsegment-1.mp4", videoMezDir)}
	avpipe.InitIOHandler(&fileInputOpener{url: xcTestResult.mezFile[0]}, &fileOutputOpener{dir: videoMezDir})
	// Now probe the generated files
	videoMezProbeInfo := boilerProbe(t, xcTestResult)

	xcTestResult.mezFile = []string{fmt.Sprintf("%s/segment-1.mp4", muxOutDir)}
	avpipe.InitIOHandler(&fileInputOpener{url: xcTestResult.mezFile[0]}, &fileOutputOpener{dir: muxOutDir})
	muxOutProbeInfo := boilerProbe(t, xcTestResult)

	assert.Equal(t, true, int(videoMezProbeInfo[0].ContainerInfo.Duration) == int(muxOutProbeInfo[0].ContainerInfo.Duration))
}

func TestMarshalParams(t *testing.T) {
	params := &avpipe.TxParams{
		VideoBitrate:       8000000,
		VideoSegDurationTs: 180000,
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
	}
	bytes, err := json.Marshal(params)
	assert.NoError(t, err)
	_ = bytes
	// TODO: Add asserts
}

func TestUnmarshalParams(t *testing.T) {
	var params avpipe.TxParams
	bytes := []byte(`{"video_bitrate":8000000,"seg_duration_ts":180000,"seg_duration_fr":50,"enc_height":720,"enc_width":1280,"tx_type":1}`)
	err := json.Unmarshal(bytes, &params)
	assert.NoError(t, err)
	assert.Equal(t, avpipe.TxVideo, int(params.TxType))
	// TODO: More checks
}

func TestProbe(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &concurrentOutputOpener{dir: "O"})
	probe, err := avpipe.Probe(filename, true)
	failNowOnError(t, err)
	assert.Equal(t, 2, len(probe.StreamInfo))

	assert.Equal(t, 27, probe.StreamInfo[0].CodecID)
	assert.Equal(t, "h264", probe.StreamInfo[0].CodecName)
	assert.Equal(t, 77, probe.StreamInfo[0].Profile) // 77 = FF_PROFILE_H264_MAIN
	assert.Equal(t, 31, probe.StreamInfo[0].Level)
	assert.Equal(t, int64(2428), probe.StreamInfo[0].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[0].StartTime)
	assert.Equal(t, int64(506151), probe.StreamInfo[0].BitRate)
	assert.Equal(t, 1280, probe.StreamInfo[0].Width)
	assert.Equal(t, 720, probe.StreamInfo[0].Height)
	assert.Equal(t, int64(12800), probe.StreamInfo[0].TimeBase.Denom().Int64())

	assert.Equal(t, 86018, probe.StreamInfo[1].CodecID)
	assert.Equal(t, "aac", probe.StreamInfo[1].CodecName)
	assert.Equal(t, 1, probe.StreamInfo[1].Profile) // 1 = FF_PROFILE_AAC_LOW
	assert.Equal(t, -99, probe.StreamInfo[1].Level)
	assert.Equal(t, int64(4183), probe.StreamInfo[1].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[1].StartTime)
	assert.Equal(t, int64(127999), probe.StreamInfo[1].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[1].Width)
	assert.Equal(t, 0, probe.StreamInfo[1].Height)
	assert.Equal(t, int64(44100), probe.StreamInfo[1].TimeBase.Denom().Int64())

	// Test StreamInfoAsArray
	a := avpipe.StreamInfoAsArray(probe.StreamInfo)
	assert.Equal(t, "h264", a[0].CodecName)
	assert.Equal(t, "aac", a[1].CodecName)
}

func TestProbeWithData(t *testing.T) {
	filename := "./media/ActOfLove-30sec.mov"

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &concurrentOutputOpener{dir: "O"})
	probe, err := avpipe.Probe(filename, true)
	failNowOnError(t, err)
	assert.Equal(t, 4, len(probe.StreamInfo))

	assert.Equal(t, 147, probe.StreamInfo[0].CodecID)
	assert.Equal(t, "prores", probe.StreamInfo[0].CodecName)
	assert.Equal(t, 3, probe.StreamInfo[0].Profile) // 3 = FF_PROFILE_MPEG4_MAIN
	assert.Equal(t, -99, probe.StreamInfo[0].Level)
	assert.Equal(t, int64(900), probe.StreamInfo[0].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[0].StartTime)
	assert.Equal(t, int64(59664772), probe.StreamInfo[0].BitRate)
	assert.Equal(t, 720, probe.StreamInfo[0].Width)
	assert.Equal(t, 486, probe.StreamInfo[0].Height)
	assert.Equal(t, int64(11988), probe.StreamInfo[0].TimeBase.Denom().Int64())

	assert.Equal(t, 65548, probe.StreamInfo[1].CodecID)
	assert.Equal(t, "pcm_s24le", probe.StreamInfo[1].CodecName)
	assert.Equal(t, -99, probe.StreamInfo[1].Profile)
	assert.Equal(t, -99, probe.StreamInfo[1].Level)
	assert.Equal(t, int64(1441552), probe.StreamInfo[1].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[1].StartTime)
	assert.Equal(t, int64(2304000), probe.StreamInfo[1].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[1].Width)
	assert.Equal(t, 0, probe.StreamInfo[1].Height)
	assert.Equal(t, int64(48000), probe.StreamInfo[1].TimeBase.Denom().Int64())

	assert.Equal(t, 65548, probe.StreamInfo[2].CodecID)
	assert.Equal(t, "pcm_s24le", probe.StreamInfo[2].CodecName)
	assert.Equal(t, -99, probe.StreamInfo[2].Profile)
	assert.Equal(t, -99, probe.StreamInfo[2].Level)
	assert.Equal(t, int64(1441552), probe.StreamInfo[2].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[2].StartTime)
	assert.Equal(t, int64(2304000), probe.StreamInfo[2].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[2].Width)
	assert.Equal(t, 0, probe.StreamInfo[2].Height)
	assert.Equal(t, int64(48000), probe.StreamInfo[2].TimeBase.Denom().Int64())

	assert.Equal(t, 0, probe.StreamInfo[3].CodecID)
	assert.Equal(t, "", probe.StreamInfo[3].CodecName)
	assert.Equal(t, -99, probe.StreamInfo[3].Profile)
	assert.Equal(t, -99, probe.StreamInfo[3].Level)
	assert.Equal(t, int64(1), probe.StreamInfo[3].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[3].StartTime)
	assert.Equal(t, int64(1), probe.StreamInfo[3].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[3].Width)
	assert.Equal(t, 0, probe.StreamInfo[3].Height)
	assert.Equal(t, int64(30), probe.StreamInfo[3].TimeBase.Denom().Int64())

	// Test StreamInfoAsArray
	a := avpipe.StreamInfoAsArray(probe.StreamInfo)
	assert.Equal(t, "prores", a[0].CodecName)
	assert.Equal(t, "", a[1].CodecName)
	assert.Equal(t, "pcm_s24le", a[2].CodecName)
}

func TestExtractImagesInterval(t *testing.T) {
	url := videoErstePath
	outPath := path.Join(baseOutPath, fn())
	params := &avpipe.TxParams{
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
		TxType:                 avpipe.TxExtractImages,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	setFastEncodeParams(params, true)

	xcTest2(t, outPath, params, nil)

	files, err := ioutil.ReadDir(outPath)
	failNowOnError(t, err)
	assert.Equal(t, 10, len(files))
	var sum int
	for _, f := range files {
		pts, err2 := strconv.ParseInt(strings.Split(f.Name(), ".")[0], 10, 32)
		assert.NoError(t, err2)
		sum += int(pts)
	}
	assert.Equal(t, 5760000, sum)
}

func TestExtractImagesList(t *testing.T) {
	url := videoErstePath
	outPath := path.Join(baseOutPath, fn())
	params := &avpipe.TxParams{
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
		TxType:                 avpipe.TxExtractImages,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	params.ExtractImagesTs = []int64{0, 512, 12800, 513024, 1242624}
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
	assert.Equal(t, 512+12800+513024+1242624, sum)
}

// Should exit after extracting the first frame
func TestExtractImagesListFast(t *testing.T) {
	url := videoErstePath
	outPath := path.Join(baseOutPath, fn())

	params := &avpipe.TxParams{
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
		TxType:                 avpipe.TxExtractImages,
		Url:                    url,
		DebugFrameLevel:        debugFrameLevel,
	}
	params.ExtractImagesTs = []int64{0}
	setFastEncodeParams(params, true)

	xcTest2(t, outPath, params, nil)

	files, err := ioutil.ReadDir(outPath)
	failNowOnError(t, err)
	assert.Equal(t, 1, len(files))
	pts, err := strconv.ParseInt(strings.Split(files[0].Name(), ".")[0], 10, 32)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), pts)
}

func TestMain(m *testing.M) {
	// call flag.Parse() here if TestMain uses flags
	setupLogging()
	os.Exit(m.Run())
}

func xcTest(t *testing.T, outputDir string, params *avpipe.TxParams, xcTestResult *XcTestResult) {
	boilerplate(t, outputDir, params.Url)
	boilerTx(t, params)
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
		probeInfo, err := avpipe.Probe(mezFile, true)
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

func boilerTx(t *testing.T, params *avpipe.TxParams) {
	err := avpipe.Tx(params)
	failNowOnError(t, err)
}

func xcTest2(t *testing.T, outputDir string, params *avpipe.TxParams, xcTestResult *XcTestResult) {
	boilerplate(t, outputDir, params.Url)
	boilerTx2(t, params)
	boilerProbe(t, xcTestResult)
}

// This test uses the following new APIs
// - to obtain a handle of running session:
//   - TxInit()
// - to run the tx session
//   - TxRun()
func boilerTx2(t *testing.T, params *avpipe.TxParams) {
	handle, err := avpipe.TxInit(params)
	failNowOnError(t, err)
	assert.Greater(t, handle, int32(0))
	err = avpipe.TxRun(handle)
	failNowOnError(t, err)
}

func setFastEncodeParams(p *avpipe.TxParams, force bool) bool {
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
		},
	})
	avpipe.SetCLoggers()
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
