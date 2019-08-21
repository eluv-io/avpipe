package live

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"testing"

	"github.com/qluvio/avpipe"
)

var testUrl string = "http://origin1.sedev02_newsdemuxclear.stage-cdhls.skydvn.com/cdsedev04demuxclearnews/13012/cd.m3u8"

func TestToolTs(t *testing.T) {

	sourceUrlStr := testUrl
	pipe := "lhr_out.ts"

	sourceUrl, err := url.Parse(sourceUrlStr)
	if err != nil {
		t.Fail()
	}

	f, err := os.Create(pipe)
	defer f.Close()

	lhr := NewLiveHlsReader(sourceUrl)

	lhr.Fill(-1, 12, f)

}

type testCtx struct {
	w io.Writer
	r io.Reader
}

func TestToolFmp4(t *testing.T) {

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

	lhr := NewLiveHlsReader(sourceUrl)

	go func() {
		lhr.Fill(-1, 1, pw)
	}()

	params := &avpipe.TxParams{
		Format:          "fmp4",
		StartTimeTs:     0,
		DurationTs:      -1,
		StartSegmentStr: "1",
		VideoBitrate:    2560000,
		AudioBitrate:    64000,
		SampleRate:      44100,
		CrfStr:          "23",
		SegDurationTs:   1001 * 60,
		SegDurationFr:   60,
		Ecodec:          "libx264",
		EncHeight:       1080,
		EncWidth:        1920,
	}

	avpipe.InitIOHandler(&inputOpener{tc: readCtx}, &outputOpener{tc: writeCtx})

	filename := "ss_out.mp4"
	errTx := avpipe.Tx(params, filename, false, false)
	fmt.Println("AVLIVE TX DONE", "err", errTx)
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
	fmt.Println("IN_OPEN", "fd", fd, "url", url)
	io.url = url
	etxInput := &inputCtx{
		r: io.tc.r,
	}
	return etxInput, nil
}

func (i *inputCtx) Read(buf []byte) (int, error) {
	fmt.Println("IN_READ", "len", len(buf))
	n, err := i.r.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	return n, err
}

func (i *inputCtx) Seek(offset int64, whence int) (int64, error) {
	fmt.Println("IN_SEEK")
	return -1, nil
}

func (i *inputCtx) Close() error {
	fmt.Println("IN_CLOSE")
	return nil
}

func (i *inputCtx) Size() int64 {
	fmt.Println("IN_SIZE")
	return 120000000
}

type outputOpener struct {
	tc testCtx
}

type outputCtx struct {
	w io.Writer
}

func (oo *outputOpener) Open(h, fd int64, stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	fmt.Println("OUT_OPEN", "fd", fd)
	oh := &outputCtx{w: oo.tc.w}
	return oh, nil
}

func (o *outputCtx) Write(buf []byte) (int, error) {
	fmt.Println("OUT_WRITE")
	return o.w.Write(buf)
}

func (o *outputCtx) Seek(offset int64, whence int) (int64, error) {
	fmt.Println("OUT_SEEK")
	return -1, nil
}

func (o *outputCtx) Close() error {
	fmt.Println("OUT_CLOSE")
	return nil
}
