package live

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/qluvio/avpipe"
	"github.com/qluvio/content-fabric/errors"
	elog "github.com/qluvio/content-fabric/log"
	"github.com/qluvio/content-fabric/util/ioutil"
)

//
// Test Streams
// * FFmpeg HLS TS stream (separate a/v): ffmpeg -re -f lavfi -i sine=b=2 -f lavfi -i testsrc -map 0:a -map 1:v -f hls -hls_time 6 -c:a aac -ac 2 -c:v h264_videotoolbox -vf scale=1280:720 -profile:v high -pix_fmt yuv420p -r 25 -g 50 -force_key_frames "expr:gte(t,n_forced*2)" -var_stream_map "a:0,name:audio,agroup:audio v:0,name:video,agroup:audio" -hls_segment_filename "%v/%d.ts" -master_pl_name master.m3u8 "%v/playlist.m3u8"
//   (use local HTTP server, e.g. http-server . -p 80 --cors)
// * FFmpeg HLS TS stream (muxed a/v): ffmpeg -re -f lavfi -i sine=b=2 -f lavfi -i testsrc -map 0:a -map 1:v -f hls -hls_time 6 -c:a aac -ac 2 -c:v h264_videotoolbox -vf scale=1280:720 -profile:v high -pix_fmt yuv420p -r 25 -g 50 -force_key_frames "expr:gte(t,n_forced*2)" -var_stream_map "v:0,a:0,name:muxed" -hls_segment_filename "%v/%d.ts" -master_pl_name master.m3u8 "%v/playlist.m3u8"
// * Sky 1080 stream: http://origin1.sedev02_newsdemuxclear.stage-cdhls.skydvn.com/cdsedev04demuxclearnews/13012/cd.m3u8
// * Sky 720 stream: http://origin1.skynews.mobile.skydvn.com/skynews/1404/latest.m3u8
//
// To save HLS files, add the following to the test:
//   TESTSaveToDir = "~/temp"
//
const manifestURLStr = "http://origin1.skynews.mobile.skydvn.com/skynews/1404/latest.m3u8"
const verboseLogging = false

var tlog = elog.Get("/eluvio/avpipe/live/test")
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
	wc           io.WriteCloser
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

func _TestHLSVideoOnly(t *testing.T) {
	params := &avpipe.TxParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		VideoBitrate:    5000000,
		SegDuration:     "30",
		ForceKeyInt:     50,
		Ecodec:          defaultVideoEncoder(),
		EncHeight:       720,
		EncWidth:        1280,
		TxType:          avpipe.TxVideo,
	}

	setupLogging()
	setupOutDir("./O")

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}
	readers, err := NewHLSReaders(manifestURL, avpipe.TxVideo) //readers, err := NewHLSReaders(manifestURL, STVideoOnly)
	if err != nil {
		t.Error(err)
	}
	reader := readers[0]
	endChan := make(chan error, 1)
	reader.Start(endChan)

	tlog.Info("Tx start", "params", fmt.Sprintf("%+v", *params))
	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})
	url := "video_hls"
	reqCtx := &testCtx{url: url, r: reader.Pipe}
	putReqCtxByURL(url, reqCtx)
	err = avpipe.Tx(params, url, verboseLogging)
	tlog.Info("Tx done", "err", err)
	if err != nil {
		t.Error("video transcoding error", "errTx", err)
	}

	ioutil.CloseCloser(reader.Pipe, tlog)
	err = <-endChan
	tlog.Info("HLSReader done", "err", err)
	if err != nil {
		t.Error("HLSReader error", "err", err)
	}
}

func _TestHLSAudioOnly(t *testing.T) {
	params := &avpipe.TxParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		AudioBitrate:    128000,
		SampleRate:      48000,
		SegDuration:     "30",
		Ecodec:          "aac", // "ac3", "aac"
		Dcodec:          "aac",
		TxType:          avpipe.TxAudio,
		//BypassTranscoding: true,
	}

	params.NumAudio = 1
	params.AudioIndex[0] = 0

	setupLogging()
	setupOutDir("./O")

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}
	readers, err := NewHLSReaders(manifestURL, avpipe.TxAudio)
	if err != nil {
		t.Error(err)
	}
	reader := readers[0]
	endChan := make(chan error, 1)
	reader.Start(endChan)

	tlog.Info("Tx start", "params", fmt.Sprintf("%+v", *params))
	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})
	url := "audio_hls"
	reqCtx := &testCtx{url: url, r: reader.Pipe}
	putReqCtxByURL(url, reqCtx)
	err = avpipe.Tx(params, url, verboseLogging)
	tlog.Info("Tx done", "err", err)
	if err != nil {
		t.Error("video transcoding error", "errTx", err)
	}

	ioutil.CloseCloser(reader.Pipe, tlog)
	err = <-endChan
	tlog.Info("HLSReader done", "err", err)
	if err != nil {
		t.Error("HLSReader error", "err", err)
	}
}

// Creates 3 audio and 3 video HLS mez files in "./O" (the source is a live hls stream)
// Then creates DASH abr-segments for each generated audio/video mez file.
// All the output files will be saved in "./O".
func _TestAudioVideoHlsLive(t *testing.T) {
	setupLogging()
	setupOutDir("./O")

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}
	readers, err := NewHLSReaders(manifestURL, avpipe.TxNone)
	if err != nil {
		t.Error(err)
	}
	reader := readers[0]
	endChan := make(chan error, 1)
	reader.Start(endChan)
	audioReader := NewRWBuffer(10000)
	videoReader := io.TeeReader(reader.Pipe, audioReader)

	done := make(chan bool, 2)
	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{})

	audioParams := &avpipe.TxParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		AudioBitrate:    128000,
		SampleRate:      48000,
		SegDuration:     "30",
		Ecodec:          "aac", // "ac3", "aac"
		Dcodec:          "aac",
		TxType:          avpipe.TxAudio,
		//BypassTranscoding: true,
	}

	audioParams.NumAudio = 1
	audioParams.AudioIndex[0] = 1

	go func(reader io.Reader) {
		tlog.Info("audio mez Tx start", "params", fmt.Sprintf("%+v", *audioParams))
		url := "audio_mez_hls"
		reqCtx := &testCtx{url: url, r: reader}
		putReqCtxByURL(url, reqCtx)
		err := avpipe.Tx(audioParams, url, verboseLogging)
		tlog.Info("audio mez Tx done", "err", err)
		if err != nil {
			t.Error("audio mez transcoding error", "err", err)
		}
		done <- true
	}(audioReader)

	videoParams := &avpipe.TxParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		VideoBitrate:    5000000,
		SegDuration:     "30",
		ForceKeyInt:     50,
		Ecodec:          defaultVideoEncoder(),
		EncHeight:       720,
		EncWidth:        1280,
		TxType:          avpipe.TxVideo,
		//BypassTranscoding: true,
	}
	go func(reader io.Reader) {
		tlog.Info("video mez Tx start", "params", fmt.Sprintf("%+v", *videoParams))
		url := "video_mez_hls"
		reqCtx := &testCtx{url: url, r: reader}
		putReqCtxByURL(url, reqCtx)
		err := avpipe.Tx(videoParams, url, verboseLogging)
		tlog.Info("video mez Tx done", "err", err)
		if err != nil {
			t.Error("video mez transcoding error", "err", err)
		}
		done <- true
	}(videoReader)

	// Wait for audio/video mez making to be finished
	<-done
	<-done
	ioutil.CloseCloser(reader.Pipe, tlog)
	err = <-endChan
	tlog.Info("HLSReader done", "err", err)
	if err != nil {
		t.Error("HLSReader error", "err", err)
	}

	// Create audio dash segments out of audio mezzanines
	audioParams.Format = "dash"
	audioParams.AudioSegDurationTs = 2 * 48000
	audioMezFiles := [3]string{"audio_mez_hls-segment-1.mp4", "audio_mez_hls-segment-2.mp4", "audio_mez_hls-segment-3.mp4"}
	go func() {
		for i, url := range audioMezFiles {
			tlog.Info("audio dash Tx start", "params", fmt.Sprintf("%+v", *audioParams), "url", url)
			reqCtx := &testCtx{url: url}
			putReqCtxByURL(url, reqCtx)
			audioParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Tx(audioParams, url, true)
			tlog.Info("audio dash Tx done", "err", err)
			if err != nil {
				t.Error("audio dash transcoding error", "err", err, "url", url)
			}
			done <- true
		}
	}()
	for _ = range audioMezFiles {
		<-done
	}

	// Create video dash segments out of video mezzanines
	videoParams.Format = "dash"
	videoParams.VideoSegDurationTs = 2 * 90000
	videoMezFiles := [3]string{"video_mez_hls-segment-1.mp4", "video_mez_hls-segment-2.mp4", "video_mez_hls-segment-3.mp4"}
	go func() {
		for i, url := range videoMezFiles {
			tlog.Info("video dash Tx start", "videoParams", fmt.Sprintf("%+v", *videoParams), "url", url)
			reqCtx := &testCtx{url: url}
			putReqCtxByURL(url, reqCtx)
			videoParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Tx(videoParams, url, true)
			tlog.Info("video dash Tx done", "err", err)
			if err != nil {
				t.Error("video dash transcoding error", "err", err, "url", url)
			}
			done <- true
		}
	}()
	for _ = range videoMezFiles {
		<-done
	}
}

func (io *inputOpener) Open(fd int64, url string) (avpipe.InputHandler, error) {
	tlog.Debug("IN_OPEN", "fd", fd, "url", url)
	io.url = url
	tc, err := getReqCtxByURL(url)
	if err != nil {
		return nil, err
	}

	tc.fd = fd
	putReqCtxByFD(fd, tc)

	var file *os.File
	// A convention for abr segmentation in our tests
	if strings.Contains(url, "mez") && strings.Contains(url, "mp4") {
		filepath := fmt.Sprintf("O/%s", url)
		file, err = os.Open(filepath)
		if err != nil {
			tlog.Error("Failed to open", "file", filepath)
		}

		input := &inputCtx{
			tc: tc,
			r:  file,
		}
		return input, nil
	}

	input := &inputCtx{
		tc: tc,
		r:  tc.r,
	}
	return input, nil
}

func (i *inputCtx) Read(buf []byte) (int, error) {
	if verboseLogging {
		tlog.Debug("IN_READ", "url", i.tc.url, "len", len(buf))
	}
	n, err := i.r.Read(buf)
	if err == io.EOF {
		tlog.Info("IN_READ got EOF", "url", i.tc.url)
		return 0, err
	}
	i.tc.bytesRead += n
	if verboseLogging {
		tlog.Debug("IN_READ DONE", "url", i.tc.url, "len", len(buf), "n", n,
			"bytesRead", i.tc.bytesRead, "bytesWritten", i.tc.bytesWritten, "err", err)
	}
	return n, err
}

func (i *inputCtx) Seek(offset int64, whence int) (int64, error) {
	tlog.Error("IN_SEEK", "url", i.tc.url)
	return 0, fmt.Errorf("IN_SEEK url=%s", i.tc.url)
}

func (i *inputCtx) Close() (err error) {
	tlog.Debug("IN_CLOSE", "url", i.tc.url)

	if i.tc.wc != nil {
		tlog.Debug("IN_CLOSE closing write side", "url", i.tc.url)
		err = i.tc.wc.Close()
	}

	if _, ok := i.r.(*os.File); ok {
		err = i.r.(*os.File).Close()
	} else if _, ok := i.r.(*RWBuffer); ok {
		tlog.Debug("IN_CLOSE closing RWBuffer", "url", i.tc.url)
		err = i.r.(*RWBuffer).Close()
	} else if _, ok := i.r.(*io.PipeReader); ok {
		err = i.r.(*io.PipeReader).Close()
	}
	return
}

func (i *inputCtx) Size() int64 {
	tlog.Debug("IN_SIZE")
	return -1
}

func (i *inputCtx) Stat(statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_IN_STAT_BYTES_READ:
		readOffset := statArgs.(*uint64)
		log.Info("STAT read offset", *readOffset)
	}
	return nil
}

func (oo *outputOpener) Open(h, fd int64, stream_index, seg_index int, _ int64,
	out_type avpipe.AVType) (avpipe.OutputHandler, error) {

	tc, err := getReqCtxByFD(h)
	if err != nil {
		return nil, err
	}

	url := tc.url
	if strings.Compare(url[len(url)-3:], "mp4") == 0 {
		url = url[:len(url)-4]
	}

	var filename string

	switch out_type {
	case avpipe.DASHVideoInit:
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./O/%s-init-stream%d.mp4", url, stream_index)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./O/%s-dash.mpd", url)
	case avpipe.DASHVideoSegment:
		fallthrough
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./O/%s-chunk-stream%d-%05d.mp4", url, stream_index, seg_index)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./O/%s-master.m3u8", url)
	case avpipe.HLSVideoM3U:
		fallthrough
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./O/%s-media_%d.m3u8", url, stream_index)
	case avpipe.AES128Key:
		filename = fmt.Sprintf("./O/%s-key.bin", url)
	case avpipe.MP4Segment:
		fallthrough
	case avpipe.FMP4AudioSegment:
		fallthrough
	case avpipe.FMP4VideoSegment:
		filename = fmt.Sprintf("./O/%s-segment-%d.mp4", url, seg_index)
	}

	tlog.Debug("OUT_OPEN", "url", tc.url, "h", h, "stream_index", stream_index, "seg_index", seg_index, "filename", filename)

	file, err := os.Create(filename)
	if err != nil {
		return nil, err
	}

	oh := &outputCtx{tc: tc, w: file, file: file}
	return oh, nil
}

func (o *outputCtx) Write(buf []byte) (int, error) {
	if verboseLogging {
		tlog.Debug("OUT_WRITE", "url", o.tc.url, "len", len(buf))
	}
	n, err := o.w.Write(buf)
	if err != nil {
		return n, err
	}
	if o.tc.bytesWritten == 0 {
		o.tc.rwDiffMax = o.tc.bytesRead - o.tc.bytesWritten
		tlog.Debug("OUT_WRITE FIRST", "url", o.tc.url, "bytesRead", o.tc.bytesRead, "bytesWritten", o.tc.bytesWritten, "diff", o.tc.rwDiffMax)
	}
	o.tc.bytesWritten += n
	if o.tc.bytesRead-o.tc.bytesWritten > o.tc.rwDiffMax {
		o.tc.rwDiffMax = o.tc.bytesRead - o.tc.bytesWritten
	}
	if verboseLogging {
		tlog.Debug("OUT_WRITE DONE", "url", o.tc.url, "len", len(buf), "n", n, "err", err,
			"bytesRead", o.tc.bytesRead, "bytesWritten", o.tc.bytesWritten, "diff", o.tc.rwDiffMax)
	}
	return n, err
}

func (o *outputCtx) Seek(offset int64, whence int) (int64, error) {
	tlog.Debug("OUT_SEEK", "url", o.tc.url)
	//return o.file.Seek(offset, whence)
	return -1, fmt.Errorf("OUT_SEEK url=%s", o.tc.url)
}

func (o *outputCtx) Close() error {
	tlog.Debug("OUT_CLOSE")
	o.file.Close()
	return nil
}

func (o *outputCtx) Stat(avType avpipe.AVType, statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_OUT_STAT_BYTES_WRITTEN:
		writeOffset := statArgs.(*uint64)
		log.Info("STAT, write offset", *writeOffset)
	case avpipe.AV_OUT_STAT_ENCODING_END_PTS:
		endPTS := statArgs.(*uint64)
		log.Info("STAT, endPTS", *endPTS)
	}
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
	padlen := blockSize - (srclen % blockSize)
	padding := bytes.Repeat([]byte{byte(padlen)}, padlen)
	return append(src, padding...)
}

func defaultVideoEncoder() string {
	ecodec := "libx264"
	if runtime.GOOS == "darwin" {
		// h264_videotoolbox on Mac for speed
		ecodec = "h264_videotoolbox"
	}
	return ecodec
}

func removeDirContents(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}
	return nil
}

func setupOutDir(dir string) error {
	_, err := os.Stat(dir)
	if os.IsNotExist(err) {
		os.Mkdir(dir, 0755)
	} else {
		err = removeDirContents(dir)
		if err != nil {
			return err
		}
	}

	return nil
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
