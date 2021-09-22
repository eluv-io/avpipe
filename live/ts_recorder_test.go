package live

import (
	"fmt"
	"io"
	"net"
	"os"
	"testing"

	"github.com/qluvio/avpipe"
)

func TestUdpToMp4V1(t *testing.T) {
	outputDir := "TestUdpToMp4V1"

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	videoParamsTs := &avpipe.TxParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		VideoBitrate:    20000000, // fox stream bitrate
		ForceKeyInt:     120,
		SegDuration:     "30.03",   // seconds
		Ecodec:          "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:       720,       // 1080
		EncWidth:        1280,      // 1920
		TxType:          avpipe.TxVideo,
		StreamId:        -1,
	}

	setupLogging()

	liveSource := NewLiveSource()
	err := liveSource.Start()
	if err != nil {
		t.Error(err)
	}

	sAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", liveSource.Port))
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
			tlog.Info("UDP timeout", err)
		}
		tlog.Info("UDP done", "err", err)
		if err := rwVideoBuf.(*RWBuffer).CloseSide(RWBufferWriteClosed); err != nil {
			t.Error(err)
		}
	}()

	avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})
	tlog.Info("Transcoding start", "videoParams", fmt.Sprintf("%+v", *videoParamsTs))
	err = avpipe.Tx(videoParamsTs, url, true)
	if err != nil {
		t.Error("Transcoding failed", "err", err)
	}
	tlog.Info("Transcoding done", "err", err)
}

func TestUdpToMp4V2(t *testing.T) {
	setupLogging()
	outputDir := "TestUdpToMp4V2"

	// Create output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		os.Mkdir(outputDir, 0755)
	}

	liveSource := NewLiveSource()
	addr := fmt.Sprintf(":%d", liveSource.Port)

	tsr, rwb, err := NewTsReaderV2(addr)
	if err != nil {
		t.Error("TsReader failed", "err", err)
	}

	audioReader := NewRWBuffer(10000)
	videoReader := io.TeeReader(rwb, audioReader)

	done := make(chan bool, 1)
	testComplet := make(chan bool, 1)

	go func(tsr *TsReader) {
	out:
		for {
			select {
			case err = <-tsr.ErrChannel:
				tlog.Error("Got error on reading UDP", err)
				break out
			case <-testComplet:
				break out
			}
		}
	}(tsr)

	err = liveSource.Start()
	if err != nil {
		t.Error(err)
	}

	audioParamsTs := &avpipe.TxParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		AudioBitrate:    384000,  // FS1-19-10-14.ts audio bitrate
		SegDuration:     "30.03", // seconds
		Dcodec2:         "ac3",
		Ecodec2:         "aac", // "aac"
		TxType:          avpipe.TxAudio,
		StreamId:        -1,
	}

	// Transcode audio mez files in background
	go func(reader io.Reader) {
		url := "audio_mez_udp2"
		reqCtx := &testCtx{url: url, r: reader}
		putReqCtxByURL(url, reqCtx)

		avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

		tlog.Info("Transcoding UDP Audio stream start", "params", fmt.Sprintf("%+v", *audioParamsTs))
		err := avpipe.Tx(audioParamsTs, url, true)
		tlog.Info("Transcoding UDP Audio stream done", "err", err, "last pts", nil)
		if err != nil {
			t.Error("Transcoding UDP Audio failed", "err", err)
		}

		done <- true
	}(audioReader)

	videoParamsTs := &avpipe.TxParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		VideoBitrate:    20000000, // fox stream bitrate
		ForceKeyInt:     120,
		SegDuration:     "30.03",   // seconds
		Ecodec:          "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:       720,       // 1080
		EncWidth:        1280,      // 1920
		TxType:          avpipe.TxVideo,
		StreamId:        -1,
	}

	go func(reader io.Reader, writer io.WriteCloser) {
		url := "video_mez_udp2"
		reqCtx := &testCtx{url: url, r: reader, wc: writer}
		putReqCtxByURL(url, reqCtx)

		avpipe.InitIOHandler(&inputOpener{dir: outputDir}, &outputOpener{dir: outputDir})

		tlog.Info("Transcoding UDP Video stream start", "params", fmt.Sprintf("%+v", *videoParamsTs))
		err := avpipe.Tx(videoParamsTs, url, true)
		tlog.Info("Transcoding UDP Video stream done", "err", err, "last pts", nil)
		if err != nil {
			t.Error("Transcoding UDP Video failed", "err", err)
		}

		done <- true
	}(videoReader, audioReader)

	<-done
	<-done

	audioParamsTs.Format = "dash"
	audioParamsTs.AudioSegDurationTs = 96106 // almost 2 * 48000
	audioMezFiles := [3]string{"audio_mez_udp2-segment-1.mp4", "audio_mez_udp2-segment-2.mp4", "audio_mez_udp2-segment-3.mp4"}

	// Now create audio dash segments out of audio mezzanines
	go func() {
		for i, url := range audioMezFiles {
			tlog.Info("Transcoding Audio Dash start", "audioParams", fmt.Sprintf("%+v", *audioParamsTs), "url", url)
			reqCtx := &testCtx{url: url}
			putReqCtxByURL(url, reqCtx)
			audioParamsTs.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Tx(audioParamsTs, url, true)
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

	videoParamsTs.Format = "dash"
	videoParamsTs.VideoSegDurationTs = 180000 // almost 2 * 90000
	videoMezFiles := [3]string{"video_mez_udp2-segment-1.mp4", "video_mez_udp2-segment-2.mp4", "video_mez_udp2-segment-3.mp4"}

	// Now create video dash segments out of audio mezzanines
	go func() {
		for i, url := range videoMezFiles {
			tlog.Info("AVL Video Dash transcoding start", "videoParams", fmt.Sprintf("%+v", *videoParamsTs), "url", url)
			reqCtx := &testCtx{url: url}
			putReqCtxByURL(url, reqCtx)
			audioParamsTs.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Tx(videoParamsTs, url, true)
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
