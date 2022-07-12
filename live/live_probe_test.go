package live

import (
	"fmt"
	"github.com/eluv-io/avpipe"
	"github.com/stretchr/testify/assert"
	"os"
	"testing"
	"time"
)

func TestProbeListenRTMP(t *testing.T) {
	setupLogging()
	outputDir := "TestProbeListenRTMP"

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	url := fmt.Sprintf("rtmp://localhost:%d/rtmp/Doj1Nr3S", liveSource.Port)

	err := liveSource.Start("rtmp")
	if err != nil {
		t.Error(err)
	}

	time.Sleep(2 * time.Second)

	XCParams := &avpipe.XcParams{
		Seekable: false,
		XcType:   avpipe.Xcprobe,
		StreamId: -1,
		Url:      url,
		//DebugFrameLevel: debugFrameLevel,
		DebugFrameLevel: true,
	}

	// Transcode audio mez files in background
	reqCtx := &testCtx{url: url}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

	tlog.Info("Probing RTMP stream start", "params", fmt.Sprintf("%+v", *XCParams))
	probeInfo, err := avpipe.Probe(XCParams)

	assert.NoError(t, err)
	assert.Equal(t, 40, probeInfo.StreamInfo[0].Level)
	liveSource.Stop()

}
