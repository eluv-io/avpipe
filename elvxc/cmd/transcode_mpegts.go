package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/mpegtsxc"
	"github.com/spf13/cobra"
)

const (
	mpegtsPacketSize      = 188
	mpegtsUDPDatagramSize = 7 * mpegtsPacketSize
	maxUDPDatagramSize    = 1<<16 - 1
)

type transcodeMPEGTSOptions struct {
	input             string
	inputPackaging    string
	outputs           []string
	selection         *goavpipe.MPEGTSSelection
	encWidth          int32
	encHeight         int32
	encoder           string
	decoder           string
	videoBitrate      int32
	rcMaxRate         int32
	rcBufferSize      int32
	crf               int32
	preset            string
	forceKeyInt       int32
	profile           string
	level             int
	gpuIndex          int32
	bitDepth          int32
	streamBitrate     int
	maxLead           time.Duration
	maxInputDatagrams int64
}

// InitTranscodeMPEGTS registers the live MPEG-TS video-transcode command. Unlike
// the generic transcode command, this path owns the UDP reader and feeds raw TS
// datagrams directly to mpegtsxc.LiveTranscoder.
func InitTranscodeMPEGTS(cmdRoot *cobra.Command) error {
	cmd := &cobra.Command{
		Use:   "transcode-mpegts",
		Short: "Transcode video in a live MPEG-TS multiplex",
		Long: "Read MPEG-TS with the Go UDP reader, transcode one selected video PID, " +
			"preserve the selected audio/data streams, and write MPEG-TS to files or UDP.",
		Args: cobra.NoArgs,
		RunE: runTranscodeMPEGTS,
	}

	cmd.Flags().StringP("input", "i", "", "Input URL (udp://host:port or rtp://host:port)")
	cmd.Flags().String("input-packaging", "auto", "Input packaging: auto, ts/raw_ts, or rtp/rtp_ts")
	cmd.Flags().StringArrayP("output", "o", nil, "Output file or udp://host:port; repeat for multiple outputs (default O/O1/output.ts)")
	cmd.Flags().String("mpegts-program-id", "", "Select one MPEG-TS PAT program ID (decimal or 0x-prefixed hex)")
	cmd.Flags().StringSlice("mpegts-pids", nil, "Select exact MPEG-TS elementary PIDs (decimal or 0x-prefixed hex)")

	cmd.Flags().Int32("enc-width", -1, "Output video width (-1 keeps the source width)")
	cmd.Flags().Int32("enc-height", -1, "Output video height (-1 keeps the source height)")
	cmd.Flags().StringP("encoder", "e", "libx264", "Video encoder")
	cmd.Flags().StringP("decoder", "d", "", "Video decoder (empty selects automatically)")
	cmd.Flags().Int32("video-bitrate", -1, "Output video bitrate in bits/s (-1 uses the encoder default)")
	cmd.Flags().Int32("rc-max-rate", 0, "Maximum encoder bitrate in bits/s (0 defaults to video-bitrate)")
	cmd.Flags().Int32("rc-buffer-size", 0, "Encoder rate-control buffer size (0 defaults to video-bitrate)")
	cmd.Flags().Int32("crf", 23, "Encoder CRF quality target")
	cmd.Flags().String("preset", "medium", "Encoder preset")
	cmd.Flags().Int32("force-keyint", 0, "Forced keyframe interval in frames")
	cmd.Flags().String("profile", "", "Encoder profile")
	cmd.Flags().Int("level", 0, "Encoder level (0 selects automatically)")
	cmd.Flags().Int32("gpu-index", -1, "GPU index")
	cmd.Flags().Int32("bitdepth", 8, "Output video bit depth: 8, 10, or 12")
	cmd.Flags().Int("stream-bitrate", 0, "CBR MPEG-TS output bitrate in bits/s (0 disables pacing)")
	cmd.Flags().Duration("max-lead", time.Second, "Maximum lead of passthrough streams over transcoded video")
	cmd.Flags().Int64("max-datagrams", 0, "Stop after this many input UDP datagrams (0 runs until interrupted)")

	cmdRoot.AddCommand(cmd)
	return nil
}

func runTranscodeMPEGTS(cmd *cobra.Command, _ []string) error {
	opts, err := transcodeMPEGTSOptionsFromCommand(cmd)
	if err != nil {
		return err
	}
	return transcodeMPEGTS(cmd.Context(), opts)
}

func transcodeMPEGTSOptionsFromCommand(cmd *cobra.Command) (transcodeMPEGTSOptions, error) {
	var opts transcodeMPEGTSOptions
	var err error

	getString := func(name string) (string, error) {
		value, getErr := cmd.Flags().GetString(name)
		if getErr != nil {
			return "", fmt.Errorf("invalid --%s: %w", name, getErr)
		}
		return value, nil
	}
	if opts.input, err = getString("input"); err != nil {
		return opts, err
	}
	if opts.inputPackaging, err = getString("input-packaging"); err != nil {
		return opts, err
	}
	if opts.outputs, err = cmd.Flags().GetStringArray("output"); err != nil {
		return opts, fmt.Errorf("invalid --output: %w", err)
	}
	if len(opts.outputs) == 0 {
		opts.outputs = []string{"O/O1/output.ts"}
	}

	programID, err := cmd.Flags().GetString("mpegts-program-id")
	if err != nil {
		return opts, fmt.Errorf("invalid --mpegts-program-id: %w", err)
	}
	var programIDs []string
	if strings.TrimSpace(programID) != "" {
		programIDs = []string{programID}
	}
	pids, err := cmd.Flags().GetStringSlice("mpegts-pids")
	if err != nil {
		return opts, fmt.Errorf("invalid --mpegts-pids: %w", err)
	}
	opts.selection, err = parseMPEGTSSelection(programIDs, pids)
	if err != nil {
		return opts, err
	}

	if opts.encWidth, err = cmd.Flags().GetInt32("enc-width"); err != nil {
		return opts, err
	}
	if opts.encHeight, err = cmd.Flags().GetInt32("enc-height"); err != nil {
		return opts, err
	}
	if opts.encoder, err = getString("encoder"); err != nil {
		return opts, err
	}
	if opts.decoder, err = getString("decoder"); err != nil {
		return opts, err
	}
	if opts.videoBitrate, err = cmd.Flags().GetInt32("video-bitrate"); err != nil {
		return opts, err
	}
	if opts.rcMaxRate, err = cmd.Flags().GetInt32("rc-max-rate"); err != nil {
		return opts, err
	}
	if opts.rcBufferSize, err = cmd.Flags().GetInt32("rc-buffer-size"); err != nil {
		return opts, err
	}
	if opts.crf, err = cmd.Flags().GetInt32("crf"); err != nil {
		return opts, err
	}
	if opts.preset, err = getString("preset"); err != nil {
		return opts, err
	}
	if opts.forceKeyInt, err = cmd.Flags().GetInt32("force-keyint"); err != nil {
		return opts, err
	}
	if opts.profile, err = getString("profile"); err != nil {
		return opts, err
	}
	if opts.level, err = cmd.Flags().GetInt("level"); err != nil {
		return opts, err
	}
	if opts.gpuIndex, err = cmd.Flags().GetInt32("gpu-index"); err != nil {
		return opts, err
	}
	if opts.bitDepth, err = cmd.Flags().GetInt32("bitdepth"); err != nil {
		return opts, err
	}
	if opts.streamBitrate, err = cmd.Flags().GetInt("stream-bitrate"); err != nil {
		return opts, err
	}
	if opts.maxLead, err = cmd.Flags().GetDuration("max-lead"); err != nil {
		return opts, err
	}
	if opts.maxInputDatagrams, err = cmd.Flags().GetInt64("max-datagrams"); err != nil {
		return opts, err
	}

	if err := opts.validate(); err != nil {
		return opts, err
	}
	return opts, nil
}

func (o transcodeMPEGTSOptions) validate() error {
	if o.input == "" {
		return errors.New("--input is required")
	}
	if !strings.HasPrefix(o.input, "udp://") && !strings.HasPrefix(o.input, "rtp://") {
		return fmt.Errorf("--input must use udp:// or rtp://: %q", o.input)
	}
	switch strings.ToLower(o.inputPackaging) {
	case "auto", "ts", "raw_ts", "rtp", "rtp_ts":
	default:
		return fmt.Errorf("invalid --input-packaging %q (want auto, ts/raw_ts, or rtp/rtp_ts)", o.inputPackaging)
	}
	if o.selection != nil && len(o.selection.ProgramIDs) > 1 {
		return errors.New("MPEG-TS video transcoding supports exactly one selected program")
	}
	if o.encoder == "" {
		return errors.New("--encoder cannot be empty")
	}
	if o.encWidth == 0 || o.encWidth < -1 {
		return errors.New("--enc-width must be -1 or greater than zero")
	}
	if o.encHeight == 0 || o.encHeight < -1 {
		return errors.New("--enc-height must be -1 or greater than zero")
	}
	if o.videoBitrate == 0 || o.videoBitrate < -1 {
		return errors.New("--video-bitrate must be -1 or greater than zero")
	}
	if o.rcMaxRate < 0 || o.rcBufferSize < 0 {
		return errors.New("--rc-max-rate and --rc-buffer-size cannot be negative")
	}
	if o.crf < 0 || o.crf > 51 {
		return errors.New("--crf must be in the range 0..51")
	}
	if o.level < 0 {
		return errors.New("--level cannot be negative")
	}
	if o.bitDepth != 8 && o.bitDepth != 10 && o.bitDepth != 12 {
		return errors.New("--bitdepth must be 8, 10, or 12")
	}
	if o.streamBitrate < 0 {
		return errors.New("--stream-bitrate cannot be negative")
	}
	if o.streamBitrate > 0 && o.videoBitrate > 0 && o.streamBitrate <= int(o.videoBitrate) {
		return errors.New("--stream-bitrate must exceed --video-bitrate to leave room for passthrough streams")
	}
	if o.maxLead <= 0 {
		return errors.New("--max-lead must be greater than zero")
	}
	if o.maxInputDatagrams < 0 {
		return errors.New("--max-datagrams cannot be negative")
	}
	if len(o.outputs) == 0 {
		return errors.New("at least one output is required")
	}
	for _, output := range o.outputs {
		if output == "" {
			return errors.New("--output cannot be empty")
		}
		if strings.Contains(output, "://") && !strings.HasPrefix(output, "udp://") {
			return fmt.Errorf("unsupported output URL %q (only udp:// is supported)", output)
		}
	}
	return nil
}

func transcodeMPEGTS(parent context.Context, opts transcodeMPEGTSOptions) error {
	inputTransport, err := newMPEGTSInputTransport(opts.input, opts.inputPackaging)
	if err != nil {
		return err
	}
	reader, err := inputTransport.Open()
	if err != nil {
		return fmt.Errorf("open MPEG-TS input %q: %w", opts.input, err)
	}
	defer reader.Close()

	output, err := openMPEGTSOutputs(opts.outputs)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	xc, err := mpegtsxc.NewLiveTranscoder(ctx, mpegtsxc.Config{
		MPEGTSSelection: opts.selection,
		EncWidth:        opts.encWidth,
		EncHeight:       opts.encHeight,
		Ecodec:          opts.encoder,
		Dcodec:          opts.decoder,
		VideoBitrate:    opts.videoBitrate,
		RcMaxRate:       opts.rcMaxRate,
		RcBufferSize:    opts.rcBufferSize,
		CrfStr:          strconv.Itoa(int(opts.crf)),
		Preset:          opts.preset,
		ForceKeyInt:     opts.forceKeyInt,
		Profile:         opts.profile,
		Level:           opts.level,
		GPUIndex:        opts.gpuIndex,
		BitDepth:        opts.bitDepth,
		StreamBitrate:   opts.streamBitrate,
		MaxLead:         opts.maxLead,
	}, output)
	if err != nil {
		return errors.Join(fmt.Errorf("start MPEG-TS transcoder: %w", err), output.Close())
	}

	readDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			xc.Cancel()
			_ = reader.Close()
		case <-readDone:
		}
	}()

	buf := make([]byte, maxUDPDatagramSize)
	var readErr error
	var datagrams int64
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			datagrams++
			if feedErr := xc.Feed(buf[:n]); feedErr != nil {
				readErr = fmt.Errorf("feed MPEG-TS datagram %d: %w", datagrams, feedErr)
				break
			}
			if opts.maxInputDatagrams > 0 && datagrams >= opts.maxInputDatagrams {
				break
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				readErr = fmt.Errorf("read MPEG-TS input: %w", err)
			}
			break
		}
	}
	close(readDone)
	_ = reader.Close()

	finishErr := xc.Finish()
	if ctx.Err() != nil {
		return readErr
	}
	return errors.Join(readErr, finishErr)
}

func newMPEGTSInputTransport(input, packaging string) (transport.Transport, error) {
	packaging = strings.ToLower(packaging)
	if packaging == "auto" {
		if strings.HasPrefix(input, "rtp://") {
			packaging = "rtp"
		} else {
			packaging = "ts"
		}
	}
	switch packaging {
	case "ts", "raw_ts":
		return transport.NewUDPTransport(input, transport.RawTs), nil
	case "rtp", "rtp_ts":
		// LiveTranscoder consumes raw TS, so retain UDP datagram boundaries but
		// remove the input RTP header in the transport handler.
		return transport.NewRTPTransport(input, transport.RawTs), nil
	default:
		return nil, fmt.Errorf("invalid MPEG-TS input packaging %q", packaging)
	}
}

func openMPEGTSOutputs(destinations []string) (io.WriteCloser, error) {
	writers := make([]io.WriteCloser, 0, len(destinations))
	for _, destination := range destinations {
		writer, err := openMPEGTSOutput(destination)
		if err != nil {
			closeErr := closeMPEGTSWriters(writers)
			return nil, errors.Join(err, closeErr)
		}
		writers = append(writers, writer)
	}
	if len(writers) == 1 {
		return writers[0], nil
	}
	return &mpegtsMultiWriteCloser{writers: writers}, nil
}

func openMPEGTSOutput(destination string) (io.WriteCloser, error) {
	if strings.HasPrefix(destination, "udp://") {
		return newMPEGTSUDPWriter(destination)
	}
	parent := filepath.Dir(destination)
	if parent != "." {
		if err := os.MkdirAll(parent, 0755); err != nil {
			return nil, fmt.Errorf("create MPEG-TS output directory %q: %w", parent, err)
		}
	}
	f, err := os.Create(destination)
	if err != nil {
		return nil, fmt.Errorf("create MPEG-TS output %q: %w", destination, err)
	}
	return f, nil
}

type mpegtsMultiWriteCloser struct {
	writers []io.WriteCloser
}

func (w *mpegtsMultiWriteCloser) Write(p []byte) (int, error) {
	var errs []error
	for _, writer := range w.writers {
		n, err := writer.Write(p)
		if err != nil {
			errs = append(errs, err)
		} else if n != len(p) {
			errs = append(errs, io.ErrShortWrite)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *mpegtsMultiWriteCloser) Close() error {
	return closeMPEGTSWriters(w.writers)
}

func closeMPEGTSWriters(writers []io.WriteCloser) error {
	errs := make([]error, 0, len(writers))
	for _, writer := range writers {
		if err := writer.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type mpegtsUDPWriter struct {
	conn        net.Conn
	buf         []byte
	destination string
	sendErrors  uint64
}

func newMPEGTSUDPWriter(destination string) (*mpegtsUDPWriter, error) {
	u, err := url.Parse(destination)
	if err != nil || u.Scheme != "udp" || u.Host == "" {
		return nil, fmt.Errorf("invalid UDP output %q", destination)
	}
	addr, err := net.ResolveUDPAddr("udp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP output %q: %w", destination, err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("open UDP output %q: %w", destination, err)
	}
	log.Info("MPEG-TS UDP output opened", "destination", destination)
	return &mpegtsUDPWriter{
		conn:        conn,
		buf:         make([]byte, 0, mpegtsUDPDatagramSize),
		destination: destination,
	}, nil
}

func (w *mpegtsUDPWriter) Write(p []byte) (int, error) {
	if len(p)%mpegtsPacketSize != 0 {
		return 0, fmt.Errorf("MPEG-TS UDP output received %d bytes, not a multiple of %d", len(p), mpegtsPacketSize)
	}
	w.buf = append(w.buf, p...)
	for len(w.buf) >= mpegtsUDPDatagramSize {
		w.send(w.buf[:mpegtsUDPDatagramSize])
		w.buf = w.buf[mpegtsUDPDatagramSize:]
	}
	return len(p), nil
}

// UDP delivery is best effort. Once the socket is open, a transient send error
// must not stop another configured sink (for example, the simultaneous file
// recording). Errors are counted and periodically reported.
func (w *mpegtsUDPWriter) send(datagram []byte) {
	n, err := w.conn.Write(datagram)
	if err != nil || n != len(datagram) {
		w.sendErrors++
		if w.sendErrors == 1 || w.sendErrors%1000 == 0 {
			log.Warn("MPEG-TS UDP output send failed",
				"destination", w.destination,
				"written", n,
				"expected", len(datagram),
				"err", err,
				"dropped", w.sendErrors)
		}
	}
}

func (w *mpegtsUDPWriter) Close() error {
	if len(w.buf) > 0 {
		w.send(w.buf)
		w.buf = nil
	}
	if w.sendErrors > 0 {
		log.Warn("MPEG-TS UDP output closed with dropped datagrams",
			"destination", w.destination, "dropped", w.sendErrors)
	}
	return w.conn.Close()
}
