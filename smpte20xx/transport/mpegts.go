package transport

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Comcast/gots/ebp"
	"github.com/Comcast/gots/pes"
	"github.com/Comcast/gots/v2"
	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/packet/adaptationfield"
	"github.com/Comcast/gots/v2/psi"
	"github.com/eluv-io/avpipe/smpte20xx/anc"
	"github.com/eluv-io/avpipe/smpte20xx/video"
)

const PcrTs uint64 = 27_000_000

// Specail TS stream and descriptor types (not defined in 'gots')
const TsStreamTypeJpegXS = 0x32
const TsDescriptorSt2038 = 0xc4 // Commonly ST 2038 or ST 291 (Evertz, Imagine, Harmonic)

// Locally defined stream types
const TsStreamTypeLocalSt2038 = 0xe4 // Locally defined type

type TsConfig struct {
	Url            string
	SaveFrameFiles bool
	ProcessVideo   bool
	ProcessData    bool
	MaxPackets     int64
	SegCfg         SegmenterConfig
}

type TsStats struct {
	nTsPackets        uint64
	nTsPacketsWritten uint64
	tsBytesReceived   uint64
	tsBytesWritten    uint64
	nPacketsVideo     uint64
	nPacketsData      uint64
	errorsCC          uint64
	errorsMisc        uint64
}

type Ts struct {
	Cfg   TsConfig
	Stats TsStats

	program  int // Only one program supported
	pmtPid   int // Only one program supported (so one PMT)
	videoPid int
	dataPid  int // Only one data stream supported

	videoType uint8
	dataType  uint8

	startPcr             uint64
	currentPcr           uint64
	pesData              packet.Accumulator
	pesBuffer            []byte
	pesAccumulatingVideo bool // PENDING(SS) Replace with packet.Accumulator
	continuityMap        map[int]uint8

	segmenter *Segmenter
}

func NewTs(tsCfg TsConfig) *Ts {
	ts := Ts{
		Cfg:           tsCfg,
		program:       -1,
		pmtPid:        -1,
		videoPid:      -1,
		dataPid:       -1,
		pesData:       nil,
		continuityMap: make(map[int]uint8),
		segmenter:     NewSegmenter(tsCfg.SegCfg),
	}
	return &ts
}

func (ts *Ts) HandleTSPacket(data [packet.PacketSize]byte, outConn net.Conn) {
	pkt := packet.Packet(data)
	pid := pkt.PID()

	// Continuity Counter check
	cc := uint8(pkt.ContinuityCounter())
	if lastCC, ok := ts.continuityMap[pid]; ok {
		expected := (lastCC + 1) & 0x0F
		if cc != expected {
			//fmt.Printf("CC mismatch on PID 0x%x: expected %d, got %d\n", pid, expected, cc)
			ts.Stats.errorsCC++
		}
	}
	ts.continuityMap[pid] = cc

	// fmt.Println("GOT PACKET", pid, cc, pkt.PayloadUnitStartIndicator())

	// Read PCR
	if pkt.HasAdaptationField() {
		a, err := pkt.AdaptationField()
		if err != nil {
			fmt.Println("ERROR: failed to extract adaptation field")
		}
		has, err := a.HasPCR()
		if err != nil {
			fmt.Println("ERROR: failed to check PCR")
		}
		if has {
			pcr, _ := a.PCR()

			if ts.startPcr == 0 {
				ts.startPcr = pcr
			}
			ts.currentPcr = pcr
			//pcrTime := time.Duration(pcr) * time.Second / 27000000
			//fmt.Println("PCR", ts.startPcr, pcr, pcrTime)
		}
	}

	// Read EBP
	readEBP := false
	if readEBP {
		ebpBytes, err := adaptationfield.EncoderBoundaryPoint(&pkt)
		if err == nil {
			boundaryPoint, err := ebp.ReadEncoderBoundaryPoint(ebpBytes)
			if err == nil {
				fmt.Printf("EBP %+v\n", boundaryPoint)
			} else {
				fmt.Printf("EBP construction error %v", err)
			}
		}
	}

	// Parse PAT to find my PMT PID
	// PENDING(SS) - we want to discard packets before PAT (received every 100ms)
	if ts.pmtPid == -1 && pid == 0x0000 && pkt.PayloadUnitStartIndicator() {

		payload, err := pkt.Payload()
		if err != nil {
			fmt.Println("ERROR: failed to retrieve packet payload", "cc=", cc, err)
			return
		}

		pat, err := psi.NewPAT(payload)

		if err == nil {
			for program, pmtPid := range pat.ProgramMap() {
				fmt.Printf("PAT: Program %d -> PMT PID 0x%x\n", program, pmtPid)
				// Only considering program "1"
				if program == 1 {
					ts.pmtPid = pmtPid
				} else {
					fmt.Printf("PAT: Unexpected program %v", program)
				}
			}
		}
		return
	}

	// Parse PMT to find video PID
	if ts.videoPid == -1 && pid == ts.pmtPid && pkt.PayloadUnitStartIndicator() {
		// PENDING(SS) We should accumulate the entire PES potentially across multiple TS packets
		payload, err := pkt.Payload()
		if err != nil {
			fmt.Println("ERROR: failed to retrieve packet payload", "cc=", cc, err)
			return
		}

		pmt, err := psi.NewPMT(payload)
		if err == nil {
			for _, es := range pmt.ElementaryStreams() {
				fmt.Printf("PMT: stream type 0x%x on PID 0x%x %v\n", es.StreamType(), es.ElementaryPid(), es.StreamTypeDescription())
				for _, desc := range es.Descriptors() {
					fmt.Printf("  %s", desc.Format()) // Format() includes newline
				}
				if es.StreamType() == TsStreamTypeJpegXS || es.IsVideoContent() {
					ts.videoPid = es.ElementaryPid()
					ts.videoType = es.StreamType()
				}
				if es.IsPrivateContent() {
					for _, desc := range es.Descriptors() {
						if desc.Tag() == TsDescriptorSt2038 {
							ts.dataPid = es.ElementaryPid()
							ts.dataType = TsStreamTypeLocalSt2038
						}
					}
				}
			}
			fmt.Printf("VIDEO PID 0x%0x\n", ts.videoPid)
			fmt.Printf("DATA PID  0x%0x\n", ts.dataPid)
		}
		return
	}

	// Process video PID
	if ts.Cfg.ProcessVideo && pid == ts.videoPid && ts.videoType == TsStreamTypeJpegXS {
		//fmt.Printf("Video PID 0x%x packet received\n", pid)
		ts.processVideoPacket(&pkt, outConn)
	}

	// Process data PID
	if ts.Cfg.ProcessData && pid == ts.dataPid && ts.dataType == TsStreamTypeLocalSt2038 {
		verbose := false
		if verbose {
			var payload []byte
			var err error
			if pkt.HasPayload() {
				payload, err = pkt.Payload()
			} else {
				payload = []byte("NONE")
			}
			fmt.Printf("DATA PID start=%v haspayload=%v err=%v bytes=% x\n", pkt.PayloadUnitStartIndicator(), pkt.HasPayload(), err, payload)
		}
		ts.processDataPacket(&pkt)
	}

	// Write output
	bytesWritten, err := ts.segmenter.WritePacket(pkt, ts.currentPcr)
	ts.Stats.tsBytesWritten += uint64(bytesWritten)
	if err != nil {
		fmt.Println("ERROR: failed to write packet", err)
	} else {
		ts.Stats.nTsPacketsWritten++
	}

	ts.Stats.nTsPackets++
	ts.Stats.tsBytesReceived += uint64(len(pkt[:]))

	if ts.Stats.nTsPackets%100_000 == 1 {
		pcrTime := time.Duration(ts.currentPcr) * time.Second / 27000000
		fmt.Println("STATS", "n", ts.Stats.nTsPackets, ts.Stats.nTsPacketsWritten, "pcr", ts.currentPcr, pcrTime, "cc errors", ts.Stats.errorsCC,
			"bytes", ts.Stats.tsBytesReceived, ts.Stats.tsBytesReceived)
	}
}

func ToTSPacket(data []byte) (packet.Packet, error) {
	if len(data) < packet.PacketSize {
		return packet.Packet{}, fmt.Errorf("not enough data for TS packet")
	}
	var pkt packet.Packet
	copy(pkt[:], data[:packet.PacketSize])
	return pkt, nil
}

func (ts *Ts) processDataPacket(pkt *packet.Packet) error {
	if pkt.PID() != ts.dataPid {
		return fmt.Errorf("wrong pid %d", pkt.PID())
	}

	// If this packet has a start indicator, consider the previous PES 'done'
	if pkt.PayloadUnitStartIndicator() {
		// Finish old PES if accumulating
		if ts.pesData != nil {
			//fmt.Println("DATA PES START INDICATOR")
			payload := ts.pesData.Bytes()
			h, err := anc.ParseAncPes(payload)
			if err != nil {
				return err
			}
			fmt.Println("VANC", h.String())

			ts.pesData.Reset()
		} else {
			// Initialize accumulator and write the first data
			ts.pesData = packet.NewAccumulator(func(b []byte) (bool, error) {
				return false, nil
			})

		}
	}

	if ts.pesData == nil {
		// If we haven't found a PES start yet, discard packet
		return nil
	}

	_, err := ts.pesData.WritePacket(pkt)
	if err != nil {
		if err == gots.ErrAccumulatorDone {
			// Current implementation never sets the done flag
			fmt.Println("WARN: PES 'done' should not happen")
		} else {
			fmt.Println("ERROR: failed to write packet to PES accumulator")
			return err
		}
	}

	return nil
}

func (ts *Ts) processVideoPacket(pkt *packet.Packet, outConn net.Conn) error {

	payload, err := pkt.Payload()
	if err != nil {
		fmt.Println("ERROR: failed to retrieve packet payload", err)
		return err
	}

	if pkt.PayloadUnitStartIndicator() {
		if ts.pesAccumulatingVideo && len(ts.pesBuffer) > 0 {
			// Save the last PES packet
			//fmt.Println("save pes", len(pesBuffer))
			ts.savePayload(ts.pesBuffer, outConn)
		}
		ts.pesBuffer = make([]byte, 0)
		ts.pesAccumulatingVideo = true
	}

	if ts.pesAccumulatingVideo {
		ts.pesBuffer = append(ts.pesBuffer, payload...)
	}

	return nil
}

func (ts *Ts) savePayload(pes []byte, outConn net.Conn) {
	if len(pes) < 9 {
		return
	}
	// Parse PES header length
	pesHeaderLength := int(pes[8])
	payloadStart := 9 + pesHeaderLength
	if payloadStart >= len(pes) {
		return
	}
	ts.extractPayload(pes, outConn)
}

var nFrames = int(0)

// extractPayload assumes the PES contains a JXS frame (one or more "codestreams")
// and extracts the raw JXS codestream (trims the JXS header)
func (ts *Ts) extractPayload(pesData []byte, outConn net.Conn) {

	outputFile := fmt.Sprintf("frame_%04d.jxs", nFrames)
	nFrames++

	ph, err := pes.NewPESHeader(pesData)
	if err != nil {
		fmt.Println("WARN: bad PES header", err)
	}

	s := ph.StreamId()
	a := ph.HasPTS()
	p := ph.PTS()
	t := ph.PacketStartCodePrefix()

	headerLength := int(pesData[8])
	if len(pesData) <= headerLength {
		panic("PES payload not found (file too small)")
	}

	// Locate the start of the JXS codestream
	payload := pesData[headerLength:]
	jxesMagic := []byte{0x6A, 0x78, 0x65, 0x73} // "jxes"
	idx := indexOf(payload, jxesMagic)
	if idx < 0 {
		fmt.Println("WARN: Could not find 'jxes' marker in PES payload")
		idx = 0 // write whole payload anyway
	}

	jxsData := payload[idx:]

	// Parse the jxes header
	jxsh, _ := video.ParseJXESHeader(jxsData)
	//jxsh.Print()
	_ = jxsh

	// Strip the jxes header
	jxsCodeStream, err := video.StripJXESHeader(jxsData)
	if err != nil {
		fmt.Println("WARN: Failed to extract JXS code stream", err)
	}
	_ = jxsCodeStream

	fmt.Println("PES ", nFrames, "stream", s, "haspts", a, "pts", p, "pfx", t, "len", len(payload), "len es", len(jxsData), "len cs", len(jxsCodeStream))

	// Save to file
	if ts.Cfg.SaveFrameFiles {
		if err := os.WriteFile(outputFile, jxsCodeStream, 0644); err != nil {
			panic(err)
		}
		fmt.Printf("Wrote %d bytes of JXS codestream to %s\n", len(jxsCodeStream), outputFile)
	}

	// Write to unix socket
	err = video.SendJXSFrame(outConn, jxsCodeStream)
	if err != nil {
		fmt.Println("WARN: failed to send JXS frame", err)
	}
}

// Basic search for a byte pattern
func indexOf(data, pattern []byte) int {
	for i := 0; i <= len(data)-len(pattern); i++ {
		match := true
		for j := 0; j < len(pattern); j++ {
			if data[i+j] != pattern[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func StripPESHeader(pes []byte) ([]byte, error) {
	if len(pes) < 9 {
		return nil, fmt.Errorf("PES too short")
	}
	// Check start code
	if pes[0] != 0x00 || pes[1] != 0x00 || pes[2] != 0x01 {
		return nil, fmt.Errorf("invalid PES start code")
	}
	streamID := pes[3]
	if streamID != 0xBD {
		return nil, fmt.Errorf("expected private_stream_1 (0xBD), got 0x%02X", streamID)
	}

	pesHeaderLen := int(pes[8]) // header_data_length
	skip := 9 + pesHeaderLen
	if len(pes) < skip {
		return nil, fmt.Errorf("PES header length invalid")
	}
	return pes[skip:], nil
}
