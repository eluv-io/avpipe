package live

import (
	"fmt"
	"github.com/eluv-io/avpipe"
	"github.com/stretchr/testify/assert"
	"os"
	"testing"
	"time"
)

// 1) Starts ffmpeg for streaming RTMP in listen mode
// 2) avpipe probe connects to listening ffmpeg and probes the stream
func TestProbeRTMPConnect(t *testing.T) {
	setupLogging()
	outputDir := "TestProbeRTMPConnect"

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("rtmp://localhost:%d/rtmp/Doj1Nr3S", liveSource.Port)

	// Start ffmpeg RTMP in listen mode
	err := liveSource.Start("rtmp_listen")
	if err != nil {
		t.Error(err)
	}

	time.Sleep(2 * time.Second)

	XCParams := &avpipe.XcParams{
		Seekable:        false,
		XcType:          avpipe.Xcprobe,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}

	// Transcode audio mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Probing RTMP stream start", "params", fmt.Sprintf("%+v", *XCParams))
	probeInfo, err := avpipe.Probe(XCParams)

	assert.NoError(t, err)
	assert.Equal(t, "h264", probeInfo.StreamInfo[0].CodecName)
	assert.Equal(t, 1920, probeInfo.StreamInfo[0].Width)
	assert.Equal(t, 1080, probeInfo.StreamInfo[0].Height)
	assert.Equal(t, 100, probeInfo.StreamInfo[0].Profile)
	assert.Equal(t, 40, probeInfo.StreamInfo[0].Level)

	assert.Equal(t, "aac", probeInfo.StreamInfo[1].CodecName)
	assert.Equal(t, int64(394000), probeInfo.StreamInfo[1].BitRate)
	assert.Equal(t, 6, probeInfo.StreamInfo[1].Channels)
	assert.Equal(t, 0, probeInfo.StreamInfo[1].ChannelLayout)

	liveSource.Stop()

}

// 1) Starts avpipe probe to listen for an incoming RTMP stream
// 2) Starts ffmpeg to connect to listening avpipe
func TestProbeRTMPListen(t *testing.T) {
	setupLogging()
	outputDir := "TestProbeRTMPListen"

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("rtmp://localhost:%d/rtmp/Doj1Nr3S", liveSource.Port)

	XCParams := &avpipe.XcParams{
		Seekable:          false,
		XcType:            avpipe.Xcprobe,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
		ConnectionTimeout: 5,
	}

	// Transcode audio mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	done := make(chan bool, 1)
	var probeInfo *avpipe.ProbeInfo
	var err error

	go func() {
		tlog.Info("Probing RTMP stream start", "params", fmt.Sprintf("%+v", *XCParams))
		probeInfo, err = avpipe.Probe(XCParams)
		done <- true
	}()

	err = liveSource.Start("rtmp_connect")
	if err != nil {
		t.Error(err)
	}

	<-done
	assert.NoError(t, err)
	tlog.Info("Probe done", "probeInfo", fmt.Sprintf("%+v", *probeInfo))
	assert.Equal(t, "h264", probeInfo.StreamInfo[0].CodecName)
	assert.Equal(t, 1920, probeInfo.StreamInfo[0].Width)
	assert.Equal(t, 1080, probeInfo.StreamInfo[0].Height)
	assert.Equal(t, 100, probeInfo.StreamInfo[0].Profile)
	assert.Equal(t, 40, probeInfo.StreamInfo[0].Level)

	assert.Equal(t, "aac", probeInfo.StreamInfo[1].CodecName)
	assert.Equal(t, int64(394000), probeInfo.StreamInfo[1].BitRate)
	assert.Equal(t, 6, probeInfo.StreamInfo[1].Channels)
	assert.Equal(t, 0, probeInfo.StreamInfo[1].ChannelLayout)

	liveSource.Stop()
}

// 1) Starts ffmpeg for streaming UDP MPEGTS
// 2) avpipe probe reads the generated UDP stream and probes the stream
func TestProbeUDPConnect(t *testing.T) {
	setupLogging()
	outputDir := "TestProbeUDPConnect"

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)

	// Start ffmpeg UDP MPEGTS
	err := liveSource.Start("udp")
	if err != nil {
		t.Error(err)
	}

	time.Sleep(2 * time.Second)

	XCParams := &avpipe.XcParams{
		Seekable:          false,
		XcType:            avpipe.Xcprobe,
		StreamId:          -1,
		Url:               url,
		DebugFrameLevel:   debugFrameLevel,
		ConnectionTimeout: 5,
	}

	// Transcode audio mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Probing MPEGTS stream start", "params", fmt.Sprintf("%+v", *XCParams))
	probeInfo, err := avpipe.Probe(XCParams)

	assert.NoError(t, err)
	assert.Equal(t, "h264", probeInfo.StreamInfo[0].CodecName)
	assert.Equal(t, 1280, probeInfo.StreamInfo[0].Width)
	assert.Equal(t, 720, probeInfo.StreamInfo[0].Height)
	assert.Equal(t, 100, probeInfo.StreamInfo[0].Profile)
	assert.Equal(t, 32, probeInfo.StreamInfo[0].Level)

	assert.Equal(t, "ac3", probeInfo.StreamInfo[1].CodecName)
	assert.Equal(t, int64(384000), probeInfo.StreamInfo[1].BitRate)
	assert.Equal(t, 6, probeInfo.StreamInfo[1].Channels)
	assert.Equal(t, 1551, probeInfo.StreamInfo[1].ChannelLayout)

	liveSource.Stop()

}

// 1) Starts avpipe probe to read UDP stream and probes the stream
// 2) Starts ffmpeg for streaming UDP MPEGTS
func TestProbeUDPListen(t *testing.T) {
	setupLogging()
	outputDir := "TestProbeUDPConnect"

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("udp://localhost:%d", liveSource.Port)

	XCParams := &avpipe.XcParams{
		Seekable:        false,
		XcType:          avpipe.Xcprobe,
		StreamId:        -1,
		Url:             url,
		DebugFrameLevel: debugFrameLevel,
	}

	// Transcode audio mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	done := make(chan bool, 1)
	var probeInfo *avpipe.ProbeInfo
	var err error

	go func() {
		tlog.Info("Probing MPEGTS stream start", "params", fmt.Sprintf("%+v", *XCParams))
		probeInfo, err = avpipe.Probe(XCParams)
		done <- true
	}()

	// Start ffmpeg UDP MPEGTS
	err = liveSource.Start("udp")
	if err != nil {
		t.Error(err)
	}

	<-done
	assert.NoError(t, err)
	assert.Equal(t, "h264", probeInfo.StreamInfo[0].CodecName)
	assert.Equal(t, 1280, probeInfo.StreamInfo[0].Width)
	assert.Equal(t, 720, probeInfo.StreamInfo[0].Height)
	assert.Equal(t, 100, probeInfo.StreamInfo[0].Profile)
	assert.Equal(t, 32, probeInfo.StreamInfo[0].Level)

	assert.Equal(t, "ac3", probeInfo.StreamInfo[1].CodecName)
	assert.Equal(t, int64(384000), probeInfo.StreamInfo[1].BitRate)
	assert.Equal(t, 6, probeInfo.StreamInfo[1].Channels)
	assert.Equal(t, 1551, probeInfo.StreamInfo[1].ChannelLayout)

	liveSource.Stop()
}
