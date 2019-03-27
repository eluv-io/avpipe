package avpipe_test

import (
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/qluvio/avpipe"
)

//Implement AVPipeInputOpener
type fileInputOpener struct {
	url string
}

func (io *fileInputOpener) Open(fd int64, url string) (avpipe.InputHandler, error) {
	f, err := os.Open(url)
	if err != nil {
		return nil, err
	}

	io.url = url
	etxInput := &fileInput{
		file: f,
	}

	return etxInput, nil
}

// Implement InputHandler
type fileInput struct {
	file *os.File // Input file
}

func (i *fileInput) Read(buf []byte) (int, error) {
	n, err := i.file.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	return n, err
}

func (i *fileInput) Seek(offset int64, whence int) (int64, error) {
	n, err := i.file.Seek(int64(offset), int(whence))
	return n, err
}

func (i *fileInput) Close() error {
	err := i.file.Close()
	return err
}

func (i *fileInput) Size() int64 {
	fi, err := i.file.Stat()
	if err != nil {
		return -1
	}
	return fi.Size()
}

//Implement AVPipeOutputOpener
type fileOutputOpener struct {
	dir string
}

func (oo *fileOutputOpener) Open(h, fd int64, stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	var filename string

	switch out_type {
	case avpipe.DASHVideoInit:
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/init-stream%d.mp4", oo.dir, stream_index)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", oo.dir)
	case avpipe.DASHVideoSegment:
		fallthrough
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/chunk-stream%d-%05d.mp4", oo.dir, stream_index, seg_index)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./%s/master.m3u8", oo.dir)
	case avpipe.HLSVideoM3U:
		fallthrough
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./%s/media_%d.m3u8", oo.dir, stream_index)
	}

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}

	oh := &fileOutput{
		url:          filename,
		stream_index: stream_index,
		seg_index:    seg_index,
		file:         f}

	return oh, nil
}

//Implement AVPipeOutputOpener
type concurrentOutputOpener struct {
	dir string
}

func (coo *concurrentOutputOpener) Open(h, fd int64, stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	var filename string
	dir := fmt.Sprintf("%s/O%d", coo.dir, h)

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.Mkdir(dir, 0755)
	}

	switch out_type {
	case avpipe.DASHVideoInit:
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/init-stream%d.mp4", dir, stream_index)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", dir)
	case avpipe.DASHVideoSegment:
		fallthrough
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/chunk-stream%d-%05d.mp4", dir, stream_index, seg_index)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./%s/master.m3u8", dir)
	case avpipe.HLSVideoM3U:
		fallthrough
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./%s/media_%d.m3u8", dir, stream_index)
	}

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}

	oh := &fileOutput{
		url:          filename,
		stream_index: stream_index,
		seg_index:    seg_index,
		file:         f}

	return oh, nil
}

// Implement OutputHandler
type fileOutput struct {
	url          string
	stream_index int
	seg_index    int
	file         *os.File
}

func (o *fileOutput) Write(buf []byte) (int, error) {
	n, err := o.file.Write(buf)
	return n, err
}

func (o *fileOutput) Seek(offset int64, whence int) (int64, error) {
	n, err := o.file.Seek(offset, whence)
	return n, err
}

func (o *fileOutput) Close() error {
	err := o.file.Close()
	return err
}

func TestSingleTranscode(t *testing.T) {
	filename := "./media/ErsteChristmas.mp4"

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
		Ecodec:             "libx264",
		EncHeight:          720,
		EncWidth:           1280,
	}

	// Create output directory if it doesn't exist
	if _, err := os.Stat("./O"); os.IsNotExist(err) {
		os.Mkdir("./O", 0755)
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &fileOutputOpener{dir: "O"})

	err := avpipe.Tx(params, filename, false)
	if err != 0 {
		t.Fail()
	}
}

func doTranscode(t *testing.T, p *avpipe.TxParams, nThreads int, filename string, reportFailure string) {
	// Create output directory if it doesn't exist
	if _, err := os.Stat("./O"); os.IsNotExist(err) {
		os.Mkdir("./O", 0755)
	}

	avpipe.InitIOHandler(&fileInputOpener{url: filename}, &concurrentOutputOpener{dir: "O"})

	done := make(chan struct{})
	for i := 0; i < nThreads; i++ {
		go func(params *avpipe.TxParams, filename string) {
			err := avpipe.Tx(params, filename, false)
			done <- struct{}{} // Signal the main goroutine
			if err != 0 && reportFailure == "" {
				t.Fail()
			} else if err != 0 {
				fmt.Printf("%s\n", reportFailure)
			}
		}(p, filename)
	}

	for i := 0; i < nThreads; i++ {
		<-done // Wait for background goroutines to finish
	}
}

func TestNvidiaTranscode(t *testing.T) {
	nThreads := 10
	filename := "./media/rocky.mp4"

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
		Ecodec:             "h264_nvenc",
		EncHeight:          720,
		EncWidth:           1280,
	}

	doTranscode(t, params, nThreads, filename, "H264_NVIDIA encoder might not be enabled or hardware might not be available")
}

func TestConcurrentTranscode(t *testing.T) {
	nThreads := 10
	filename := "./media/rocky.mp4"

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
		Ecodec:             "libx264",
		EncHeight:          720,
		EncWidth:           1280,
	}

	doTranscode(t, params, nThreads, filename, "")

}
