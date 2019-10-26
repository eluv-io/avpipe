package live

import (
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/qluvio/avpipe"
)

var videoParamsTs = &avpipe.TxParams{
	Format:          "fmp4-segment",
	Seekable:        false,
	DurationTs:      -1,
	StartSegmentStr: "1",
	VideoBitrate:    20000000, // fox stream bitrate
	SegDurationTs:   180000,
	SegDurationFr:   120,
	SegDuration:     "30.03",             // seconds
	Ecodec:          "h264_videotoolbox", // libx264 software / h264_videotoolbox mac hardware
	EncHeight:       720,                 // 1080
	EncWidth:        1280,                // 1920
	TxType:          avpipe.TxVideo,
}

func TestUdpToMp4(t *testing.T) {

	setupLogging()
	outFileName = "ts_out"

	addr := ":21001"
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Error(err)
	}

	pr, pw := io.Pipe()
	readCtx := testCtx{r: pr}
	writeCtx := testCtx{}

	go func() {
		tlog.Info("UDP start")
		if err := readUdp(conn, pw); err != nil {
			t.Error(err)
		}
		tlog.Info("UDP done", "err", err)
		if err := pw.Close(); err != nil {
			t.Error(err)
		}
	}()

	avpipe.InitIOHandler(&inputOpener{tc: readCtx}, &outputOpener{tc: writeCtx})
	var lastPts int64
	tlog.Info("Tx start", "videoParams", fmt.Sprintf("%+v", *videoParamsTs))
	errTx := avpipe.Tx(videoParamsTs, "video_out.mp4", false, true, &lastPts)
	if errTx != 0 {
		t.Error("Tx failed", "err", errTx)
	}
	tlog.Info("Tx done", "err", errTx, "last pts", lastPts)
}
