package mpegts

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Comcast/gots/v2/packet"
	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe/broadcastproto/tlv"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	mio "github.com/eluv-io/common-go/media/io"
	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/common-go/media/tracker"
	elog "github.com/eluv-io/log-go"
	"github.com/eluv-io/utc-go"
)

const (
	PcrTs  uint64 = 27_000_000
	PcrMax uint64 = ((1 << 33) * 300) + (1 << 9)
)

// StripTsPadding indicates whether the payload of TS padding packets in RTP-TS streams should be stripped. For now
// this is not configurable per stream, since it has low performance impact and potentially saves bandwidth.
var StripTsPadding = atomic.NewBool(false)

// outputTlvWrapCap is the head room reserved in front of each pooled datagram so the TLV output framing can be written
// into it zero-copy (see Packet.FrameTlv / ProcessDatagramPacket): a TLV header plus, for ATS-TS, the
// arrival-timestamp prefix.
const outputTlvWrapCap = tlv.TLV_HEADER_LEN + tlv.AtsTimestampLen

var mpegtslog = elog.Get("avpipe/broadcastproto/mpegts")

type SequentialOpener interface {
	OpenNext() (io.WriteCloser, error)
	Stat(args any) error
	ReportStart() error
	// ReportBytesWritten pushes a lightweight, high-frequency signal for stall detection - the total number of bytes
	// written so far - separate from Stat's full ExportedStats push, which runs on its own, much slower cadence. See
	// MpegtsPacketProcessor's StartReportingStats.
	ReportBytesWritten(n uint64) error
}

type MpegtsPacketProcessor struct {
	cfg TsConfig

	inFd         int64
	opener       SequentialOpener
	segStartTime time.Time // wall-clock time at the start of the current segment
	currentWc    io.WriteCloser

	stats *TSStats
	// rtpStats is used to keep track of RTP-specific information in the case that the packaging is
	// RTP-MPEGTS. It is _nil_ iff cfg.Packaging is not RTP-TS.
	rtpStats *RTPStats

	// tracker tracks the incoming stream's timing and integrity (continuity counters, PCR/RTP clock correlation, gap
	// detection, input-validation errors, etc.); its stats are surfaced via ExportedStats.Stream. It is safe for
	// concurrent use by its own design (TrackPacket from the packet-processing path, Snapshot from
	// reportFullStatsLoop), independent of any locking in MpegtsPacketProcessor itself.
	tracker tracker.MediaTracker

	// connStats, if set, reports the underlying network connection's statistics (e.g. SRT protocol stats),
	// surfaced via ExportedStats.Srt. nil for a source that doesn't report them (e.g. plain UDP), or before the
	// caller has wired one in - a NetReader doesn't exist yet when NewMpegtsPacketProcessor runs, so this is set
	// after the fact via SetConnStatsSource, not passed to the constructor.
	connStats connStatsSource

	outBuf   []byte // Preallocated byte buffer
	closeCh  chan struct{}
	stopOnce sync.Once
	stopErr  error

	startLogged bool // ensure logging TS processing route once
}

func NewMpegtsPacketProcessor(cfg TsConfig, seqOpener SequentialOpener, inFd int64) *MpegtsPacketProcessor {
	var rtpStats *RTPStats
	if cfg.Packaging == transport.RtpTs {
		rtpStats = &RTPStats{}
	}
	mpegtslog.Info("mpegts packet processor created",
		"fd", inFd,
		"packaging", string(cfg.Packaging),
		"rtp_stats", rtpStats != nil,
		"segment_length_sec", cfg.SegmentLengthSec)
	return &MpegtsPacketProcessor{
		cfg:      cfg,
		opener:   seqOpener,
		inFd:     inFd,
		stats:    NewTSStats(),
		rtpStats: rtpStats,
		tracker: tracker.NewMediaTracker(
			fmt.Sprintf("fd-%d", inFd),
			tracker.Config{Rtp: cfg.Packaging == transport.RtpTs}),
		// Max datagram size plus room for the TLV header and (for ATS-TS) the arrival timestamp prefix.
		outBuf:  make([]byte, 64*1024+tlv.TLV_HEADER_LEN+tlv.AtsTimestampLen),
		closeCh: make(chan struct{}),
	}
}

type TsConfig struct {
	SegmentLengthSec uint64
	Packaging        transport.TsPackagingMode

	AnalyzeVideo bool
	AnalyzeData  bool
}

// TSStats holds avpipe's own operational stats about its output pipeline (segmentation, writing, channel
// backpressure). All stream-integrity stats (continuity-counter errors, PCR wraps, input-validation errors, per-PID
// stream structure, and the total packets/bytes received) now live in mpp.tracker (tracker.MediaTracker) and are
// surfaced via ExportedStats.Stream; ExportedStats.populate also maps a subset of them onto the legacy
// ExportedTSStats/ExportedRTPStats fields below for backward JSON compatibility.
type TSStats struct {
	PacketsWritten atomic.Uint64
	// PacketsDropped is updated by the sender to the channel, which is why it is a pointer
	PacketsDropped *atomic.Uint64
	BytesWritten   atomic.Uint64
	// BadPackets counts TS packets that fail CheckErrors(); computed here (not sourced from mpp.tracker) so it isn't
	// conflated with mpp.tracker's own BadPackets, which counts a different, RTP-only condition. See
	// ExportedStats.populate.
	BadPackets atomic.Uint64
	// FaultyPaddingPackets/StrippedPaddingPackets are byproducts of the padding-stripping operation itself (see
	// stripTsPadding/RemoveTsPadding) - an avpipe output-pipeline concern, not a stream-integrity stat.
	FaultyPaddingPackets   atomic.Uint64
	StrippedPaddingPackets atomic.Uint64

	MaxBufInPeriod atomic.Uint64
	MinBufInPeriod atomic.Uint64

	NumSegments    atomic.Int64
	NumTimedRotate atomic.Int64

	ErrorsOther         atomic.Uint64
	ErrorsOpeningOutput atomic.Uint64
	ErrorsWriting       atomic.Uint64
}

// RTPStats holds avpipe's own RTP-specific bookkeeping that mpp.tracker does not itself expose. Sequence/timestamp
// tracking, gap detection, and clock correlation are all delegated to mpp.tracker's "rtp" ClockStats; see
// ExportedStats.populate.
type RTPStats struct {
	// BadPackets counts a malformed RTP header, an unsupported RTP version, or at least one contained TS packet
	// failing CheckErrors() - computed here (not sourced from mpp.tracker) so it isn't conflated with mpp.tracker's
	// own BadPackets, which counts RTP-layer failures only. See ExportedStats.populate.
	BadPackets atomic.Uint64
}

func NewTSStats() *TSStats {
	return &TSStats{}
}

// ProcessDatagramPacket analyzes and writes the datagram held by pkt, framing the TLV output zero-copy into pkt's
// reserved head room (see Packet.FrameTlv), and decoding RTP/MPEG-TS lazily via pkt.Rtp()/pkt.Ts(). Framing does not
// mutate pkt (Data and the decode cursor are untouched), so this is safe even when pkt is shared with other
// consumers through the pool's reference counting - as long as no other consumer decodes a layer on the same pkt (see
// the pkt.Rtp() call below).
func (mpp *MpegtsPacketProcessor) ProcessDatagramPacket(now time.Time, pkt *pktpool.Packet) {
	datagram := pkt.Data
	if !mpp.startLogged {
		mpp.startLogged = true
		mpegtslog.Info("mpegts processing first datagram",
			"fd", mpp.inFd,
			"packaging", string(mpp.cfg.Packaging),
			"datagram_len", len(datagram))
		// Prompt first ping so stall detection doesn't have to wait for reportBytesWrittenLoop's first tick -
		// regardless of transport: both RTP and raw TS packaging feed mpp.stats.BytesWritten via writeDatagram below,
		// and content-fabric's CopyModeRawOnly stall check applies independently of packaging. Deferred because
		// BytesWritten is only updated by writeDatagram, so reading it now (before this datagram's bytes are counted)
		// would report a stale value one packet behind.
		defer mpp.reportBytesWritten()
	}

	// Feed the tracker unconditionally, before any of the accept/reject checks below, so it sees (and counts) every
	// datagram exactly as received - including ones this method goes on to drop. Its returned error is used only for
	// statistics (surfaced via ExportedStats.Stream/populate below); it does not affect the write decision below,
	// which mirrors avpipe's own framing requirements (RTP header length, exact multiple-of-188 TS payload) rather
	// than the tracker's more general integrity checks.
	_ = mpp.tracker.TrackPacket(utc.New(now), pkt)

	mpegtsOffset := 0

	if mpp.cfg.Packaging == transport.RtpTs {
		if len(datagram) < 12+188 { // RTP header + at least one TS packet
			return
		}

		// Only MpegTsConsumer decodes any layer of a shared pkt today (Fmp4Consumer only reads .Data), so calling
		// Rtp()/Ts() here is safe. If a future consumer ever decodes a layer on the same shared pkt, that call must be
		// sequenced relative to this one - pktpool.Packet's decode cursor is shared and forward-only. mpp.tracker's
		// call above already decoded this layer, so this is a cached accessor, not a re-parse.
		rtpLayer, err := pkt.Rtp()
		if err != nil {
			mpp.rtpStats.BadPackets.Inc()
			return
		}
		hdr := rtpLayer.Packet().Header
		if hdr.Version != 2 {
			mpp.rtpStats.BadPackets.Inc()
			return
		}
		// Header length derived from where pion actually placed the payload, not Header.MarshalSize() (which can
		// under-report for extension-bearing packets) - see pktpool.RtpPacket.decode's own comment for why.
		mpegtsOffset = len(datagram) - len(rtpLayer.Payload) - int(hdr.PaddingSize)
	} else {
		// raw MPEG-TS
		if len(datagram) < 188 { // require at least one TS packet, otherwise drop it
			return
		}
	}

	// mpp.tracker's call above already decoded this layer, so this is a cached accessor, not a re-parse.
	tsLayer, err := pkt.Ts()
	if err != nil {
		// Ts() requires the RTP payload to be an exact multiple of 188 bytes; a well-formed MPEGTS-over-RTP
		// source always satisfies this, so a mismatch here indicates a malformed/corrupt datagram. Already counted
		// by mpp.tracker (ErrorStats.IncompletePackets).
		mpegtslog.Throttle("ts-decode").Warn("mpegts processing error", err, "fd", mpp.inFd)
		return
	}
	tsPackets := tsLayer.Packets()

	// This second pass over the already-decoded tsPackets is cheap (no re-parsing): it maintains the padding-stripping
	// decision below, which (unlike mpp.tracker's own input-validation) requires knowing whether *this* datagram is
	// safe to compact in place.
	badPackets := false
	hasPadding := false
	for _, tsPkt := range tsPackets {
		if tsPkt.CheckErrors() != nil {
			mpp.stats.BadPackets.Inc()
			badPackets = true
			continue
		}
		if mpp.cfg.Packaging == transport.RtpTs && tsPkt.IsNull() {
			// do not remove padding here, just flag it. We will remove it later if and only if none of the packets
			// in the datagram are bad.
			hasPadding = true
		}
	}
	if badPackets && mpp.cfg.Packaging == transport.RtpTs {
		mpp.rtpStats.BadPackets.Inc()
	}

	if mpp.cfg.AnalyzeData || mpp.cfg.AnalyzeVideo {
		// TODO(Nate): Copy over some of the logic analyzing this stuff
	}

	mpp.writeDatagram(now, datagram, pkt, !badPackets && hasPadding && StripTsPadding.Load(), mpegtsOffset)
}

// StartReportingStats kicks off two independent, differently-paced reporting loops: a fast one pushing just the
// BytesWritten signal that content-fabric's live-recorder needs for stall detection (see ReportBytesWritten's doc), and
// a slow one pushing the full ExportedStats (tracker snapshot, SRT stats) for logging/status-reporting purposes.
// Splitting them means the full-stats gather (mpp.tracker.Snapshot, which walks every tracked PID) only runs as often
// as it's actually needed, with no caching/locking required - see reportFullStatsLoop.
func (mpp *MpegtsPacketProcessor) StartReportingStats() {
	go mpp.reportBytesWrittenLoop()
	go mpp.reportFullStatsLoop()
}

// reportBytesWrittenLoop pushes ReportBytesWritten on a fast, fixed interval - must stay smaller than the 1s interval
// the live-recorder uses to calculate stalls (see ReportBytesWritten's doc).
func (mpp *MpegtsPacketProcessor) reportBytesWrittenLoop() {
	reportingInterval := 900 * time.Millisecond
	ticker := time.NewTicker(reportingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			mpp.reportBytesWritten()
		case <-mpp.closeCh:
			return
		}
	}
}

// reportBytesWritten pushes the total bytes written so far - the one signal content-fabric's live-recorder needs, at
// high frequency, to detect a stalled stream (comparing successive values; see startHealthChecking on the
// content-fabric side). Deliberately minimal: no tracker snapshot, no lock, just an atomic load and a push.
func (mpp *MpegtsPacketProcessor) reportBytesWritten() {
	_ = mpp.opener.ReportBytesWritten(mpp.stats.BytesWritten.Load())
}

// reportFullStatsLoop pushes full ExportedStats on a slow, fixed interval.
func (mpp *MpegtsPacketProcessor) reportFullStatsLoop() {
	fullStatsInterval := 5 * time.Second
	// Reused across every tick by this goroutine alone, so CopyInto/Snapshot's destination-reuse bounds this to
	// near-zero allocation once the tracked PID set stabilizes.
	var stats ExportedStats
	ticker := time.NewTicker(fullStatsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			mpp.pushStatsInto(&stats)
		case <-mpp.closeCh:
			return
		}
	}
}

// PushStats gathers and reports the processor's full stats into a fresh, one-off destination. Exported for callers
// outside the two reporting loops (e.g. tests, or a manual one-off report); a caller doing this repeatedly should
// call pushStatsInto directly with its own persistent, exclusively-owned destination instead, to avoid allocating
// one on every call - see reportFullStatsLoop.
func (mpp *MpegtsPacketProcessor) PushStats() {
	mpp.pushStatsInto(&ExportedStats{})
}

// pushStatsInto gathers statistics into the caller-supplied dst instead of a fresh one-off allocation. dst must be
// exclusively owned by the caller for the duration of this call.
func (mpp *MpegtsPacketProcessor) pushStatsInto(dst *ExportedStats) {
	now := time.Now()
	if dst.Stream == nil {
		dst.Stream = &tracker.Stats{}
	}
	mpp.tracker.Snapshot(dst.Stream, true, utc.New(now), tracker.SnapshotOptions{})

	// Reuse dst.Srt's existing allocation (if any) as the destination, so a caller that reuses dst across calls (e.g.
	// reportFullStatsLoop) doesn't allocate a new SrtConnStats on every tick.
	cs := mio.ConnStats{SRT: dst.Srt}
	dst.Srt = nil
	if mpp.connStats != nil && mpp.connStats.ConnStats(&cs, true) {
		dst.Srt = cs.SRT
	}

	dst.populate(mpp.stats, mpp.rtpStats)
	mpp.resetChannelSizeStats()

	_ = mpp.opener.Stat(*dst)
}

// connStatsSource is implemented by *NetReader; kept as a minimal interface (rather than depending on NetReader
// directly) so MpegtsPacketProcessor stays testable with a fake. See SetConnStatsSource.
type connStatsSource interface {
	ConnStats(into *mio.ConnStats, details bool) bool
}

// SetConnStatsSource wires src as the source of ExportedStats.Srt. It is a setter rather than a constructor
// parameter because the underlying connection (a *NetReader, see bypass.go/custom.go) doesn't exist until after
// NewMpegtsPacketProcessor has already been called and returned.
func (mpp *MpegtsPacketProcessor) SetConnStatsSource(src connStatsSource) {
	mpp.connStats = src
}

func (mpp *MpegtsPacketProcessor) ReportStart() {
	_ = mpp.opener.ReportStart()
}

func (mpp *MpegtsPacketProcessor) Stop() error {
	mpp.stopOnce.Do(func() {
		close(mpp.closeCh)
		if mpp.currentWc != nil {
			mpp.stopErr = mpp.currentWc.Close()
			if mpp.stopErr != nil {
				mpegtslog.Error("failed to close final MPEGTS output", "err", mpp.stopErr)
			}
			mpp.currentWc = nil
		}
	})
	return mpp.stopErr
}

func (mpp *MpegtsPacketProcessor) RegisterPacketsDropped(packetsDropped *atomic.Uint64) {
	mpp.stats.PacketsDropped = packetsDropped
}

func (mpp *MpegtsPacketProcessor) UpdateChannelSizeStats(size int) {
	maxBuf := mpp.stats.MaxBufInPeriod.Load()
	minBuf := mpp.stats.MinBufInPeriod.Load()

	if uint64(size) > maxBuf {
		mpp.stats.MaxBufInPeriod.CompareAndSwap(maxBuf, uint64(size))
	}

	if uint64(size) < minBuf {
		mpp.stats.MinBufInPeriod.CompareAndSwap(minBuf, uint64(size))
	}
}

func (mpp *MpegtsPacketProcessor) resetChannelSizeStats() {
	maxU64 := ^uint64(0)
	mpp.stats.MaxBufInPeriod.Store(0)
	mpp.stats.MinBufInPeriod.Store(maxU64)
}

func (mpp *MpegtsPacketProcessor) writeDatagram(
	now time.Time, datagram []byte, pkt *pktpool.Packet, removePadding bool, rtpPayloadOffset int) {

	if mpp.currentWc == nil {
		if mpp.stats.ErrorsOpeningOutput.Load() > 50 {
			return
		}

		err := mpp.openNextOutput(now)
		if err != nil {
			return
		}
	}

	segmentLength := time.Duration(mpp.cfg.SegmentLengthSec) * time.Second
	if segmentLength > 0 && now.Sub(mpp.segStartTime) >= segmentLength {
		mpegtslog.Debug("opening next output", "reason", "wallclock", "segStartTime", mpp.segStartTime)
		mpp.stats.NumTimedRotate.Inc()
		err := mpp.openNextOutput(now)
		if err != nil {
			return
		}
	}

	switch mpp.cfg.Packaging {
	case transport.RawTs, transport.RtpTs, transport.AtsTs:
	default:
		goavpipe.Log.Error("packaging mode unknown. Bailing out on writing datagram", "packaging_mode", mpp.cfg.Packaging)
		return
	}

	dataToWrite, ok := mpp.frame(now, datagram, pkt, removePadding, rtpPayloadOffset)
	if !ok {
		return
	}

	startTime := time.Now()
	n, err := mpp.currentWc.Write(dataToWrite)
	dur := time.Since(startTime)
	if dur > 50*time.Millisecond {
		goavpipe.Log.Warn("mpegts write too slow", "dur", dur)
	}

	if err != nil {
		mpp.stats.ErrorsWriting.Inc()
		return
	}
	mpp.stats.PacketsWritten.Inc()
	mpp.stats.BytesWritten.Add(uint64(n))
}

// frame builds the framed output bytes for the datagram according to the configured packaging. When pkt is non-nil
// (datagram == pkt.Data) the common case is framed zero-copy into pkt's head room via Packet.FrameTlv; the pool-less
// []byte case and the (rare) padding-stripping case copy into mpp.outBuf. In no case is the input datagram mutated, so
// framing is always safe when the datagram is shared with other consumers.
func (mpp *MpegtsPacketProcessor) frame(
	now time.Time,
	datagram []byte,
	pkt *pktpool.Packet,
	removePadding bool,
	rtpPayloadOffset int,
) (out []byte, ok bool) {

	switch mpp.cfg.Packaging {
	case transport.RtpTs:
		if removePadding && StripTsPadding.Load() {
			// Padding removal compacts the datagram, so the result cannot be a zero-copy view of the input: copy into
			// outBuf and strip there, leaving the (possibly shared) input untouched.
			return mpp.frameStripped(datagram, rtpPayloadOffset)
		}
		return mpp.frameTlv(pkt, datagram, byte(tlv.TlvTypeRtpTs), nil)

	case transport.AtsTs:
		// The TLV value is an 8-byte arrival timestamp followed by the raw TS datagram.
		var ts [tlv.AtsTimestampLen]byte
		binary.BigEndian.PutUint64(ts[:], uint64(now.UnixNano()))
		return mpp.frameTlv(pkt, datagram, byte(tlv.TlvTypeAtsTs), ts[:])

	default: // transport.RawTs and any pass-through: write the datagram unchanged.
		return datagram, true
	}
}

// frameTlv wraps datagram (with the optional prefix between header and payload) in a TLV header. With a pooled packet
// it frames zero-copy into the packet's head room via Packet.FrameTlv (non-mutating, so safe for a shared packet); the
// pool-less []byte caller copies header + prefix + datagram into mpp.outBuf.
func (mpp *MpegtsPacketProcessor) frameTlv(pkt *pktpool.Packet, datagram []byte, typ byte, prefix []byte) ([]byte, bool) {
	if pkt != nil {
		out, err := pkt.FrameTlv(typ, prefix)
		if err != nil {
			mpp.stats.ErrorsOther.Inc()
			return nil, false
		}
		return out, true
	}
	tlvHeader, err := tlv.TlvHeader(len(prefix)+len(datagram), tlv.TlvType(typ))
	if err != nil {
		mpp.stats.ErrorsOther.Inc()
		return nil, false
	}
	n := copy(mpp.outBuf, tlvHeader)
	n += copy(mpp.outBuf[n:], prefix)
	n += copy(mpp.outBuf[n:], datagram)
	return mpp.outBuf[:n], true
}

// frameStripped copies datagram into mpp.outBuf behind a TLV-header-sized slot, strips TS padding within that copy
// (never touching the possibly-shared input), then writes the RtpTsNoPad header in front of the stripped body so the
// header and body form one contiguous slice.
func (mpp *MpegtsPacketProcessor) frameStripped(datagram []byte, rtpPayloadOffset int) ([]byte, bool) {
	const hdr = tlv.TLV_HEADER_LEN
	n := copy(mpp.outBuf[hdr:], datagram)
	stripped := mpp.stripTsPadding(mpp.outBuf[hdr:hdr+n], rtpPayloadOffset)
	tlvHeader, err := tlv.TlvHeader(len(stripped), tlv.TlvTypeRtpTsNoPad)
	if err != nil {
		mpp.stats.ErrorsOther.Inc()
		return nil, false
	}
	copy(mpp.outBuf, tlvHeader)
	return mpp.outBuf[:hdr+len(stripped)], true
}

// stripTsPadding removes TS padding from datagram in place and records the padding stats, returning the shortened
// slice (which aliases datagram's backing storage).
func (mpp *MpegtsPacketProcessor) stripTsPadding(datagram []byte, rtpPayloadOffset int) []byte {
	data, stripped, faulty := RemoveTsPadding(datagram, rtpPayloadOffset)
	mpp.stats.StrippedPaddingPackets.Add(uint64(stripped))
	mpp.stats.FaultyPaddingPackets.Add(uint64(faulty))
	return data
}

func (mpp *MpegtsPacketProcessor) openNextOutput(now time.Time) error {
	startTime := time.Now()
	var closeDone time.Time
	defer func() {
		doneTime := time.Now()
		duration := doneTime.Sub(startTime)
		if duration > 50*time.Millisecond {
			mpegtslog.Warn("slow openNextOutput", "duration", duration, "startToClose", closeDone.Sub(startTime), "closeToDone", doneTime.Sub(closeDone))
		}
	}()
	if mpp.currentWc != nil {
		err := mpp.currentWc.Close()
		if err != nil {
			mpegtslog.Error("Failed to close current output", "err", err)
		}
		mpp.currentWc = nil
	}
	closeDone = time.Now()

	wc, err := mpp.opener.OpenNext()
	if err != nil {
		count := mpp.stats.ErrorsOpeningOutput.Inc()
		mpegtslog.Error("Failed to open next segment", "count", count, err)
		return err
	}
	mpp.stats.NumSegments.Inc()
	mpp.currentWc = wc
	mpp.segStartTime = now
	return nil
}

// toTSPacket returns data as a *packet.Packet: a zero-copy conversion that aliases data's underlying array rather
// than copying it, so mutations through the result (or to data) are visible through both. The caller must not use
// the result after data is mutated, reused, or released. Panics if data is not at least 188 bytes.
func toTSPacket(data []byte) *packet.Packet {
	return (*packet.Packet)(data)
}

// RemoveTsPadding removes the padding payload of TS padding packets within the given RTP packet. The removal is
// performed in-place. The TS header of padding packets is preserved. Returns the RTP packet with the stripped TS
// packets and the number of stripped and faulty padding packets.
func RemoveTsPadding(pkt []byte, rtpHdrLen int) (res []byte, stripped, faulty int) {
outer:
	for offset := rtpHdrLen; offset+188 <= len(pkt); offset += 188 {
		tsPkt := toTSPacket(pkt[offset : offset+188])
		if tsPkt.IsNull() && tsPkt.CheckErrors() == nil {
			for i := 4; i < 188; i++ {
				// make sure the payload is really just padding
				if pkt[offset+i] != 0xFF {
					faulty++
					continue outer
				}
			}

			// a padding packet: strip the payload.
			// TS header: 4 bytes, payload: 184 bytes
			copy(pkt[offset+4:], pkt[offset+188:]) // preserve padding packet header
			pkt = pkt[:len(pkt)-184]               // adjust datagram size...
			offset -= 184                          // ... and offset to account for the removed payload
			stripped++
		}
	}
	return pkt, stripped, faulty
}
