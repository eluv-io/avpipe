package main

import (
	"flag"
	"fmt"
	"os"
	"avpipe"
)

//Implement AVPipeInputOpener
type avPipeEtxInputOpener struct {
	url string
}

func (io *avPipeEtxInputOpener) Open(url string) (avpipe.AVPipeInputInterface, error) {
	f, err := os.Open(url)
	if err != nil {
		return nil, err
	}

	io.url = url
	etxInput := &avPipeEtxInput{
		file: f,
	}

	return etxInput, nil
}

// Implement AVPipeInputInterface
type avPipeEtxInput struct {
	file *os.File // Input file
}

func (i *avPipeEtxInput) Read(buf []byte) (int, error) {
	n, err := i.file.Read(buf)
	return n, err
}

func (i *avPipeEtxInput) Seek(offset int64, whence int) (int64, error) {
	n, err := i.file.Seek(int64(offset), int(whence))
	return n, err
}

func (i *avPipeEtxInput) Close() error {
	err := i.file.Close()
	return err
}

//Implement AVPipeOutputOpener
type avPipeEtxOutputOpener struct {
}

func (oo *avPipeEtxOutputOpener) Open(stream_index, seg_index int, url string) (avpipe.AVPipeOutputInterface, error) {
	f, err := os.OpenFile(url, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}

	h := &avPipeEtxOutput{
		url:          url,
		stream_index: stream_index,
		seg_index:    seg_index,
		file:         f}

	return h, nil
}

// Implement AVPipeOutputInterface
type avPipeEtxOutput struct {
	url          string
	stream_index int
	seg_index    int
	file         *os.File
}

func (o *avPipeEtxOutput) Write(buf []byte) (int, error) {
	n, err := o.file.Write(buf)
	return n, err
}

func (o *avPipeEtxOutput) Seek(offset int64, whence int) (int64, error) {
	n, err := o.file.Seek(offset, whence)
	return n, err
}

func (o *avPipeEtxOutput) Close() error {
	err := o.file.Close()
	return err
}

// TxParams should match with txparams_t in C library
type TxParams struct {
	startTimeTs        int32
	durationTs         int32
	startSegmentStr    []byte
	videoBitrate       int32
	audioBitrate       int32
	sampleRate         int32
	crfStr             []byte
	segDurationTs      int32
	segDurationFr      int32
	segDurationSecsStr []byte
	codec              []byte
	encHeight          int32
	encWidth           int32
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

/*
    TODO: pass params from go to C
	params := &C.TxParams{
		startTimeTs:        0,
		durationTs:         -1,
		startSegmentStr:    C.CString("1"),
		videoBitrate:       2560000,
		audioBitrate:       64000,
		sampleRate:         44100,
		crfStr:             C.CString("23"),
		segDurationTs:      1001 * 60,
		segDurationFr:      60,
		segDurationSecsStr: C.CString("2.002"),
		codec:              C.CString("libx264"),
		encHeight:          720,
		encWidth:           1280,
	} */

	avpipe.InitAVPipeIOHandler(&avPipeEtxInputOpener{url: filename.value}, &avPipeEtxOutputOpener{})
	err := avpipe.Tx(nil, filename.value)
	if err != 0 {
		fmt.Fprintf(os.Stderr, "Failed transcoding %s\n", filename.value)
	}
}
