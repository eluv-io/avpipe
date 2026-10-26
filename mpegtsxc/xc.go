package mpegtsxc

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe/broadcastproto/mpegts"
	"github.com/eluv-io/avpipe/goavpipe"
)

// Config holds the transcode parameters shared by live and parts mode.
type Config struct {
	// MPEGTSSelection filters the source multiplex before classification and
	// transcoding. The transcode pipeline requires the resolved selection to
	// contain exactly one program and one explicitly selected video PID.
	MPEGTSSelection *goavpipe.MPEGTSSelection

	// Encoder (both modes)
	EncWidth  int32  // downscale target width (-1 = source width)
	EncHeight int32  // downscale target height (-1 = source height)
	Ecodec    string // video encoder (e.g. libx264, libx265, h264_nvenc, hevc_nvenc)
	Dcodec    string // video decoder (empty = auto; e.g. h264_cuvid, hevc_cuvid)
	// VideoBitrate in bits/s (-1 = encoder default); also sets RcMaxRate/RcBufferSize
	// to cap the encoder so the output fits the CBR mux
	VideoBitrate int32
	// RcMaxRate / RcBufferSize override the VBV cap (0 = default to VideoBitrate)
	RcMaxRate    int32
	RcBufferSize int32
	// CrfStr is the x264/x265 CRF quality target. Note that CRF takes precedence over
	// VideoBitrate in libx264 (capped-CRF mode); empty keeps the avpipe default ("23").
	CrfStr      string
	Preset      string // encoder preset (x264: ultrafast..placebo; nvenc: p1..p7)
	ForceKeyInt int32  // GOP size / forced keyframe interval in frames (0 = encoder default)
	Profile     string // encoder profile (e.g. "high", "main")
	Level       int    // encoder level (0 = encoder default)
	GPUIndex    int32  // GPU index for nvenc/cuvid (-1/0 = default GPU)
	BitDepth    int32  // encode bit depth (0 = default 8)

	// Timing
	// StreamBitrate is the exact output TS bitrate in bits/s for CBR output via null
	// padding. Required in parts mode (defines the virtual output clock); in live mode
	// 0 disables pacing. Must be higher than VideoBitrate + passthrough peak.
	StreamBitrate int
	PcrLead       time.Duration // PCR / video-emission lead ahead of DTS (default 300ms)
	PcrInterval   time.Duration // output PCR cadence (default 35ms; DVB max is 40ms)
	MaxLead       time.Duration // live mode only: max passthrough lead over video (default 1s)

	// Parts mode (RTP datagrams in/out)
	DatagramPackets int           // TS packets per output datagram (default 7)
	SeqGapThreshold int           // input discontinuity: unwrapped RTP seq jump (default 64)
	TsGapThreshold  time.Duration // input discontinuity: RTP timestamp jump (default 1s)
	SSRC            uint32        // output RTP SSRC (0 = reuse the input's)
	PayloadType     uint8         // output RTP payload type (0 = reuse the input's)

	// XcParamsHook, if set, gets final say on the avpipe transcode params.
	XcParamsHook func(*goavpipe.XcParams)
}

func (c Config) withDefaults() Config {
	if c.EncWidth == 0 {
		c.EncWidth = -1
	}
	if c.EncHeight == 0 {
		c.EncHeight = -1
	}
	if c.Ecodec == "" {
		c.Ecodec = "libx264"
	}
	if c.VideoBitrate == 0 {
		c.VideoBitrate = -1
	}
	if c.PcrLead <= 0 {
		c.PcrLead = defaultPcrLead
	}
	if c.PcrInterval <= 0 {
		c.PcrInterval = pcrOutIntervalMs * time.Millisecond
	}
	if c.MaxLead <= 0 {
		c.MaxLead = time.Second
	}
	if c.DatagramPackets <= 0 {
		c.DatagramPackets = 7
	}
	if c.SeqGapThreshold <= 0 {
		c.SeqGapThreshold = 64
	}
	if c.TsGapThreshold <= 0 {
		c.TsGapThreshold = time.Second
	}
	return c
}

// StatsSnapshot is a point-in-time view of the pipeline counters.
type StatsSnapshot struct {
	Datagrams uint64 // input datagrams processed
	TsVideo   uint64 // input TS packets on the video PID
	TsOther   uint64 // input TS packets passed through (other + PSI)
	VideoPID  int    // source video PID (-1 until the PMT is parsed)

	SelectionReady     bool
	SelectedProgramIDs []uint16
	SelectedPMTPIDs    []uint16
	SelectedPCRPIDs    []uint16
	SelectedPIDs       []uint16

	FifoLen        int    // passthrough FIFO occupancy
	FifoDropped    uint64 // passthrough packets dropped (live mode only)
	ForwardDropped uint64 // video datagrams dropped because avpipe was behind (live mode only)

	OutDatagrams      uint64 // output RTP datagrams emitted (parts mode)
	Discontinuities   uint64 // input discontinuities detected (parts mode)
	BadInputDatagrams uint64 // input datagrams skipped due to RTP parse errors (parts mode)

	// GridBehind is how far content currently lags the CBR slot grid (parts mode).
	// Persistently > 0 means StreamBitrate is too low for the content rate: the
	// output media timeline stretches and ts-paced consumers fall behind real time.
	GridBehind time.Duration

	// OtherAheadOfVideoMs is how far each non-video PID's PES PTS leads the video's
	// (the empirical input for choosing MaxLead).
	OtherAheadOfVideoMs map[int]float64

	CbrMode     bool // live mode: CBR pacer active
	PhaseLocked bool // live CBR mode: pacer is phase-locked to the source clock
}

// LiveTranscoder preserves the CLI/UDP behavior: raw-TS datagrams in, a continuous TS
// packet stream out through sink, optionally paced to StreamBitrate on the wall clock
// with the output phase-locked to the source clock.
type LiveTranscoder struct {
	cfg        Config
	classifier *Classifier
	stats      *Stats
	srcClock   *sourceClock // nil unless CBR
	fifo       *PassthroughFifo
	proc       *processor
	videoCh    chan []byte
	session    *xcSession
	muxDone    chan error

	forwardDropped atomic.Uint64
	stopWatch      chan struct{}
	finishOnce     sync.Once
	finishErr      error
}

// NewLiveTranscoder starts the live-mode pipeline. The sink receives whole 188-byte TS
// packets (paced to StreamBitrate when > 0) and is closed when the pipeline drains.
// Cancellation of ctx aborts the transcode (equivalent to Cancel).
func NewLiveTranscoder(ctx context.Context, cfg Config, sink io.WriteCloser) (*LiveTranscoder, error) {
	cfg = cfg.withDefaults()

	if err := validateTranscodeSelection(cfg.MPEGTSSelection); err != nil {
		return nil, err
	}
	selector, err := mpegts.NewSelector(cfg.MPEGTSSelection)
	if err != nil {
		return nil, fmt.Errorf("mpegtsxc: invalid MPEG-TS selection: %w", err)
	}
	classifier := NewClassifier()
	stats := newStats()

	// Source clock for phase-locking the pacer to the source (only in CBR mode)
	cbr := cfg.StreamBitrate > 0
	var srcClock *sourceClock
	if cbr {
		srcClock = newSourceClock()
	}

	// Passthrough FIFO for "other" packets (audio, data, PCR-PID, PSI)
	// Sized so it can safely hold passthrough packets for the duration of transcoding.
	fifo := NewPassthroughFifo(16384)
	proc := newProcessor(classifier, selector, fifo, stats, srcClock, nil)

	// Channel from classified/filtered input -> avpipe xc; closing signals EOF
	videoCh := make(chan []byte, 8192)
	// Channel avpipe xc -> mpegts muxer/interleaver (closed by the xc session)
	videoOutCh := make(chan videoPkt, 8192)

	t := &LiveTranscoder{
		cfg:        cfg,
		classifier: classifier,
		stats:      stats,
		srcClock:   srcClock,
		fifo:       fifo,
		proc:       proc,
		videoCh:    videoCh,
		muxDone:    make(chan error, 1),
		stopWatch:  make(chan struct{}),
	}

	t.session = startXcSession(&cfg, videoCh, videoOutCh, classifier, cbr, nil)

	// In CBR mode the pacer wraps the sink: it emits at exactly StreamBitrate and pads
	var out io.WriteCloser = sink
	if cbr {
		out = newPacer(sink, cfg.StreamBitrate, classifier, srcClock, ticks27(cfg.PcrLead))
		log.Info("mpegts-xc CBR output", "streamBitrate", cfg.StreamBitrate, "videoBitrate", cfg.VideoBitrate)
	}
	go func() {
		t.muxDone <- muxOutput("live", out, fifo, videoOutCh, ticks27(cfg.MaxLead))
	}()

	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				t.Cancel()
			case <-t.stopWatch:
			}
		}()
	}

	return t, nil
}

// Feed pushes one raw-TS datagram (whole 188-byte packets, no RTP header) into the
// pipeline. Never blocks: if the video transcode is behind, the datagram's video
// packets are dropped and counted.
func (t *LiveTranscoder) Feed(tsDatagram []byte) error {
	forward, err := t.proc.handleDatagram(tsDatagram)
	if err != nil {
		return err
	}
	if len(forward) > 0 {
		select {
		case t.videoCh <- forward:
		default:
			n := t.forwardDropped.Inc()
			if n%100 == 1 {
				log.Warn("mpegts-xc: dropping forwarded packets, avpipe behind", "dropped", n)
			}
		}
	}
	return nil
}

// Finish signals input EOF, drains the pipeline (encoder flush, FIFO drain) and
// returns the first pipeline error. Idempotent.
func (t *LiveTranscoder) Finish() error {
	t.finishOnce.Do(func() {
		close(t.videoCh)
		t.fifo.Close()
		xcErr := <-t.session.done
		muxErr := <-t.muxDone
		close(t.stopWatch)
		if xcErr != nil {
			t.finishErr = xcErr
		} else {
			t.finishErr = muxErr
		}
	})
	return t.finishErr
}

// Cancel aborts the avpipe transcode immediately. The caller should still call Finish
// to drain and collect the error. Idempotent, safe from any goroutine.
func (t *LiveTranscoder) Cancel() {
	t.session.Cancel()
}

// Stats returns a snapshot of the pipeline counters.
func (t *LiveTranscoder) Stats() StatsSnapshot {
	sn := t.stats.snapshot(t.classifier.VideoPID())
	populateSelectionStats(&sn, t.proc.selector)
	sn.FifoLen = t.fifo.Len()
	sn.FifoDropped = t.fifo.Dropped()
	sn.ForwardDropped = t.forwardDropped.Load()
	sn.CbrMode = t.srcClock != nil
	if t.srcClock != nil {
		sn.PhaseLocked = t.srcClock.Locked()
	}
	return sn
}

func validateTranscodeSelection(selection *goavpipe.MPEGTSSelection) error {
	if err := selection.Validate(); err != nil {
		return fmt.Errorf("mpegtsxc: invalid MPEG-TS selection: %w", err)
	}
	if selection != nil && len(selection.ProgramIDs) > 1 {
		return fmt.Errorf("mpegtsxc: retranscode supports exactly one selected program")
	}
	return nil
}

func populateSelectionStats(stats *StatsSnapshot, selector *mpegts.Selector) {
	if selector == nil {
		return
	}
	snapshot := selector.Snapshot()
	stats.SelectionReady = snapshot.Ready
	stats.SelectedProgramIDs = snapshot.ProgramIDs
	stats.SelectedPMTPIDs = snapshot.PMTPIDs
	stats.SelectedPCRPIDs = snapshot.PCRPIDs
	stats.SelectedPIDs = snapshot.SelectedPIDs
}

// LogStats logs the periodic pipeline stats (CLI convenience).
func (t *LiveTranscoder) LogStats() {
	t.stats.Log(t.classifier.VideoPID(), t.fifo.Len(), t.fifo.Dropped())
}
