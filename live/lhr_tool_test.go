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
)

// Sky 1080 stream: http://origin1.sedev02_newsdemuxclear.stage-cdhls.skydvn.com/cdsedev04demuxclearnews/13012/cd.m3u8
// Sky 720 stream: http://origin1.skynews.mobile.skydvn.com/skynews/1404/latest.m3u8
// Fox stream: https://content.uplynk.com/channel/089cd376140c40d3a64c7c1dcccb4467.m3u8
var testUrl string = "https://content.uplynk.com/channel/089cd376140c40d3a64c7c1dcccb4467.m3u8"

func TestToolTs(t *testing.T) {

	avpipe.SetCLoggers()
	sourceUrlStr := testUrl
	pipe := "lhr_out.ts"

	sourceUrl, err := url.Parse(sourceUrlStr)
	if err != nil {
		t.Fail()
	}

	f, err := os.Create(pipe)
	defer f.Close()

	lhr := NewHLSReader(sourceUrl)

	mezDurationRat := big.NewRat(int64(2702700), int64(90000)) // Fox stream
	lhr.Fill(-1, 0, mezDurationRat, f)

}

type testCtx struct {
	w io.Writer
	r io.Reader
}

var bytesRead, bytesWritten, rwDiffMax int

func makeOneMez(t *testing.T) {

}

func TestToolFmp4(t *testing.T) {

	log.SetDebug()
	avpipe.SetCLoggers()

	sourceUrlStr := testUrl
	pipe := "lhr_out.mp4"

	sourceUrl, err := url.Parse(sourceUrlStr)
	if err != nil {
		t.Fail()
	}

	// Final output - the fmp4 stream
	f, err := os.Create(pipe)
	defer f.Close()

	pr, pw := io.Pipe()

	readCtx := testCtx{r: pr}
	writeCtx := testCtx{w: f}

	lhr := NewHLSReader(sourceUrl)

	go func() {
		mezDurationRat := big.NewRat(int64(2702700), int64(90000)) // Fox stream
		//mezDurationRat := big.NewRat(int64(30), int64(1))
		lhr.Fill(-1, 0, mezDurationRat, pw)
		log.Info("AVL FILL DONE")
		pw.Close()
	}()

	avpipe.InitIOHandler(&inputOpener{tc: readCtx}, &outputOpener{tc: writeCtx})

	// videoParams := &avpipe.TxParams{
	// 	Format:      "fmp4",
	// 	StartTimeTs: 0,
	// 	// StartPts           int32
	// 	DurationTs:      2700000,
	// 	StartSegmentStr: "1",
	// 	VideoBitrate:    3189984,
	// 	// AudioBitrate:  128000,
	// 	// SampleRate:    48000,
	// 	// CrfStr:        "20",
	// 	SegDurationTs: 180000,
	// 	SegDurationFr: 50,
	// 	// FrameDurationTs    int32
	// 	// StartFragmentIndex int32
	// 	Ecodec: "libx264",
	// 	// Dcodec             string
	// 	EncHeight: 720,
	// 	EncWidth:  1280,
	// 	// CryptIV            string
	// 	// CryptKey           string
	// 	// CryptKID           string
	// 	// CryptKeyURL        string
	// 	// CryptScheme        CryptScheme
	// 	TxType: avpipe.TxVideo,
	// }

	videoParams := &avpipe.TxParams{
		Format:          "fmp4",
		StartTimeTs:     0,
		DurationTs:      2702700, //5405400
		StartSegmentStr: "1",
		VideoBitrate:    6000000,
		SegDurationTs:   180180, //360360
		SegDurationFr:   60,
		Ecodec:          "libx264",
		EncHeight:       720,
		EncWidth:        1280,
		TxType:          avpipe.TxVideo,
	}

	xcparams := fmt.Sprintf("%+v", *videoParams)
	log.Info("AVLIVE transcoding start", "xcparams", xcparams)
	errTx := avpipe.Tx(videoParams, "video_out.mp4", false, true)
	log.Info("AVLIVE Video TX DONE", "err", errTx)

	/* TODO: Make audio transcoding functional.
	audioParams := &avpipe.TxParams{
		Format:          "fmp4",
		StartTimeTs:     0,
		DurationTs:      2700000,
		StartSegmentStr: "1",
		VideoBitrate:    8100000,
		CrfStr:          "20",
		SegDurationTs:   180000,
		SegDurationFr:   50,
		Ecodec:          "libx264",
		EncHeight:       720,
		EncWidth:        1280,
		TxType:          avpipe.TxAudio,
	}

	xcparams := fmt.Sprintf("%+v", *audioParams)
	log.Info("AVLIVE transcoding start", "xcparams", xcparams)
	errTx := avpipe.Tx(audioParams, "audio_out.mp4", false, true)
	log.Info("AVLIVE Audio TX DONE", "err", errTx)*/

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
	log.Debug("AVL IN_OPEN", "fd", fd, "url", url)
	io.url = url
	etxInput := &inputCtx{
		r: io.tc.r,
	}
	return etxInput, nil
}

func (i *inputCtx) Read(buf []byte) (int, error) {
	log.Debug("AVL IN_READ", "len", len(buf))
	n, err := i.r.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	bytesRead += n
	log.Debug("AVL IN_READ DONE", "len", len(buf), "n", n,
		"bytesRead", bytesRead, "bytesWritten", bytesWritten, "err", err)
	return n, err
}

func (i *inputCtx) Seek(offset int64, whence int) (int64, error) {
	log.Debug("AVL IN_SEEK")
	return -1, nil
}

func (i *inputCtx) Close() error {
	log.Debug("AVL IN_CLOSE")
	i.r.(*io.PipeReader).Close()
	return nil
}

func (i *inputCtx) Size() int64 {
	log.Debug("AVL IN_SIZE")
	return -1
}

type outputOpener struct {
	tc testCtx
}

type outputCtx struct {
	w io.Writer
}

func (oo *outputOpener) Open(h, fd int64, stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	log.Debug("AVL OUT_OPEN", "fd", fd)
	oh := &outputCtx{w: oo.tc.w}
	return oh, nil
}

func (o *outputCtx) Write(buf []byte) (int, error) {
	log.Debug("AVL OUT_WRITE", "len", len(buf))
	n, err := o.w.Write(buf)
	if err != nil {
		return n, err
	}
	if bytesWritten == 0 {
		rwDiffMax = bytesRead - bytesWritten
		log.Debug("AVL OUT_WRITE FIRST", "bytesRead", bytesRead, "bytesWritten", bytesWritten, "diff", rwDiffMax)
	}
	bytesWritten += n
	if bytesRead-bytesWritten > rwDiffMax {
		rwDiffMax = bytesRead - bytesWritten
	}
	log.Debug("AVL OUT_WRITE DONE", "len", len(buf), "n", n, "err", err,
		"bytesRead", bytesRead, "bytesWritten", bytesWritten, "diff", rwDiffMax)
	return n, err
}

func (o *outputCtx) Seek(offset int64, whence int) (int64, error) {
	log.Debug("AVL OUT_SEEK")
	return -1, nil
}

func (o *outputCtx) Close() error {
	log.Debug("AVL OUT_CLOSE")
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
		t.Error("newDecryptWriter")
	}
	if _, err = dw.Write(ciphertext); err != nil {
		t.Error("dw.Write")
	}
	if _, err = dw.Flush(); err != nil {
		t.Error("dw.Flush")
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
