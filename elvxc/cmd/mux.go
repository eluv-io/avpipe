package cmd

import "C"
import (
	"fmt"
	"github.com/eluv-io/avpipe"
	"github.com/spf13/cobra"
	"io"
	"io/ioutil"
	"net/http"
	"os"
)

type AVCmdMuxInputOpener struct {
	URL string
}

func (inputOpener *AVCmdMuxInputOpener) Open(fd int64, url string) (avpipe.InputHandler, error) {
	log.Debug("elvxcMuxInputOpener", "url", url)
	if url[:7] == "http://" || url[:8] == "https://" {
		resp, err := http.Get(url)
		if err != nil {
			return nil, err
		}
		muxInput := &elvxcMuxInput{
			resp: resp,
		}

		return muxInput, nil
	}

	f, err := os.OpenFile(url, os.O_RDONLY, 0755)
	if err != nil {
		return nil, err
	}

	inputOpener.URL = url
	muxInput := &elvxcMuxInput{
		file: f,
	}

	return muxInput, nil
}

// Implement InputHandler
type elvxcMuxInput struct {
	file *os.File       // Input file
	resp *http.Response // If the url is a http/https request
	body []byte         // Body of http request if url is a http/https request
	rpos int            // Read position
}

func (muxInput *elvxcMuxInput) Read(buf []byte) (n int, err error) {
	// If it is network url
	if muxInput.resp != nil {
		if muxInput.body == nil {
			muxInput.body, err = ioutil.ReadAll(muxInput.resp.Body)
			log.Debug("elvxcMuxInput Read", "body size", len(muxInput.body), "err", err)
			if err != nil && err != io.EOF {
				return -1, err
			}
		}
		if muxInput.rpos >= len(muxInput.body) {
			return 0, nil
		}
		copied := copy(buf, muxInput.body[muxInput.rpos:])
		muxInput.rpos += copied
		log.Debug("elvxcMuxInput Read (network)", "buf len", len(buf), "rpos", muxInput.rpos, "copied", copied)
		return copied, nil
	}

	n, err = muxInput.file.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	return
}

func (muxInput *elvxcMuxInput) Seek(offset int64, whence int) (int64, error) {
	if muxInput.resp != nil {
		return 0, fmt.Errorf("No Seek for network IO")
	}
	n, err := muxInput.file.Seek(int64(offset), int(whence))
	return n, err
}

func (muxInput *elvxcMuxInput) Close() (err error) {
	if muxInput.resp != nil {
		muxInput.resp.Body.Close()
		return
	}
	err = muxInput.file.Close()
	return err
}

func (muxInput *elvxcMuxInput) Size() int64 {
	fi, err := muxInput.file.Stat()
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (muxInput *elvxcMuxInput) Stat(statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_IN_STAT_BYTES_READ:
		readOffset := statArgs.(*uint64)
		log.Info("elvxcMuxInput", "stat read offset", *readOffset)
	case avpipe.AV_IN_STAT_DECODING_AUDIO_START_PTS:
		startPTS := statArgs.(*uint64)
		log.Info("elvxcMuxInput", "audio start PTS", *startPTS)
	case avpipe.AV_IN_STAT_DECODING_VIDEO_START_PTS:
		startPTS := statArgs.(*uint64)
		log.Info("elvxcMuxInput", "video start PTS", *startPTS)
	}

	return nil
}

//Implement AVPipeOutputOpener
type AVCmdMuxOutputOpener struct {
}

func (outputOpener *AVCmdMuxOutputOpener) Open(filename string, fd int64, outType avpipe.AVType) (avpipe.OutputHandler, error) {

	if outType != avpipe.MP4Segment &&
		outType != avpipe.FMP4AudioSegment &&
		outType != avpipe.FMP4VideoSegment {
		return nil, fmt.Errorf("Invalid outType=%d", outType)
	}

	log.Debug("elvxcMuxOutputOpener", "filename", filename)

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}

	oh := &elvxcMuxOutput{
		url:  filename,
		file: f}

	return oh, nil
}

// Implement OutputHandler
type elvxcMuxOutput struct {
	url  string
	file *os.File
}

func (muxOutput *elvxcMuxOutput) Write(buf []byte) (int, error) {
	log.Debug("elvxcMuxOutput Write", "buf len", len(buf))
	n, err := muxOutput.file.Write(buf)
	return n, err
}

func (muxOutput *elvxcMuxOutput) Seek(offset int64, whence int) (int64, error) {
	n, err := muxOutput.file.Seek(offset, whence)
	return n, err
}

func (muxOutput *elvxcMuxOutput) Close() error {
	err := muxOutput.file.Close()
	return err
}

func (muxOutput *elvxcMuxOutput) Stat(avType avpipe.AVType, statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_OUT_STAT_BYTES_WRITTEN:
		writeOffset := statArgs.(*uint64)
		log.Info("elvxcMuxOutput", "STAT, write offset", *writeOffset)
	case avpipe.AV_OUT_STAT_ENCODING_END_PTS:
		endPTS := statArgs.(*uint64)
		log.Info("elvxcMuxOutput", "STAT, endPTS", *endPTS)

	}

	return nil
}

func InitMux(cmdRoot *cobra.Command) error {
	cmdTranscode := &cobra.Command{
		Use:   "mux",
		Short: "Mux some media files based on a muxing spec",
		Long:  "Mux some media files based on a muxing spec and produce one output file",
		//Args:  cobra.ExactArgs(1),
		RunE: doMux,
	}

	cmdRoot.AddCommand(cmdTranscode)

	cmdTranscode.PersistentFlags().StringP("filename", "f", "", "(mandatory) muxing output filename.")
	cmdTranscode.PersistentFlags().String("mux-spec", "", "(mandatory) muxing spec file.")

	return nil
}

func doMux(cmd *cobra.Command, args []string) error {

	filename := cmd.Flag("filename").Value.String()
	if len(filename) == 0 {
		return fmt.Errorf("Filename is needed after -f")
	}

	muxSpecFile := cmd.Flag("mux-spec").Value.String()
	if len(muxSpecFile) == 0 {
		return fmt.Errorf("mux-spec is needed to do muxing")
	}

	muxSpec, err := ioutil.ReadFile(muxSpecFile)
	if err != nil {
		return fmt.Errorf("Could not read mux-spec file %s", muxSpecFile)
	}
	log.Debug("doMux", "mux_spec", string(muxSpec))

	params := &avpipe.XcParams{
		MuxingSpec:      string(muxSpec),
		Url:             filename,
		DebugFrameLevel: true,
	}

	avpipe.InitUrlMuxIOHandler(filename, &AVCmdMuxInputOpener{URL: filename}, &AVCmdMuxOutputOpener{})

	return avpipe.Mux(params)
}
