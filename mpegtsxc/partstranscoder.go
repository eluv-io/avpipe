package mpegtsxc

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe/broadcastproto/mpegts"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
)

// PartsTranscoder transcodes the video PID of an RTP-encapsulated MPEGTS stream in a
// pure timestamp domain (no wall clock): complete input RTP datagrams in — e.g. the
// TLV values of recorded rtp_ts MPEGTS parts — and output RTP datagrams out, emitted
// as a CBR mux on a virtual output clock with synthesized RTP timestamps and PCR.
// Throughput is governed entirely by the caller and the encoder, so a caller reading
// recorded parts catches up naturally after a restart.
//
// Concurrency contract: Feed and Finish must be called from a single goroutine (Feed
// blocks for backpressure); Cancel may be called from any goroutine. The emit
// callback runs on an internal goroutine, in output order; a non-nil error from emit
// aborts the job.
type PartsTranscoder struct {
	cfg        Config
	classifier *Classifier
	stats      *Stats
	timeline   *mediaTimeline
	gaps       *rtpGapDetector
	fifo       *PassthroughFifo
	proc       *processor
	packager   *rtpPackager
	videoCh    chan []byte
	session    *xcSession

	xcEnded  chan struct{} // closed when XcInit/XcRun returned; xcErr set before
	xcErr    error
	muxEnded chan struct{} // closed when the merge/packager goroutine returned
	muxErr   error

	// feedMu serializes Feed with Finish's close of videoCh: cancellation can leave
	// a caller goroutine parked in Feed's channel send while another goroutine
	// (observing the cancel) already calls Finish - closing the channel under a
	// pending send panics. A blocked Feed holds feedMu; Finish waits for it (the
	// send is always released: by the encoder draining the channel, or by xcEnded
	// closing after a cancel or encoder failure).
	feedMu       sync.Mutex
	videoChClose bool // set under feedMu before videoCh is closed

	errMu    sync.Mutex
	firstErr error

	rtpParamsSet bool
	badDatagrams atomic.Uint64

	stopWatch  chan struct{}
	finishOnce sync.Once
	finishErr  error
}

// NewPartsTranscoder starts the parts-mode pipeline. cfg.StreamBitrate is required —
// it defines the virtual CBR output clock and must exceed the video bitrate plus the
// passthrough peak. Cancellation of ctx aborts the transcode (equivalent to Cancel).
func NewPartsTranscoder(ctx context.Context, cfg Config, emit func(OutputDatagram) error) (*PartsTranscoder, error) {
	cfg = cfg.withDefaults()
	if cfg.StreamBitrate <= 0 {
		return nil, fmt.Errorf("mpegtsxc: StreamBitrate is required in parts mode")
	}
	if cfg.VideoBitrate <= 0 {
		return nil, fmt.Errorf("mpegtsxc: VideoBitrate is required in parts mode (the encoder must be capped below StreamBitrate)")
	}
	if err := validateTranscodeSelection(cfg.MPEGTSSelection); err != nil {
		return nil, err
	}

	selector, err := mpegts.NewSelector(cfg.MPEGTSSelection)
	if err != nil {
		return nil, fmt.Errorf("mpegtsxc: invalid MPEG-TS selection: %w", err)
	}
	classifier := NewClassifier()
	stats := newStats()
	timeline := &mediaTimeline{}
	gaps := newRtpGapDetector(cfg.SeqGapThreshold, int64(cfg.TsGapThreshold.Seconds()*90000))

	// The FIFO must absorb the passthrough packets produced while the encoder works
	// through its latency (lookahead + B-frames); pushes block when it fills.
	fifo := NewPassthroughFifo(65536)
	proc := newProcessor(classifier, selector, fifo, stats, nil, timeline)
	packager := newRtpPackager(&cfg, classifier, emit)

	videoCh := make(chan []byte, 8192)
	videoOutCh := make(chan videoPkt, 8192)

	t := &PartsTranscoder{
		cfg:        cfg,
		classifier: classifier,
		stats:      stats,
		timeline:   timeline,
		gaps:       gaps,
		fifo:       fifo,
		proc:       proc,
		packager:   packager,
		videoCh:    videoCh,
		xcEnded:    make(chan struct{}),
		muxEnded:   make(chan struct{}),
		stopWatch:  make(chan struct{}),
	}

	t.session = startXcSession(&cfg, videoCh, videoOutCh, classifier, true /* stripPCR */, timeline)

	go func() {
		err := <-t.session.done
		t.xcErr = err
		if err != nil {
			t.fail(err)
		}
		close(t.xcEnded)
	}()

	go func() {
		err := muxMerge(fifo, videoOutCh, packager.Packet)
		if err == nil {
			err = packager.Finish()
		}
		t.muxErr = err
		if err != nil {
			t.fail(err)
			t.session.Cancel()
			// Drain both inputs so blocked producers (Feed's FIFO pushes, the xc leg)
			// unwind; the channels are closed by Finish / the xc session.
			go func() {
				for range fifo.Chan() {
				}
			}()
			go func() {
				for range videoOutCh {
				}
			}()
		}
		close(t.muxEnded)
	}()

	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				t.fail(ctx.Err())
				t.Cancel()
			case <-t.stopWatch:
			}
		}()
	}

	return t, nil
}

// Feed pushes one complete input RTP datagram (12-byte header included, i.e. one
// rtp_ts TLV value). Blocks for backpressure. Returns the sticky pipeline error once
// the job has failed or been cancelled; datagrams that fail RTP parsing are counted
// and skipped without error. Safe against a concurrent Finish (see feedMu).
func (t *PartsTranscoder) Feed(rtpDatagram []byte) error {
	t.feedMu.Lock()
	defer t.feedMu.Unlock()
	if t.videoChClose {
		return t.errOrClosed()
	}
	if err := t.err(); err != nil {
		return err
	}

	hdr, err := transport.ParseRTPHeader(rtpDatagram)
	if err != nil {
		n := t.badDatagrams.Inc()
		if n%1000 == 1 {
			log.Warn("mpegts-xc: skipping bad input RTP datagram", "err", err, "count", n)
		}
		return nil
	}
	if !t.rtpParamsSet {
		t.rtpParamsSet = true
		t.packager.SetInputRtpParams(hdr.SSRC, hdr.PayloadType)
	}
	if t.gaps.Update(hdr.SequenceNumber, hdr.Timestamp) {
		t.packager.NoteDiscontinuity()
	}

	// Passthrough pushes inside may block until the merge drains them.
	forward, err := t.proc.handleDatagram(rtpDatagram[hdr.ByteLength():])
	if err != nil {
		return err
	}

	if len(forward) > 0 {
		select {
		case t.videoCh <- forward:
		case <-t.xcEnded:
			// The transcode leg is gone (error or cancel) — don't block forever.
			return t.errOrClosed()
		}
	}
	return t.err()
}

// Finish signals input EOF, drains the pipeline (encoder flush, exact-merge drain,
// final partial datagram) and returns the first pipeline error. Must not be called
// concurrently with Feed. Idempotent.
func (t *PartsTranscoder) Finish() error {
	t.finishOnce.Do(func() {
		// Wait for any in-flight Feed before closing its send channel (a pending
		// send on a channel that gets closed panics). The Feed always exits: the
		// encoder drains the channel, or xcEnded closes on cancel/failure.
		t.feedMu.Lock()
		t.videoChClose = true
		close(t.videoCh) // EOF to avpipe => encoder flush, then videoOutCh closes
		t.feedMu.Unlock()

		<-t.xcEnded
		t.fifo.Close()
		<-t.muxEnded
		close(t.stopWatch)

		if t.xcErr != nil {
			t.finishErr = t.xcErr
		} else {
			t.finishErr = t.muxErr
		}
	})
	return t.finishErr
}

// Cancel aborts the transcode immediately. The caller should still call Finish to
// drain and collect the error. Idempotent, safe from any goroutine.
func (t *PartsTranscoder) Cancel() {
	// Wrap so the sticky error is attributable to Cancel (vs. an external
	// context) when it surfaces from Feed/Finish; errors.Is still matches.
	t.fail(fmt.Errorf("mpegtsxc: transcode cancelled: %w", context.Canceled))
	t.session.Cancel()
}

// Stats returns a snapshot of the pipeline counters.
func (t *PartsTranscoder) Stats() StatsSnapshot {
	sn := t.stats.snapshot(t.classifier.VideoPID())
	populateSelectionStats(&sn, t.proc.selector)
	sn.FifoLen = t.fifo.Len()
	sn.OutDatagrams = t.packager.OutDatagrams()
	sn.Discontinuities = t.gaps.Discontinuities()
	sn.GridBehind = t.packager.GridBehind()
	sn.BadInputDatagrams = t.badDatagrams.Load()
	return sn
}

// BadDatagrams returns the count of input datagrams skipped due to RTP parse errors.
func (t *PartsTranscoder) BadDatagrams() uint64 { return t.badDatagrams.Load() }

func (t *PartsTranscoder) fail(err error) {
	t.errMu.Lock()
	if t.firstErr == nil {
		t.firstErr = err
	}
	t.errMu.Unlock()
}

func (t *PartsTranscoder) err() error {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.firstErr
}

func (t *PartsTranscoder) errOrClosed() error {
	if err := t.err(); err != nil {
		return err
	}
	return fmt.Errorf("mpegtsxc: transcode leg ended")
}
