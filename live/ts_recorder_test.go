package live

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
)

func TestUdpToMp4(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow live stream test in short mode")
	}
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://127.0.0.1:%d", liveSource.Port)

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

	goavpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

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

// This test ingests a real-time-paced UDP MPEG-TS source, which is subject to
// genuine UDP packet loss (confirmed via /proc/net/snmp RcvbufErrors during repeated local runs).
// avpipe has a documented recovery path for this (avpipe_xc.c's "GAP detected"/"AUDIO GAP detected"
// logging.  When frames are lost, the next surviving frame's muxed sample duration spans
// the gap, i.e. (missing_frames+1)*normal_duration.  With no guarantee against packet loss,
// asserting zero duration deviation on a live UDP source conflates "ingest survived and stayed
// correctly ordered" with "no packet was lost"
//
// Across 25 local runs (7 failures under the strict assert.Equal(0, ...)
// check), every reported duration deviation - video and audio - matched a
// logged GAP/AUDIO GAP entry exactly, including two runs
// with the identical measured UDP drop count where one passed and one
// failed (loss location, not loss count, determines impact - a drop that
// clips a reference frame cascades to every frame depending on it until the
// next IDR; a drop in disposable data is invisible). These tolerances are
// calibrated from that data: normal runs saw 0-2 duration problems per file
// (worst observed under load was higher; 3 leaves headroom), and the largest
// single gap was 14 missing video frames / 9 missing audio frames (a factor
// of 40 is a circuit breaker for something qualitatively worse, not a
// realistic live-loss number).
const (
	maxDurProblemsPerFile = 3
	maxDurDeviationFactor = 40
)

// isDurationTolerable is the pure decision behind requireTolerableDuration,
// factored out so the boundary logic can be unit tested (TestDurationToleranceBoundaries
// below) without a failing case ever marking a real *testing.T failed.
func isDurationTolerable(result *xc.ABRSegmentResult) (ok bool, reason string) {
	if result.DtsProblems != 0 {
		return false, fmt.Sprintf("%d DTS problems", result.DtsProblems)
	}
	if result.DurProblems > maxDurProblemsPerFile {
		return false, fmt.Sprintf("%d duration problems exceeds tolerance of %d", result.DurProblems, maxDurProblemsPerFile)
	}
	if result.MaxDurDeviationFactor > maxDurDeviationFactor {
		return false, fmt.Sprintf("a single duration gap (%dx normal) exceeds tolerance of %dx",
			result.MaxDurDeviationFactor, maxDurDeviationFactor)
	}
	return true, ""
}

// requireTolerableDuration asserts DTS continuity exactly (packet loss does not
// break it - see comment above), and DurProblems/MaxDurDeviationFactor within
// the tolerances documented above rather than requiring exact zero.
func requireTolerableDuration(t *testing.T, result *xc.ABRSegmentResult, filename string) {
	t.Helper()
	ok, reason := isDurationTolerable(result)
	assert.Truef(t, ok, "%s: %s: %v", filename, reason, result.Errors)
}

// TestDurationToleranceBoundaries proves isDurationTolerable still rejects
// cases it's supposed to reject - it's easy for a tolerance to accidentally
// tolerate everything. Synthetic ABRSegmentResult values, no live source, no
// UDP, runs in milliseconds; calls the pure predicate directly (not through
// requireTolerableDuration) so an intentionally-bad case can't mark this test
// itself failed - only a wrong pass/fail verdict does.
func TestDurationToleranceBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		result   *xc.ABRSegmentResult
		wantPass bool
	}{
		{"clean", &xc.ABRSegmentResult{}, true},
		{"at DurProblems tolerance", &xc.ABRSegmentResult{DurProblems: maxDurProblemsPerFile, MaxDurDeviationFactor: 2}, true},
		{"one over DurProblems tolerance", &xc.ABRSegmentResult{DurProblems: maxDurProblemsPerFile + 1, MaxDurDeviationFactor: 2}, false},
		{"at gap-factor tolerance", &xc.ABRSegmentResult{DurProblems: 1, MaxDurDeviationFactor: maxDurDeviationFactor}, true},
		{"one over gap-factor tolerance", &xc.ABRSegmentResult{DurProblems: 1, MaxDurDeviationFactor: maxDurDeviationFactor + 1}, false},
		{"any DTS problem still fails, regardless of duration tolerance", &xc.ABRSegmentResult{DtsProblems: 1}, false},
	}
	for _, c := range cases {
		ok, reason := isDurationTolerable(c.result)
		if ok != c.wantPass {
			t.Errorf("case %q: isDurationTolerable pass=%v (reason=%q), want pass=%v", c.name, ok, reason, c.wantPass)
		}
	}
}

func TestMultiAudioUdpToMp4(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow live stream test in short mode")
	}
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	liveSource := NewLiveSource()
	defer liveSource.Stop()
	url := fmt.Sprintf("udp://127.0.0.1:%d", liveSource.Port)

	err := liveSource.Start("multi_audio_udp")
	if err != nil {
		t.Fatal(err)
	}

	const liveTimeout = 6 * time.Minute
	liveStart := time.Now()
	timeout := time.After(liveTimeout)

	xcParams := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		Seekable:            false,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		AudioBitrate:        384000,
		VideoBitrate:        20000000,
		ForceKeyInt:         48,
		VideoSegDurationTs:  2700000,
		AudioSegDurationTs:  1428480,
		Ecodec2:             "aac",     // "aac"
		Ecodec:              "libx264", // libx264 software / h264_videotoolbox mac hardware
		Preset:              "veryfast",
		EncHeight:           720,  // 1080
		EncWidth:            1280, // 1920
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

	goavpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	// Run first Xc in a goroutine so we can enforce a timeout
	xcDone := make(chan error, 1)
	go func() {
		tlog.Info("Transcoding UDP stream multi audio start", "params", fmt.Sprintf("%+v", *xcParams))
		xcDone <- avpipe.Xc(xcParams)
	}()

	var liveElapsed time.Duration
	select {
	case err = <-xcDone:
		liveElapsed = time.Since(liveStart)
		tlog.Info("Transcoding UDP stream multi audio done", "err", err, "elapsed", liveElapsed)
		t.Logf("live UDP transcode finished in %v (floor is ~118 s: asset + 5 s idle timeout; longer means a backlog was still draining)", liveElapsed)
		assert.Equal(t, avpipe.EAV_IO_TIMEOUT, err, "expected EAV_IO_TIMEOUT when UDP source ends")
	case <-timeout:
		t.Fatalf("Transcoding UDP stream multi audio timed out after %v", liveTimeout)
	}

	// Verify video mez parts
	videoMezFiles, err := filepath.Glob(filepath.Join(outputDir, "video-mez-segment-*.mp4"))
	assert.NoError(t, err)
	sort.Strings(videoMezFiles)
	assert.NotEmpty(t, videoMezFiles, "no video mez segments produced")
	tlog.Info("Video mez segments", "count", len(videoMezFiles))

	for i, f := range videoMezFiles {
		result, vErr := xc.ValidateABRSegment(f)
		assert.NoError(t, vErr, "ValidateABRSegment failed for %s", f)
		if vErr != nil {
			continue
		}
		isLast := i == len(videoMezFiles)-1
		tlog.Info("Video mez validation", "file", filepath.Base(f),
			"frames", result.FrameCount, "timescale", result.Timescale,
			"sample_dur", result.SampleDur, "dts_start", result.DtsStart,
			"dts_end", result.DtsEnd, "last", isLast,
			"dur_problems", result.DurProblems, "max_dur_deviation_factor", result.MaxDurDeviationFactor)
		requireTolerableDuration(t, result, f)
		if !isLast {
			// All non-last parts should have the expected duration
			expectedDurTs := uint64(xcParams.VideoSegDurationTs)
			actualDurTs := result.DtsEnd - result.DtsStart
			assert.Equal(t, expectedDurTs, actualDurTs,
				"video mez duration mismatch for %s: expected %d got %d", filepath.Base(f), expectedDurTs, actualDurTs)
		}
	}

	// Verify audio mez parts for each audio stream (output uses 0-based indices)
	for _, streamIdx := range []int{0, 1, 2} {
		audioFiles, err := filepath.Glob(filepath.Join(outputDir, fmt.Sprintf("audio-mez-segment%d-*.mp4", streamIdx)))
		assert.NoError(t, err)
		sort.Strings(audioFiles)
		assert.NotEmpty(t, audioFiles, "no audio mez segments for stream %d", streamIdx)
		tlog.Info("Audio mez segments", "stream", streamIdx, "count", len(audioFiles))

		for i, f := range audioFiles {
			result, vErr := xc.ValidateABRSegment(f)
			assert.NoError(t, vErr, "ValidateABRSegment failed for %s", f)
			if vErr != nil {
				continue
			}
			isLast := i == len(audioFiles)-1
			tlog.Info("Audio mez validation", "file", filepath.Base(f),
				"frames", result.FrameCount, "timescale", result.Timescale,
				"sample_dur", result.SampleDur, "dts_start", result.DtsStart,
				"dts_end", result.DtsEnd, "last", isLast,
				"dur_problems", result.DurProblems, "max_dur_deviation_factor", result.MaxDurDeviationFactor)
			requireTolerableDuration(t, result, f)
			if !isLast {
				expectedDurTs := uint64(xcParams.AudioSegDurationTs)
				actualDurTs := result.DtsEnd - result.DtsStart
				assert.Equal(t, expectedDurTs, actualDurTs,
					"audio mez duration mismatch for %s: expected %d got %d", filepath.Base(f), expectedDurTs, actualDurTs)
			}
		}
	}

	xcParams.AudioIndex = []int32{0}
	xcParams.Format = "dash"
	xcParams.Dcodec2 = "aac"
	xcParams.AudioSegDurationTs = 96106 // almost 2 * 48000
	xcParams.XcType = goavpipe.XcAudio
	audioMezFiles := [3]string{"audio-mez-segment1-1.mp4", "audio-mez-segment1-2.mp4", "audio-mez-segment1-3.mp4"}

	// Buffered for every file so the worker can never block on send after a
	// timeout has abandoned this loop.
	done := make(chan bool, len(audioMezFiles))

	// Now create audio dash segments out of audio mezzanines
	go func() {
		for i, url := range audioMezFiles {
			xcParams.Url = outputDir + "/" + url
			tlog.Info("Transcoding Audio Dash start", "audioParams", fmt.Sprintf("%+v", *xcParams), "url", xcParams.Url)
			reqCtx := &testCtx{url: xcParams.Url}
			putReqCtxByURL(xcParams.Url, reqCtx)
			xcParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			start := time.Now()
			err := avpipe.Xc(xcParams)
			tlog.Info("Transcoding Audio Dash done", "err", err, "url", xcParams.Url, "elapsed", time.Since(start))
			t.Logf("audio dash %s finished in %v", url, time.Since(start))
			if err != nil {
				t.Error("Transcoding Audio Dash failed", "err", err, "url", xcParams.Url)
			}
			done <- true
		}
	}()

	// These are file-based, not source-paced, so they get their own timer.
	const dashTimeout = 2 * time.Minute
	dashTimer := time.After(dashTimeout)
	for completed := 0; completed < len(audioMezFiles); completed++ {
		select {
		case <-done:
		case <-dashTimer:
			t.Fatalf("Transcoding Audio Dash timed out after %v (%d of %d done; live phase took %v)",
				dashTimeout, completed, len(audioMezFiles), liveElapsed)
		}
	}
}

// Cancels the live stream transcoding immediately after initializing the transcoding (after XcInit).
func TestUdpToMp4WithCancelling1(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://127.0.0.1:%d", liveSource.Port)

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

	goavpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

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
	err = runAndFiniXc(handle)
	assert.Equal(t, avpipe.EAV_CANCELLED, err)
	// A job cancelled before XcRun should never open its input
	assert.Equal(t, 0, reqCtx.openCount, "input was opened despite cancel before XcRun")
}

// Cancels the live stream transcoding immediately after starting the transcoding (1 sec after XcRun).
func TestUdpToMp4WithCancelling2(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://127.0.0.1:%d", liveSource.Port)
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

	goavpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *xcParams))
	handle, err := avpipe.XcInit(xcParams)
	if err != nil {
		t.Error("XcInitializing UDP stream failed", "err", err)
	}
	go func() {
		err := runAndFiniXc(handle)
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
	// Control for TestUdpToMp4WithCancelling1's openCount == 0: a run that did start opens the input once.
	assert.Equal(t, 1, reqCtx.openCount, "input open count")
}

// Cancels the live stream transcoding some time after starting the transcoding (20 sec after XcRun).
func TestUdpToMp4WithCancelling3(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow live stream test in short mode")
	}
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	log.Info("STARTING " + outputDir)

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://127.0.0.1:%d", liveSource.Port)
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

	goavpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *xcParams))
	handle, err := avpipe.XcInit(xcParams)
	if err != nil {
		t.Error("XcInitializing UDP stream failed", "err", err)
	}
	go func() {
		err := runAndFiniXc(handle)
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
	url := fmt.Sprintf("udp://127.0.0.1:%d", liveSource.Port)
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

	goavpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})
	tlog.Info("Transcoding UDP stream start", "params", fmt.Sprintf("%+v", *xcParams))

	handle, err := avpipe.XcInit(xcParams)
	if err != nil {
		t.Error("XcInitializing UDP stream failed", "err", err)
	}

	go func() {
		err := runAndFiniXc(handle)
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
