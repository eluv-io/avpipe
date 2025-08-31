package mpegts

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/Comcast/gots/v2/packet"
	elog "github.com/eluv-io/log-go"
	"go.uber.org/atomic"
)

const PcrTs uint64 = 27_000_000
const PcrMax uint64 = ((1 << 33) * 300) + (1 << 9)

var mpegtslog = elog.Get("avpipe/broadcastproto/mpegts")

type SequentialOpener interface {
	OpenNext() (io.WriteCloser, error)
}

// Special TS stream and descriptor types (not defined in 'gots')
const TsStreamTypeJpegXS = 0x32
const TsDescriptorSt2038 = 0xc4 // Commonly ST 2038 or ST 291 (Evertz, Imagine, Harmonic)

// Locally defined stream types
const TsStreamTypeLocalSt2038 = 0xe4 // Locally defined type

type MpegtsPacketProcessor struct {
	cfg TsConfig

	inFd         int64
	opener       SequentialOpener
	segStartPcr  uint64 // PCR at the start of the current segment
	segStartTime time.Time
	currentWc    io.WriteCloser

	// statsMu is _only_ used for updating cc errors by PID
	statsMu sync.Mutex

	stats         *TSStats
	continuityMap map[int]uint8 // Map of PID to last continuity counter
	pcr           uint64        // Last seen PCR value

	closeCh chan struct{}
}

func NewMpegtsPacketProcessor(cfg TsConfig, seqOpener SequentialOpener, inFd int64) *MpegtsPacketProcessor {
	return &MpegtsPacketProcessor{
		cfg:           cfg,
		opener:        seqOpener,
		inFd:          inFd,
		continuityMap: make(map[int]uint8),
		stats:         NewTSStats(),
		closeCh:       make(chan struct{}),
	}
}

type TsConfig struct {
	SegmentLengthSec uint64

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

func NewTSStats() *TSStats {
	return &TSStats{
		ErrorsCCByPid: make(map[int]uint64),
	}
}

func (mpp *MpegtsPacketProcessor) ProcessPackets(packets []byte) {
	for offset := 0; offset+188 <= len(packets); offset += 188 {
		p := toTSPacket(packets[offset : offset+188])
		mpp.HandlePacket(p)
	}
	if len(packets)%188 != 0 {
		mpp.stats.ErrorsIncompletePackets.Inc()
	}
}

func (mpp *MpegtsPacketProcessor) HandlePacket(pkt packet.Packet) {
	mpp.stats.PacketsReceived.Inc()
	mpp.stats.BytesReceived.Add(uint64(len(pkt)))

	if pkt.CheckErrors() != nil {
		mpp.stats.BadPackets.Inc()
		return
	}

	mpp.checkContinuityCounter(pkt)
	mpp.updatePCR(pkt)

	if mpp.pcr == 0 {
		// Wait for at least one packet with PCR before writing
		return
	}

	if mpp.cfg.AnalyzeData || mpp.cfg.AnalyzeVideo {
		// TODO(Nate): Copy over some of the logic analyzing this stuff
	}

	s := time.Now()
	mpp.writePacket(pkt)
	dur := time.Since(s)
	if dur > 50*time.Millisecond {
		mpegtslog.Warn("MPEGTS writePacket took too long", "duration", dur)
	}

}

// Kick off a job that periodically logs the stats of this job
func (mpp *MpegtsPacketProcessor) StartReportingStats() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mpp.statsMu.Lock()
				v, _ := json.Marshal(mpp.stats)
				mpegtslog.Debug("mpegts stats", "stats", string(v))
				mpp.statsMu.Unlock()
				mpp.resetChannelSizeStats()
			case <-mpp.closeCh:
				return
			}
		}
	}()
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

func (mpp *MpegtsPacketProcessor) writePacket(pkt packet.Packet) {
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

	n, err := mpp.currentWc.Write(pkt[:])

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
	if len(data) != packet.PacketSize {
		// Should never occur if called correctly
		panic("invalid TS packet size")
	}
	var pkt packet.Packet
	copy(pkt[:], data[:packet.PacketSize])
	return pkt
}
