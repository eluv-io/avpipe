package live

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"testing"

	"github.com/qluvio/avpipe"
	elog "github.com/qluvio/content-fabric/log"
)

var tlog = elog.Get("/eluvio/avpipe/live/test")

var verboseLogging bool = false

// Sky 1080 stream: http://origin1.sedev02_newsdemuxclear.stage-cdhls.skydvn.com/cdsedev04demuxclearnews/13012/cd.m3u8
// Sky 720 stream: http://origin1.skynews.mobile.skydvn.com/skynews/1404/latest.m3u8
var manifestURLStr string = "http://origin1.skynews.mobile.skydvn.com/skynews/1404/latest.m3u8"
var recordingDuration *big.Rat = big.NewRat(int64(2700000), int64(90000))
var videoParams *avpipe.TxParams = &avpipe.TxParams{
	Format:          "fmp4",
	DurationTs:      2700000,
	StartSegmentStr: "1",
	VideoBitrate:    5000000, // 1080 probe: 8234400, 720 probe: 3189984
	SegDurationTs:   -1,      //180000,
	SegDurationFr:   -1,      // 50,
	ForceKeyInt:     50,
	Ecodec:          "libx264",
	EncHeight:       720,  // 1080
	EncWidth:        1280, // 1920
	TxType:          avpipe.TxVideo,
}

// Fox stream
//var manifestURLStr string = "https://content.uplynk.com/channel/089cd376140c40d3a64c7c1dcccb4467.m3u8"
//var recordingDuration *big.Rat = big.NewRat(int64(2702700), int64(90000))
//var videoParams *avpipe.TxParams = &avpipe.TxParams{
//	Format:          "fmp4",
//	SkipOverPts:     0,
//	DurationTs:      2702700, //5405400
//	StartSegmentStr: "1",
//	VideoBitrate:    1557559,
//	SegDurationTs:   180180, //360360
//	SegDurationFr:   60,     //120
//	Ecodec:          "libx264",
//	EncHeight:       432,
//	EncWidth:        768,
//	TxType:          avpipe.TxVideo,
//}

var outFileName string = "out"

func TestToolTs(t *testing.T) {
	setupLogging()

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Fail()
	}

	f, err := os.Create(outFileName + ".ts")
	defer f.Close()

	lhr := NewHLSReader(manifestURL)
	if err = lhr.Fill(recordingDuration, f); err != nil {
		t.Error(err)
	}
}

type testCtx struct {
	w io.Writer
	r io.Reader
}

var bytesRead, bytesWritten, rwDiffMax int

func TestToolFmp4(t *testing.T) {
	setupLogging()

	// Save stream files instead of transcoding if specified
	//   Make sure go test timeout is big enough:
	//     go test -timeout 24h --run TestToolFmp4
	//TESTSaveToDir = "/temp/fox"

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}
	lhr := NewHLSReader(manifestURL)

	// lhr contains the recording state so use a single instance
	outFileName = "lhr_out_1"
	recordFmp4(t, lhr)
	outFileName = "lhr_out_2"
	recordFmp4(t, lhr)
	outFileName = "lhr_out_3"
	recordFmp4(t, lhr)
}

func recordFmp4(t *testing.T, lhr *HLSReader) {
	pr, pw := io.Pipe()
	readCtx := testCtx{r: pr}
	writeCtx := testCtx{}

	go func() {
		lhr.Fill(recordingDuration, pw)
		tlog.Info("AVL Fill done")
		pw.Close()
	}()

	avpipe.InitIOHandler(&inputOpener{tc: readCtx}, &outputOpener{tc: writeCtx})
	videoParams.SkipOverPts = lhr.NextSkipOverPts
	tlog.Info("AVL Tx start", "videoParams", fmt.Sprintf("%+v", *videoParams))
	errTx := avpipe.Tx(videoParams, "video_out.mp4", true, &lhr.NextSkipOverPts)
	tlog.Info("AVL Tx done", "err", errTx, "last pts", lhr.NextSkipOverPts)

	if errTx != 0 {
		t.Error("AVL Video transcoding failed", "errTx", errTx)
	}

	// TODO: Audio - avpipe.Tx(audioParams, ...
}

//Implement AVPipeInputOpener
type inputOpener struct {
	url string
	tc  testCtx
}

type inputCtx struct {
	r io.Reader
}

func (io *inputOpener) Open(fd int64, url string) (avpipe.InputHandler, error) {
	tlog.Debug("AVL IN_OPEN", "fd", fd, "url", url)
	io.url = url
	etxInput := &inputCtx{
		r: io.tc.r,
	}
	return etxInput, nil
}

func (i *inputCtx) Read(buf []byte) (int, error) {
	if verboseLogging {
		tlog.Debug("AVL IN_READ", "len", len(buf))
	}
	n, err := i.r.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	bytesRead += n
	if verboseLogging {
		tlog.Debug("AVL IN_READ DONE", "len", len(buf), "n", n,
			"bytesRead", bytesRead, "bytesWritten", bytesWritten, "err", err)
	}
	return n, err
}

func (i *inputCtx) Seek(offset int64, whence int) (int64, error) {
	tlog.Debug("AVL IN_SEEK")
	return -1, nil
}

func (i *inputCtx) Close() (err error) {
	tlog.Debug("AVL IN_CLOSE")
	if _, ok := i.r.(*RWBuffer); ok {
		err = i.r.(*RWBuffer).Close(RWBufferReadClosed)
	} else {
		err = i.r.(*io.PipeReader).Close()
	}
	return
}

func (i *inputCtx) Size() int64 {
	tlog.Debug("AVL IN_SIZE")
	return -1
}

type outputOpener struct {
	tc testCtx
}

type outputCtx struct {
	w    io.Writer
	file *os.File
}

func (oo *outputOpener) Open(h, fd int64, stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	tlog.Debug("AVL OUT_OPEN", "fd", fd)

	file, err := os.Create(fmt.Sprintf("%s_%d_%03d.mp4", outFileName, stream_index, seg_index))
	if err != nil {
		return nil, err
	}

	oh := &outputCtx{w: file, file: file}
	return oh, nil
}

func (o *outputCtx) Write(buf []byte) (int, error) {
	if verboseLogging {
		tlog.Debug("AVL OUT_WRITE", "len", len(buf))
	}
	n, err := o.w.Write(buf)
	if err != nil {
		return n, err
	}
	if bytesWritten == 0 {
		rwDiffMax = bytesRead - bytesWritten
		tlog.Debug("AVL OUT_WRITE FIRST", "bytesRead", bytesRead, "bytesWritten", bytesWritten, "diff", rwDiffMax)
	}
	bytesWritten += n
	if bytesRead-bytesWritten > rwDiffMax {
		rwDiffMax = bytesRead - bytesWritten
	}
	if verboseLogging {
		tlog.Debug("AVL OUT_WRITE DONE", "len", len(buf), "n", n, "err", err,
			"bytesRead", bytesRead, "bytesWritten", bytesWritten, "diff", rwDiffMax)
	}
	return n, err
}

func (o *outputCtx) Seek(offset int64, whence int) (int64, error) {
	tlog.Debug("AVL OUT_SEEK")
	//return o.file.Seek(offset, whence)
	return -1, nil
}

func (o *outputCtx) Close() error {
	tlog.Debug("AVL OUT_CLOSE")
	o.file.Close()
	return nil
}

func TestDecrypt(t *testing.T) {
	encDec(t, []byte(""))
	encDec(t, []byte("1"))
	encDec(t, []byte("exampleplaintext")) // 16
	encDec(t, []byte("abcdefghijklmnopqrstuvwxyz"))
}

func encDec(t *testing.T, plaintext []byte) {
	paddedplaintext := padPKCS5(plaintext, aes.BlockSize)

	var key []byte
	var err error
	if key, err = hex.DecodeString("6368616e676520746869732070617373"); err != nil {
		t.Error(err)
	}

	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		t.Error(err)
	}

	var block cipher.Block
	if block, err = aes.NewCipher(key); err != nil {
		t.Error(err)
	}
	mode := cipher.NewCBCEncrypter(block, iv)

	ciphertext := make([]byte, len(paddedplaintext))
	mode.CryptBlocks(ciphertext, paddedplaintext)
	// fmt.Printf("%x\n", ciphertext)

	var dec bytes.Buffer
	var dw *decryptWriter
	if dw, err = newDecryptWriter(&dec, key, iv); err != nil {
		t.Error("newDecryptWriter", "err", err)
	}
	if _, err = dw.Write(ciphertext); err != nil {
		t.Error("dw.Write", "err", err)
	}
	if _, err = dw.Flush(); err != nil {
		t.Error("dw.Flush", "err", err)
	}
	// fmt.Println(dec.String())
	if bytes.Compare(plaintext, dec.Bytes()) != 0 {
		t.Error(string(plaintext), dec.String())
	}
}

func padPKCS5(src []byte, blockSize int) []byte {
	srclen := len(src)
	padlen := (blockSize - (srclen % blockSize))
	padding := bytes.Repeat([]byte{byte(padlen)}, padlen)
	return append(src, padding...)
}

func setupLogging() {
	elog.SetDefault(&elog.Config{
		Level:   "debug",
		Handler: "text",
		File: &elog.LumberjackConfig{
			Filename:  "lhr.log",
			LocalTime: true,
		},
	})
	avpipe.SetCLoggers()
}
