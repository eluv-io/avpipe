package cmd

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	"github.com/eluv-io/avpipe"
	"github.com/spf13/cobra"
)

// elvxcInputOpener implements avpipe.InputOpener
type elvxcInputOpener struct {
	url string
}

func (io *elvxcInputOpener) Open(fd int64, url string) (avpipe.InputHandler, error) {
	log.Debug("AVCMD InputOpener.Open", "fd", fd, "url", url)

	if (len(url) >= 4 && url[0:4] == "rtmp") ||
		(len(url) >= 3 && url[0:3] == "udp") ||
		(len(url) >= 3 && url[0:3] == "srt") {
		return &noopElvxcInput{}, nil
	}

	f, err := os.OpenFile(url, os.O_RDONLY, 0755)
	if err != nil {
		return nil, err
	}

	io.url = url
	excInput := &elvxcInput{
		file: f,
		url:  url,
	}

	return excInput, nil
}

type noopElvxcInput struct{}

func (io *noopElvxcInput) Read(buf []byte) (int, error)                 { return 0, nil }
func (io *noopElvxcInput) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (io *noopElvxcInput) Close() error                                 { return nil }
func (io *noopElvxcInput) Size() int64                                  { return 0 }
func (i *noopElvxcInput) Stat(streamIndex int, statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_IN_STAT_BYTES_READ:
		readOffset := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "read offset", *readOffset, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_AUDIO_FRAME_READ:
		audioFrameRead := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "audioFrameRead", *audioFrameRead, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_VIDEO_FRAME_READ:
		videoFrameRead := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "videoFrameRead", *videoFrameRead, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_DECODING_AUDIO_START_PTS:
		startPTS := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "audio start PTS", *startPTS, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_DECODING_VIDEO_START_PTS:
		startPTS := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "video start PTS", *startPTS, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_DATA_SCTE35:
		log.Info("AVCMD InputHandler.Stat", "scte35", statArgs, "streamIndex", streamIndex)
	}

	return nil
}

// elvxcInput implements avpipe.InputHandler
type elvxcInput struct {
	url  string
	file *os.File // Input file
}

func (i *elvxcInput) Read(buf []byte) (int, error) {
	if i.url[0:4] == "rtmp" {
		return 0, nil
	}
	n, err := i.file.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	return n, err
}

func (i *elvxcInput) Seek(offset int64, whence int) (int64, error) {
	if i.url[0:4] == "rtmp" {
		return 0, nil
	}

	n, err := i.file.Seek(int64(offset), int(whence))
	return n, err
}

func (i *elvxcInput) Close() error {
	if i.url[0:4] == "rtmp" {
		return nil
	}

	err := i.file.Close()
	return err
}

func (i *elvxcInput) Size() int64 {
	fi, err := i.file.Stat()
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (i *elvxcInput) Stat(streamIndex int, statType avpipe.AVStatType, statArgs interface{}) error {
	switch statType {
	case avpipe.AV_IN_STAT_BYTES_READ:
		readOffset := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "read offset", *readOffset, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_AUDIO_FRAME_READ:
		audioFrameRead := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "audioFrameRead", *audioFrameRead, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_VIDEO_FRAME_READ:
		videoFrameRead := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "videoFrameRead", *videoFrameRead, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_DECODING_AUDIO_START_PTS:
		startPTS := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "audio start PTS", *startPTS, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_DECODING_VIDEO_START_PTS:
		startPTS := statArgs.(*uint64)
		log.Info("AVCMD InputHandler.Stat", "video start PTS", *startPTS, "streamIndex", streamIndex)
	case avpipe.AV_IN_STAT_DATA_SCTE35:
		log.Info("AVCMD InputHandler.Stat", "scte35", statArgs, "streamIndex", streamIndex)
	}

	return nil
}

// elvxcOutputOpener implements avpipe.OutputOpener
type elvxcOutputOpener struct {
	dir string
}

func (oo *elvxcOutputOpener) Open(h, fd int64, stream_index, seg_index int,
	pts int64, out_type avpipe.AVType) (avpipe.OutputHandler, error) {

	log.Debug("AVCMD OutputOpener.Open", "h", h, "fd", fd,
		"stream_index", stream_index, "seg_index", seg_index, "pts", pts, "out_type", out_type)

	var filename string
	dir := fmt.Sprintf("%s/O%d", oo.dir, h)

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err = os.Mkdir(dir, 0755); err != nil {
			return nil, err
		}
	}

	switch out_type {
	case avpipe.DASHVideoInit:
		fallthrough
	case avpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/init-stream%d.m4s", dir, stream_index)
	case avpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", dir)
	case avpipe.DASHVideoSegment:
		fallthrough
	case avpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/chunk-stream%d-%05d.m4s", dir, stream_index, seg_index)
	case avpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./%s/master.m3u8", dir)
	case avpipe.HLSVideoM3U:
		fallthrough
	case avpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./%s/media_%d.m3u8", dir, stream_index)
	case avpipe.AES128Key:
		filename = fmt.Sprintf("./%s/key.bin", dir)
	case avpipe.MP4Stream:
		filename = fmt.Sprintf("%s/mp4-stream.mp4", dir)
	case avpipe.FMP4Stream:
		filename = fmt.Sprintf("%s/fmp4-stream.mp4", dir)
	case avpipe.MP4Segment:
		filename = fmt.Sprintf("%s/segment%d-%05d.mp4", dir, stream_index, seg_index)
	case avpipe.FMP4VideoSegment:
		filename = fmt.Sprintf("%s/fmp4-vsegment%d-%05d.mp4", dir, stream_index, seg_index)
	case avpipe.FMP4AudioSegment:
		filename = fmt.Sprintf("%s/fmp4-asegment%d-%05d.mp4", dir, stream_index, seg_index)
	case avpipe.FrameImage:
		filename = fmt.Sprintf("%s/%d.jpeg", dir, pts)
	case avpipe.MpegtsSegment:
		filename = fmt.Sprintf("%s/ts-segment-%05d.ts", dir, seg_index)
	}

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}

	oh := &elvxcOutput{
		url:          filename,
		stream_index: stream_index,
		seg_index:    seg_index,
		file:         f}

	return oh, nil
}

// elvxcOutput implement avpipe.OutputHandler
type elvxcOutput struct {
	url          string
	stream_index int
	seg_index    int
	file         *os.File
}

func (o *elvxcOutput) Write(buf []byte) (int, error) {
	n, err := o.file.Write(buf)
	return n, err
}

func (o *elvxcOutput) Seek(offset int64, whence int) (int64, error) {
	n, err := o.file.Seek(offset, whence)
	return n, err
}

func (o *elvxcOutput) Close() error {
	err := o.file.Close()
	return err
}

func (o *elvxcOutput) Stat(streamIndex int, avType avpipe.AVType, statType avpipe.AVStatType, statArgs interface{}) error {
	doLog := func(args ...interface{}) {
		logArgs := []interface{}{"stat", statType.Name(), "avType", avType.Name(), "streamIndex", streamIndex}
		logArgs = append(logArgs, args...)
		log.Info("AVCMD Outhandler.Stat", logArgs...)
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
	case avpipe.AV_OUT_STAT_FRAME_WRITTEN:
		encodingStats := statArgs.(*avpipe.EncodingFrameStats)
		doLog("encodingStats", encodingStats)
	}
	return nil
}

func getAudioIndexes(params *avpipe.XcParams, audioIndexes string) (err error) {
	if len(audioIndexes) == 0 {
		return
	}

	indexes := strings.Split(audioIndexes, ",")
	for _, indexStr := range indexes {
		index, err := strconv.Atoi(indexStr)
		if err != nil {
			return fmt.Errorf("Invalid audio indexes")
		}
		params.AudioIndex = append(params.AudioIndex, int32(index))
	}

	return nil
}

// parseExtractImagesTs converts the extract-images-ts string parameter, e.g.
// "0,64000,128000,1152000", to an int64 array in avpipe.XcParams
func parseExtractImagesTs(params *avpipe.XcParams, s string) (err error) {
	if len(s) == 0 {
		return
	}
	frames := strings.Split(s, ",")
	params.ExtractImagesTs = make([]int64, len(frames))
	for i, frame := range frames {
		var v int64
		if v, err = strconv.ParseInt(frame, 10, 64); err != nil {
			return fmt.Errorf("invalid frame PTS %s", frame)
		}
		params.ExtractImagesTs[i] = v
	}
	return
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

	cmdTranscode.PersistentFlags().StringP("filename", "f", "", "(mandatory) filename to be transcoded.")
	cmdTranscode.PersistentFlags().BoolP("bypass", "b", false, "bypass transcoding.")
	cmdTranscode.PersistentFlags().BoolP("debug-frame-level", "", false, "debug frame level.")
	cmdTranscode.PersistentFlags().BoolP("skip-decoding", "", false, "skip decoding when start-time-ts is set.")
	cmdTranscode.PersistentFlags().BoolP("listen", "", false, "listen mode for RTMP.")
	cmdTranscode.PersistentFlags().Int32("connection-timeout", 0, "connection timeout for RTMP when listening on a port or MPEGTS to receive first UDP datagram.")
	cmdTranscode.PersistentFlags().Int32P("threads", "t", 1, "transcoding threads.")
	cmdTranscode.PersistentFlags().StringP("audio-index", "", "", "the indexes of audio stream (comma separated).")
	cmdTranscode.PersistentFlags().StringP("channel-layout", "", "", "audio channel layout.")
	cmdTranscode.PersistentFlags().Int32P("gpu-index", "", -1, "Use the GPU with specified index for transcoding (export CUDA_DEVICE_ORDER=PCI_BUS_ID would use smi index).")
	cmdTranscode.PersistentFlags().Int32P("sync-audio-to-stream-id", "", -1, "sync audio to video iframe of specific stream-id when input stream is mpegts")
	cmdTranscode.PersistentFlags().StringP("encoder", "e", "libx264", "encoder codec, default is 'libx264', can be: 'libx264', 'libx265', 'h264_nvenc', 'h264_videotoolbox', or 'mjpeg'.")
	cmdTranscode.PersistentFlags().StringP("audio-encoder", "", "aac", "audio encoder, default is 'aac', can be: 'aac', 'ac3', 'mp2', 'mp3'.")
	cmdTranscode.PersistentFlags().StringP("decoder", "d", "", "video decoder, default is 'h264', can be: 'h264', 'h264_cuvid', 'jpeg2000', 'hevc'.")
	cmdTranscode.PersistentFlags().StringP("audio-decoder", "", "", "audio decoder, default is '' and will be automatically chosen.")
	cmdTranscode.PersistentFlags().StringP("format", "", "dash", "package format, can be 'dash', 'hls', 'mp4', 'fmp4', 'segment', 'fmp4-segment', or 'image2'.")
	cmdTranscode.PersistentFlags().StringP("filter-descriptor", "", "", " Audio filter descriptor the same as ffmpeg format")
	cmdTranscode.PersistentFlags().Int32P("force-keyint", "", 0, "force IDR key frame in this interval.")
	cmdTranscode.PersistentFlags().BoolP("equal-fduration", "", false, "force equal frame duration. Must be 0 or 1 and only valid for 'fmp4-segment' format.")
	cmdTranscode.PersistentFlags().StringP("xc-type", "", "", "transcoding type, can be 'all', 'video', 'audio', 'audio-join', 'audio-pan', 'audio-merge', 'extract-images' or 'extract-all-images'.")
	cmdTranscode.PersistentFlags().Int32P("crf", "", 23, "mutually exclusive with video-bitrate.")
	cmdTranscode.PersistentFlags().StringP("preset", "", "medium", "Preset string to determine compression speed, can be: 'ultrafast', 'superfast', 'veryfast', 'faster', 'fast', 'medium', 'slow', 'slower', 'veryslow'")
	cmdTranscode.PersistentFlags().Int64P("start-time-ts", "", 0, "offset to start transcoding")
	cmdTranscode.PersistentFlags().Int32P("stream-id", "", -1, "if it is valid it will be used to transcode elementary stream with that stream-id")
	cmdTranscode.PersistentFlags().Int64P("start-pts", "", 0, "starting PTS for output.")
	cmdTranscode.PersistentFlags().Int32P("sample-rate", "", -1, "For aac output sample rate is set to input sample rate and this parameter is ignored.")
	cmdTranscode.PersistentFlags().Int32P("start-segment", "", 1, "start segment number >= 1.")
	cmdTranscode.PersistentFlags().Int32P("start-frag-index", "", 1, "start fragment index >= 1.")
	cmdTranscode.PersistentFlags().Int32P("video-bitrate", "", -1, "output video bitrate, mutually exclusive with crf.")
	cmdTranscode.PersistentFlags().Int32P("audio-bitrate", "", 128000, "output audio bitrate.")
	cmdTranscode.PersistentFlags().Int32P("rc-max-rate", "", 0, "maximum encoding bit rate, used in conjuction with rc-buffer-size.")
	cmdTranscode.PersistentFlags().Int32P("rc-buffer-size", "", 0, "determines the interval used to limit bit rate.")
	cmdTranscode.PersistentFlags().Int32P("enc-height", "", -1, "default -1 means use source height.")
	cmdTranscode.PersistentFlags().Int32P("enc-width", "", -1, "default -1 means use source width.")
	cmdTranscode.PersistentFlags().Int32P("video-time-base", "", 0, "Video encoder timebase, must be > 0 (the actual timebase would be 1/video-time-base).")
	cmdTranscode.PersistentFlags().Int32P("video-frame-duration-ts", "", 0, "Frame duration of the output video in time base.")
	cmdTranscode.PersistentFlags().Int64P("duration-ts", "", -1, "default -1 means entire stream.")
	cmdTranscode.PersistentFlags().Int64P("audio-seg-duration-ts", "", 0, "(mandatory if format is not 'segment' and transcoding audio) audio segment duration time base (positive integer).")
	cmdTranscode.PersistentFlags().Int64P("video-seg-duration-ts", "", 0, "(mandatory if format is not 'segment' and transcoding video) video segment duration time base (positive integer).")
	cmdTranscode.PersistentFlags().StringP("seg-duration", "", "30", "(mandatory if format is 'segment') segment duration seconds (positive integer), default is 30.")
	cmdTranscode.PersistentFlags().Int32P("seg-duration-fr", "", 0, "(mandatory if format is not 'segment') segment duration frame (positive integer).")
	cmdTranscode.PersistentFlags().String("crypt-iv", "", "128-bit AES IV, as 32 char hex.")
	cmdTranscode.PersistentFlags().String("crypt-key", "", "128-bit AES key, as 32 char hex.")
	cmdTranscode.PersistentFlags().String("crypt-kid", "", "16-byte key ID, as 32 char hex.")
	cmdTranscode.PersistentFlags().String("crypt-key-url", "", "specify a key URL in the manifest.")
	cmdTranscode.PersistentFlags().String("crypt-scheme", "none", "encryption scheme, default is 'none', can be: 'aes-128', 'cbc1', 'cbcs', 'cenc', 'cens'.")
	cmdTranscode.PersistentFlags().String("wm-text", "", "add text to the watermark display.")
	cmdTranscode.PersistentFlags().String("wm-timecode", "", "add timecode watermark to each frame.")
	cmdTranscode.PersistentFlags().Float32("wm-timecode-rate", -1, "Watermark timecode frame rate.")
	cmdTranscode.PersistentFlags().String("wm-xloc", "", "the xLoc of the watermark as specified by a fraction of width.")
	cmdTranscode.PersistentFlags().String("wm-yloc", "", "the yLoc of the watermark as specified by a fraction of height.")
	cmdTranscode.PersistentFlags().Float32("wm-relative-size", 0.05, "font/shadow relative size based on frame height.")
	cmdTranscode.PersistentFlags().String("wm-color", "black", "watermark font color.")
	cmdTranscode.PersistentFlags().BoolP("wm-shadow", "", true, "watermarking with shadow.")
	cmdTranscode.PersistentFlags().String("wm-shadow-color", "white", "watermark shadow color.")
	cmdTranscode.PersistentFlags().String("wm-overlay", "", "watermark overlay image file.")
	cmdTranscode.PersistentFlags().String("wm-overlay-type", "png", "watermark overlay image file type, can be 'png', 'jpg', 'gif'.")
	cmdTranscode.PersistentFlags().String("max-cll", "", "Maximum Content Light Level and Maximum Frame Average Light Level, only valid if encoder is libx265.")
	cmdTranscode.PersistentFlags().String("master-display", "", "Master display, only valid if encoder is libx265.")
	cmdTranscode.PersistentFlags().Int32("bitdepth", 8, "Refers to number of colors each pixel can have, can be 8, 10, 12.")
	cmdTranscode.PersistentFlags().Int64P("extract-image-interval-ts", "", -1, "extract frames at this interval.")
	cmdTranscode.PersistentFlags().StringP("extract-images-ts", "", "", "the frames to extract (PTS, comma separated).")
	cmdTranscode.PersistentFlags().BoolP("seekable", "", true, "seekable stream.")
	cmdTranscode.PersistentFlags().Int32("rotate", 0, "Rotate the output video frame (valid values 0, 90, 180, 270).")
	cmdTranscode.PersistentFlags().StringP("profile", "", "", "Encoding profile for video. If it is not determined, it will be set automatically.")
	cmdTranscode.PersistentFlags().Int32("level", 0, "Encoding level for video. If it is not determined, it will be set automatically.")
	cmdTranscode.PersistentFlags().Int32("deinterlace", 0, "Deinterlace filter (values 0 - none, 1 - bwdif_field, 2 - bwdif_frame send_frame).")
	cmdTranscode.PersistentFlags().Bool("copy-mpegts", false, "Create a copy of the MPEGTS input (for MPEGTS, SRT, RTP)")

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

	seekable, err := cmd.Flags().GetBool("seekable")
	if err != nil {
		return fmt.Errorf("Invalid seekable flag")
	}

	debugFrameLevel, err := cmd.Flags().GetBool("debug-frame-level")
	if err != nil {
		return fmt.Errorf("Invalid debug-frame-level flag")
	}

	skipDecoding, err := cmd.Flags().GetBool("skip-decoding")
	if err != nil {
		return fmt.Errorf("Invalid skip-decoding flag")
	}

	listen, err := cmd.Flags().GetBool("listen")
	if err != nil {
		return fmt.Errorf("Invalid listen flag")
	}

	connectionTimeout, err := cmd.Flags().GetInt32("connection-timeout")
	if err != nil {
		return fmt.Errorf("Invalid connection-timeout flag")
	}

	forceEqualFrameDuration, err := cmd.Flags().GetBool("equal-fduration")
	if err != nil {
		return fmt.Errorf("Invalid equal-fduration flag")
	}

	nThreads, err := cmd.Flags().GetInt32("threads")
	if err != nil {
		return fmt.Errorf("Invalid threads flag")
	}

	audioIndex := cmd.Flag("audio-index").Value.String()

	channelLayout := 0
	channelLayoutStr := cmd.Flag("channel-layout").Value.String()
	if len(channelLayoutStr) > 0 {
		channelLayout = avpipe.ChannelLayout(channelLayoutStr)
		if channelLayout == 0 {
			return fmt.Errorf("Invalid channle layout")
		}
	}

	gpuIndex, err := cmd.Flags().GetInt32("gpu-index")
	if err != nil {
		return fmt.Errorf("Invalid gpu index flag")
	}

	syncAudioToStreamId, err := cmd.Flags().GetInt32("sync-audio-to-stream-id")
	if err != nil {
		return fmt.Errorf("Invalid sync-audio-to-stream-id flag")
	}

	encoder := cmd.Flag("encoder").Value.String()
	if len(encoder) == 0 {
		return fmt.Errorf("Encoder is needed after -e")
	}

	audioEncoder := cmd.Flag("audio-encoder").Value.String()
	if len(audioEncoder) == 0 {
		return fmt.Errorf("Audio encoder is missing")
	}

	decoder := cmd.Flag("decoder").Value.String()
	audioDecoder := cmd.Flag("audio-decoder").Value.String()

	format := cmd.Flag("format").Value.String()
	if format != "dash" && format != "hls" && format != "mp4" && format != "fmp4" && format != "segment" && format != "fmp4-segment" && format != "image2" {
		return fmt.Errorf("Package format is not valid, can be 'dash', 'hls', 'mp4', 'fmp4', 'segment', 'fmp4-segment', or 'image2'")
	}

	filterDescriptor := cmd.Flag("filter-descriptor").Value.String()

	watermarkTimecode := cmd.Flag("wm-timecode").Value.String()
	watermarkTimecodeRate, _ := cmd.Flags().GetFloat32("wm-timecode-rate")
	if len(watermarkTimecode) > 0 && watermarkTimecodeRate <= 0 {
		return fmt.Errorf("Watermark timecode rate is needed")
	}
	watermarkText := cmd.Flag("wm-text").Value.String()
	watermarkXloc := cmd.Flag("wm-xloc").Value.String()
	watermarkYloc := cmd.Flag("wm-yloc").Value.String()
	watermarkFontColor := cmd.Flag("wm-color").Value.String()
	watermarkRelativeSize, _ := cmd.Flags().GetFloat32("wm-relative-size")
	watermarkShadow, _ := cmd.Flags().GetBool("watermark-shadow")
	watermarkShadowColor := cmd.Flag("wm-shadow-color").Value.String()
	watermarkOverlay := cmd.Flag("wm-overlay").Value.String()

	var watermarkOverlayType avpipe.ImageType
	watermarkOverlayTypeStr := cmd.Flag("wm-overlay-type").Value.String()
	switch watermarkOverlayTypeStr {
	case "png":
		fallthrough
	case "PNG":
		watermarkOverlayType = avpipe.PngImage
	case "jpg":
		fallthrough
	case "JPG":
		watermarkOverlayType = avpipe.JpgImage
	case "gif":
		fallthrough
	case "GIF":
		watermarkOverlayType = avpipe.GifImage
	default:
		watermarkOverlayType = avpipe.UnknownImage
	}
	if len(watermarkOverlay) > 0 && watermarkOverlayType == avpipe.UnknownImage {
		return fmt.Errorf("Watermark overlay type is not valid, can be 'png', 'jpg', 'gif'")
	}

	var overlayImage []byte
	if len(watermarkOverlay) > 0 {
		overlayImage, err = ioutil.ReadFile(watermarkOverlay)
		if err != nil {
			return err
		}
	}

	streamId, err := cmd.Flags().GetInt32("stream-id")
	if err != nil {
		return fmt.Errorf("stream-id is not valid, must be >= 0")
	}

	xcTypeStr := cmd.Flag("xc-type").Value.String()
	if streamId < 0 && xcTypeStr != "all" &&
		xcTypeStr != "video" &&
		xcTypeStr != "audio" &&
		xcTypeStr != "audio-join" &&
		xcTypeStr != "audio-pan" &&
		xcTypeStr != "audio-merge" &&
		xcTypeStr != "extract-images" &&
		xcTypeStr != "extract-all-images" {
		return fmt.Errorf("Transcoding type is not valid, with no stream-id can be 'all', 'video', 'audio', 'audio-join', 'audio-pan', 'audio-merge', or 'extract-images'")
	}
	xcType := avpipe.XcTypeFromString(xcTypeStr)
	if xcType == avpipe.XcAudio && len(encoder) == 0 {
		encoder = "aac"
	}

	maxCLL := cmd.Flag("max-cll").Value.String()
	masterDisplay := cmd.Flag("master-display").Value.String()
	bitDepth, err := cmd.Flags().GetInt32("bitdepth")
	if err != nil {
		return fmt.Errorf("bitdepth is not valid, should be 8, 10, or 12")
	}

	crf, err := cmd.Flags().GetInt32("crf")
	if err != nil || crf < 0 || crf > 51 {
		return fmt.Errorf("crf is not valid, should be in 0..51")
	}

	preset := cmd.Flag("preset").Value.String()
	if preset != "ultrafast" && preset != "superfast" && preset != "veryfast" && preset != "faster" &&
		preset != "fast" && preset != "medium" && preset != "slow" && preset != "slower" && preset != "veryslow" {
		return fmt.Errorf("preset is not valid, should be one of: 'ultrafast', 'superfast', 'veryfast', 'faster', 'fast', 'medium', 'slow', 'slower', 'veryslow'")
	}

	startTimeTs, err := cmd.Flags().GetInt64("start-time-ts")
	if err != nil {
		return fmt.Errorf("start-time-ts is not valid")
	}

	startPts, err := cmd.Flags().GetInt64("start-pts")
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

	forceKeyInterval, err := cmd.Flags().GetInt32("force-keyint")
	if err != nil {
		return fmt.Errorf("force-keyint is not valid")
	}

	startFragmentIndex, err := cmd.Flags().GetInt32("start-frag-index")
	if err != nil {
		return fmt.Errorf("start-frag-index is not valid")
	}

	videoBitrate, err := cmd.Flags().GetInt32("video-bitrate")
	if err != nil {
		return fmt.Errorf("video-bitrate is not valid")
	}

	audioBitrate, err := cmd.Flags().GetInt32("audio-bitrate")
	if err != nil {
		return fmt.Errorf("audio-bitrate is not valid")
	}

	rcMaxRate, err := cmd.Flags().GetInt32("rc-max-rate")
	if err != nil {
		return fmt.Errorf("rc-max-rate is not valid")
	}

	rcBufferSize, err := cmd.Flags().GetInt32("rc-buffer-size")
	if err != nil {
		return fmt.Errorf("rc-buffer-size is not valid")
	}

	encHeight, err := cmd.Flags().GetInt32("enc-height")
	if err != nil {
		return fmt.Errorf("enc-height is not valid")
	}

	encWidth, err := cmd.Flags().GetInt32("enc-width")
	if err != nil {
		return fmt.Errorf("enc-width is not valid")
	}

	videoTimeBase, err := cmd.Flags().GetInt32("video-time-base")
	if err != nil {
		return fmt.Errorf("video-time-base is not valid")
	}

	videoFrameDurationTs, err := cmd.Flags().GetInt32("video-frame-duration-ts")
	if err != nil {
		return fmt.Errorf("video-frame-duration-ts is not valid")
	}

	durationTs, err := cmd.Flags().GetInt64("duration-ts")
	if err != nil {
		return fmt.Errorf("Duration ts is not valid")
	}

	audioSegDurationTs, err := cmd.Flags().GetInt64("audio-seg-duration-ts")
	if err != nil ||
		(format != "segment" && format != "fmp4-segment" &&
			audioSegDurationTs == 0 &&
			(xcType == avpipe.XcAll || xcType == avpipe.XcAudio ||
				xcType == avpipe.XcAudioJoin || xcType == avpipe.XcAudioMerge)) {
		return fmt.Errorf("Audio seg duration ts is not valid")
	}

	videoSegDurationTs, err := cmd.Flags().GetInt64("video-seg-duration-ts")
	if err != nil || (format != "segment" && format != "fmp4-segment" && format != "mp4" &&
		videoSegDurationTs == 0 && (xcType == avpipe.XcAll || xcType == avpipe.XcVideo)) {
		return fmt.Errorf("Video seg duration ts is not valid")
	}

	segDuration := cmd.Flag("seg-duration").Value.String()
	if format == "segment" && len(segDuration) == 0 {
		return fmt.Errorf("Seg duration ts is not valid")
	}

	crfStr := strconv.Itoa(int(crf))
	startSegmentStr := strconv.Itoa(int(startSegment))

	rotate, err := cmd.Flags().GetInt32("rotate")
	if err != nil {
		return fmt.Errorf("Invalid rotate value")
	}

	level, err := cmd.Flags().GetInt32("level")
	if err != nil {
		return fmt.Errorf("Invalid level value")
	}

	profile := cmd.Flag("profile").Value.String()

	deinterlace, err := cmd.Flags().GetInt32("deinterlace")
	if err != nil {
		return fmt.Errorf("Invalid deinterlace value")
	}

	copyMpegts, err := cmd.Flags().GetBool("copy-mpegts")
	if err != nil {
		return fmt.Errorf("Invalid copy-mpegts value")
	}

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

	extractImageIntervalTs, err := cmd.Flags().GetInt64("extract-image-interval-ts")
	if err != nil {
		return fmt.Errorf("extract-image-interval-ts is not valid")
	}

	dir := "O"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.Mkdir(dir, 0755)
	}

	params := &avpipe.XcParams{
		Url:                    filename,
		BypassTranscoding:      bypass,
		Format:                 format,
		StartTimeTs:            startTimeTs,
		StartPts:               startPts,
		DurationTs:             durationTs,
		StartSegmentStr:        startSegmentStr,
		StartFragmentIndex:     startFragmentIndex,
		VideoBitrate:           videoBitrate,
		AudioBitrate:           audioBitrate,
		SampleRate:             sampleRate,
		CrfStr:                 crfStr,
		Preset:                 preset,
		AudioSegDurationTs:     audioSegDurationTs,
		VideoSegDurationTs:     videoSegDurationTs,
		SegDuration:            segDuration,
		Ecodec:                 encoder,
		Ecodec2:                audioEncoder,
		Dcodec:                 decoder,
		Dcodec2:                audioDecoder,
		EncHeight:              encHeight, // -1 means use source height, other values 2160, 720
		EncWidth:               encWidth,  // -1 means use source width, other values 3840, 1280
		CryptIV:                cryptIV,
		CryptKey:               cryptKey,
		CryptKID:               cryptKID,
		CryptKeyURL:            cryptKeyURL,
		CryptScheme:            cryptScheme,
		XcType:                 xcType,
		CopyMpegts:             copyMpegts,
		WatermarkTimecode:      watermarkTimecode,
		WatermarkTimecodeRate:  watermarkTimecodeRate,
		WatermarkText:          watermarkText,
		WatermarkXLoc:          watermarkXloc,
		WatermarkYLoc:          watermarkYloc,
		WatermarkRelativeSize:  watermarkRelativeSize,
		WatermarkFontColor:     watermarkFontColor,
		WatermarkShadow:        watermarkShadow,
		WatermarkShadowColor:   watermarkShadowColor,
		WatermarkOverlay:       string(overlayImage),
		WatermarkOverlayType:   watermarkOverlayType,
		ForceKeyInt:            forceKeyInterval,
		RcMaxRate:              rcMaxRate,
		RcBufferSize:           rcBufferSize,
		GPUIndex:               gpuIndex,
		MaxCLL:                 maxCLL,
		MasterDisplay:          masterDisplay,
		BitDepth:               bitDepth,
		ForceEqualFDuration:    forceEqualFrameDuration,
		SyncAudioToStreamId:    int(syncAudioToStreamId),
		StreamId:               streamId,
		Listen:                 listen,
		ConnectionTimeout:      int(connectionTimeout),
		FilterDescriptor:       filterDescriptor,
		SkipDecoding:           skipDecoding,
		ExtractImageIntervalTs: extractImageIntervalTs,
		ChannelLayout:          channelLayout,
		DebugFrameLevel:        debugFrameLevel,
		VideoTimeBase:          int(videoTimeBase),
		VideoFrameDurationTs:   int(videoFrameDurationTs),
		Seekable:               seekable,
		Rotate:                 int(rotate),
		Profile:                profile,
		Level:                  int(level),
		Deinterlace:            int(deinterlace),
	}

	err = getAudioIndexes(params, audioIndex)
	if err != nil {
		return err
	}

	params.WatermarkOverlayLen = len(params.WatermarkOverlay)

	extractImages := cmd.Flag("extract-images-ts").Value.String()
	if err = parseExtractImagesTs(params, extractImages); err != nil {
		return err
	}

	avpipe.InitIOHandler(&elvxcInputOpener{url: filename}, &elvxcOutputOpener{dir: dir})

	done := make(chan interface{})

	for i := 0; i < int(nThreads); i++ {
		go func(params *avpipe.XcParams, filename string) {

			err := avpipe.Xc(params)
			if err != nil {
				done <- fmt.Errorf("Failed transcoding %s, err=%v", filename, err)
			} else {
				done <- nil
			}
		}(params, filename)
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
