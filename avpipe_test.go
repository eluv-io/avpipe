package avpipe_test

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"testing"

	"github.com/qluvio/avpipe"
	log "github.com/qluvio/content-fabric/log"
	"github.com/stretchr/testify/assert"
)

//Implement AVPipeInputOpener
type fileInputOpener struct {
	url string
}

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

//Implement AVPipeOutputOpener
type fileOutputOpener struct {
	dir string
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
		fallthrough
	case avpipe.FMP4Segment:
		filename = fmt.Sprintf("./%s/segment-%d.mp4", oo.dir, seg_index)
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

func TestSingleTranscode(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"

	params := &avpipe.TxParams{
		BypassTranscoding: false,
		Format:            "hls",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		VideoBitrate:      2560000,
		AudioBitrate:      64000,
		SampleRate:        44100,
		CrfStr:            "23",
		SegDurationTs:     1001 * 60,
		Ecodec:            "libx264",
		EncHeight:         720,
		EncWidth:          1280,
		TxType:            avpipe.TxVideo,
	}

	// Create output directory if it doesn't exist
	if _, err := os.Stat("./O"); os.IsNotExist(err) {
		os.Mkdir("./O", 0755)
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: "O"})

	var lastInputPts int64
	err := avpipe.Tx(params, filename, true, &lastInputPts)
	if err != 0 {
		t.Fail()
	}

	params.TxType = avpipe.TxAudio
	params.Ecodec = "aac"
	params.AudioIndex = -1
	lastInputPts = 0
	err = avpipe.Tx(params, filename, false, &lastInputPts)
	if err != 0 {
		t.Fail()
	}

}

func doTranscode(t *testing.T, p *avpipe.TxParams, nThreads int, filename string, reportFailure string) {
	// Create output directory if it doesn't exist
	if _, err := os.Stat("./O"); os.IsNotExist(err) {
		os.Mkdir("./O", 0755)
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &concurrentOutputOpener{dir: "O"})

	done := make(chan struct{})
	for i := 0; i < nThreads; i++ {
		go func(params *avpipe.TxParams, filename string) {
			var lastInputPts int64
			err := avpipe.Tx(params, filename, false, &lastInputPts)
			done <- struct{}{} // Signal the main goroutine
			if err != 0 && reportFailure == "" {
				t.Fail()
			} else if err != 0 {
				fmt.Printf("%s\n", reportFailure)
			}
		}(p, filename)
	}

	for i := 0; i < nThreads; i++ {
		<-done // Wait for background goroutines to finish
	}
}

func TestNvidiaTranscode(t *testing.T) {
	nThreads := 10
	filename := "./media/rocky.mp4"

	params := &avpipe.TxParams{
		Format:          "hls",
		StartTimeTs:     0,
		DurationTs:      -1,
		StartSegmentStr: "1",
		VideoBitrate:    2560000,
		AudioBitrate:    64000,
		SampleRate:      44100,
		CrfStr:          "23",
		SegDurationTs:   1001 * 60,
		Ecodec:          "h264_nvenc",
		EncHeight:       720,
		EncWidth:        1280,
		TxType:          avpipe.TxVideo,
	}

	doTranscode(t, params, nThreads, filename, "H264_NVIDIA encoder might not be enabled or hardware might not be available")
}

func TestConcurrentTranscode(t *testing.T) {
	nThreads := 10
	filename := "./media/rocky.mp4"

	params := &avpipe.TxParams{
		Format:          "hls",
		StartTimeTs:     0,
		DurationTs:      -1,
		StartSegmentStr: "1",
		VideoBitrate:    2560000,
		AudioBitrate:    64000,
		SampleRate:      44100,
		CrfStr:          "23",
		SegDurationTs:   1001 * 60,
		Ecodec:          "libx264",
		EncHeight:       720,
		EncWidth:        1280,
		TxType:          avpipe.TxVideo,
	}

	doTranscode(t, params, nThreads, filename, "")

}

func TestAACMezMaker(t *testing.T) {
	filename := "./media/bond-seg1.aac"

	setupLogging()

	params := &avpipe.TxParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "30",
		Ecodec:            "aac",
		Dcodec:            "aac",
		AudioBitrate:      128000,
		SampleRate:        48000,
		EncHeight:         -1,
		EncWidth:          -1,
		TxType:            avpipe.TxAudio,
	}

	// Create output directory if it doesn't exist
	if _, err := os.Stat("./O"); os.IsNotExist(err) {
		os.Mkdir("./O", 0755)
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: "O"})

	lastInputPts := int64(0)
	avpipe.Tx(params, filename, false, &lastInputPts)

	// Now probe the generated files
	probeInfo, err := avpipe.Probe("O/segment-1.mp4", true)
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

func TestAC3TsMezMaker(t *testing.T) {
	filename := "./media/FS1-19-10-14.ts"

	setupLogging()

	params := &avpipe.TxParams{
		BypassTranscoding: false,
		Format:            "fmp4-segment",
		StartTimeTs:       0,
		DurationTs:        -1,
		StartSegmentStr:   "1",
		SegDuration:       "30",
		Ecodec:            "ac3",
		Dcodec:            "ac3",
		AudioBitrate:      128000,
		SampleRate:        48000,
		EncHeight:         -1,
		EncWidth:          -1,
		TxType:            avpipe.TxAudio,
		AudioIndex:        1,
	}

	// Create output directory if it doesn't exist
	if _, err := os.Stat("./O"); os.IsNotExist(err) {
		os.Mkdir("./O", 0755)
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: "O"})

	lastInputPts := int64(0)
	avpipe.Tx(params, filename, false, &lastInputPts)

	// Now probe the generated files
	probeInfo, err := avpipe.Probe("O/segment-1.mp4", true)
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

func TestMarshalParams(t *testing.T) {
	params := &avpipe.TxParams{
		VideoBitrate:  8000000,
		SegDurationTs: 180000,
		EncHeight:     720,
		EncWidth:      1280,
		TxType:        avpipe.TxVideo,
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

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &concurrentOutputOpener{dir: "O"})
	probe, err := avpipe.Probe(filename, true)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(probe.StreamInfo))

	assert.Equal(t, 27, probe.StreamInfo[0].CodecID)
	assert.Equal(t, "h264", probe.StreamInfo[0].CodecName)
	assert.Equal(t, int64(2428), probe.StreamInfo[0].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[0].StartTime)
	assert.Equal(t, int64(506151), probe.StreamInfo[0].BitRate)
	assert.Equal(t, 1280, probe.StreamInfo[0].Width)
	assert.Equal(t, 720, probe.StreamInfo[0].Height)
	assert.Equal(t, int64(12800), probe.StreamInfo[0].TimeBase.Denom().Int64())

	assert.Equal(t, 86018, probe.StreamInfo[1].CodecID)
	assert.Equal(t, "aac", probe.StreamInfo[1].CodecName)
	assert.Equal(t, int64(4183), probe.StreamInfo[1].NBFrames)
	assert.Equal(t, int64(0), probe.StreamInfo[1].StartTime)
	assert.Equal(t, int64(127999), probe.StreamInfo[1].BitRate)
	assert.Equal(t, 0, probe.StreamInfo[1].Width)
	assert.Equal(t, 0, probe.StreamInfo[1].Height)
	assert.Equal(t, int64(44100), probe.StreamInfo[1].TimeBase.Denom().Int64())
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
