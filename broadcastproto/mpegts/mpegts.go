package mpegts

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"github.com/Comcast/gots/v2/packet"
	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe/broadcastproto/tlv"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	elog "github.com/eluv-io/log-go"
)

const (
	// StripTsPadding indicates whether the payload of TS padding packets in RTP-TS streams should be stripped. For now
	// this is not configurable per stream, since it has low performance impact and potentially saves bandwidth.
	StripTsPadding        = true
	PcrTs          uint64 = 27_000_000
	PcrMax         uint64 = ((1 << 33) * 300) + (1 << 9)

	// Special TS stream and descriptor types (not defined in 'gots')
	TsStreamTypeJpegXS = 0x32
	TsDescriptorSt2038 = 0xc4 // Commonly ST 2038 or ST 291 (Evertz, Imagine, Harmonic)
	// Locally defined stream types
	TsStreamTypeLocalSt2038 = 0xe4 // Locally defined type
)

var mpegtslog = elog.Get("avpipe/broadcastproto/mpegts")

type SequentialOpener interface {
	OpenNext() (io.WriteCloser, error)
	Stat(args string) error
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

	continuityMap map[int]uint8 // Map of PID to last continuity counter
	pcr           uint64        // Last seen PCR value
	outBuf        []byte        // Preallocated byte buffer
	closeCh       chan struct{}
}

func NewMpegtsPacketProcessor(cfg TsConfig, seqOpener SequentialOpener, inFd int64) *MpegtsPacketProcessor {
	var rtpStats *RTPStats
	if cfg.Packaging == transport.RtpTs {
		rtpStats = &RTPStats{}
	}
	return &MpegtsPacketProcessor{
		cfg:           cfg,
		opener:        seqOpener,
		inFd:          inFd,
		continuityMap: make(map[int]uint8),
		stats:         NewTSStats(),
		rtpStats:      rtpStats,
		outBuf:        make([]byte, 64*1024), // Max datagram size
		closeCh:       make(chan struct{}),
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
	PacketsDropped *atomic.Uint64
	BadPackets     atomic.Uint64
	BytesReceived  atomic.Uint64
	BytesWritten   atomic.Uint64

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

	BadPacketCount atomic.Uint64
	// LongHeaderCount keeps track of RTP headers longer than 12 bytes
	LongHeaderCount atomic.Uint64
}

func NewTSStats() *TSStats {
	return &TSStats{
		ErrorsCCByPid: make(map[int]uint64),
	}
}

func (mpp *MpegtsPacketProcessor) ProcessPackets(packets []byte) {
	for offset := 0; offset+188 <= len(packets); offset += 188 {
		p := toTSPacket(packets[offset : offset+188])
		mpp.HandleMpegtsPacket(p)
	}
	if len(packets)%188 != 0 {
		mpp.stats.ErrorsIncompletePackets.Inc()
	}
}

func (mpp *MpegtsPacketProcessor) ProcessDatagram(datagram []byte) {
	mpegtsOffset := 0
	if mpp.cfg.Packaging == transport.RtpTs {
		dgHeader, err := transport.ParseRTPHeader(datagram)
		if err != nil {
			mpp.rtpStats.BadPacketCount.Inc()
			return
		}
		mpegtsOffset = dgHeader.ByteLength()
		if mpegtsOffset != 12 {
			mpp.rtpStats.LongHeaderCount.Inc()
		}
		swapped := mpp.rtpStats.FirstTimestamp.CompareAndSwap(0, dgHeader.Timestamp)
		if swapped {
			mpp.rtpStats.RefTime = time.Now()
			mpp.PushStats()
		}
		mpp.rtpStats.LastTimestamp.Store(dgHeader.Timestamp)

		// TODO: Sequence number / discontinuity processing
	}

	// Extract PCR
	packets := datagram[mpegtsOffset:]
	badPackets := 0
	packetCount := int(math.Trunc(float64(len(packets)) / 188))
	for offset := 0; offset+188 <= len(packets); offset += 188 {
		pkt := toTSPacket(packets[offset : offset+188])
		err := mpp.HandleMpegtsPacket(pkt)
		if err != nil {
			badPackets++
		} else if mpp.cfg.Packaging == transport.RtpTs && StripTsPadding {
			if pkt.IsNull() {
				// a padding packet: strip the payload.
				// TS header: 4 bytes, payload: 184 bytes
				copy(datagram[offset+4:], datagram[offset+188:]) // preserve padding packet header
				datagram = datagram[:len(datagram)-184]          // adjust datagram size...
				offset -= 184                                    // ... and offset to account for the removed payload
			}
		}
	}

	if float64(badPackets) > 0.5*float64(packetCount) {
		// TODO: Should we put this in the rtp stats?
		if mpp.cfg.Packaging == transport.RtpTs {
			mpp.rtpStats.BadPacketCount.Inc()
		}
		return
	}

	if mpp.pcr == 0 {
		// Wait for at least one packet with PCR before writing
		return
	}

	mpp.writeDatagram(datagram)
}

func (mpp *MpegtsPacketProcessor) HandleMpegtsPacket(pkt packet.Packet) error {
	mpp.stats.PacketsReceived.Inc()
	mpp.stats.BytesReceived.Add(uint64(len(pkt)))

	if pkt.CheckErrors() != nil {
		mpp.stats.BadPackets.Inc()
		return errors.New("bad mpegts packet")
	}

	mpp.checkContinuityCounter(pkt)
	mpp.updatePCR(pkt)

	if mpp.cfg.AnalyzeData || mpp.cfg.AnalyzeVideo {
		// TODO(Nate): Copy over some of the logic analyzing this stuff
	}

	return nil
}

// Kick off a job that periodically logs the stats of this job
func (mpp *MpegtsPacketProcessor) StartReportingStats() {
	reportingInterval := 30 * time.Second
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
	v, _ := json.Marshal(mpp.stats)
	mpegtslog.Debug("mpegts stats", "stats", string(v))
	v, _ = json.Marshal(mpp.rtpStats)
	mpegtslog.Debug("rtp/mpegts stats", "stats", string(v))
	mpp.statsMu.Unlock()
	mpp.resetChannelSizeStats()
	// PENDING(SS) - create a combined JSON mpegts and rtp
	mpp.opener.Stat(string(v))
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
	max := mpp.stats.MaxBufInPeriod.Load()
	min := mpp.stats.MinBufInPeriod.Load()

	if uint64(size) > max {
		mpp.stats.MaxBufInPeriod.CompareAndSwap(max, uint64(size))
	}

	if uint64(size) < min {
		mpp.stats.MinBufInPeriod.CompareAndSwap(min, uint64(size))
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

func (mpp *MpegtsPacketProcessor) updatePCR(pkt packet.Packet) {
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

	pcr, err := a.PCR()
	if err != nil {
		mpp.stats.ErrorsAdapationField.Inc()
		return
	}
	mpp.pcr = pcr

	mpp.stats.FirstPCR.CompareAndSwap(0, mpp.pcr)
	mpp.stats.LastPCR.Store(mpp.pcr)
}

func (mpp *MpegtsPacketProcessor) writeDatagram(datagram []byte) {
	if mpp.currentWc == nil {
		if mpp.stats.ErrorsOpeningOutput.Load() > 50 {
			return
		}

		err := mpp.openNextOutput()
		if err != nil {
			return
		}
	}

	curCloseToZero := PcrTs*30 > mpp.pcr
	prevCloseToMax := PcrMax-(PcrTs*60) < mpp.segStartPcr
	pcrWrapped := prevCloseToMax && curCloseToZero
	pcrPastSegmentBounds := mpp.pcr > mpp.segStartPcr && mpp.pcr-mpp.segStartPcr > mpp.cfg.SegmentLengthSec*PcrTs
	segTimeFarTooLong := time.Since(mpp.segStartTime) > time.Duration(mpp.cfg.SegmentLengthSec)*time.Second*2
	if pcrWrapped || pcrPastSegmentBounds || segTimeFarTooLong {
		mpegtslog.Debug("opening next output", "pcrWrapped", pcrWrapped, "pcrPastSegmentBounds", pcrPastSegmentBounds, "pcr", mpp.pcr, "segStartPcr", mpp.segStartPcr)
		if pcrWrapped {
			mpp.stats.NumWraps.Inc()
		}
		if segTimeFarTooLong {
			mpp.stats.NumTimedRotate.Inc()
		}
		err := mpp.openNextOutput()
		if err != nil {
			return
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
		if StripTsPadding {
			tlvType = tlv.TlvTypeRtpTsNoPad
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

func (mpp *MpegtsPacketProcessor) openNextOutput() error {
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
		mpegtslog.Error("Failed to open next segment", "err", err)
		mpp.stats.ErrorsOpeningOutput.Inc()
		if mpp.stats.ErrorsOpeningOutput.Load() > 50 {
			mpegtslog.Fatal("Too many errors opening output segments, giving up attempts")
		}
		return err
	}
	mpp.stats.NumSegments.Inc()
	mpp.currentWc = wc
	mpp.segStartPcr = mpp.pcr
	mpp.segStartTime = time.Now()
	return nil
}

// toTSPacket converts a byte slice to a TS packet.
// If the byte slice is not exactly 188 bytes, it panics.
func toTSPacket(data []byte) packet.Packet {
	// simply perform a type conversion, which does not copy any data, but references the same underlying data. Panics
	// if the data is not 188 bytes.
	return packet.Packet(data)
}
