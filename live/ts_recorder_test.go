package live

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"os"
	"testing"
	"time"

	"github.com/eluv-io/avpipe"
)

func TestUdpToMp4(t *testing.T) {
	setupLogging()
	outputDir := "TestUdpToMp4"

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)

	done := make(chan bool, 1)
	testComplet := make(chan bool, 1)

	err := liveSource.Start()
	if err != nil {
		t.Error(err)
	}

	XCParams := &avpipe.XcParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		AudioBitrate:    384000,   // FS1-19-10-14.ts audio bitrate
		VideoBitrate:    20000000, // fox stream bitrate
		ForceKeyInt:     120,
		SegDuration:     "30.03", // seconds
		Dcodec2:         "ac3",
		Ecodec2:         "aac",     // "aac"
		Ecodec:          "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:       720,       // 1080
		EncWidth:        1280,      // 1920
		XcType:          avpipe.XcAll,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}

	// Transcode audio mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *XCParams))
	err = avpipe.Xc(XCParams)
	tlog.Info("Transcoding UDP stream done", "err", err, "last pts", nil)
	if err != nil {
		t.Error("Transcoding UDP stream failed", "err", err)
	}

	XCParams.Format = "dash"
	XCParams.Dcodec2 = "aac"
	XCParams.AudioSegDurationTs = 96106 // almost 2 * 48000
	XCParams.XcType = avpipe.XcAudio
	audioMezFiles := [3]string{"audio-mez-udp-segment-1.mp4", "audio-mez-udp-segment-2.mp4", "audio-mez-udp-segment-3.mp4"}

	// Now create audio dash segments out of audio mezzanines
	go func() {
		for i, url := range audioMezFiles {
			tlog.Info("Transcoding Audio Dash start", "audioParams", fmt.Sprintf("%+v", *XCParams), "url", url)
			reqCtx := &testCtx{url: url}
			XCParams.Url = url
			putReqCtxByURL(url, reqCtx)
			XCParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Xc(XCParams)
			tlog.Info("Transcoding Audio Dash done", "err", err)
			if err != nil {
				t.Error("Transcoding Audio Dash failed", "err", err, "url", url)
			}
			done <- true
		}
	}()

	for _ = range audioMezFiles {
		<-done
	}

	XCParams.Format = "dash"
	XCParams.VideoSegDurationTs = 180000 // almost 2 * 90000
	XCParams.XcType = avpipe.XcVideo
	videoMezFiles := [3]string{"video-mez-udp-segment-1.mp4", "video-mez-udp-segment-2.mp4", "video-mez-udp-segment-3.mp4"}

	// Now create video dash segments out of audio mezzanines
	go func() {
		for i, url := range videoMezFiles {
			tlog.Info("AVL Video Dash transcoding start", "videoParams", fmt.Sprintf("%+v", *XCParams), "url", url)
			reqCtx := &testCtx{url: url}
			XCParams.Url = url
			putReqCtxByURL(url, reqCtx)
			XCParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Xc(XCParams)
			tlog.Info("Transcoding Video Dash done", "err", err)
			if err != nil {
				t.Error("Transcoding Video Dash failed", "err", err, "url", url)
			}
			done <- true
		}
	}()

	for _ = range videoMezFiles {
		<-done
	}

	testComplet <- true
}

// Cancels the live stream transcoding immediately after initializing the transcoding (after XcInit).
func TestUdpToMp4WithCancelling1(t *testing.T) {
	setupLogging()
	outputDir := "TestUdpToMp4WithCancelling1"
	log.Info("STARTING " + outputDir)

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)

	err := liveSource.Start()
	if err != nil {
		t.Error(err)
	}

	XCParams := &avpipe.XcParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		AudioBitrate:    384000,   // FS1-19-10-14.ts audio bitrate
		VideoBitrate:    20000000, // fox stream bitrate
		ForceKeyInt:     120,
		SegDuration:     "30.03", // seconds
		Dcodec2:         "ac3",
		Ecodec2:         "aac",     // "aac"
		Ecodec:          "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:       720,       // 1080
		EncWidth:        1280,      // 1920
		XcType:          avpipe.XcAll,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *XCParams))
	handle, err := avpipe.XcInit(XCParams)
	if err != nil {
		t.Error("XcInitializing UDP stream failed", "err", err)
	}
	err = avpipe.XcCancel(handle)
	assert.NoError(t, err)
	if err != nil {
		t.Error("Cancelling UDP stream failed", "err", err, "url", url)
		t.FailNow()
	} else {
		tlog.Info("Cancelling UDP stream completed", "err", err, "url", url)
	}
}

// Cancels the live stream transcoding immediately after starting the transcoding (1 sec after XcRun).
func TestUdpToMp4WithCancelling2(t *testing.T) {
	setupLogging()
	outputDir := "TestUdpToMp4WithCancelling2"
	log.Info("STARTING " + outputDir)

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)
	done := make(chan bool, 1)

	err := liveSource.Start()
	if err != nil {
		t.Error(err)
	}

	XCParams := &avpipe.XcParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		AudioBitrate:    384000,   // FS1-19-10-14.ts audio bitrate
		VideoBitrate:    20000000, // fox stream bitrate
		ForceKeyInt:     120,
		SegDuration:     "30.03", // seconds
		Dcodec2:         "ac3",
		Ecodec2:         "aac",     // "aac"
		Ecodec:          "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:       720,       // 1080
		EncWidth:        1280,      // 1920
		XcType:          avpipe.XcAll,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *XCParams))
	handle, err := avpipe.XcInit(XCParams)
	if err != nil {
		t.Error("XcInitializing UDP stream failed", "err", err)
	}
	go func() {
		err := avpipe.XcRun(handle)
		if err != nil && err != avpipe.EAV_CANCELLED {
			t.Error("Transcoding UDP stream failed", "err", err)
		}
		done <- true
	}()

	// Wait 1 second for transcoding to start
	time.Sleep(1 * time.Second)

	err = avpipe.XcCancel(handle)
	assert.NoError(t, err)
	if err != nil {
		t.Error("Cancelling UDP stream failed", "err", err)
		t.FailNow()
	} else {
		tlog.Info("Cancelling UDP stream completed", "err", err)
	}

	<-done
}

// Cancels the live stream transcoding some time after starting the transcoding (20 sec after XcRun).
func TestUdpToMp4WithCancelling3(t *testing.T) {
	setupLogging()
	outputDir := "TestUdpToMp4WithCancelling3"
	log.Info("STARTING " + outputDir)

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)
	done := make(chan bool, 1)

	err := liveSource.Start()
	if err != nil {
		t.Error(err)
	}

	XCParams := &avpipe.XcParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		AudioBitrate:    384000,   // FS1-19-10-14.ts audio bitrate
		VideoBitrate:    20000000, // fox stream bitrate
		ForceKeyInt:     120,
		SegDuration:     "30.03", // seconds
		Dcodec2:         "ac3",
		Ecodec2:         "aac",     // "aac"
		Ecodec:          "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:       720,       // 1080
		EncWidth:        1280,      // 1920
		XcType:          avpipe.XcAll,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *XCParams))
	handle, err := avpipe.XcInit(XCParams)
	if err != nil {
		t.Error("XcInitializing UDP stream failed", "err", err)
	}
	go func() {
		err := avpipe.XcRun(handle)
		if err != nil && err != avpipe.EAV_CANCELLED {
			t.Error("Transcoding UDP stream failed", "err", err)
		}
		done <- true
	}()

	// Wait 20 second for transcoding to start
	time.Sleep(20 * time.Second)

	err = avpipe.XcCancel(handle)
	assert.NoError(t, err)
	if err != nil {
		t.Error("Cancelling UDP stream failed", "err", err, "url", url)
		t.FailNow()
	} else {
		tlog.Info("Cancelling UDP stream completed", "err", err, "url", url)
	}

	<-done
}

// Cancels the live stream transcoding immediately 1 sec after starting the transcoding (after XcRun), while there is no source.
func TestUdpToMp4WithCancelling4(t *testing.T) {
	setupLogging()
	outputDir := "TestUdpToMp4WithCancelling4"
	log.Info("STARTING " + outputDir)

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)
	done := make(chan bool, 1)

	err := liveSource.Start()
	if err != nil {
		t.Error(err)
	}

	XCParams := &avpipe.XcParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		AudioBitrate:    384000,   // FS1-19-10-14.ts audio bitrate
		VideoBitrate:    20000000, // fox stream bitrate
		ForceKeyInt:     120,
		SegDuration:     "30.03", // seconds
		Dcodec2:         "ac3",
		Ecodec2:         "aac",     // "aac"
		Ecodec:          "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:       720,       // 1080
		EncWidth:        1280,      // 1920
		XcType:          avpipe.XcAll,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}

	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})
	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *XCParams))

	handle, err := avpipe.XcInit(XCParams)
	if err != nil {
		t.Error("XcInitializing UDP stream failed", "err", err)
	}

	go func() {
		err := avpipe.XcRun(handle)
		if err != nil && err != avpipe.EAV_CANCELLED {
			t.Error("Transcoding UDP stream failed", "err", err)
		}
		done <- true
	}()

	time.Sleep(1 * time.Second)
	liveSource.Stop()

	err = avpipe.XcCancel(handle)
	assert.NoError(t, err)
	if err != nil {
		t.Error("Cancelling UDP stream failed", "err", err, "url", url)
		t.FailNow()
	} else {
		tlog.Info("Cancelling UDP stream completed", "err", err, "url", url)
	}

	<-done
}
