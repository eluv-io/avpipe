package mpegts

import (
	"io"

	"github.com/Comcast/gots/packet"
	elog "github.com/eluv-io/log-go"
)

const PcrTs uint64 = 27_000_000

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

	inFd        int64
	opener      SequentialOpener
	segStartPcr uint64 // PCR at the start of the current segment
	currentWc   io.WriteCloser

	stats         *TSStats
	continuityMap map[int]uint8 // Map of PID to last continuity counter
	pcr           uint64        // Last seen PCR value
}

func NewMpegtsPacketProcessor(cfg TsConfig, seqOpener SequentialOpener, inFd int64) *MpegtsPacketProcessor {
	return &MpegtsPacketProcessor{
		cfg:           cfg,
		opener:        seqOpener,
		inFd:          inFd,
		continuityMap: make(map[int]uint8),
		stats:         NewTSStats(),
	}
}

type TsConfig struct {
	SegmentLengthSec uint64

	AnalyzeVideo bool
	AnalyzeData  bool
}

type TSStats struct {
	PacketsReceived uint64
	PacketsWritten  uint64
	BadPackets      uint64
	BytesReceived   uint64
	BytesWritten    uint64

	VideoPacketCount uint64
	AudioPacketCount uint64
	DataPacketCount  uint64

	FirstPCR    uint64 // First seen PCR value
	LastPCR     uint64 // Last seen PCR value
	NumSegments int64

	// Errors in the continuity counter
	ErrorsCC                uint64
	ErrorsCCByPid           map[int]uint64
	ErrorsAdapationField    uint64
	ErrorsOther             uint64
	ErrorsIncompletePackets uint64
	ErrorsOpeningOutput     uint64
	ErrorsWriting           uint64
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
		mpp.stats.ErrorsIncompletePackets++
	}
}

func (mpp *MpegtsPacketProcessor) HandlePacket(pkt packet.Packet) {
	mpp.stats.PacketsReceived++
	mpp.stats.BytesReceived += uint64(len(pkt))

	mpp.checkContinuityCounter(pkt)
	mpp.updatePCR(pkt)

	if mpp.pcr == 0 {
		// Wait for at least one packet with PCR before writing
		return
	}

	if mpp.cfg.AnalyzeData || mpp.cfg.AnalyzeVideo {
		// TODO(Nate): Copy over some of the logic analyzing this stuff
	}

	mpp.writePacket(pkt)

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
		mpp.stats.ErrorsCC++
		if _, ok := mpp.stats.ErrorsCCByPid[pid]; !ok {
			mpp.stats.ErrorsCCByPid[pid] = 1
		} else {
			mpp.stats.ErrorsCCByPid[pid]++
		}
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
		mpp.stats.ErrorsAdapationField++
		return
	} else if !hasPcr {
		return
	}

	pcr, err := a.PCR()
	if err != nil {
		mpp.stats.ErrorsAdapationField++
		return
	}
	mpp.pcr = pcr

	if mpp.stats.FirstPCR == 0 {
		mpp.stats.FirstPCR = mpp.pcr
	}
	mpp.stats.LastPCR = mpp.pcr
}

func (mpp *MpegtsPacketProcessor) writePacket(pkt packet.Packet) {
	if mpp.currentWc == nil {
		if mpp.stats.ErrorsOpeningOutput > 50 {
			return
		}

		err := mpp.openNextOutput()
		if err != nil {
			return
		}
	}

	if mpp.pcr > mpp.segStartPcr && mpp.pcr-mpp.segStartPcr > mpp.cfg.SegmentLengthSec*PcrTs {
		err := mpp.openNextOutput()
		if err != nil {
			return
		}
	}

	n, err := mpp.currentWc.Write(pkt[:])
	if err != nil {
		mpp.stats.ErrorsWriting++
		return
	}
	mpp.stats.PacketsWritten++
	mpp.stats.BytesWritten += uint64(n)
}

func (mpp *MpegtsPacketProcessor) openNextOutput() error {
	if mpp.currentWc != nil {
		err := mpp.currentWc.Close()
		if err != nil {
			mpegtslog.Error("Failed to close current output", "err", err)
		}
		mpp.currentWc = nil
	}

	wc, err := mpp.opener.OpenNext()
	if err != nil {
		mpegtslog.Error("Failed to open next segment", "err", err)
		mpp.stats.ErrorsOpeningOutput++
		if mpp.stats.ErrorsOpeningOutput > 50 {
			mpegtslog.Fatal("Too many errors opening output segments, giving up attempts")
		}
		return err
	}
	mpp.stats.NumSegments++
	mpp.currentWc = wc
	mpp.segStartPcr = mpp.pcr
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
