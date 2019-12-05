package live

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/qluvio/content-fabric/errors"
	"io"
	"math/big"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/qluvio/avpipe"
	elog "github.com/qluvio/content-fabric/log"
)

var tlog = elog.Get("/eluvio/avpipe/live/test")

var verboseLogging bool = false
var requestURLTable map[string]*testCtx = map[string]*testCtx{}
var urlMutex *sync.RWMutex = &sync.RWMutex{}
var requestFDTable map[int64]*testCtx = map[int64]*testCtx{}
var fdMutex *sync.RWMutex = &sync.RWMutex{}

type testCtx struct {
	fd           int64
	url          string
	bytesRead    int
	bytesWritten int
	rwDiffMax    int
	w            io.Writer
	r            io.Reader
}

//Implement AVPipeInputOpener
type inputOpener struct {
	url string
	tc  testCtx
}

type inputCtx struct {
	tc *testCtx
	r  io.Reader
}

type outputOpener struct {
	tc *testCtx
}

type outputCtx struct {
	tc   *testCtx
	w    io.Writer
	file *os.File
}

// Sky 1080 stream: http://origin1.sedev02_newsdemuxclear.stage-cdhls.skydvn.com/cdsedev04demuxclearnews/13012/cd.m3u8
// Sky 720 stream: http://origin1.skynews.mobile.skydvn.com/skynews/1404/latest.m3u8
var manifestURLStr string = "http://origin1.skynews.mobile.skydvn.com/skynews/1404/latest.m3u8"
var recordingDuration *big.Rat = big.NewRat(int64(3*2700000), int64(90000))
var recordingDurationHlsV1 *big.Rat = big.NewRat(int64(2700000), int64(90000))

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

func getReqCtxByURL(url string) (*testCtx, error) {
	urlMutex.RLock()
	defer urlMutex.RUnlock()

	reqCtx := requestURLTable[url]
	if reqCtx == nil {
		return nil, errors.E("find request context", errors.K.NotExist, "url", url)
	}

	return reqCtx, nil
}

func putReqCtxByURL(url string, reqCtx *testCtx) {
	urlMutex.Lock()
	defer urlMutex.Unlock()

	requestURLTable[url] = reqCtx
}

func getReqCtxByFD(fd int64) (*testCtx, error) {
	fdMutex.RLock()
	defer fdMutex.RUnlock()

	reqCtx := requestFDTable[fd]
	if reqCtx == nil {
		return nil, errors.E("find request context", errors.K.NotExist, "fd", fd)
	}

	return reqCtx, nil
}

func putReqCtxByFD(fd int64, reqCtx *testCtx) {
	fdMutex.Lock()
	defer fdMutex.Unlock()

	requestFDTable[fd] = reqCtx
}

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

func TestVideoHlsLive(t *testing.T) {
	params := &avpipe.TxParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		VideoBitrate:    5000000, // 1080 probe: 8234400, 720 probe: 3189984
		SegDurationTs:   -1,      //180000,
		SegDuration:     "30",
		SegDurationFr:   -1, // 50,
		ForceKeyInt:     50,
		Ecodec:          "libx264", // MacOS h264_videotoolbox
		EncHeight:       720,  // 1080
		EncWidth:        1280, // 1920
		TxType:          avpipe.TxVideo,
	}

	setupLogging()

	// Save stream files instead of transcoding if specified
	//   Make sure go test timeout is big enough:
	//     go test -timeout 24h --run TestToolFmp4
	//TESTSaveToDir = "/temp/fox"

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}

	// lhr contains the recording state so use a single instance
	lhr := NewHLSReader(manifestURL)
	rwb := NewRWBuffer(10000)

	go func() {
		lhr.Fill(recordingDuration, rwb)
		tlog.Info("AVL Fill done")
		rwb.(*RWBuffer).CloseSide(RWBufferWriteClosed)
	}()

	url := "video_hls"
	reqCtx := &testCtx{url: url, r: rwb}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})
	var NextSkipOverPts int64
	tlog.Info("AVL Tx start", "videoParams", fmt.Sprintf("%+v", *params))
	errTx := avpipe.Tx(params, url, true, &NextSkipOverPts)
	tlog.Info("AVL Tx done", "err", errTx, "last pts", NextSkipOverPts)

	if errTx != 0 {
		t.Error("AVL Video transcoding failed", "errTx", errTx)
	}
}

func TestVideoHlsLiveV2(t *testing.T) {
	params := &avpipe.TxParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		VideoBitrate:    5000000, // 1080 probe: 8234400, 720 probe: 3189984
		SegDurationTs:   -1,      //180000,
		SegDuration:     "30",
		SegDurationFr:   -1, // 50,
		ForceKeyInt:     50,
		Ecodec:          "libx264",
		EncHeight:       720,  // 1080
		EncWidth:        1280, // 1920
		TxType:          avpipe.TxVideo,
	}

	setupLogging()

	// Save stream files instead of transcoding if specified
	//   Make sure go test timeout is big enough:
	//     go test -timeout 24h --run TestToolFmp4
	//TESTSaveToDir = "/temp/fox"

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}

	lhr := NewHLSReaderV2(manifestURL)

	outFileName = "lhr_out"
	url := "video_hls"
	reqCtx := &testCtx{url: url, r: lhr}
	putReqCtxByURL(url, reqCtx)

	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	tlog.Info("AVL Tx start", "videoParams", fmt.Sprintf("%+v", *params))
	errTx := avpipe.Tx(params, url, true, nil)
	tlog.Info("AVL Tx done", "err", errTx)

	if errTx != 0 {
		t.Error("AVL Video transcoding failed", "errTx", errTx)
	}
}

func TestAudioHlsLive(t *testing.T) {
	params := &avpipe.TxParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000, // TODO 3 * 1443840 should work but Sky stream's 1/90000 time base seems to be used to measure duration
		StartSegmentStr: "1",
		AudioBitrate:    128000,
		SampleRate:      48000,
		SegDurationTs:   -1, // 1443840
		SegDuration:     "30", // 30.08
		SegDurationFr:   -1,
		Ecodec:          "aac", // "ac3", "aac"
		Dcodec:          "aac", // "aac", "h264"
		AudioIndex:      11,
		TxType:          avpipe.TxAudio,
		//BypassTranscoding: true,
	}

	setupLogging()

	// Save stream files instead of transcoding if specified
	//   Make sure go test timeout is big enough:
	//     go test -timeout 24h --run TestToolFmp4
	//TESTSaveToDir = "/temp/fox"

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}

	lhr := NewHLSReaderV2(manifestURL)
	outFileName = "lhr_out"

	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	url := "audio_hls"
	reqCtx := &testCtx{url: url, r: lhr}
	putReqCtxByURL(url, reqCtx)

	var NextSkipOverPts int64
	tlog.Info("AVL Tx start", "audioHlsParams", fmt.Sprintf("%+v", *params))
	errTx := avpipe.Tx(params, url, true, &NextSkipOverPts)
	tlog.Info("AVL Tx done", "err", errTx, "last pts", NextSkipOverPts)

	if errTx != 0 {
		t.Error("AVL Audio transcoding failed", "errTx", errTx)
	}
}

func TestAudioVideoHlsLive(t *testing.T) {

	setupLogging()

	// Save stream files instead of transcoding if specified
	//   Make sure go test timeout is big enough:
	//     go test -timeout 24h --run TestToolFmp4
	//TESTSaveToDir = "/temp/fox"

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}
	rwb := NewHLSReaderV2(manifestURL)
	//var buf2 bytes.Buffer
	audioReader := NewRWBuffer(10000)

	videoReader := io.TeeReader(rwb, audioReader)

	done := make(chan bool, 1)

	// lhr contains the recording state so use a single instance
	outFileName = "lhr_out"

	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	audioHlsParams := &avpipe.TxParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		AudioBitrate:    128000, // 78187,
		SampleRate:      48000,
		SegDurationTs:   -1, //180000,
		SegDuration:     "30",
		SegDurationFr:   -1, // 50,
		ForceKeyInt:     50,
		Ecodec:          "aac", // "ac3", "aac"
		Dcodec:          "aac", // "aac", "h264"
		AudioIndex:      11,
		TxType:          avpipe.TxAudio,
		//BypassTranscoding: true,
	}

	// Transcode audio in background
	go func(reader io.Reader) {
		var NextSkipOverPts int64

		tlog.Info("AVL Audio Tx start", "audioHlsParams", fmt.Sprintf("%+v", *audioHlsParams))
		url := "audio_hls"
		reqCtx := &testCtx{url: url, r: reader}
		putReqCtxByURL(url, reqCtx)
		errTx := avpipe.Tx(audioHlsParams, url, true, &NextSkipOverPts)
		tlog.Info("AVL Audio Tx done", "err", errTx, "last pts", NextSkipOverPts)

		if errTx != 0 {
			t.Error("AVL Audio transcoding failed", "errTx", errTx)
		}
		done <- true
	}(audioReader)

	videoHlsParams := &avpipe.TxParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		VideoBitrate:    5000000, // 1080 probe: 8234400, 720 probe: 3189984
		SegDurationTs:   -1,      //180000,
		SegDuration:     "30",
		SegDurationFr:   -1, // 50,
		ForceKeyInt:     50,
		Ecodec:          "libx264", // "ac3", "aac"
		EncHeight:       720,       // 1080
		EncWidth:        1280,      // 1920
		TxType:          avpipe.TxVideo,
		//BypassTranscoding: true,
	}

	go func(reader io.Reader) {
		var NextSkipOverPts int64
		tlog.Info("AVL Video Tx start", "videoHlsParams", fmt.Sprintf("%+v", *videoHlsParams))
		url := "video_hls"
		reqCtx := &testCtx{url: url, r: reader}
		putReqCtxByURL(url, reqCtx)
		errTx := avpipe.Tx(videoHlsParams, url, true, &NextSkipOverPts)
		tlog.Info("AVL Video Tx done", "err", errTx, "last pts", NextSkipOverPts)

		if errTx != 0 {
			t.Error("AVL Video transcoding failed", "errTx", errTx)
		}
		done <- true
	}(videoReader)

	// Wait for audio/video transcoding to be finished
	<-done
	<-done
}

// TestToolFmp4 records HLS mezzanines using one one avpipe.Tx invocation per mez
// and the returnled 'last PTS' to skip during the following avpipe.Tx invocation
func TestVideoFmp4(t *testing.T) {
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

	recordFmp4(t, lhr, "video_fmp4_1")
	recordFmp4(t, lhr, "video_fmp4_2")
	recordFmp4(t, lhr, "video_fmp4_3")
}

func recordFmp4(t *testing.T, lhr *HLSReader, url string) {
	params := &avpipe.TxParams{
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

	rwb := NewRWBuffer(10000)
	reqCtx := &testCtx{url: url, r: rwb}
	putReqCtxByURL(url, reqCtx)

	go func() {
		err := lhr.Fill(recordingDurationHlsV1, rwb)
		tlog.Info("AVL Fill done", "err", err)
		rwb.(*RWBuffer).CloseSide(RWBufferWriteClosed)
	}()

	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})
	var NextSkipOverPts int64
	tlog.Info("AVL Tx start", "videoParams", fmt.Sprintf("%+v", *params))
	errTx := avpipe.Tx(params, url, true, &NextSkipOverPts)
	tlog.Info("AVL Tx done", "err", errTx, "last pts", NextSkipOverPts)

	if errTx != 0 {
		t.Error("AVL Video transcoding failed", "errTx", errTx)
	}
}

func (io *inputOpener) Open(fd int64, url string) (avpipe.InputHandler, error) {
	tlog.Debug("AVL IN_OPEN", "fd", fd, "url", url)
	io.url = url
	tc, err := getReqCtxByURL(url)
	if err != nil {
		return nil, err
	}

	tc.fd = fd
	putReqCtxByFD(fd, tc)

	etxInput := &inputCtx{
		tc: tc,
		r:  tc.r,
	}
	return etxInput, nil
}

func (i *inputCtx) Read(buf []byte) (int, error) {
	if verboseLogging {
		tlog.Debug("AVL IN_READ", "url", i.tc.url, "len", len(buf))
	}
	n, err := i.r.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	i.tc.bytesRead += n
	if verboseLogging {
		tlog.Debug("AVL IN_READ DONE", "url", i.tc.url, "len", len(buf), "n", n,
			"bytesRead", i.tc.bytesRead, "bytesWritten", i.tc.bytesWritten, "err", err)
	}
	return n, err
}

func (i *inputCtx) Seek(offset int64, whence int) (int64, error) {
	tlog.Debug("AVL IN_SEEK")
	return -1, nil
}

func (i *inputCtx) Close() (err error) {
	tlog.Debug("AVL IN_CLOSE", "url", i.tc.url)
	if _, ok := i.r.(*RWBuffer); ok {
		err = i.r.(*RWBuffer).CloseSide(RWBufferReadClosed)
	} else if _, ok := i.r.(*io.PipeReader); ok {
		err = i.r.(*io.PipeReader).Close()
	}
	return
}

func (i *inputCtx) Size() int64 {
	tlog.Debug("AVL IN_SIZE")
	return -1
}

func (oo *outputOpener) Open(h, fd int64, stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	tc, err := getReqCtxByFD(h)
	if err != nil {
		return nil, err
	}

	tlog.Debug("AVL OUT_OPEN", "url", tc.url, "h", h, "stream_index", stream_index, "seg_index", seg_index)

	file, err := os.Create(fmt.Sprintf("%s_%d_%03d.mp4", tc.url, stream_index, seg_index))
	if err != nil {
		return nil, err
	}

	oh := &outputCtx{tc: tc, w: file, file: file}
	return oh, nil
}

func (o *outputCtx) Write(buf []byte) (int, error) {
	if verboseLogging {
		tlog.Debug("AVL OUT_WRITE", "url", o.tc.url, "len", len(buf))
	}
	n, err := o.w.Write(buf)
	if err != nil {
		return n, err
	}
	if o.tc.bytesWritten == 0 {
		o.tc.rwDiffMax = o.tc.bytesRead - o.tc.bytesWritten
		tlog.Debug("AVL OUT_WRITE FIRST", "url", o.tc.url, "bytesRead", o.tc.bytesRead, "bytesWritten", o.tc.bytesWritten, "diff", o.tc.rwDiffMax)
	}
	o.tc.bytesWritten += n
	if o.tc.bytesRead-o.tc.bytesWritten > o.tc.rwDiffMax {
		o.tc.rwDiffMax = o.tc.bytesRead - o.tc.bytesWritten
	}
	if verboseLogging {
		tlog.Debug("AVL OUT_WRITE DONE", "url", o.tc.url, "len", len(buf), "n", n, "err", err,
			"bytesRead", o.tc.bytesRead, "bytesWritten", o.tc.bytesWritten, "diff", o.tc.rwDiffMax)
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
