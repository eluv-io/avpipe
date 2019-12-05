package live

import (
	"fmt"
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
	SegDurationTs:   -1,
	SegDurationFr:   -1,
	ForceKeyInt:     120,
	SegDuration:     "30.03",   // seconds
	Ecodec:          "libx264", // libx264 software / h264_videotoolbox mac hardware
	EncHeight:       720,       // 1080
	EncWidth:        1280,      // 1920
	TxType:          avpipe.TxVideo,
}

/*
 * To run this test, run ffmpeg in separate console to produce UDP packets with a ts file:
 *
 * ffmpeg -re -i media/FS1-19-10-15.ts -c copy -f mpegts udp://127.0.0.1:21001?pkt_size=1316
 *
 */
func TestUdpToMp4(t *testing.T) {

	setupLogging()
	outFileName = "ts_out"

	sAddr, err := net.ResolveUDPAddr("udp", ":21001")
	if err != nil {
		t.Error(err)
	}
	conn, err := net.ListenUDP("udp", sAddr)
	if err != nil {
		t.Error(err)
	}
	conn.SetReadBuffer(8 * 1024 * 1024)

	rwVideoBuf := NewRWBuffer(100000)

	url := "video_udp"
	reqCtx := &testCtx{url: url, r: rwVideoBuf}
	putReqCtxByURL(url, reqCtx)

	go func() {
		tlog.Info("UDP start")
		if err := readUdp(conn, rwVideoBuf); err != nil {
			t.Error(err)
		}
		tlog.Info("UDP done", "err", err)
		if err := rwVideoBuf.(*RWBuffer).CloseSide(RWBufferWriteClosed); err != nil {
			t.Error(err)
		}
	}()

	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})
	var lastPts int64
	tlog.Info("Tx start", "videoParams", fmt.Sprintf("%+v", *videoParamsTs))
	errTx := avpipe.Tx(videoParamsTs, url, true, &lastPts)
	if errTx != 0 {
		t.Error("Tx failed", "err", errTx)
	}
	tlog.Info("Tx done", "err", errTx, "last pts", lastPts)
}

/*
 * To run this test, run ffmpeg in separate console to produce UDP packets with a ts file:
 *
 * ffmpeg -re -i media/FS1-19-10-15.ts -c copy -f mpegts udp://127.0.0.1:21001?pkt_size=1316
 *
 */
func TestUdpToMp4V2(t *testing.T) {

	setupLogging()
	outFileName = "ts_out"

	r := NewTsReaderV2(":21001")
	url := "video_udp2"
	reqCtx := &testCtx{url: url, r: r}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	tlog.Info("Tx start", "videoParams", fmt.Sprintf("%+v", *videoParamsTs))
	errTx := avpipe.Tx(videoParamsTs, url, true, nil)
	tlog.Info("Tx done", "err", errTx, "last pts", nil)

	if errTx != 0 {
		t.Error("Tx failed", "err", errTx)
	}
}
