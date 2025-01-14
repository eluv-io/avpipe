package live

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"path"
	"testing"
	"time"

	"github.com/eluv-io/avpipe"
)

func TestSrtToMp4(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("srt://localhost:%d?mode=listener&recv_buffer_size=256000&ffs=256000", liveSource.Port)

	done := make(chan bool, 1)
	testComplete := make(chan bool, 1)

	xcParams := &avpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DecodingDurationTs:  -1,
		EncodingDurationTs:  -1,
		StartSegmentStr:     "1",
		AudioBitrate:        256000,
		VideoBitrate:        20000000,
		ForceKeyInt:         60,
		SegDuration:         "30", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              avpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	// Transcode audio mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	go func() {
		tlog.Info("Transcoding SRT stream start", "params", fmt.Sprintf("%+v", *xcParams))
		err := avpipe.Xc(xcParams)
		tlog.Info("Transcoding SRT stream done", "err", err, "last pts", nil)
		if err != nil && err != avpipe.EAV_READ_INPUT {
			t.Error("Transcoding SRT stream failed", "err", err)
		}
		done <- true
	}()

	err := liveSource.Start("srt")
	if err != nil {
		t.Error(err)
	}

	// Wait for the srt recording to be finished
	<-done

	xcParams.Format = "dash"
	xcParams.Dcodec2 = "aac"
	xcParams.AudioSegDurationTs = 96000 // almost 2 * 48000
	xcParams.XcType = avpipe.XcAudio
	audioMezFiles := [2]string{"audio-mez-segment0-1.mp4", "audio-mez-segment0-2.mp4"}

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
	xcParams.XcType = avpipe.XcVideo
	videoMezFiles := [2]string{"video-mez-segment-1.mp4", "video-mez-segment-2.mp4"}

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

// Cancels the SRT live stream transcoding, with no source, immediately after initializing the transcoding (after XcInit).
// This test was hanging with avpipe release-1.15 and before (this is fixed in release-1.16).
func TestSrtToMp4WithCancelling0(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	done := make(chan bool, 1)
	liveSource := NewLiveSource()
	url := fmt.Sprintf("srt://localhost:%d?mode=listener&recv_buffer_size=256000&ffs=256000", liveSource.Port)

	xcParams := &avpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DecodingDurationTs:  -1,
		EncodingDurationTs:  -1,
		StartSegmentStr:     "1",
		AudioBitrate:        256000,
		VideoBitrate:        20000000,
		ForceKeyInt:         60,
		SegDuration:         "30", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              avpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	var handle int32
	var err error
	go func() {
		tlog.Info("Transcoding SRT stream start", "params", fmt.Sprintf("%+v", *xcParams))
		handle, err = avpipe.XcInit(xcParams)
		if err != nil {
			t.Error("XcInit initializing SRT stream failed", "err", err)
		}

		done <- true
	}()

	err = avpipe.XcCancel(handle)
	assert.NoError(t, err)
	if err != nil {
		t.Error("Cancelling SRT stream failed", "err", err, "url", url)
		t.FailNow()
	} else {
		tlog.Info("Cancelling SRT stream completed", "err", err, "url", url)
	}

	<-done
}

// Cancels the SRT live stream transcoding immediately after initializing the transcoding (after XcInit).
func TestSrtToMp4WithCancelling1(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	done := make(chan bool, 1)
	liveSource := NewLiveSource()
	url := fmt.Sprintf("srt://localhost:%d?mode=listener&recv_buffer_size=256000&ffs=256000", liveSource.Port)

	xcParams := &avpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DecodingDurationTs:  -1,
		EncodingDurationTs:  -1,
		StartSegmentStr:     "1",
		AudioBitrate:        256000,
		VideoBitrate:        20000000,
		ForceKeyInt:         60,
		SegDuration:         "30", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              avpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	var handle int32
	var err error
	go func() {
		tlog.Info("Transcoding SRT stream start", "params", fmt.Sprintf("%+v", *xcParams))
		handle, err = avpipe.XcInit(xcParams)
		if err != nil {
			t.Error("XcInit initializing SRT stream failed", "err", err)
		}

		done <- true
	}()

	err = liveSource.Start("srt")
	if err != nil {
		t.Error(err)
	}

	<-done

	err = avpipe.XcCancel(handle)
	assert.NoError(t, err)
	if err != nil {
		t.Error("Cancelling SRT stream failed", "err", err, "url", url)
		t.FailNow()
	} else {
		tlog.Info("Cancelling SRT stream completed", "err", err, "url", url)
	}
}

// Cancels the SRT live stream transcoding immediately after starting the transcoding (1 sec after XcRun).
func TestSrtToMp4WithCancelling2(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("srt://localhost:%d?mode=listener&recv_buffer_size=256000&ffs=256000", liveSource.Port)
	done := make(chan bool, 1)

	xcParams := &avpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DecodingDurationTs:  -1,
		EncodingDurationTs:  -1,
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
		XcType:              avpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	var handle int32
	var err error
	go func() {
		tlog.Info("Transcoding SRT stream start", "params", fmt.Sprintf("%+v", *xcParams))
		handle, err = avpipe.XcInit(xcParams)
		if err != nil {
			t.Error("XcInit initializing SRT stream failed", "err", err)
		}
		err = avpipe.XcRun(handle)
		if err != nil && err != avpipe.EAV_CANCELLED {
			t.Error("Transcoding SRT stream failed", "err", err)
		}
		done <- true
	}()

	err = liveSource.Start("srt")
	if err != nil {
		t.Error(err)
	}

	// Wait 1 second for transcoding to start
	time.Sleep(1 * time.Second)

	err = avpipe.XcCancel(handle)
	assert.NoError(t, err)
	if err != nil {
		t.Error("Cancelling SRT stream failed", "err", err)
		t.FailNow()
	} else {
		tlog.Info("Cancelling SRT stream completed", "err", err)
	}

	<-done
}

// Cancels the SRT live stream transcoding some time after starting the transcoding (20 sec after XcRun).
func TestSrtToMp4WithCancelling3(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("srt://localhost:%d?mode=listener&recv_buffer_size=256000&ffs=256000", liveSource.Port)
	done := make(chan bool, 1)

	xcParams := &avpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DecodingDurationTs:  -1,
		EncodingDurationTs:  -1,
		StartSegmentStr:     "1",
		AudioBitrate:        256000,
		VideoBitrate:        20000000,
		ForceKeyInt:         60,
		SegDuration:         "30", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              avpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	// Transcode audio/video mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	var handle int32
	var err error
	go func() {

		tlog.Info("Transcoding SRT stream start", "params", fmt.Sprintf("%+v", *xcParams))
		handle, err = avpipe.XcInit(xcParams)
		if err != nil {
			t.Error("XcInit initializing SRT stream failed", "err", err)
		}

		done <- true
	}()

	err = liveSource.Start("srt")
	if err != nil {
		t.Error(err)
	}

	<-done

	go func() {
		err := avpipe.XcRun(handle)
		if err != nil && err != avpipe.EAV_CANCELLED {
			t.Error("Transcoding SRT stream failed", "err", err)
		}
		done <- true
	}()

	// Wait 20 second for transcoding to be done
	time.Sleep(20 * time.Second)

	err = avpipe.XcCancel(handle)
	assert.NoError(t, err)
	if err != nil {
		t.Error("Cancelling SRT stream failed", "err", err, "url", url)
		t.FailNow()
	} else {
		tlog.Info("Cancelling SRT stream completed", "err", err, "url", url)
	}

	<-done
}

// Cancels the SRT live stream transcoding immediately 1 sec after starting the transcoding (after XcRun), while there is no source.
func TestSrtToMp4WithCancelling4(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("srt://localhost:%d?mode=listener&recv_buffer_size=256000&ffs=256000", liveSource.Port)
	done := make(chan bool, 1)

	xcParams := &avpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DecodingDurationTs:  -1,
		EncodingDurationTs:  -1,
		StartSegmentStr:     "1",
		AudioBitrate:        256000,
		VideoBitrate:        20000000,
		ForceKeyInt:         60,
		SegDuration:         "30", // seconds
		Dcodec2:             "ac3",
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:           720,       // 1080
		EncWidth:            1280,      // 1920
		XcType:              avpipe.XcAll,
		StreamId:            -1,
		Url:                 url,
		SyncAudioToStreamId: -1,
		DebugFrameLevel:     debugFrameLevel,
	}

	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	var handle int32
	var err error
	go func() {
		tlog.Info("Transcoding SRT stream start", "params", fmt.Sprintf("%+v", *xcParams))

		handle, err = avpipe.XcInit(xcParams)
		if err != nil {
			t.Error("XcInitializing SRT stream failed", "err", err)
		}

		done <- true
	}()

	err = liveSource.Start("srt")
	if err != nil {
		t.Error(err)
	}

	<-done

	go func() {
		err := avpipe.XcRun(handle)
		if err != nil && err != avpipe.EAV_CANCELLED {
			t.Error("Transcoding SRT stream failed", "err", err)
		}
		done <- true
	}()

	time.Sleep(1 * time.Second)
	liveSource.Stop()

	err = avpipe.XcCancel(handle)
	assert.NoError(t, err)
	if err != nil {
		t.Error("Cancelling SRT stream failed", "err", err, "url", url)
		t.FailNow()
	} else {
		tlog.Info("Cancelling SRT stream completed", "err", err, "url", url)
	}

	<-done
}
