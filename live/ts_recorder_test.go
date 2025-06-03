package live

import (
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
)

func TestUdpToMp4(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)

	done := make(chan bool, 1)
	testComplete := make(chan bool, 1)

	err := liveSource.Start("udp")
	if err != nil {
		t.Error(err)
	}

	xcParams := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		AudioBitrate:        384000,
		VideoBitrate:        20000000,
		ForceKeyInt:         120,
		SegDuration:         "30.03", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              goavpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	// Transcode audio mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *xcParams))
	err = avpipe.Xc(xcParams)
	tlog.Info("Transcoding UDP stream done", "err", err, "last pts", nil)
	if err != nil && err != avpipe.EAV_IO_TIMEOUT {
		t.Error("Transcoding UDP stream failed", "err", err)
	}

	xcParams.Format = "dash"
	xcParams.Dcodec2 = "aac"
	xcParams.AudioSegDurationTs = 96106 // almost 2 * 48000
	xcParams.XcType = goavpipe.XcAudio
	audioMezFiles := [3]string{"audio-mez-segment0-1.mp4", "audio-mez-segment0-2.mp4", "audio-mez-segment0-3.mp4"}

	// Now create audio dash segments out of audio mezzanines
	go func() {
		for i, url := range audioMezFiles {
			xcParams.Url = outputDir + "/" + url
			tlog.Info("Transcoding Audio Dash start", "audioParams", fmt.Sprintf("%+v", *xcParams), "url", xcParams.Url)
			reqCtx := &testCtx{url: xcParams.Url}
			putReqCtxByURL(xcParams.Url, reqCtx)
			xcParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Xc(xcParams)
			tlog.Info("Transcoding Audio Dash done", "err", err)
			if err != nil {
				t.Error("Transcoding Audio Dash failed", "err", err, "url", xcParams.Url)
			}
			done <- true
		}
	}()

	for _ = range audioMezFiles {
		<-done
	}

	xcParams.Format = "dash"
	xcParams.VideoSegDurationTs = 180000 // almost 2 * 90000
	xcParams.XcType = goavpipe.XcVideo
	videoMezFiles := [3]string{"video-mez-segment-1.mp4", "video-mez-segment-2.mp4", "video-mez-segment-3.mp4"}

	// Now create video dash segments out of audio mezzanines
	go func() {
		for i, url := range videoMezFiles {
			xcParams.Url = outputDir + "/" + url
			tlog.Info("AVL Video Dash transcoding start", "videoParams", fmt.Sprintf("%+v", *xcParams), "url", xcParams.Url)
			reqCtx := &testCtx{url: xcParams.Url}
			putReqCtxByURL(xcParams.Url, reqCtx)
			xcParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Xc(xcParams)
			tlog.Info("Transcoding Video Dash done", "err", err)
			if err != nil {
				t.Error("Transcoding Video Dash failed", "err", err, "url", xcParams.Url)
			}
			done <- true
		}
	}()

	for _ = range videoMezFiles {
		<-done
	}

	testComplete <- true
}

func TestMultiAudioUdpToMp4(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)

	err := liveSource.Start("multi_audio_udp")
	if err != nil {
		t.Error(err)
	}

	xcParams := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		AudioBitrate:        384000,
		VideoBitrate:        20000000,
		ForceKeyInt:         60,
		VideoSegDurationTs:  2700000,
		AudioSegDurationTs:  1428480,
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              goavpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	xcParams.AudioIndex = []int32{1, 2, 3}

	// Transcode audio mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream multi audio start", "params", fmt.Sprintf("%+v", *xcParams))
	err = avpipe.Xc(xcParams)
	tlog.Info("Transcoding UDP stream multi audio done", "err", err, "last pts", nil)
	if err != nil {
		t.Error("Transcoding UDP stream multi audio failed", "err", err)
	}

	done := make(chan bool, 1)

	xcParams.AudioIndex = []int32{0}
	xcParams.Format = "dash"
	xcParams.Dcodec2 = "aac"
	xcParams.AudioSegDurationTs = 96106 // almost 2 * 48000
	xcParams.XcType = goavpipe.XcAudio
	audioMezFiles := [3]string{"audio-mez-segment1-1.mp4", "audio-mez-segment1-2.mp4", "audio-mez-segment1-3.mp4"}

	// Now create audio dash segments out of audio mezzanines
	go func() {
		for i, url := range audioMezFiles {
			xcParams.Url = outputDir + "/" + url
			tlog.Info("Transcoding Audio Dash start", "audioParams", fmt.Sprintf("%+v", *xcParams), "url", xcParams.Url)
			reqCtx := &testCtx{url: xcParams.Url}
			putReqCtxByURL(xcParams.Url, reqCtx)
			xcParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Xc(xcParams)
			tlog.Info("Transcoding Audio Dash done", "err", err)
			if err != nil {
				t.Error("Transcoding Audio Dash failed", "err", err, "url", xcParams.Url)
			}
			done <- true
		}
	}()

	for _ = range audioMezFiles {
		<-done
	}
}

// Cancels the live stream transcoding immediately after initializing the transcoding (after XcInit).
func TestUdpToMp4WithCancelling1(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)

	err := liveSource.Start("udp")
	if err != nil {
		t.Error(err)
	}

	xcParams := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		AudioBitrate:        384000,
		VideoBitrate:        20000000,
		ForceKeyInt:         120,
		SegDuration:         "30.03", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              goavpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *xcParams))
	handle, err := avpipe.XcInit(xcParams)
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
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)
	done := make(chan bool, 1)

	err := liveSource.Start("udp")
	if err != nil {
		t.Error(err)
	}

	xcParams := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		AudioBitrate:        384000,   // FS1-19-10-14.ts audio bitrate
		VideoBitrate:        20000000, // fox stream bitrate
		ForceKeyInt:         120,
		SegDuration:         "30.03", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              goavpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *xcParams))
	handle, err := avpipe.XcInit(xcParams)
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
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)
	done := make(chan bool, 1)

	err := liveSource.Start("udp")
	if err != nil {
		t.Error(err)
	}

	xcParams := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		AudioBitrate:        384000,
		VideoBitrate:        20000000,
		ForceKeyInt:         120,
		SegDuration:         "30.03", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              goavpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *xcParams))
	handle, err := avpipe.XcInit(xcParams)
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
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)
	done := make(chan bool, 1)

	err := liveSource.Start("udp")
	if err != nil {
		t.Error(err)
	}

	xcParams := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		AudioBitrate:        384000,
		VideoBitrate:        20000000,
		ForceKeyInt:         120,
		SegDuration:         "30.03", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              goavpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})
	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *xcParams))

	handle, err := avpipe.XcInit(xcParams)
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
