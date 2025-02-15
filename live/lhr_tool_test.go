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
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/errors-go"
	elog "github.com/eluv-io/log-go"
	"github.com/stretchr/testify/assert"
)

// Test Streams
//   - FFmpeg HLS TS stream (separate a/v): ffmpeg -re -f lavfi -i sine=b=2 -f lavfi -i testsrc -map 0:a -map 1:v -f hls -hls_time 6 -c:a aac -ac 2 -c:v h264_videotoolbox -vf scale=1280:720 -profile:v high -pix_fmt yuv420p -r 25 -g 50 -force_key_frames "expr:gte(t,n_forced*2)" -var_stream_map "a:0,name:audio,agroup:audio v:0,name:video,agroup:audio" -hls_segment_filename "%v/%d.ts" -master_pl_name master.m3u8 "%v/playlist.m3u8"
//     (use local HTTP server, e.g. http-server . -p 80 --cors)
//   - FFmpeg HLS TS stream (muxed a/v): ffmpeg -re -f lavfi -i sine=b=2 -f lavfi -i testsrc -map 0:a -map 1:v -f hls -hls_time 6 -c:a aac -ac 2 -c:v h264_videotoolbox -vf scale=1280:720 -profile:v high -pix_fmt yuv420p -r 25 -g 50 -force_key_frames "expr:gte(t,n_forced*2)" -var_stream_map "v:0,a:0,name:muxed" -hls_segment_filename "%v/%d.ts" -master_pl_name master.m3u8 "%v/playlist.m3u8"
//   - Sky 1080 stream: http://origin1.sedev02_newsdemuxclear.stage-cdhls.skydvn.com/cdsedev04demuxclearnews/13012/cd.m3u8
//   - Sky 720 stream: http://origin1.skynews.mobile.skydvn.com/skynews/1404/latest.m3u8
//
// To save HLS files, add the following to the test:
//
//	TESTSaveToDir = "~/temp"
//
// Akamai live stream
const manifestURLStr = "https://moctobpltc-i.akamaihd.net/hls/live/571329/eight/playlist.m3u8"
const debugFrameLevel = false

const baseOutPath = "test_out"

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

// Implement AVPipeInputOpener
type inputOpener struct {
	dir string
	url string
	tc  testCtx
}

type inputCtx struct {
	tc *testCtx
	r  io.Reader
}

type outputOpener struct {
	tc  *testCtx
	dir string
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

func TestHLSVideoOnly(t *testing.T) {
	params := &avpipe.XcParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		VideoBitrate:    5000000,
		SegDuration:     "30.03",
		ForceKeyInt:     60,
		Ecodec:          defaultVideoEncoder(),
		EncHeight:       720,
		EncWidth:        1280,
		XcType:          avpipe.XcVideo,
		DebugFrameLevel: debugFrameLevel,
		StreamId:        -1,
	}

	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}
	readers, err := NewHLSReaders(manifestURL, avpipe.XcVideo) //readers, err := NewHLSReaders(manifestURL, STVideoOnly)
	if err != nil {
		t.Error(err)
	}
	reader := readers[0]
	endChan := make(chan error, 1)
	reader.Start(endChan)

	tlog.Info("Xc start", "params", fmt.Sprintf("%+v", *params))
	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{dir: outputDir})
	url := "video_hls"
	params.Url = url
	reqCtx := &testCtx{url: url, r: reader.Pipe}
	putReqCtxByURL(url, reqCtx)
	err = avpipe.Xc(params)
	tlog.Info("Xc done", "err", err)
	if err != nil {
		t.Error("video transcoding error", "errXc", err)
	}

	log.Call(reader.Pipe.Close, "close hls reader", tlog.Error)
	err = <-endChan
	tlog.Info("HLSReader done", "err", err)
	if err != nil {
		t.Error("HLSReader error", "err", err)
	}
}

func TestHLSAudioOnly(t *testing.T) {
	params := &avpipe.XcParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		AudioBitrate:    128000,
		SampleRate:      48000,
		SegDuration:     "30",
		Ecodec2:         "aac", // "ac3", "aac"
		XcType:          avpipe.XcAudio,
		DebugFrameLevel: debugFrameLevel,
		StreamId:        -1,
	}

	params.AudioIndex = []int32{1}

	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}
	readers, err := NewHLSReaders(manifestURL, avpipe.XcAudio)
	if err != nil {
		t.Error(err)
	}
	reader := readers[0]
	endChan := make(chan error, 1)
	reader.Start(endChan)

	tlog.Info("Xc start", "params", fmt.Sprintf("%+v", *params))
	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{dir: outputDir})
	url := "audio_hls"
	params.Url = url
	reqCtx := &testCtx{url: url, r: reader.Pipe}
	putReqCtxByURL(url, reqCtx)
	err = avpipe.Xc(params)
	tlog.Info("Xc done", "err", err)
	if err != nil {
		t.Error("video transcoding error", "errXc", err)
	}

	log.Call(reader.Pipe.Close, "close hls reader", tlog.Error)
	err = <-endChan
	tlog.Info("HLSReader done", "err", err)
	if err != nil {
		t.Error("HLSReader error", "err", err)
	}
}

// Creates 3 audio and 3 video HLS mez files in "test_out/" (the source is a live hls stream)
// Then creates DASH abr-segments for each generated audio/video mez file.
// All the output files will be saved in directory determined by outputDir.
func TestHLSAudioVideoLive(t *testing.T) {
	setupLogging()
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	manifestURL, err := url.Parse(manifestURLStr)
	if err != nil {
		t.Error(err)
	}
	readers, err := NewHLSReaders(manifestURL, avpipe.XcNone)
	if err != nil {
		t.Error(err)
	}
	reader := readers[0]
	endChan := make(chan error, 1)
	reader.Start(endChan)
	audioReader := NewRWBuffer(10000)
	videoReader := io.TeeReader(reader.Pipe, audioReader)

	done := make(chan bool, 2)
	avpipe.InitIOHandler(&inputOpener{}, &outputOpener{dir: outputDir})

	audioParams := &avpipe.XcParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		AudioBitrate:    128000,
		SampleRate:      48000,
		SegDuration:     "30",
		Ecodec2:         "aac",
		XcType:          avpipe.XcAudio,
		DebugFrameLevel: debugFrameLevel,
		StreamId:        -1,
	}

	audioParams.AudioIndex = []int32{1}

	go func(reader io.Reader) {
		tlog.Info("audio mez Xc start", "params", fmt.Sprintf("%+v", *audioParams))
		url := "audio_mez_hls"
		audioParams.Url = url
		reqCtx := &testCtx{url: url, r: reader}
		putReqCtxByURL(url, reqCtx)
		err := avpipe.Xc(audioParams)
		tlog.Info("audio mez Xc done", "err", err)
		if err != nil {
			t.Error("audio mez transcoding error", "err", err)
		}
		done <- true
	}(audioReader)

	videoParams := &avpipe.XcParams{
		Format:          "fmp4-segment",
		DurationTs:      3 * 2700000,
		StartSegmentStr: "1",
		VideoBitrate:    5000000,
		SegDuration:     "30",
		ForceKeyInt:     60,
		Ecodec:          defaultVideoEncoder(),
		EncHeight:       720,
		EncWidth:        1280,
		XcType:          avpipe.XcVideo,
		Url:             "video_mez_hls",
		DebugFrameLevel: debugFrameLevel,
		StreamId:        -1,
	}
	go func(reader io.Reader) {
		tlog.Info("video mez Xc start", "params", fmt.Sprintf("%+v", *videoParams))
		reqCtx := &testCtx{url: videoParams.Url, r: reader}
		putReqCtxByURL(videoParams.Url, reqCtx)
		err := avpipe.Xc(videoParams)
		tlog.Info("video mez Xc done", "err", err)
		if err != nil {
			t.Error("video mez transcoding error", "err", err)
		}
		done <- true
	}(videoReader)

	// Wait for audio/video mez making to be finished
	<-done
	<-done
	log.Call(reader.Pipe.Close, "close hls reader", tlog.Error)
	err = <-endChan
	tlog.Info("HLSReader done", "err", err)
	if err != nil {
		t.Error("HLSReader error", "err", err)
	}

	// Create audio dash segments out of audio mezzanines
	audioParams.Format = "dash"
	audioParams.AudioIndex = []int32{0}
	audioParams.AudioSegDurationTs = 2 * 48000
	audioMezFiles := [3]string{"audio-mez-segment-1.mp4", "audio-mez-segment-2.mp4", "audio-mez-segment-3.mp4"}
	go func() {
		for i, url := range audioMezFiles {
			audioParams.Url = outputDir + "/" + url
			tlog.Info("audio dash Xc start", "params", fmt.Sprintf("%+v", *audioParams), "url", audioParams.Url)
			reqCtx := &testCtx{url: audioParams.Url}
			putReqCtxByURL(audioParams.Url, reqCtx)
			audioParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Xc(audioParams)
			tlog.Info("audio dash Xc done", "err", err)
			if err != nil {
				t.Error("audio dash transcoding error", "err", err, "url", audioParams.Url)
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
	videoMezFiles := [3]string{"video-mez-segment-1.mp4", "video-mez-segment-2.mp4", "video-mez-segment-3.mp4"}
	go func() {
		for i, url := range videoMezFiles {
			videoParams.Url = outputDir + "/" + url
			tlog.Info("video dash Xc start", "videoParams", fmt.Sprintf("%+v", *videoParams), "url", videoParams.Url)
			reqCtx := &testCtx{url: videoParams.Url}
			putReqCtxByURL(videoParams.Url, reqCtx)
			videoParams.StartSegmentStr = fmt.Sprintf("%d", i*15+1)
			err := avpipe.Xc(videoParams)
			tlog.Info("video dash Xc done", "err", err)
			if err != nil {
				t.Error("video dash transcoding error", "err", err, "url", videoParams.Url)
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

	if (len(url) >= 4 && url[0:4] == "rtmp") || (len(url) >= 3 && url[0:3] == "udp") {
		tc.fd = fd
		putReqCtxByFD(fd, tc)
		return &inputCtx{tc: tc}, nil
	}

	tc.fd = fd
	putReqCtxByFD(fd, tc)

	var file *os.File
	// A convention for abr segmentation in our tests
	if strings.Contains(url, "mez") && strings.Contains(url, "mp4") {
		file, err = os.Open(url)
		if err != nil {
			tlog.Error("Failed to open", "file", url)
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
	if debugFrameLevel {
		tlog.Debug("IN_READ", "url", i.tc.url, "len", len(buf))
	}
	n, err := i.r.Read(buf)
	if err == io.EOF {
		tlog.Info("IN_READ got EOF", "url", i.tc.url)
		return 0, err
	}
	i.tc.bytesRead += n
	if debugFrameLevel {
		tlog.Debug("IN_READ DONE", "url", i.tc.url, "len", len(buf), "n", n,
			"bytesRead", i.tc.bytesRead, "bytesWritten", i.tc.bytesWritten, "err", err)
	}
	return n, err
}

func (i *inputCtx) Seek(offset int64, whence int) (int64, error) {
	if i.tc.url[0:3] == "udp" {
		tlog.Error("IN_SEEK", "url", i.tc.url)
		return 0, fmt.Errorf("IN_SEEK url=%s", i.tc.url)
	}

	file, ok := i.r.(*os.File)
	if !ok {
		tlog.Debug("Seek() not allowed on non-file input", "url", i.tc.url)
		return 0, fmt.Errorf("IN_SEEK url=%s", i.tc.url)
	}

	n, err := file.Seek(offset, whence)
	if debugFrameLevel {
		tlog.Debug("IN_SEEK", "url", i.tc.url, "fd", i.tc.fd, "n", n, "err", err)
	}
	return n, err
}

func (i *inputCtx) Close() (err error) {
	tlog.Debug("IN_CLOSE", "url", i.tc.url)

	if i.tc.wc != nil {
		if debugFrameLevel {
			tlog.Debug("IN_CLOSE closing write side", "url", i.tc.url)
		}
		err = i.tc.wc.Close()
	}

	if _, ok := i.r.(*os.File); ok {
		err = i.r.(*os.File).Close()
	} else if _, ok := i.r.(*RWBuffer); ok {
		if debugFrameLevel {
			tlog.Debug("IN_CLOSE closing RWBuffer", "url", i.tc.url)
		}
		err = i.r.(*RWBuffer).Close()
	} else if _, ok := i.r.(*io.PipeReader); ok {
		err = i.r.(*io.PipeReader).Close()
	}
	return
}

func (i *inputCtx) Size() int64 {
	if (len(i.tc.url) > 3 && i.tc.url[0:3] == "udp") ||
		(len(i.tc.url) > 4 && i.tc.url[0:4] == "rtmp") {
		if debugFrameLevel {
			tlog.Debug("IN_SIZE", "url", i.tc.url)
		}
		return -1
	}

	file, ok := i.r.(*os.File)
	if !ok {
		tlog.Debug("Size() not allowed on non-file input", "url", i.tc.url)
		return -1
	}

	fi, err := file.Stat()
	if err == nil {
		tlog.Debug("IN_SIZE", "url", i.tc.url, "fd", i.tc.fd, "size", fi.Size(), "err", err)
		return fi.Size()
	}
	tlog.Debug("IN_SIZE", "url", i.tc.url, "fd", i.tc.fd, "err", err)
	return -1
}

func (i *inputCtx) Stat(streamIndex int, statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_IN_STAT_BYTES_READ:
		readOffset := statArgs.(*uint64)
		if debugFrameLevel {
			log.Debug("STAT read offset", *readOffset, "streamIndex", streamIndex)
		}
	}
	return nil
}

func (oo *outputOpener) Open(h, fd int64, streamIndex, segIndex int, _ int64,
	outType avpipe.AVType) (avpipe.OutputHandler, error) {

	tc, err := getReqCtxByFD(h)
	if err != nil {
		return nil, err
	}

	url := tc.url
	if strings.Compare(url[len(url)-3:], "mp4") == 0 {
		url = url[:len(url)-4]
	}

	var filename string

	switch outType {
	case avpipe.DASHVideoInit:
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/video-init-stream%d.mp4", oo.dir, streamIndex)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", oo.dir)
	case avpipe.DASHVideoSegment:
		filename = fmt.Sprintf("./%s/video-chunk-stream%d-%05d.mp4", oo.dir, streamIndex, segIndex)
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/audio-chunk-stream%d-%05d.mp4", oo.dir, streamIndex, segIndex)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./%s/master.m3u8", oo.dir)
	case avpipe.HLSVideoM3U:
		filename = fmt.Sprintf("./%s/video-media_%d.m3u8", oo.dir, streamIndex)
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./%s/audio-media_%d.m3u8", oo.dir, streamIndex)
	case avpipe.AES128Key:
		filename = fmt.Sprintf("./%s/%s-key.bin", oo.dir, url)
	case avpipe.MP4Segment:
		filename = fmt.Sprintf("./%s/segment-%d.mp4", oo.dir, segIndex)
	case avpipe.FMP4AudioSegment:
		filename = fmt.Sprintf("./%s/audio-mez-segment%d-%d.mp4", oo.dir, streamIndex, segIndex)
	case avpipe.FMP4VideoSegment:
		filename = fmt.Sprintf("./%s/video-mez-segment-%d.mp4", oo.dir, segIndex)
	}

	tlog.Debug("OUT_OPEN", "url", tc.url, "h", h, "streamIndex", streamIndex, "segIndex", segIndex, "filename", filename, "outType", outType)

	file, err := os.Create(filename)
	if err != nil {
		return nil, err
	}

	oh := &outputCtx{tc: tc, w: file, file: file}
	return oh, nil
}

func (o *outputCtx) Write(buf []byte) (int, error) {
	if debugFrameLevel {
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
	if debugFrameLevel {
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

func (o *outputCtx) Stat(streamIndex int, avType avpipe.AVType, statType avpipe.AVStatType, statArgs interface{}) error {
	doLog := func(args ...interface{}) {
		if debugFrameLevel {
			logArgs := []interface{}{"stat", statType.Name(), "avType", avType.Name(), "streamIndex", streamIndex}
			logArgs = append(logArgs, args...)
			log.Debug("STAT", logArgs...)
		}
	}

	switch statType {
	case avpipe.AV_OUT_STAT_BYTES_WRITTEN:
		writeOffset := statArgs.(*uint64)
		doLog("write offset", *writeOffset)
	case avpipe.AV_OUT_STAT_ENCODING_END_PTS:
		endPTS := statArgs.(*uint64)
		doLog("endPTS", *endPTS)
	case avpipe.AV_OUT_STAT_START_FILE:
		segIdx := statArgs.(*int)
		doLog("segIdx", *segIdx)
	case avpipe.AV_OUT_STAT_END_FILE:
		segIdx := statArgs.(*int)
		doLog("segIdx", *segIdx)
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

func failNowOnError(t *testing.T, err error) {
	if err != nil {
		assert.NoError(t, err)
		t.FailNow()
	}
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

// fn returns the caller's function name, e.g. pkg.Foo
func fn() (fname string) {
	fname = "unknown"
	if pc, _, _, ok := runtime.Caller(1); ok {
		if f := runtime.FuncForPC(pc); f != nil {
			fname = path.Base(f.Name())
		}
	}
	return
}

func setupOutDir(t *testing.T, dir string) {

	var err error
	if _, err = os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(dir, 0755)
		}
	} else {
		err = removeDirContents(dir)
	}
	failNowOnError(t, err)
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
