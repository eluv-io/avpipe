package cmd

import (
	"fmt"
	"io"
	"os"
	"strconv"

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
	case avpipe.AES128Key:
		filename = fmt.Sprintf("./%s/key.bin", dir)
	case avpipe.MP4Stream:
		filename = fmt.Sprintf("%s/live-stream.mp4", dir)
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

	cmdTranscode.PersistentFlags().StringP("filename", "f", "", "(mandatory) filename to be transcoded")
	cmdTranscode.PersistentFlags().BoolP("bypass", "b", false, "bypass transcoding")
	cmdTranscode.PersistentFlags().Int32P("threads", "t", 1, "transcoding threads")
	cmdTranscode.PersistentFlags().StringP("encoder", "e", "libx264", "encoder codec, default is 'libx264', can be: 'libx264', 'h264_nvenc', 'h264_videotoolbox'")
	cmdTranscode.PersistentFlags().StringP("decoder", "d", "h264", "decoder codec, default is 'h264', can be: 'h264', 'h264_cuvid'")
	cmdTranscode.PersistentFlags().StringP("format", "", "dash", "package format, can be 'dash', 'hls' or 'mp4'.")
	cmdTranscode.PersistentFlags().Int32P("crf", "", 23, "mutually exclusive with video-bitrate.")
	cmdTranscode.PersistentFlags().Int32P("start-time-ts", "", 0, "")
	cmdTranscode.PersistentFlags().Int32P("start-pts", "", 0, "starting PTS for output")
	cmdTranscode.PersistentFlags().Int32P("sample-rate", "", -1, "")
	cmdTranscode.PersistentFlags().Int32P("start-segment", "", 1, "start segment number >= 1.")
	cmdTranscode.PersistentFlags().Int32P("video-bitrate", "", -1, "mutually exclusive with crf.")
	cmdTranscode.PersistentFlags().Int32P("audio-bitrate", "", -1, "Default: -1")
	cmdTranscode.PersistentFlags().Int32P("enc-height", "", -1, "default -1 means use source height")
	cmdTranscode.PersistentFlags().Int32P("enc-width", "", -1, "default -1 means use source width")
	cmdTranscode.PersistentFlags().Int32P("duration-ts", "", -1, "default -1 means entire stream")
	cmdTranscode.PersistentFlags().Int32P("seg-duration-ts", "", 0, "(mandatory) segment duration time base (positive integer)")
	cmdTranscode.PersistentFlags().Int32P("seg-duration-fr", "", 0, "(mandatory) segment duration frame (positive integer)")
	cmdTranscode.PersistentFlags().String("crypt-iv", "", "128-bit AES IV, as 32 char hex")
	cmdTranscode.PersistentFlags().String("crypt-key", "", "128-bit AES key, as 32 char hex")
	cmdTranscode.PersistentFlags().String("crypt-kid", "", "16-byte key ID, as 32 char hex")
	cmdTranscode.PersistentFlags().String("crypt-key-url", "", "specify a key URL in the manifest")
	cmdTranscode.PersistentFlags().String("crypt-scheme", "none", "encryption scheme, default is 'none', can be: 'aes-128', 'cbc1', 'cbcs', 'cenc', 'cens'")

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

	format := cmd.Flag("format").Value.String()
	if format != "dash" && format != "hls" && format != "mp4" {
		return fmt.Errorf("Pakage format is not valid, can be 'dash', 'hls', or mp4")
	}

	crf, err := cmd.Flags().GetInt32("crf")
	if err != nil || crf < 0 || crf > 51 {
		return fmt.Errorf("crf is not valid, should be in 0..51")
	}

	startTimeTs, err := cmd.Flags().GetInt32("start-time-ts")
	if err != nil {
		return fmt.Errorf("start-time-ts is not valid")
	}

	startPts, err := cmd.Flags().GetInt32("start-pts")
	if err != nil || startPts < 0 {
		return fmt.Errorf("start-pts is not valid, must be >=0")
	}

	sampleRate, err := cmd.Flags().GetInt32("sample-rate")
	if err != nil {
		return fmt.Errorf("sample-rate is not valid")
	}

	startSegment, err := cmd.Flags().GetInt32("start-segment")
	if err != nil {
		return fmt.Errorf("start-segment is not valid")
	}

	videoBitrate, err := cmd.Flags().GetInt32("video-bitrate")
	if err != nil {
		return fmt.Errorf("video-bitrate is not valid")
	}

	audioBitrate, err := cmd.Flags().GetInt32("audio-bitrate")
	if err != nil {
		return fmt.Errorf("audio-bitrate is not valid")
	}

	encHeight, err := cmd.Flags().GetInt32("enc-height")
	if err != nil {
		return fmt.Errorf("enc-height is not valid")
	}

	encWidth, err := cmd.Flags().GetInt32("enc-width")
	if err != nil {
		return fmt.Errorf("enc-width is not valid")
	}

	durationTs, err := cmd.Flags().GetInt32("duration-ts")
	if err != nil {
		return fmt.Errorf("Duration ts is not valid")
	}

	segDurationTs, err := cmd.Flags().GetInt32("seg-duration-ts")
	if err != nil || segDurationTs == 0 {
		return fmt.Errorf("Seg duration ts is not valid")
	}

	segDurationFr, err := cmd.Flags().GetInt32("seg-duration-fr")
	if err != nil || segDurationFr == 0 {
		return fmt.Errorf("Seg duration fr is not valid")
	}

	crfStr := strconv.Itoa(int(crf))
	startSegmentStr := strconv.Itoa(int(startSegment))

	cryptScheme := avpipe.CryptNone
	val := cmd.Flag("crypt-scheme").Value.String()
	if len(val) > 0 {
		switch val {
		case "aes-128":
			cryptScheme = avpipe.CryptAES128
		case "cenc":
			cryptScheme = avpipe.CryptCENC
		case "cbc1":
			cryptScheme = avpipe.CryptCBC1
		case "cens":
			cryptScheme = avpipe.CryptCENS
		case "cbcs":
			cryptScheme = avpipe.CryptCBCS
		case "none":
			break
		default:
			return fmt.Errorf("Invalid crypt-scheme: %s", val)
		}
	}
	cryptIV := cmd.Flag("crypt-iv").Value.String()
	cryptKey := cmd.Flag("crypt-key").Value.String()
	cryptKID := cmd.Flag("crypt-kid").Value.String()
	cryptKeyURL := cmd.Flag("crypt-key-url").Value.String()

	dir := "O"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.Mkdir(dir, 0755)
	}

	params := &avpipe.TxParams{
		Format:          format,
		StartTimeTs:     startTimeTs,
		StartPts:        startPts,
		DurationTs:      durationTs,
		StartSegmentStr: startSegmentStr,
		VideoBitrate:    videoBitrate,
		AudioBitrate:    audioBitrate,
		SampleRate:      sampleRate,
		CrfStr:          crfStr,
		SegDurationTs:   segDurationTs,
		SegDurationFr:   segDurationFr,
		Ecodec:          encoder,
		Dcodec:          decoder,
		EncHeight:       encHeight, // -1 means use source height, other values 2160, 720
		EncWidth:        encWidth,  // -1 means use source width, other values 3840, 1280
		CryptIV:         cryptIV,
		CryptKey:        cryptKey,
		CryptKID:        cryptKID,
		CryptKeyURL:     cryptKeyURL,
		CryptScheme:     cryptScheme,
	}

	avpipe.InitIOHandler(&avcmdInputOpener{url: filename}, &avcmdOutputOpener{dir: dir})

	done := make(chan interface{})

	for i := 0; i < int(nThreads); i++ {
		go func(params *avpipe.TxParams, filename string, bypass bool) {
			rc := avpipe.Tx(params, filename, bypass, true)
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
