package mpegts

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Comcast/gots/v2/packet"
	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe/broadcastproto/tlv"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/util/timeutil"
	elog "github.com/eluv-io/log-go"
)

const (
	PcrTs  uint64 = 27_000_000
	PcrMax uint64 = ((1 << 33) * 300) + (1 << 9)
)

// pcrStaleSegmentDivisor sets the PCR staleness window: a pinned PID is considered stale after no PCR for
// (segment length / pcrStaleSegmentDivisor). PCRs normally arrive at least once per second (every 100ms per spec),
// so this is far longer than any healthy gap while still detecting a dead PCR well within a single segment.
const pcrStaleSegmentDivisor = 2

// minSegmentWallClockDivisor sets the minimum wall-clock interval between PCR-driven rotations to
// (segment length / minSegmentWallClockDivisor). It caps the segment creation rate if the source PCR advances much
// faster than real time, so segments are never produced more than this factor faster than nominal.
const minSegmentWallClockDivisor = 4

type rotationReason int

const (
	rotationNone rotationReason = iota
	rotationPCR
	rotationPCRWrap
	rotationWallClock
)

// StripTsPadding indicates whether the payload of TS padding packets in RTP-TS streams should be stripped. For now
// this is not configurable per stream, since it has low performance impact and potentially saves bandwidth.
var StripTsPadding = atomic.NewBool(false)

var mpegtslog = elog.Get("avpipe/broadcastproto/mpegts")

type SequentialOpener interface {
	OpenNext() (io.WriteCloser, error)
	Stat(args any) error
	ReportStart() error
}

type MpegtsPacketProcessor struct {
	cfg TsConfig

	inFd         int64
	opener       SequentialOpener
	segStartPcr  uint64 // PCR at the start of the current segment
	segStartTime time.Time
	currentWc    io.WriteCloser

	// statsMu is _only_ used for updating cc errors by PID
	statsMu sync.Mutex

	stats *TSStats
	// rtpStats is used to keep track of RTP-specific information in the case that the packaging is
	// RTP-MPEGTS. It is _nil_ iff cfg.Packaging is not RTP-TS.
	rtpStats *RTPStats
	// Periodic for logging stats
	periodicStatsLog timeutil.Periodic

	continuityMap map[int]uint8 // Map of PID to last continuity counter
	pcr           uint64        // Last seen PCR value
	// pcrPid is the PID of the stream we extracted the PCR from. Pinning to a single PID provides a stable PCR for
	// multi-program streams with independent PCRs. PID 0 is reserved for the PAT (which cannot carry PCRs), so it is
	// safe to use 0 as the "not set" value.
	pcrPid        int
	pcrLastSeenAt time.Time // Last time we saw a PCR from the selected PID.
	outBuf        []byte    // Preallocated byte buffer
	closeCh       chan struct{}

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
		cfg:              cfg,
		opener:           seqOpener,
		inFd:             inFd,
		continuityMap:    make(map[int]uint8),
		stats:            NewTSStats(),
		rtpStats:         rtpStats,
		periodicStatsLog: timeutil.NewPeriodic(30 * time.Second),
		outBuf:           make([]byte, 64*1024), // Max datagram size
		closeCh:          make(chan struct{}),
	}
}

type TsConfig struct {
	SegmentLengthSec uint64
	Packaging        transport.TsPackagingMode

	AnalyzeVideo bool
	AnalyzeData  bool
}

type TSStats struct {
	PacketsReceived atomic.Uint64
	PacketsWritten  atomic.Uint64
	// PacketsDropped is updated by the sender to the channel, which is why it is a pointer
	PacketsDropped         *atomic.Uint64
	SmallPacketsDropped    atomic.Uint64 // small packets (< 188 bytes) are dropped
	RtcpPacketsDropped     atomic.Uint64 // small dropped packets with are likely RTCP (included in SmallPacketsDropped)
	BadPackets             atomic.Uint64
	BytesReceived          atomic.Uint64
	BytesWritten           atomic.Uint64
	PaddingPackets         atomic.Uint64
	FaultyPaddingPackets   atomic.Uint64
	StrippedPaddingPackets atomic.Uint64

	MaxBufInPeriod atomic.Uint64
	MinBufInPeriod atomic.Uint64

	VideoPacketCount atomic.Uint64
	AudioPacketCount atomic.Uint64
	DataPacketCount  atomic.Uint64

	FirstPCR       atomic.Uint64 // First seen PCR value
	LastPCR        atomic.Uint64 // Last seen PCR value
	NumSegments    atomic.Int64
	NumWraps       atomic.Int64
	NumTimedRotate atomic.Int64

	// Errors in the continuity counter
	ErrorsCC                atomic.Uint64
	ErrorsAdapationField    atomic.Uint64
	ErrorsOther             atomic.Uint64
	ErrorsIncompletePackets atomic.Uint64
	ErrorsOpeningOutput     atomic.Uint64
	ErrorsWriting           atomic.Uint64
	ErrorsCCByPid           map[int]uint64
}

type RTPStats struct {
	FirstSeqNum     atomic.Uint32
	LastSeqNum      atomic.Uint32
	SeqNumSkipTot   atomic.Uint32
	SeqNumSkipCount atomic.Uint64

	// RTP timestamp interpretation is different by application
	FirstTimestamp atomic.Uint32
	LastTimestamp  atomic.Uint32
	RefTime        time.Time // System time when first timestamp is set

	BadPackets  atomic.Uint64 // Invalid RTP packets
	LongHeaders atomic.Uint64 // LongHeaders keeps track of RTP headers longer than 12 bytes
}

func NewTSStats() *TSStats {
	return &TSStats{
		ErrorsCCByPid: make(map[int]uint64),
	}
}

func (mpp *MpegtsPacketProcessor) ProcessDatagram(now time.Time, datagram []byte) {
	if !mpp.startLogged {
		mpp.startLogged = true
		mpegtslog.Info("mpegts processing first datagram",
			"fd", mpp.inFd,
			"packaging", string(mpp.cfg.Packaging),
			"datagram_len", len(datagram))
	}

	mpegtsOffset := 0
	if mpp.cfg.Packaging == transport.RtpTs {
		if len(datagram) < 12+188 { // RTP header + at least one TS packet
			mpp.stats.SmallPacketsDropped.Inc()
			if isRTCP(datagram) {
				mpp.stats.RtcpPacketsDropped.Inc()
			}
			return
		}

		dgHeader, err := transport.ParseRTPHeader(datagram)
		if err != nil {
			mpp.rtpStats.BadPackets.Inc()
			return
		}
		mpegtsOffset = dgHeader.ByteLength()
		if mpegtsOffset != 12 {
			mpp.rtpStats.LongHeaders.Inc()
		}
		swapped := mpp.rtpStats.FirstTimestamp.CompareAndSwap(0, dgHeader.Timestamp)
		if swapped {
			mpp.rtpStats.RefTime = now
			mpp.rtpStats.FirstSeqNum.Store(uint32(dgHeader.SequenceNumber))
			defer mpp.PushStats() // defer so that ts stats are included in the stats
		}
		mpp.rtpStats.LastTimestamp.Store(dgHeader.Timestamp)
		mpp.rtpStats.LastSeqNum.Store(uint32(dgHeader.SequenceNumber))

		// TODO: Sequence number / discontinuity processing
	} else if len(datagram) < 188 { // RTPat least one TS packet
		mpp.stats.SmallPacketsDropped.Inc()
		return
	}

	// Extract PCR
	badPackets := 0
	hasPadding := false
	for offset := mpegtsOffset; offset+188 <= len(datagram); offset += 188 {
		pkt := toTSPacket(datagram[offset : offset+188])
		err := mpp.HandleMpegtsPacket(now, pkt)
		if err != nil {
			badPackets++
		} else if mpp.cfg.Packaging == transport.RtpTs {
			if pkt.IsNull() {
				mpp.stats.PaddingPackets.Inc()
				// do not remove padding here, just flag it. We will remove it later if and only if none of the packets
				// in the datagram are bad.
				hasPadding = true
			}
		}
	}

	if badPackets > 0 {
		if mpp.cfg.Packaging == transport.RtpTs {
			mpp.rtpStats.BadPackets.Inc()
		}
	}

	mpp.writeDatagram(now, datagram, badPackets == 0 && hasPadding && StripTsPadding.Load(), mpegtsOffset)
}

func (mpp *MpegtsPacketProcessor) HandleMpegtsPacket(now time.Time, pkt packet.Packet) error {
	mpp.stats.PacketsReceived.Inc()
	mpp.stats.BytesReceived.Add(uint64(len(pkt)))

	if pkt.CheckErrors() != nil {
		mpp.stats.BadPackets.Inc()
		return errors.New("bad mpegts packet")
	}

	mpp.checkContinuityCounter(pkt)
	mpp.updatePCR(now, pkt)

	if mpp.cfg.AnalyzeData || mpp.cfg.AnalyzeVideo {
		// TODO(Nate): Copy over some of the logic analyzing this stuff
	}

	return nil
}

// StartReportingStats kicks off a job that periodically logs the stats
func (mpp *MpegtsPacketProcessor) StartReportingStats() {
	// must be smaller than the 1s interval used by the live-recorder for calculation of "stalls"
	reportingInterval := 900 * time.Millisecond
	go func() {
		ticker := time.NewTicker(reportingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mpp.PushStats()
			case <-mpp.closeCh:
				return
			}
		}
	}()
}

func (mpp *MpegtsPacketProcessor) PushStats() {
	mpp.statsMu.Lock()
	exportStats := exportStats(mpp.stats, mpp.rtpStats)
	mpp.statsMu.Unlock()
	mpp.resetChannelSizeStats()

	mpp.periodicStatsLog.Do(func() {
		mpegtslog.Debug("mpegts stats", "fd", mpp.inFd, "stats", exportStats)
	})
	_ = mpp.opener.Stat(exportStats)
}

func (mpp *MpegtsPacketProcessor) ReportStart() {
	_ = mpp.opener.ReportStart()
}

func (mpp *MpegtsPacketProcessor) Stop() {
	if mpp.closeCh != nil {
		close(mpp.closeCh)
		mpp.closeCh = nil
	}
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

func (mpp *MpegtsPacketProcessor) checkContinuityCounter(pkt packet.Packet) {
	pid := pkt.PID()

	if !pkt.HasPayload() || pkt.IsNull() {
		// per spec, continuity counter only applies to packets with payload
		return
	}

	cc := uint8(pkt.ContinuityCounter())

	lastCC, exists := mpp.continuityMap[pid]
	mpp.continuityMap[pid] = cc

	if exists && cc != (lastCC+1)%16 {
		mpp.statsMu.Lock()
		mpp.stats.ErrorsCC.Inc()
		if _, ok := mpp.stats.ErrorsCCByPid[pid]; !ok {
			mpp.stats.ErrorsCCByPid[pid] = 1
		} else {
			mpp.stats.ErrorsCCByPid[pid]++
		}
		mpp.statsMu.Unlock()
	}
}

func (mpp *MpegtsPacketProcessor) updatePCR(now time.Time, pkt packet.Packet) {
	if !pkt.HasAdaptationField() {
		return
	}

	// Cannot fail as we already checked for adaptation field
	a, _ := pkt.AdaptationField()

	hasPcr, err := a.HasPCR()
	if err != nil {
		mpp.stats.ErrorsAdapationField.Inc()
		return
	} else if !hasPcr {
		return
	}

	pid := pkt.PID()
	if mpp.pcrPid != 0 && pid != mpp.pcrPid {
		// We have already pinned a PCR PID and this packet is from a different program: ignore it.
		return
	}

	pcr, err := a.PCR()
	if err != nil {
		mpp.stats.ErrorsAdapationField.Inc()
		return
	}

	// pinningPcrPid is true when no PCR PID is currently pinned (at startup or after a wall-clock rotation unpinned
	// it), so this PCR (re)pins the PID and is always accepted.
	pinningPcrPid := mpp.pcrPid == 0
	// A healthy PCR strictly advances, except for the rare counter wrap (a large backward jump). If it stagnates or
	// moves backward without wrapping, the source clock is unhealthy: leave mpp.pcr / mpp.pcrLastSeenAt untouched so
	// that rotationDecision falls back to wall-clock segmentation once the PCR goes stale.
	pcrWrapped := pcr < mpp.pcr && mpp.pcr-pcr > PcrMax/2
	if !pinningPcrPid && pcr <= mpp.pcr && !pcrWrapped {
		return
	}

	mpp.pcr = pcr
	mpp.pcrPid = pid
	mpp.pcrLastSeenAt = now
	if pinningPcrPid && mpp.currentWc != nil {
		mpp.segStartPcr = pcr
	}

	mpp.stats.FirstPCR.CompareAndSwap(0, mpp.pcr)
	mpp.stats.LastPCR.Store(mpp.pcr)
}

func (mpp *MpegtsPacketProcessor) pcrIsStale(now time.Time) bool {
	segmentLength := time.Duration(mpp.cfg.SegmentLengthSec) * time.Second
	return segmentLength > 0 &&
		!mpp.pcrLastSeenAt.IsZero() &&
		now.Sub(mpp.pcrLastSeenAt) > segmentLength/pcrStaleSegmentDivisor
}

func (mpp *MpegtsPacketProcessor) checkRotation(now time.Time) rotationReason {
	segmentLength := time.Duration(mpp.cfg.SegmentLengthSec) * time.Second
	if mpp.currentWc == nil || segmentLength == 0 {
		return rotationNone
	}

	// No usable PCR - either none seen yet, or the pinned PID went silent: fall back to wall-clock segmentation.
	if mpp.pcrPid == 0 || mpp.pcrIsStale(now) {
		if now.Sub(mpp.segStartTime) > segmentLength {
			return rotationWallClock
		}
		return rotationNone
	}

	// Enforce a minimum wall-clock interval between PCR-driven rotations, guarding against creating segments far too
	// frequently when the source PCR runs much faster than real time (e.g. a broken encoder clock) or a large burst
	// of buffered data is flushed at once.
	if now.Sub(mpp.segStartTime) < segmentLength/minSegmentWallClockDivisor {
		return rotationNone
	}

	if mpp.pcr < mpp.segStartPcr && mpp.segStartPcr-mpp.pcr > PcrMax/2 {
		return rotationPCRWrap
	}
	if mpp.pcr > mpp.segStartPcr && mpp.pcr-mpp.segStartPcr > mpp.cfg.SegmentLengthSec*PcrTs {
		return rotationPCR
	}
	return rotationNone
}

func (mpp *MpegtsPacketProcessor) writeDatagram(now time.Time, datagram []byte, removePadding bool, rtpPayloadOffset int) {
	if mpp.currentWc == nil {
		if mpp.stats.ErrorsOpeningOutput.Load() > 50 {
			return
		}

		err := mpp.openNextOutput(now)
		if err != nil {
			return
		}
	}

	if res := mpp.checkRotation(now); res != rotationNone {
		switch res {
		case rotationPCRWrap:
			mpp.stats.NumWraps.Inc()
			mpegtslog.Debug("opening next output", "reason", "pcr_wrap", "pcr", mpp.pcr, "segStartPcr", mpp.segStartPcr)
		case rotationPCR:
			mpegtslog.Debug("opening next output", "reason", "pcr", "pcr", mpp.pcr, "segStartPcr", mpp.segStartPcr)
		case rotationWallClock:
			mpp.stats.NumTimedRotate.Inc()
			mpegtslog.Debug("opening next output", "reason", "wallclock",
				"last_seg_start_time", mpp.segStartTime, "time_since_start", now.Sub(mpp.segStartTime))
		}
		if err := mpp.openNextOutput(now); err != nil {
			return
		}
		if res == rotationWallClock {
			// Unpin the PCR PID so a fresh PID can be selected for the new segment.
			mpp.pcrPid = 0
		}
	}

	switch mpp.cfg.Packaging {
	case transport.RawTs, transport.RtpTs:
	default:
		goavpipe.Log.Error("packaging mode unknown. Bailing out on writing datagram", "packaging_mode", mpp.cfg.Packaging)
		return
	}

	dataToWrite := datagram

	if mpp.cfg.Packaging == transport.RtpTs {
		var tlvType = tlv.TlvTypeRtpTs
		if removePadding && StripTsPadding.Load() {
			tlvType = tlv.TlvTypeRtpTsNoPad
			var stripped, faulty int
			datagram, stripped, faulty = RemoveTsPadding(datagram, rtpPayloadOffset)
			mpp.stats.StrippedPaddingPackets.Add(uint64(stripped))
			mpp.stats.FaultyPaddingPackets.Add(uint64(faulty))
		}
		tlvHeader, err := tlv.TlvHeader(len(datagram), tlvType)
		if err != nil {
			mpp.stats.ErrorsOther.Inc()
			return
		}
		copy(mpp.outBuf, tlvHeader)
		copy(mpp.outBuf[len(tlvHeader):], datagram)
		dataToWrite = mpp.outBuf[:len(tlvHeader)+len(datagram)]
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
	mpp.segStartPcr = mpp.pcr
	mpp.segStartTime = now
	return nil
}

// toTSPacket converts a byte slice to a TS packet.
// If the byte slice is not exactly 188 bytes, it panics.
func toTSPacket(data []byte) packet.Packet {
	// simply perform a type conversion, which does not copy any data, but references the same underlying data. Panics
	// if the data is not 188 bytes.
	return packet.Packet(data)
}

// RemoveTsPadding removes the padding payload of TS padding packets within the given RTP packet. The removal is
// performed in-place. The TS header of padding packets is preserved. Returns the RTP packet with the stripped TS
// packets and the number of stripped and faulty padding packets.
func RemoveTsPadding(pkt []byte, rtpHdrLen int) (res []byte, stripped, faulty int) {
outer:
	for offset := rtpHdrLen; offset+188 <= len(pkt); offset += 188 {
		tsPkt := toTSPacket(pkt[offset : offset+188])
		if tsPkt.CheckErrors() != nil && tsPkt.IsNull() {
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

func isRTCP(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	// The second byte (index 1) contains the Payload Type
	pt := data[1]

	// RTCP Packet Types:
	// 200: SR (Sender Report)
	// 201: RR (Receiver Report)
	// 202: SDES (Source Description)
	// 203: BYE (Goodbye)
	// 204: APP (Application-defined)
	return pt >= 200 && pt <= 204
}
