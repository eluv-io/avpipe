package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/qluvio/avpipe"
	"github.com/spf13/cobra"
)

//Implement AVPipeInputOpener
type avcmdInputOpener struct {
	url string
}

func (io *avcmdInputOpener) Open(fd int64, url string) (avpipe.InputHandler, error) {
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
	dir string
}

func (oo *avcmdOutputOpener) Open(h, fd int64, stream_index, seg_index int, out_type avpipe.AVType) (avpipe.OutputHandler, error) {
	var filename string
	dir := fmt.Sprintf("%s/O%d", oo.dir, h)

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

	oh := &avcmdOutput{
		url:          filename,
		stream_index: stream_index,
		seg_index:    seg_index,
		file:         f}

	return oh, nil
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

func InitTranscode(cmdRoot *cobra.Command) error {
	cmdTranscode := &cobra.Command{
		Use:   "transcode",
		Short: "Transcode a media file",
		Long:  "Transcode a media file and produce segment files in O directory",
		//Args:  cobra.ExactArgs(1),
		RunE: doTranscode,
	}

	cmdRoot.AddCommand(cmdTranscode)

	cmdTranscode.PersistentFlags().StringP("filename", "f", "", "filename to be transcoded")
	cmdTranscode.PersistentFlags().BoolP("bypass", "b", false, "bypass transcoding")
	cmdTranscode.PersistentFlags().Int32P("threads", "t", 1, "transcoding threads")
	cmdTranscode.PersistentFlags().StringP("encoder", "e", "libx264", "encoder codec, default is 'libx264', can be: 'libx264', 'h264_nvenc', 'h264_videotoolbox'")
	cmdTranscode.PersistentFlags().StringP("decoder", "d", "h264", "decoder codec, default is 'h264', can be: 'h264', 'h264_cuvid'")

	return nil
}

func doTranscode(cmd *cobra.Command, args []string) error {

	filename := cmd.Flag("filename").Value.String()
	if len(filename) == 0 {
		return fmt.Errorf("Filename is needed after -f")
	}

	bypass, err := cmd.Flags().GetBool("bypass")
	if err != nil {
		return fmt.Errorf("Invalid bypass flag")
	}

	nThreads, err := cmd.Flags().GetInt32("threads")
	if err != nil {
		return fmt.Errorf("Invalid threads flag")
	}

	encoder := cmd.Flag("encoder").Value.String()
	if len(encoder) == 0 {
		return fmt.Errorf("Encoder is needed after -e")
	}

	decoder := cmd.Flag("decoder").Value.String()
	if len(decoder) == 0 {
		return fmt.Errorf("Decoder is needed after -d")
	}

	dir := "O"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.Mkdir(dir, 0755)
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
		Ecodec:             encoder,
		Dcodec:             decoder,
		EncHeight:          2160, // -1 means use source height, other values 2160, 720
		EncWidth:           3840, // -1 means use source width, other values 3840, 1280
	}

	avpipe.InitIOHandler(&avcmdInputOpener{url: filename}, &avcmdOutputOpener{dir: dir})

	done := make(chan interface{})

	for i := 0; i < int(nThreads); i++ {
		go func(params *avpipe.TxParams, filename string, bypass bool) {
			rc := avpipe.Tx(params, filename, bypass)
			if rc != 0 {
				done <- fmt.Errorf("Failed transcoding %s", filename)
			} else {
				done <- nil
			}
		}(params, filename, bypass)
	}

	var lastError error
	for i := 0; i < int(nThreads); i++ {
		err := <-done // Wait for background goroutines to finish
		if err != nil {
			lastError = err.(error)
			fmt.Println(err)
		}
	}

	return lastError
}
