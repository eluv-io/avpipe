package live

import (
	"fmt"
	"testing"
	"time"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeWithRetry probes params.Url, retrying transient failures.
func probeWithRetry(t *testing.T, params *goavpipe.XcParams) (probeInfo *goavpipe.ProbeInfo, err error) {
	t.Helper()
	for i := 0; i < 3; i++ {
		probeInfo, err = avpipe.Probe(params)
		if err == nil {
			return probeInfo, nil
		}
		tlog.Info("probe attempt failed, retrying", "attempt", i+1, "url", params.Url, "err", err)
		time.Sleep(time.Second)
	}
	return probeInfo, err
}

// requireProbe fails unless the probe succeeded and returned at least minStreams streams.
func requireProbe(t *testing.T, probeInfo *goavpipe.ProbeInfo, err error, minStreams int) {
	t.Helper()
	require.NoError(t, err)
	require.NotNil(t, probeInfo)
	require.GreaterOrEqualf(t, len(probeInfo.Streams), minStreams,
		"probe returned %d streams, expected at least %d", len(probeInfo.Streams), minStreams)
}

// 1) Starts ffmpeg for streaming RTMP in listen mode
// 2) avpipe probe connects to listening ffmpeg and probes the stream
func TestProbeRTMPConnect(t *testing.T) {
	setupLogging()

	liveSource := NewLiveSource()
	defer liveSource.Stop()
	url := fmt.Sprintf(RTMP_SOURCE, liveSource.Port)

	// Start ffmpeg RTMP in listen mode
	err := liveSource.Start("rtmp_listen")
	if err != nil {
		t.Error(err)
	}

	time.Sleep(2 * time.Second)

	XCParams := &goavpipe.XcParams{
		Seekable:        false,
		XcType:          goavpipe.Xcprobe,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}

	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	goavpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	tlog.Info("Probing RTMP stream start", "params", fmt.Sprintf("%+v", *XCParams))
	probeInfo, err := probeWithRetry(t, XCParams)

	requireProbe(t, probeInfo, err, 2)
	assert.Equal(t, "h264", probeInfo.Streams[0].CodecName)
	assert.Equal(t, 1920, probeInfo.Streams[0].Width)
	assert.Equal(t, 1080, probeInfo.Streams[0].Height)
	assert.Equal(t, 578, probeInfo.Streams[0].Profile)
	assert.Equal(t, 40, probeInfo.Streams[0].Level)

	assert.Equal(t, "aac", probeInfo.Streams[1].CodecName)
	assert.Equal(t, int64(55566), probeInfo.Streams[1].BitRate)
	assert.Equal(t, 2, probeInfo.Streams[1].Channels)
	assert.Equal(t, 3, probeInfo.Streams[1].ChannelLayout)

	liveSource.Stop()

}

// 1) Starts avpipe probe to listen for an incoming RTMP stream
// 2) Starts ffmpeg to connect to listening avpipe
func TestProbeRTMPListen(t *testing.T) {
	setupLogging()

	liveSource := NewLiveSource()
	defer liveSource.Stop()
	url := fmt.Sprintf("rtmp://localhost:%d/rtmp/Doj1Nr3S", liveSource.Port)

	XCParams := &goavpipe.XcParams{
		Seekable:          false,
		XcType:            goavpipe.Xcprobe,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
		ConnectionTimeout: 5,
		Listen:            true,
	}

	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	goavpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	done := make(chan bool, 1)
	var probeInfo *goavpipe.ProbeInfo
	var probeErr error

	go func() {
		tlog.Info("Probing RTMP stream start", "params", fmt.Sprintf("%+v", *XCParams))
		probeInfo, probeErr = avpipe.Probe(XCParams)
		done <- true
	}()

	time.Sleep(1 * time.Second)
	err := liveSource.Start("rtmp_connect")
	if err != nil {
		t.Error(err)
	}

	<-done
	requireProbe(t, probeInfo, probeErr, 2)
	tlog.Info("Probe done", "probeInfo", fmt.Sprintf("%+v", *probeInfo))
	assert.Equal(t, "h264", probeInfo.Streams[0].CodecName)
	assert.Equal(t, 1920, probeInfo.Streams[0].Width)
	assert.Equal(t, 1080, probeInfo.Streams[0].Height)
	assert.Equal(t, 578, probeInfo.Streams[0].Profile)
	assert.Equal(t, 40, probeInfo.Streams[0].Level)

	assert.Equal(t, "aac", probeInfo.Streams[1].CodecName)
	assert.Equal(t, int64(55566), probeInfo.Streams[1].BitRate)
	assert.Equal(t, 2, probeInfo.Streams[1].Channels)
	assert.Equal(t, 3, probeInfo.Streams[1].ChannelLayout)

	liveSource.Stop()
}

func TestProbeRTMPNoStream(t *testing.T) {
	setupLogging()

	liveSource := NewLiveSource()
	defer liveSource.Stop()
	url := fmt.Sprintf("rtmp://localhost:%d/rtmp/Doj1Nr3S", liveSource.Port)

	XCParams := &goavpipe.XcParams{
		Seekable:          false,
		XcType:            goavpipe.Xcprobe,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
		ConnectionTimeout: 2,
	}

	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	goavpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	tlog.Info("Probing RTMP stream start", "params", fmt.Sprintf("%+v", *XCParams))
	probeInfo, err := avpipe.Probe(XCParams)

	assert.Error(t, err)
	assert.Equal(t, (*goavpipe.ProbeInfo)(nil), probeInfo)
}

// 1) Starts ffmpeg for streaming UDP MPEGTS
// 2) avpipe probe reads the generated UDP stream and probes the stream
func TestProbeUDPConnect(t *testing.T) {
	setupLogging()

	liveSource := NewLiveSource()
	defer liveSource.Stop()
	url := fmt.Sprintf("udp://127.0.0.1:%d", liveSource.Port)

	// Start ffmpeg UDP MPEGTS
	err := liveSource.Start("udp")
	if err != nil {
		t.Error(err)
	}

	time.Sleep(2 * time.Second)

	XCParams := &goavpipe.XcParams{
		Seekable:          false,
		XcType:            goavpipe.Xcprobe,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
		ConnectionTimeout: 5,
	}

	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	goavpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	tlog.Info("Probing MPEGTS stream start", "params", fmt.Sprintf("%+v", *XCParams))
	probeInfo, err := probeWithRetry(t, XCParams)

	requireProbe(t, probeInfo, err, 2)
	assert.Equal(t, "h264", probeInfo.Streams[0].CodecName)
	assert.Equal(t, 1280, probeInfo.Streams[0].Width)
	assert.Equal(t, 720, probeInfo.Streams[0].Height)
	assert.Equal(t, 100, probeInfo.Streams[0].Profile)
	assert.Equal(t, 32, probeInfo.Streams[0].Level)

	assert.Equal(t, "ac3", probeInfo.Streams[1].CodecName)
	assert.Equal(t, int64(384000), probeInfo.Streams[1].BitRate)
	assert.Equal(t, 6, probeInfo.Streams[1].Channels)
	assert.Equal(t, 1551, probeInfo.Streams[1].ChannelLayout)

	liveSource.Stop()

}

// 1) Starts avpipe probe to read UDP stream and probes the stream
// 2) Starts ffmpeg for streaming UDP MPEGTS
func TestProbeUDPListen(t *testing.T) {

	setupLogging()

	liveSource := NewLiveSource()
	defer liveSource.Stop()
	url := fmt.Sprintf("udp://127.0.0.1:%d", liveSource.Port)

	XCParams := &goavpipe.XcParams{
		Seekable:        false,
		XcType:          goavpipe.Xcprobe,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}

	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	goavpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	done := make(chan bool, 1)
	var probeInfo *goavpipe.ProbeInfo
	var probeErr error

	go func() {
		tlog.Info("Probing MPEGTS stream start", "params", fmt.Sprintf("%+v", *XCParams))
		probeInfo, probeErr = avpipe.Probe(XCParams)
		done <- true
	}()

	// Start ffmpeg UDP MPEGTS
	err := liveSource.Start("udp")
	if err != nil {
		t.Error(err)
	}

	<-done
	requireProbe(t, probeInfo, probeErr, 2)
	assert.Equal(t, "h264", probeInfo.Streams[0].CodecName)
	assert.Equal(t, 1280, probeInfo.Streams[0].Width)
	assert.Equal(t, 720, probeInfo.Streams[0].Height)
	assert.Equal(t, 100, probeInfo.Streams[0].Profile)
	assert.Equal(t, 32, probeInfo.Streams[0].Level)

	assert.Equal(t, "ac3", probeInfo.Streams[1].CodecName)
	assert.Equal(t, int64(384000), probeInfo.Streams[1].BitRate)
	assert.Equal(t, 6, probeInfo.Streams[1].Channels)
	assert.Equal(t, 1551, probeInfo.Streams[1].ChannelLayout)

	liveSource.Stop()
}

func TestProbeUDPNoStream(t *testing.T) {

	setupLogging()

	liveSource := NewLiveSource()
	defer liveSource.Stop()
	url := fmt.Sprintf("udp://127.0.0.1:%d", liveSource.Port)

	XCParams := &goavpipe.XcParams{
		Seekable:          false,
		XcType:            goavpipe.Xcprobe,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
		ConnectionTimeout: 2,
	}

	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	goavpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	tlog.Info("Probing MPEGTS stream start", "params", fmt.Sprintf("%+v", *XCParams))
	probeInfo, err := avpipe.Probe(XCParams)

	assert.Error(t, err)
	assert.Equal(t, (*goavpipe.ProbeInfo)(nil), probeInfo)
}
