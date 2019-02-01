package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/qluvio/avpipe"
)

//Implement AVPipeInputOpener
type avcmdInputOpener struct {
	url string
}

func (io *avcmdInputOpener) Open(url string) (avpipe.InputHandler, error) {
	f, err := os.Open(url)
	if err != nil {
		return nil, err
	}

	io.url = url
	etxInput := &avcmdInput{
		file: f,
	}

	return etxInput, nil
}

// Implement InputHandler
type avcmdInput struct {
	file *os.File // Input file
}

func (i *avcmdInput) Read(buf []byte) (int, error) {
	n, err := i.file.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	return n, err
}

func (i *avcmdInput) Seek(offset int64, whence int) (int64, error) {
	n, err := i.file.Seek(int64(offset), int(whence))
	return n, err
}

func (i *avcmdInput) Close() error {
	err := i.file.Close()
	return err
}

func (i *avcmdInput) Size() int64 {
	fi, err := i.file.Stat()
	if err != nil {
		return -1
	}
	return fi.Size()
}

//Implement AVPipeOutputOpener
type avcmdOutputOpener struct {
}

func (oo *avcmdOutputOpener) Open(stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	var filename string

	switch out_type {
	case avpipe.DASHVideoInit:
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./O/init-stream%d.mp4", stream_index)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./O/dash.mpd")
	case avpipe.DASHVideoSegment:
		fallthrough
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./O/chunk-stream%d-%05d.mp4", stream_index, seg_index)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./O/master.m3u8")
	case avpipe.HLSVideoM3U:
		fallthrough
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./O/media_%d.m3u8", stream_index)
	}

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}

	h := &avcmdOutput{
		url:          filename,
		stream_index: stream_index,
		seg_index:    seg_index,
		file:         f}

	return h, nil
}

// Implement OutputHandler
type avcmdOutput struct {
	url          string
	stream_index int
	seg_index    int
	file         *os.File
}

func (o *avcmdOutput) Write(buf []byte) (int, error) {
	n, err := o.file.Write(buf)
	return n, err
}

func (o *avcmdOutput) Seek(offset int64, whence int) (int64, error) {
	n, err := o.file.Seek(offset, whence)
	return n, err
}

func (o *avcmdOutput) Close() error {
	err := o.file.Close()
	return err
}

type filenameFlag struct {
	set   bool
	value string
}

func (f *filenameFlag) Set(filename string) error {
	f.value = filename
	f.set = true
	return nil
}

func (f *filenameFlag) String() string {
	return f.value
}

func main() {
	var filename filenameFlag

	flag.Var(&filename, "filename", "filename for transcoding (output goes to ./O)")
	flag.Parse()

	if !filename.set {
		flag.Usage()
		return
	}

	params := &avpipe.TxParams{
		Format:             "hls",
		StartTimeTs:        0,
		DurationTs:         -1,
		StartSegmentStr:    "1",
		VideoBitrate:       2560000,
		AudioBitrate:       64000,
		SampleRate:         44100,
		CrfStr:             "23",
		SegDurationTs:      1001 * 60,
		SegDurationFr:      60,
		SegDurationSecsStr: "2.002",
		Codec:              "libx264",
		EncHeight:          720,
		EncWidth:           1280,
	}

	avpipe.InitIOHandler(&avcmdInputOpener{url: filename.value}, &avcmdOutputOpener{})

	err := avpipe.Tx(params, filename.value)
	if err != 0 {
		fmt.Fprintf(os.Stderr, "Failed transcoding %s\n", filename.value)
	}
}
