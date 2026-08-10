package mpegtsxc

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/broadcastproto/mpegts"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/rtp"
)

// InitTranscodeProcessor creates a TranscodeProcessor and registers it as a bypass
// processor — the retranscode_stream analog of what avpipe.XcInit does for raw_only
// (it cannot live in XcInit itself: this package imports avpipe, so avpipe cannot
// import it back). The returned handle (< -1) works with avpipe.XcRun and
// avpipe.XcCancel unchanged; output parts flow through the standard sequential
// writer (stream index 99, goavpipe.MpegtsSegment) to the host's output opener.
func InitTranscodeProcessor(params *goavpipe.XcParams) (*TranscodeProcessor, int32, error) {
	seqOpenerF := func(inFd int64) mpegts.SequentialOpener {
		// 99 is the stream index used for mpegts output (see avpipe.XcInit): the
		// mpegts segments mux all streams, but the writing interface needs an index.
		return avpipe.NewAVPipeSequentialOutWriter(inFd, 99, goavpipe.MpegtsSegment)
	}
	p, err := NewTranscodeProcessor(params, seqOpenerF)
	if err != nil {
		return nil, -1, err
	}
	return p, goavpipe.Globals.InitBypassProcessor(p), nil
}

// TranscodeProcessor adapts PartsTranscoder to goavpipe.BypassProcessor so a host
// (the content fabric) can drive the parts-mode transcode through the standard
// XcInit/XcRun/XcCancel flow: input RTP datagrams are pushed via WriteDatagram, and
// output datagrams are TLV-framed (rtp_ts) into rotating MPEGTS parts by an
// mpegts.MpegtsPacketProcessor through the sequential opener — exactly like the
// raw_only bypass path, so the host's output plumbing works verbatim.
//
// Part rotation runs on media time: the "now" passed to ProcessDatagram advances
// with the output RTP timestamps (anchored to the wall clock at the first output
// datagram), so each part holds ~PartDuration of media regardless of how bursty the
// input feed is.
//
// Lifecycle: Start (from XcRun) makes WriteDatagram operational; the feeder calls
// WriteDatagram until done, then Flush. Cancel (from XcCancel) unblocks a pending
// WriteDatagram with an error — the feeder must still call Flush to complete the
// shutdown; Wait (from XcRun) blocks until Flush finished.
type TranscodeProcessor struct {
	xcParams   *goavpipe.XcParams
	cfg        Config
	seqOpenerF mpegts.SequentialOpenerFactory

	mu        sync.Mutex
	started   bool
	cancelled bool
	t         *PartsTranscoder
	pp        *mpegts.MpegtsPacketProcessor

	ready chan struct{} // closed when Start completed
	done  chan struct{} // closed when Flush completed
	err   error

	// media-time part rotation
	haveRef      bool
	refWall      time.Time
	lastRtpTs    uint32
	rtpUnwrapped int64

	dropped atomic.Uint64 // never incremented (blocking pipeline); see Start

	flushOnce sync.Once
	flushErr  error
}

// NewTranscodeProcessor validates the params and creates the processor. The pipeline
// starts on Start(fd) (called by avpipe.XcRun for bypass handles).
func NewTranscodeProcessor(params *goavpipe.XcParams, seqOpenerF mpegts.SequentialOpenerFactory) (*TranscodeProcessor, error) {
	if params.InputCfg.CopyMode != goavpipe.CopyModeRetranscode {
		return nil, fmt.Errorf("mpegtsxc: copy mode must be %q", goavpipe.CopyModeRetranscode)
	}
	if params.InputCfg.CopyPackaging != transport.RtpTs {
		return nil, fmt.Errorf("mpegtsxc: only rtp_ts copy packaging is supported (got %q)", params.InputCfg.CopyPackaging)
	}
	if params.InputCfg.StreamBitrate <= 0 {
		return nil, fmt.Errorf("mpegtsxc: stream_bitrate is required")
	}
	if params.VideoBitrate <= 0 {
		return nil, fmt.Errorf("mpegtsxc: video_bitrate is required (the encoder must be capped below stream_bitrate)")
	}
	params.InputCfg.Processor = params.InputCfg.Processor.ApplyDefaults()

	cfg := Config{
		EncWidth:      params.EncWidth,
		EncHeight:     params.EncHeight,
		Ecodec:        params.Ecodec,
		Dcodec:        params.Dcodec,
		VideoBitrate:  params.VideoBitrate,
		RcMaxRate:     params.RcMaxRate,
		RcBufferSize:  params.RcBufferSize,
		CrfStr:        params.CrfStr,
		Preset:        params.Preset,
		ForceKeyInt:   params.ForceKeyInt,
		Profile:       params.Profile,
		Level:         params.Level,
		GPUIndex:      params.GPUIndex,
		BitDepth:      params.BitDepth,
		StreamBitrate: params.InputCfg.StreamBitrate,
	}.withDefaults()

	return &TranscodeProcessor{
		xcParams:   params,
		cfg:        cfg,
		seqOpenerF: seqOpenerF,
		ready:      make(chan struct{}),
		done:       make(chan struct{}),
	}, nil
}

func (p *TranscodeProcessor) XcParams() *goavpipe.XcParams { return p.xcParams }

// Start creates the part writer and the transcode pipeline. Non-blocking; called by
// avpipe.XcRun with the fd of the opened input URL.
func (p *TranscodeProcessor) Start(fd int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return fmt.Errorf("mpegtsxc: transcode processor already started")
	}
	if p.cancelled {
		return fmt.Errorf("mpegtsxc: transcode processor cancelled before start")
	}

	tsCfg := mpegts.TsConfig{
		SegmentLengthSec: uint64(p.xcParams.InputCfg.Processor.PartDuration.Duration() / time.Second),
		Packaging:        transport.RtpTs,
	}
	p.pp = mpegts.NewMpegtsPacketProcessor(tsCfg, p.seqOpenerF(fd), fd)
	// The stats exporter dereferences the (normally consumer-registered) dropped
	// counter; this pipeline never drops - it blocks for backpressure.
	p.pp.RegisterPacketsDropped(&p.dropped)

	t, err := NewPartsTranscoder(nil, p.cfg, p.emit)
	if err != nil {
		p.pp.Stop()
		return err
	}
	p.t = t
	p.started = true
	close(p.ready)

	log.Info("mpegts-xc transcode processor started", "fd", fd, "url", p.xcParams.Url,
		"streamBitrate", p.cfg.StreamBitrate, "videoBitrate", p.cfg.VideoBitrate,
		"partDuration", p.xcParams.InputCfg.Processor.PartDuration)
	return nil
}

// emit receives output datagrams from the pipeline (in output order, on the mux
// goroutine) and hands them to the part writer on a media-time clock, clamped to
// the wall clock: at the live edge parts rotate on media time (drift-free), but
// while catching up - when media time advances faster than real time - rotation
// follows the wall clock instead, so it never outpaces the host's wall-clock
// minimum-rotation-period limit (parts then simply hold more media).
func (p *TranscodeProcessor) emit(d OutputDatagram) error {
	if !p.haveRef {
		p.haveRef = true
		p.refWall = time.Now()
		p.lastRtpTs = d.RtpTs
		p.pp.ReportStart()
		p.pp.StartReportingStats()
	}
	// Unwrap the (non-decreasing) output RTP ts and advance media time from it.
	p.rtpUnwrapped += int64(int32(d.RtpTs - p.lastRtpTs))
	p.lastRtpTs = d.RtpTs

	elapsed := rtp.TicksToDuration(p.rtpUnwrapped)
	if wall := time.Since(p.refWall); wall < elapsed {
		elapsed = wall
	}
	p.pp.ProcessDatagram(p.refWall.Add(elapsed), d.Data)
	return nil
}

// WriteDatagram pushes one complete input RTP datagram (one rtp_ts TLV value).
// Blocks for backpressure. Safe to call before Start completes. Returns the sticky
// pipeline error once the job has failed or been cancelled.
func (p *TranscodeProcessor) WriteDatagram(datagram []byte) error {
	select {
	case <-p.done:
		return fmt.Errorf("mpegtsxc: transcode processor stopped")
	default:
	}
	select {
	case <-p.ready:
	case <-p.done:
		return fmt.Errorf("mpegtsxc: transcode processor stopped")
	}
	return p.t.Feed(datagram)
}

// Flush drains the pipeline (encoder flush, merge drain, final datagram), closes the
// current output part and completes the processor — Wait unblocks. Must be called by
// the feeder (never concurrently with WriteDatagram), including after Cancel.
func (p *TranscodeProcessor) Flush() error {
	p.flushOnce.Do(func() {
		select {
		case <-p.ready:
		default:
			// Never started: nothing to drain.
			p.mu.Lock()
			p.cancelled = true
			p.mu.Unlock()
			close(p.done)
			return
		}
		p.flushErr = p.t.Finish()
		p.pp.Stop()
		p.pp.CloseOutput()
		p.mu.Lock()
		p.err = p.flushErr
		p.mu.Unlock()
		close(p.done)
	})
	return p.flushErr
}

// Cancel aborts the transcode: a blocked WriteDatagram returns with an error and
// subsequent calls fail fast. The feeder must still call Flush to finish shutdown.
func (p *TranscodeProcessor) Cancel() {
	p.mu.Lock()
	p.cancelled = true
	t := p.t
	p.mu.Unlock()
	if t != nil {
		t.Cancel()
	}
}

// Status implements goavpipe.BypassProcessor.
func (p *TranscodeProcessor) Status() (running bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return false, p.err
	default:
	}
	return p.started, p.err
}

// Wait blocks until the processor has fully shut down (Flush completed).
func (p *TranscodeProcessor) Wait() {
	<-p.done
}

// Stats returns a snapshot of the pipeline counters (zero value before Start).
func (p *TranscodeProcessor) Stats() StatsSnapshot {
	p.mu.Lock()
	t := p.t
	p.mu.Unlock()
	if t == nil {
		return StatsSnapshot{}
	}
	return t.Stats()
}
