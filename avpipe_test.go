package avpipe_test

import (
	"encoding/json"
	"fmt"
	"github.com/qluvio/avpipe/avcmd/cmd"
	"io"
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qluvio/avpipe"
	log "github.com/qluvio/content-fabric/log"
	"github.com/stretchr/testify/assert"
)

const baseOutPath = "test_out"

//Implement AVPipeInputOpener
type fileInputOpener struct {
	url string
}

type testStatsInfo struct {
	audioFramesRead         uint64
	videoFramesRead         uint64
	encodingAudioFrameStats avpipe.EncodingFrameStats
	encodingVideoFrameStats avpipe.EncodingFrameStats
}

var statsInfo testStatsInfo

func (io *fileInputOpener) Open(fd int64, url string) (avpipe.InputHandler, error) {
	f, err := os.Open(url)
	if err != nil {
		return nil, err
	}

	io.url = url
	etxInput := &fileInput{
		file: f,
	}

	return etxInput, nil
}

// Implement InputHandler
type fileInput struct {
	file *os.File // Input file
}

func (i *fileInput) Read(buf []byte) (int, error) {
	n, err := i.file.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	return n, err
}

func (i *fileInput) Seek(offset int64, whence int) (int64, error) {
	n, err := i.file.Seek(int64(offset), int(whence))
	return n, err
}

func (i *fileInput) Close() error {
	err := i.file.Close()
	return err
}

func (i *fileInput) Size() int64 {
	fi, err := i.file.Stat()
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (i *fileInput) Stat(statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_IN_STAT_BYTES_READ:
		readOffset := statArgs.(*uint64)
		log.Info("AVP TEST IN STAT", "STAT read offset", *readOffset)
	case avpipe.AV_IN_STAT_AUDIO_FRAME_READ:
		audioFramesRead := statArgs.(*uint64)
		log.Info("AVP TEST IN STAT", "audioFramesRead", *audioFramesRead)
		statsInfo.audioFramesRead = *audioFramesRead
	case avpipe.AV_IN_STAT_VIDEO_FRAME_READ:
		videoFramesRead := statArgs.(*uint64)
		log.Info("AVP TEST IN STAT", "videoFramesRead", *videoFramesRead)
		statsInfo.videoFramesRead = *videoFramesRead
	case avpipe.AV_IN_STAT_DECODING_AUDIO_START_PTS:
		startPTS := statArgs.(*uint64)
		log.Info("AVP TEST IN STAT", "audio start PTS", *startPTS)
	case avpipe.AV_IN_STAT_DECODING_VIDEO_START_PTS:
		startPTS := statArgs.(*uint64)
		log.Info("AVP TEST IN STAT", "video start PTS", *startPTS)
	}
	return nil
}

//Implement AVPipeOutputOpener
type fileOutputOpener struct {
	dir string
	err error
}

func (oo *fileOutputOpener) Open(h, fd int64, stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	var filename string

	switch out_type {
	case avpipe.DASHVideoInit:
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/init-stream%d.mp4", oo.dir, stream_index)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", oo.dir)
	case avpipe.DASHVideoSegment:
		fallthrough
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/chunk-stream%d-%05d.mp4", oo.dir, stream_index, seg_index)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./%s/master.m3u8", oo.dir)
	case avpipe.HLSVideoM3U:
		fallthrough
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./%s/media_%d.m3u8", oo.dir, stream_index)
	case avpipe.AES128Key:
		filename = fmt.Sprintf("./%s/key.bin", oo.dir)
	case avpipe.MP4Segment:
		filename = fmt.Sprintf("./%s/segment-%d.mp4", oo.dir, seg_index)
	case avpipe.FMP4VideoSegment:
		filename = fmt.Sprintf("./%s/vsegment-%d.mp4", oo.dir, seg_index)
	case avpipe.FMP4AudioSegment:
		filename = fmt.Sprintf("./%s/asegment-%d.mp4", oo.dir, seg_index)
	case avpipe.FrameImage:
		filename = fmt.Sprintf("./%s/%d.jpeg", oo.dir, seg_index)
	}

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		oo.err = err
		return nil, err
	}

	oh := &fileOutput{
		url:          filename,
		stream_index: stream_index,
		seg_index:    seg_index,
		file:         f}

	return oh, nil
}

//Implement AVPipeOutputOpener
type concurrentOutputOpener struct {
	dir string
}

func (coo *concurrentOutputOpener) Open(h, fd int64, stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	var filename string
	dir := fmt.Sprintf("%s/O%d", coo.dir, h)

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.Mkdir(dir, 0755)
	}

	switch out_type {
	case avpipe.DASHVideoInit:
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/init-stream%d.mp4", dir, stream_index)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", dir)
	case avpipe.DASHVideoSegment:
		fallthrough
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/chunk-stream%d-%05d.mp4", dir, stream_index, seg_index)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./%s/master.m3u8", dir)
	case avpipe.HLSVideoM3U:
		fallthrough
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./%s/media_%d.m3u8", dir, stream_index)
	case avpipe.AES128Key:
		filename = fmt.Sprintf("./%s/key.bin", dir)
	}

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}

	oh := &fileOutput{
		url:          filename,
		stream_index: stream_index,
		seg_index:    seg_index,
		file:         f}

	return oh, nil
}

// Implement OutputHandler
type fileOutput struct {
	url          string
	stream_index int
	seg_index    int
	file         *os.File
}

func (o *fileOutput) Write(buf []byte) (int, error) {
	n, err := o.file.Write(buf)
	return n, err
}

func (o *fileOutput) Seek(offset int64, whence int) (int64, error) {
	n, err := o.file.Seek(offset, whence)
	return n, err
}

func (o *fileOutput) Close() error {
	err := o.file.Close()
	return err
}

func (o fileOutput) Stat(avType avpipe.AVType, statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_OUT_STAT_BYTES_WRITTEN:
		writeOffset := statArgs.(*uint64)
		log.Info("AVP TEST OUT STAT", "STAT, write offset", *writeOffset)
	case avpipe.AV_OUT_STAT_ENCODING_END_PTS:
		endPTS := statArgs.(*uint64)
		log.Info("AVP TEST OUT STAT", "STAT, endPTS", *endPTS)
	case avpipe.AV_OUT_STAT_FRAME_WRITTEN:
		encodingStats := statArgs.(*avpipe.EncodingFrameStats)
		log.Info("AVP TEST OUT STAT", "avType", avType,
			"encodingStats", encodingStats)
		if avType == avpipe.FMP4AudioSegment {
			statsInfo.encodingAudioFrameStats = *encodingStats
		} else {
			statsInfo.encodingVideoFrameStats = *encodingStats
		}
	}

	return nil
}

func TestSingleABRTranscode(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"
	outputDir := "SingleABRTranscode"

	setupLogging()
	log.Info("STARTING TestSingleABRTranscode")

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		CrfStr:             "23",
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             "libx264",
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, true)
	if err != nil {
		t.Fail()
	}

	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"
	params.NumAudio = -1
	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

}

func TestSingleABRTranscodeByStreamId(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"
	outputDir := "SingleABRTranscodeByStreamId"

	setupLogging()
	log.Info("STARTING TestSingleABRTranscodeByStreamId")

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		CrfStr:             "23",
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             "libx264",
		EncHeight:          720,
		EncWidth:           1280,
		StreamId:           1,
		NumAudio:           -1,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, true)
	if err != nil {
		t.Fail()
	}

	params.StreamId = 2
	params.Ecodec2 = "aac"
	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

}

func TestSingleABRTranscodeWithWatermark(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"
	outputDir := "SingleABRTranscodeWithWatermark"

	setupLogging()
	log.Info("STARTING TestSingleABRTranscode")

	params := &avpipe.TxParams{
		BypassTranscoding:     false,
		Format:                "hls",
		StartTimeTs:           0,
		DurationTs:            -1,
		StartSegmentStr:       "1",
		VideoBitrate:          2560000,
		AudioBitrate:          64000,
		SampleRate:            44100,
		CrfStr:                "23",
		VideoSegDurationTs:    1001 * 60,
		Ecodec:                "libx264",
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
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, true)
	if err != nil {
		t.Fail()
	}
}

func TestSingleABRTranscodeWithOverlayWatermark(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"
	outputDir := "SingleABRTranscodeWithOverlayWatermark"

	setupLogging()
	log.Info("STARTING TestSingleABRTranscode")

	overlayImage, err := ioutil.ReadFile("./media/fox_watermark.png")
	if err != nil {
		t.Fail()
	}

	params := &avpipe.TxParams{
		BypassTranscoding:    false,
		Format:               "hls",
		StartTimeTs:          0,
		DurationTs:           -1,
		StartSegmentStr:      "1",
		VideoBitrate:         2560000,
		AudioBitrate:         64000,
		SampleRate:           44100,
		CrfStr:               "23",
		VideoSegDurationTs:   1001 * 60,
		Ecodec:               "libx264",
		EncHeight:            720,
		EncWidth:             1280,
		TxType:               avpipe.TxVideo,
		WatermarkYLoc:        "main_h*0.7",
		WatermarkXLoc:        "main_w/2-overlay_w/2",
		WatermarkOverlay:     string(overlayImage),
		WatermarkOverlayLen:  len(overlayImage),
		WatermarkOverlayType: avpipe.PngImage,
		StreamId:             -1,
	}

	// Create output directory if it doesn't exist
	err = setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, true)
	if err != nil {
		t.Fail()
	}
}

// This test uses the following new APIs
// - to obtain a handle of running session:
//   - TxInit()
// - to run the tx session
//   - TxRun()
func TestV2SingleABRTranscode(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"
	outputDir := "V2SingleABRTranscode"

	setupLogging()
	log.Info("STARTING TestV2SingleABRTranscode")

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		CrfStr:             "23",
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             "libx264",
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	handle, err := avpipe.TxInit(params, filename, true)
	assert.NoError(t, err)
	assert.Equal(t, true, handle > 0)
	err = avpipe.TxRun(handle)
	assert.NoError(t, err)

	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"
	params.NumAudio = 1
	params.AudioIndex[0] = 1
	handle, err = avpipe.TxInit(params, filename, true)
	assert.NoError(t, err)
	assert.Equal(t, true, handle > 0)
	err = avpipe.TxRun(handle)
	assert.NoError(t, err)
}

func TestV2SingleABRTranscodeIOHandler(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"
	outputDir := "V2SingleABRTranscodeIOHandler"

	setupLogging()
	log.Info("STARTING TestV2SingleABRTranscode")

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		CrfStr:             "23",
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             "libx264",
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	handle, err := avpipe.TxInit(params, filename, true)
	assert.NoError(t, err)
	assert.Equal(t, true, handle > 0)
	err = avpipe.TxRun(handle)
	assert.NoError(t, err)

	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"
	params.NumAudio = 1
	params.AudioIndex[0] = 1
	handle, err = avpipe.TxInit(params, filename, true)
	assert.NoError(t, err)
	assert.Equal(t, true, handle > 0)
	err = avpipe.TxRun(handle)
	assert.NoError(t, err)
}

func TestV2SingleABRTranscodeCancelling(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"
	outputDir := "V2SingleABRTranscodeCancelling"

	setupLogging()
	log.Info("STARTING TestV2SingleABRTranscodeCancelling")

	params := &avpipe.TxParams{
		BypassTranscoding:  false,
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		CrfStr:             "23",
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             "libx264",
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	handle, err := avpipe.TxInit(params, filename, true)
	assert.NoError(t, err)
	assert.Equal(t, true, handle > 0)
	go func(handle int32) {
		// Wait for 3 sec the transcoding starts, then cancel it.
		time.Sleep(3 * time.Second)
		err := avpipe.TxCancel(handle)
		assert.NoError(t, err)
	}(handle)
	err = avpipe.TxRun(handle)
	assert.Error(t, err)

	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"
	params.NumAudio = 1
	params.AudioIndex[0] = 1
	handle, err = avpipe.TxInit(params, filename, true)
	assert.NoError(t, err)
	assert.Equal(t, true, handle > 0)
	err = avpipe.TxCancel(handle)
	assert.NoError(t, err)
	err = avpipe.TxRun(handle)
	assert.Error(t, err)
}

func doTranscode(t *testing.T, p *avpipe.TxParams, nThreads int, filename string, reportFailure string) {
	outputDir := "O"
	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &concurrentOutputOpener{dir: outputDir})

	done := make(chan struct{})
	for i := 0; i < nThreads; i++ {
		go func(params *avpipe.TxParams, filename string) {
			err := avpipe.Tx(params, filename, false)
			done <- struct{}{} // Signal the main goroutine
			if err != nil && reportFailure == "" {
				t.Fail()
			} else if err != nil {
				fmt.Printf("%s\n", reportFailure)
				log.Error("doTranscode failed", err)
			}
		}(p, filename)
	}

	for i := 0; i < nThreads; i++ {
		<-done // Wait for background goroutines to finish
	}
}

func TestNvidiaABRTranscode(t *testing.T) {
	nThreads := 10
	filename := "./media/rocky.mp4"

	setupLogging()
	log.Info("STARTING TestNvidiaABRTranscode")

	params := &avpipe.TxParams{
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		CrfStr:             "23",
		VideoSegDurationTs: 1001 * 60,
		AudioSegDurationTs: 1001 * 60,
		Ecodec:             "h264_nvenc",
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
	}

	doTranscode(t, params, nThreads, filename, "H264_NVIDIA encoder might not be enabled or hardware might not be available")
}

func TestConcurrentABRTranscode(t *testing.T) {
	nThreads := 10
	filename := "./media/rocky.mp4"

	setupLogging()
	log.Info("STARTING TestConcurrentABRTranscode")

	params := &avpipe.TxParams{
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		CrfStr:             "23",
		VideoSegDurationTs: 1001 * 60,
		Ecodec:             "libx264",
		EncHeight:          720,
		EncWidth:           1280,
		TxType:             avpipe.TxVideo,
		StreamId:           -1,
	}

	doTranscode(t, params, nThreads, filename, "")
}

func TestAAC2AACMezMaker(t *testing.T) {
	filename := "./media/bond-seg1.aac"
	outputDir := "AAC2AAC"

	setupLogging()
	log.Info("STARTING TestAAC2AACMezMaker")

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
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	avpipe.Tx(params, filename, false)

	mezFile := fmt.Sprintf("%s/asegment-1.mp4", outputDir)
	// Now probe the generated files
	probeInfo, err := avpipe.Probe(mezFile, true)
	if err != nil {
		t.Error(err)
	}

	timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
	if timebase.Cmp(big.NewInt(48000)) != 0 {
		t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
	}

	sampleRate := probeInfo.StreamInfo[0].SampleRate
	if sampleRate != 48000 {
		t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
	}
}

func TestAC3TsAC3MezMaker(t *testing.T) {
	filename := "./media/FS1-19-10-15-2-min.ts"
	outputDir := "AC3TsAC3Mez"

	setupLogging()
	log.Info("STARTING TestAC3TsAC3MezMaker")

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
	}

	params.AudioIndex[0] = 0

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	assert.NoError(t, err)

	mezFile := fmt.Sprintf("%s/asegment-1.mp4", outputDir)
	// Now probe the generated files
	probeInfo, err := avpipe.Probe(mezFile, true)
	assert.NoError(t, err)

	timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
	assert.Equal(t, true, timebase.Cmp(big.NewInt(48000)) == 0)

	sampleRate := probeInfo.StreamInfo[0].SampleRate
	assert.Equal(t, 48000, sampleRate)
}

func TestAC3TsAACMezMaker(t *testing.T) {
	filename := "./media/FS1-19-10-15-2-min.ts"
	outputDir := "AC3TsACCMez"

	setupLogging()
	log.Info("STARTING TestAC3TsAACMezMaker")

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec:              "ac3",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		TxType:              avpipe.TxAudio,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
	}

	params.AudioIndex[0] = 0

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	mezFile := fmt.Sprintf("%s/asegment-1.mp4", outputDir)
	// Now probe the generated files
	probeInfo, err := avpipe.Probe(mezFile, true)
	if err != nil {
		t.Error(err)
	}

	timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
	if timebase.Cmp(big.NewInt(48000)) != 0 {
		t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
	}

	sampleRate := probeInfo.StreamInfo[0].SampleRate
	if sampleRate != 48000 {
		t.Error("Unexpected SampleRate", probeInfo.StreamInfo[0].SampleRate)
	}
}

func TestMP2TsAACMezMaker(t *testing.T) {
	filename := "./media/FS1-19-10-15-2-min.ts"
	//filename := "./media/FS1-15-MP2.mp2"
	outputDir := "MP2TsACCMez"

	setupLogging()
	log.Info("STARTING TestMP2TsAACMezMaker")

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "mp2",
		Dcodec:              "mp2",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		TxType:              avpipe.TxAudio,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
	}

	params.AudioIndex[0] = 1

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	mezFile := fmt.Sprintf("%s/asegment-1.mp4", outputDir)
	// Now probe the generated files
	probeInfo, err := avpipe.Probe(mezFile, true)
	if err != nil {
		t.Error(err)
	}

	timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
	if timebase.Cmp(big.NewInt(48000)) != 0 {
		t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
	}

	sampleRate := probeInfo.StreamInfo[0].SampleRate
	if sampleRate != 48000 {
		t.Error("Unexpected SampleRate", probeInfo.StreamInfo[0].SampleRate)
	}
}

func TestDownmix2AACMezMaker(t *testing.T) {
	filename := "./media/BOND23-CLIP-downmix-2min.mov"
	outputDir := "Downmix2ACCMez"

	setupLogging()
	log.Info("STARTING TestDownmix2AACMezMaker")

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec:              "pcm_s24le",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		TxType:              avpipe.TxAudio,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
	}

	params.AudioIndex[0] = 6

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	mezFile := fmt.Sprintf("%s/asegment-1.mp4", outputDir)
	// Now probe the generated files
	probeInfo, err := avpipe.Probe(mezFile, true)
	if err != nil {
		t.Error(err)
	}

	timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
	if timebase.Cmp(big.NewInt(48000)) != 0 {
		t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
	}

	sampleRate := probeInfo.StreamInfo[0].SampleRate
	if sampleRate != 48000 {
		t.Error("Unexpected sample rate", probeInfo.StreamInfo[0].SampleRate)
	}
}

func Test2Mono1Stereo(t *testing.T) {
	filename := "./media/AGAIG-clip-2mono.mp4"
	outputDir := "2Mono1Stereo"

	setupLogging()
	log.Info("STARTING Test2Mono1Stereo")

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec:              "",
		TxType:              avpipe.TxAudioJoin,
		NumAudio:            2,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
	}

	params.AudioIndex[0] = 1
	params.AudioIndex[1] = 2

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, true)
	if err != nil {
		t.Fail()
	}

	for i := 1; i <= 2; i++ {
		mezFile := fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i)
		// Now probe the generated files
		probeInfo, err := avpipe.Probe(mezFile, true)
		if err != nil {
			t.Error(err)
		}

		timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
		if timebase.Cmp(big.NewInt(48000)) != 0 {
			t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
		}

		if avpipe.ChannelLayoutName(probeInfo.StreamInfo[0].Channels,
			probeInfo.StreamInfo[0].ChannelLayout) != "stereo" {
			t.Error("Unexpected channel layout", probeInfo.StreamInfo[0].ChannelLayout)
		}
	}

}

func Test2Channel1Stereo(t *testing.T) {
	filename := "./media/multichannel_audio_clip.mov"
	outputDir := "2Channel1Stereo"

	setupLogging()
	log.Info("STARTING Test2Channel1Stereo")

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec:              "",
		TxType:              avpipe.TxAudioPan,
		NumAudio:            1,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		FilterDescriptor:    "[0:1]pan=stereo|c0<c1+0.707*c2|c1<c2+0.707*c1[aout]",
	}

	params.AudioIndex[0] = 1

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, true)
	if err != nil {
		t.Fail()
	}

	for i := 1; i <= 2; i++ {
		mezFile := fmt.Sprintf("%s/asegment-%d.mp4", outputDir, i)
		// Now probe the generated files
		probeInfo, err := avpipe.Probe(mezFile, true)
		if err != nil {
			t.Error(err)
		}

		timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
		if timebase.Cmp(big.NewInt(48000)) != 0 {
			t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
		}

		if avpipe.ChannelLayoutName(probeInfo.StreamInfo[0].Channels,
			probeInfo.StreamInfo[0].ChannelLayout) != "stereo" {
			t.Error("Unexpected channel layout", probeInfo.StreamInfo[0].ChannelLayout)
		}
	}
}

// Timebase of BOB923HL_clip_timebase_1001_60000.MXF is 1001/60000
func TestIrregularTsMezMaker_1001_60000(t *testing.T) {
	filename := "./media/BOB923HL_clip_timebase_1001_60000.MXF"
	outputDir := "IrregularTsMezMaker_1001_60000"

	setupLogging()
	log.Info("STARTING TestIrregularTsMezMaker_1001_60000")

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30.03",
		Ecodec:              "libx264",
		Dcodec:              "",
		EncHeight:           720,
		EncWidth:            1280,
		TxType:              avpipe.TxVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         120,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	for i := 1; i <= 4; i++ {
		mezFile := fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i)
		// Now probe the generated files
		probeInfo, err := avpipe.Probe(mezFile, true)
		if err != nil {
			t.Error(err)
		}

		timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
		if timebase.Cmp(big.NewInt(60000)) != 0 {
			t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
		}

		if avpipe.GetPixelFormatName(probeInfo.StreamInfo[0].PixFmt) != "yuv420p" {
			t.Error("Unexpected pixel format", probeInfo.StreamInfo[0].PixFmt)
		}
	}

}

// Timebase of Rigify-2min is 1/24
func TestIrregularTsMezMaker_1_24(t *testing.T) {
	filename := "./media/Rigify-2min.mp4"
	outputDir := "IrregularTsMezMaker_1_24"

	setupLogging()
	log.Info("STARTING TestIrregularTsMezMaker_1_24")

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec:              "libx264",
		Dcodec:              "",
		EncHeight:           720,
		EncWidth:            1280,
		TxType:              avpipe.TxVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	for i := 1; i <= 4; i++ {
		mezFile := fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i)
		// Now probe the generated files
		probeInfo, err := avpipe.Probe(mezFile, true)
		if err != nil {
			t.Error(err)
		}

		timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
		if timebase.Cmp(big.NewInt(12288)) != 0 {
			t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
		}

		if avpipe.GetPixelFormatName(probeInfo.StreamInfo[0].PixFmt) != "yuv420p" {
			t.Error("Unexpected pixel format", probeInfo.StreamInfo[0].PixFmt)
		}
	}

}

// Timebase of Rigify-2min is 1/10000
func TestIrregularTsMezMaker_1_10000(t *testing.T) {
	filename := "./media/Rigify-2min-10000ts.mp4"
	outputDir := "IrregularTsMezMaker_1_10000"

	setupLogging()
	log.Info("STARTING TestIrregularTsMezMaker_1_10000")

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec:              "libx264",
		Dcodec:              "",
		EncHeight:           720,
		EncWidth:            1280,
		TxType:              avpipe.TxVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	for i := 1; i <= 4; i++ {
		mezFile := fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i)
		// Now probe the generated files
		probeInfo, err := avpipe.Probe(mezFile, true)
		if err != nil {
			t.Error(err)
		}

		timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
		if timebase.Cmp(big.NewInt(10000)) != 0 {
			t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
		}

		if avpipe.GetPixelFormatName(probeInfo.StreamInfo[0].PixFmt) != "yuv420p" {
			t.Error("Unexpected pixel format", probeInfo.StreamInfo[0].PixFmt)
		}
	}
}

func TestAVPipeMXF_H265MezMaker(t *testing.T) {
	filename := "./media/across_the_universe_4k_clip_60sec.mxf"
	outputDir := "H265MXF"

	setupLogging()
	log.Info("STARTING TestAVPipeMXF_H265MezMaker")

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
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	mezFile := fmt.Sprintf("%s/vsegment-1.mp4", outputDir)
	// Now probe the generated files
	probeInfo, err := avpipe.Probe(mezFile, true)
	if err != nil {
		t.Error(err)
	}

	timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
	if timebase.Cmp(big.NewInt(24000)) != 0 {
		t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
	}

	pixelFormat := probeInfo.StreamInfo[0].SampleRate
	// 0 means AV_PIX_FMT_YUV420P
	if pixelFormat != 0 {
		t.Error("Unexpected PixelFormat", probeInfo.StreamInfo[0].PixFmt)
	}
}

func TestAVPipeHEVC_H264MezMaker(t *testing.T) {
	filename := "./media/across_the_universe_4k_clip_30sec.mp4"
	outputDir := "HEVC_H264"

	setupLogging()
	log.Info("STARTING TestAVPipeHEVC_H264MezMaker")

	params := &avpipe.TxParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "15.03",
		Ecodec:            "libx264",
		Dcodec:            "hevc",
		EncHeight:         -1,
		EncWidth:          -1,
		TxType:            avpipe.TxVideo,
		StreamId:          -1,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	mezFile := fmt.Sprintf("%s/vsegment-1.mp4", outputDir)
	// Now probe the generated files
	probeInfo, err := avpipe.Probe(mezFile, true)
	if err != nil {
		t.Error(err)
	}

	timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
	if timebase.Cmp(big.NewInt(24000)) != 0 {
		t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
	}

	pixelFormat := probeInfo.StreamInfo[0].SampleRate
	// 0 means AV_PIX_FMT_YUV420P
	if pixelFormat != 0 {
		t.Error("Unexpected PixelFormat", probeInfo.StreamInfo[0].PixFmt)
	}
}

func TestAVPipeHEVC_H265ABRTranscode(t *testing.T) {
	filename := "./media/across_the_universe_4k_clip_30sec.mp4"
	outputDir := "HEVC_H265ABR"

	setupLogging()
	log.Info("STARTING TestAVPipeHEVC_H265ABRTranscode")

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
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}
}

func TestAVPipeStats(t *testing.T) {
	filename := "./media/Rigify-2min.mp4"
	outputDir := "Rigify-2min-stats"

	setupLogging()
	log.Info("STARTING TestAVPipeStats")

	params := &avpipe.TxParams{
		BypassTranscoding:   false,
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec:              "libx264",
		Dcodec:              "",
		Ecodec2:             "aac",
		EncHeight:           720,
		EncWidth:            1280,
		TxType:              avpipe.TxAll,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
	}

	// Create output directory if it doesn't exist
	err := setupOutDir(outputDir)
	if err != nil {
		t.Fail()
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: outputDir})

	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	for i := 1; i <= 4; i++ {
		mezFile := fmt.Sprintf("%s/vsegment-%d.mp4", outputDir, i)
		// Now probe the generated files
		probeInfo, err := avpipe.Probe(mezFile, true)
		if err != nil {
			t.Error(err)
			return
		}

		timebase := *probeInfo.StreamInfo[0].TimeBase.Denom()
		if timebase.Cmp(big.NewInt(12288)) != 0 {
			t.Error("Unexpected TimeBase", probeInfo.StreamInfo[0].TimeBase)
		}

		if avpipe.GetPixelFormatName(probeInfo.StreamInfo[0].PixFmt) != "yuv420p" {
			t.Error("Unexpected pixel format", probeInfo.StreamInfo[0].PixFmt)
		}
	}

	assert.Equal(t, int64(2880), statsInfo.encodingVideoFrameStats.TotalFramesWritten)
	assert.Equal(t, int64(720), statsInfo.encodingVideoFrameStats.FramesWritten)
	assert.Equal(t, int64(5625), statsInfo.encodingAudioFrameStats.TotalFramesWritten)
	assert.Equal(t, int64(1406), statsInfo.encodingAudioFrameStats.FramesWritten)
	assert.Equal(t, uint64(5625), statsInfo.audioFramesRead)
	assert.Equal(t, uint64(2880), statsInfo.videoFramesRead)
}

// This unit test is almost a complete test for mez, abr, muxing and probing. It does:
// 1) Creates audio and video mez files
// 2) Creates ABR segments using audio and video mez files in step 1
// 3) Mux the ABR audio and video segments from step 2
// 4) Probes the initial mez file from step 1 and mux output from step 3. The duration has to be equal.
func TestABRMuxing(t *testing.T) {
	filename := "./media/creed_1_min.mov"
	log.Info("STARTING TestABRMuxing")
	setupLogging()

	videoMezDir := "VideoMez4Muxing"
	audioMezDir := "AudioMez4Muxing"
	videoABRDir := "VideoABR4Muxing"
	audioABRDir := "AudioABR4Muxing"
	muxOutDir := "MuxingOutput"

	// Create video mez files
	err := setupOutDir(videoMezDir)
	if err != nil {
		t.Fail()
	}

	params := &avpipe.TxParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		VideoBitrate:      2560000,
		AudioBitrate:      128000,
		SampleRate:        44100,
		CrfStr:            "23",
		SegDuration:       "30.03",
		Ecodec:            "libx264",
		EncHeight:         720,
		EncWidth:          1280,
		TxType:            avpipe.TxVideo,
		StreamId:          -1,
		NumAudio:          -1,
	}

	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: videoMezDir})
	err = avpipe.Tx(params, filename, true)
	if err != nil {
		t.Fail()
	}

	// Create audio mez files
	err = setupOutDir(audioMezDir)
	if err != nil {
		t.Fail()
	}

	params.TxType = avpipe.TxAudio
	params.Ecodec2 = "aac"

	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: audioMezDir})
	err = avpipe.Tx(params, filename, false)
	if err != nil {
		t.Fail()
	}

	// Create video ABR files
	err = setupOutDir(videoABRDir)
	if err != nil {
		t.Fail()
	}

	filename = videoMezDir + "/vsegment-1.mp4"
	params.TxType = avpipe.TxVideo
	params.Format = "dash"
	params.Ecodec = "libx264"
	params.VideoSegDurationTs = 48000

	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: videoABRDir})
	err = avpipe.Tx(params, filename, true)
	if err != nil {
		t.Fail()
	}

	// Create audio ABR files
	err = setupOutDir(audioABRDir)
	if err != nil {
		t.Fail()
	}

	filename = audioMezDir + "/asegment-1.mp4"
	params.TxType = avpipe.TxAudio
	params.Format = "dash"
	params.Ecodec2 = "aac"
	params.AudioSegDurationTs = 96000

	avpipe.InitUrlIOHandler(filename, &fileInputOpener{url: filename}, &fileOutputOpener{dir: audioABRDir})
	err = avpipe.Tx(params, filename, true)
	if err != nil {
		t.Fail()
	}

	// Create playable file by muxing audio/video segments
	err = setupOutDir(muxOutDir)
	if err != nil {
		t.Fail()
	}

	muxSpec := "abr-mux\n"
	muxSpec += "audio,1," + audioABRDir + "/init-stream0.mp4\n"
	for i := 1; i <= 15; i++ {
		muxSpec += fmt.Sprintf("%s%s%s%02d%s\n", "audio,1,", audioABRDir, "/chunk-stream0-000", i, ".mp4")
	}
	muxSpec += "video,1,VideoABR4Muxing/init-stream0.mp4\n"
	for i := 1; i <= 15; i++ {
		muxSpec += fmt.Sprintf("%s%s%s%02d%s\n", "video,1,", videoABRDir, "/chunk-stream0-000", i, ".mp4")
	}
	filename = muxOutDir + "/segment-1.mp4"
	params.MuxingSpec = muxSpec
	log.Debug("TestABRMuxing", "muxSpec", string(muxSpec))

	avpipe.InitUrlMuxIOHandler(filename, &cmd.AVCmdMuxInputOpener{URL: filename}, &cmd.AVCmdMuxOutputOpener{})

	err = avpipe.Mux(params, filename, true)
	if err != nil {
		t.Fail()
	}

	// Now probe mez video and output file and become sure both have the same duration
	videoMezFile := fmt.Sprintf("%s/vsegment-1.mp4", videoMezDir)
	avpipe.InitIOHandler(&fileInputOpener{url: videoMezFile}, &fileOutputOpener{dir: videoMezDir})
	// Now probe the generated files
	videoMezProbeInfo, err := avpipe.Probe(videoMezFile, true)
	if err != nil {
		t.Error(err)
	}

	muxOutFile := fmt.Sprintf("%s/segment-1.mp4", muxOutDir)
	avpipe.InitIOHandler(&fileInputOpener{url: muxOutFile}, &fileOutputOpener{dir: muxOutDir})
	muxOutProbeInfo, err := avpipe.Probe(muxOutFile, true)
	if err != nil {
		t.Error(err)
	}

	log.Debug("TestABRMuxing", "mezDuration", videoMezProbeInfo.ContainerInfo.Duration, "muxOutDuration", muxOutProbeInfo.ContainerInfo.Duration)
	assert.Equal(t, true, int(videoMezProbeInfo.ContainerInfo.Duration) == int(muxOutProbeInfo.ContainerInfo.Duration))
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
	if err != nil {
		t.Error(err)
	}
	fmt.Println(string(bytes))
	// TODO: Add asserts
}

func TestUnmarshalParams(t *testing.T) {
	var params avpipe.TxParams
	bytes := []byte(`{"video_bitrate":8000000,"seg_duration_ts":180000,"seg_duration_fr":50,"enc_height":720,"enc_width":1280,"tx_type":1}`)
	err := json.Unmarshal(bytes, &params)
	if err != nil {
		t.Error(err)
	}
	if params.TxType != avpipe.TxVideo {
		t.Error("Unexpected TxType", params.TxType)
	}
	// TODO: More checks
}

func TestProbe(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"

	setupLogging()

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &concurrentOutputOpener{dir: "O"})
	probe, err := avpipe.Probe(filename, true)
	assert.NoError(t, err)
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

	setupLogging()

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &concurrentOutputOpener{dir: "O"})
	probe, err := avpipe.Probe(filename, true)
	assert.NoError(t, err)
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

func TestExtractImages(t *testing.T) {
	videoPath := "media/ErsteChristmas.mp4"
	outPath := baseOutPath + "/TestExtractImages"

	setupLogging()
	log.Info("STARTING TestExtractImages")

	p := newTxParams()
	p.Ecodec = "mjpeg"
	p.Format = "image2"
	p.TxType = avpipe.TxExtractImages

	err := setupOutDir(outPath)
	assert.NoError(t, err)

	oo := &fileOutputOpener{dir: outPath}
	avpipe.InitIOHandler(&fileInputOpener{url: videoPath}, oo)

	handle, err := avpipe.TxInit(p, videoPath, true)
	assert.NoError(t, err)
	assert.Equal(t, true, handle > 0)

	err = avpipe.TxRun(handle)
	assert.NoError(t, err)
	assert.NoError(t, oo.err)

	files, err := ioutil.ReadDir(outPath)
	assert.NoError(t, err)
	assert.Equal(t, 10, len(files))
	var sum int
	for _, f := range files {
		pts, err2 := strconv.ParseInt(strings.Split(f.Name(), ".")[0], 10, 32)
		assert.NoError(t, err2)
		sum += int(pts)
	}
	assert.Equal(t, 5760000, sum)
}

// newTxParams modifies parameters for speed
func newTxParams() *avpipe.TxParams {
	p := avpipe.NewTxParams()

	// libx264
	p.CrfStr = "51"
	p.Preset = "ultrafast"

	// aac
	p.AudioBitrate = 64000

	if runtime.GOOS == "darwin" {
		p.Ecodec = "h264_videotoolbox" // half the time of libx264
	}
	return p
}

func removeDirContents(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
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

func setupOutDir(dir string) (err error) {
	if _, err = os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(dir, 0755)
		}
	} else {
		err = removeDirContents(dir)
	}
	return
}
