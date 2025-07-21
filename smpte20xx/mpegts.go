package main

import (
	"encoding/hex"
	"fmt"
	"net"

	"github.com/Comcast/gots/v2"
	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/psi"
)

var myProgram = 1
var myPmt = -1
var myVideoPid = -1
var myDataPid = 0x105 //0x012 //0x12d

var isVideoJpegXS = false

func handleTSPacket(ts [packet.PacketSize]byte, outConn net.Conn) {
	pkt := packet.Packet(ts)
	pid := pkt.PID()

	// Continuity Counter check
	cc := uint8(pkt.ContinuityCounter())
	if lastCC, ok := continuityMap[pid]; ok {
		expected := (lastCC + 1) & 0x0F
		if cc != expected {
			//fmt.Printf("CC mismatch on PID 0x%x: expected %d, got %d\n", pid, expected, cc)
		}
	}
	continuityMap[pid] = cc

	//fmt.Println("GOT PACKET", pid, cc, pkt.PayloadUnitStartIndicator())

	// Parse PAT to find my PMT PID
	if myPmt == -1 && pid == 0x0000 && pkt.PayloadUnitStartIndicator() {

		payload, err := pkt.Payload()
		if err != nil {
			fmt.Println("ERROR: failed to retrieve packet payload", "cc=", cc, err)
			return
		}

		pat, err := psi.NewPAT(payload)

		if err == nil {
			for program, pmtPID := range pat.ProgramMap() {
				fmt.Printf("PAT: Program %d -> PMT PID 0x%x\n", program, pmtPID)
				if program == myProgram {
					myPmt = pmtPID
				}
			}
		}
		return
	}

	// Parse PMT to find video PID
	if myVideoPid == -1 && pid == myPmt && pkt.PayloadUnitStartIndicator() {

		payload, err := pkt.Payload()
		if err != nil {
			fmt.Println("ERROR: failed to retrieve packet payload", "cc=", cc, err)
			return
		}

		pmt, err := psi.NewPMT(payload)
		if err == nil {
			for _, es := range pmt.ElementaryStreams() {
				fmt.Printf("PMT: stream type 0x%x on PID 0x%x\n", es.StreamType(), es.ElementaryPid())
				if es.StreamType() == StreamTypeJpegXS || es.IsVideoContent() {
					myVideoPid = es.ElementaryPid()
					fmt.Println("VIDEO STREAM", myVideoPid)
					if es.StreamType() == StreamTypeJpegXS {
						isVideoJpegXS = true
					}

				}
			}
		}
		return
	}

	// Process video PID
	if pid == myVideoPid && isVideoJpegXS {
		//fmt.Printf("Video PID 0x%x packet received\n", pid)
		processPacket(&pkt, outConn)
	}

	// Process data PID
	if pid == myDataPid {
		var payload []byte
		var err error
		if pkt.HasPayload() {
			payload, err = pkt.Payload()
		} else {
			payload = []byte("NONE")
		}
		fmt.Println("DATA PID", pkt.PayloadUnitStartIndicator(), pkt.HasPayload(), err, payload)
		processDataPacket(&pkt)
	}
}

var continuityMap = make(map[int]uint8)

func toTSPacket(data []byte) (packet.Packet, error) {
	if len(data) < packet.PacketSize {
		return packet.Packet{}, fmt.Errorf("not enough data for TS packet")
	}
	var pkt packet.Packet
	copy(pkt[:], data[:packet.PacketSize])
	return pkt, nil
}

// parseSMPTE2038Payload
func parseSMPTE2038Payload(payload []byte) {
	if len(payload) < 16 {
		fmt.Println("Short SMPTE 2038 header")
		return
	}

	fmt.Printf("PES DATA: % x\n", payload)

	payload = payload[16:] // Skip 2038 PES header

	for len(payload) >= 6 {
		did := payload[0]
		sdid := payload[1]
		dataCount := int(payload[2])
		if len(payload) < 3+dataCount+1 {
			fmt.Println("Truncated ANC packet")
			return
		}

		userData := payload[3 : 3+dataCount]
		checksum := payload[3+dataCount]

		fmt.Printf("ANC Packet: DID=0x%02x SDID=0x%02x Count=%d Checksum=0x%02x\n",
			did, sdid, dataCount, checksum)
		fmt.Printf("Payload: % x\n", userData)

		payload = payload[4+dataCount:] // move to next packet
	}
}

var pesData packet.Accumulator = nil

func processDataPacket(pkt *packet.Packet) error {

	if pkt.PID() != myDataPid {
		return fmt.Errorf("wrong pid %d", pkt.PID())
	}

	fmt.Println("DATA PROCESS", pesData)
	if pesData == nil {
		// Function to detect when a PES packet is complete.
		isDone := func(b []byte) (bool, error) {
			// SMPTE 2038 PES packets are usually self-contained in one PES
			// Check if we received enough bytes to assume the PES is complete
			if len(b) < 6 {
				return false, nil
			}
			// PES header: 6 bytes + optional
			pesHeaderLen := int(b[4]) + 6
			totalLen := pesHeaderLen + int(b[4+1])<<8 + int(b[4+2])

			fmt.Println("DATA ISDONE", "len", len(b), "total", totalLen)
			// Safety: done when we exceed declared PES length
			return true /*len(b) >= totalLen*/, nil
		}

		pesData = packet.NewAccumulator(isDone)
	}

	w, err := pesData.WritePacket(pkt)
	fmt.Println("DATA PACKET", w, err)
	if err == gots.ErrAccumulatorDone {
		payload := pesData.Bytes()
		parseSMPTE2038Payload(payload)
		pesData.Reset()
	}

	return nil
}

// ANC packet structure constants
const (
	MinANCPacketLen = 5 // DID + SDID + DC + 1 data byte + checksum
)

// ParseANCPackets parses a buffer of ANC data and prints details for each packet.
func ParseANCPackets(data []byte) {
	offset := 0
	packetIndex := 0
	for offset+MinANCPacketLen <= len(data) {
		did := data[offset]
		sdid := data[offset+1]
		dc := int(data[offset+2])
		if offset+3+dc >= len(data) {
			fmt.Printf("[ANC %02d] Incomplete ANC packet at offset %d\n", packetIndex, offset)
			break
		}

		userdata := data[offset+3 : offset+3+dc]
		checksum := data[offset+3+dc]

		computed := did ^ sdid ^ byte(dc)
		for _, b := range userdata {
			computed ^= b
		}

		fmt.Printf("[ANC %02d] DID: 0x%02X, SDID: 0x%02X, DC: %d, CHK: 0x%02X (expected 0x%02X)\n",
			packetIndex, did, sdid, dc, checksum, computed)
		fmt.Printf("  Data: %s\n", hex.EncodeToString(userdata))

		if checksum != computed {
			fmt.Println("  WARNING: Checksum mismatch!")
		}

		// Advance to next ANC packet
		offset += 3 + dc + 1
		packetIndex++
	}
	if offset < len(data) {
		fmt.Printf("Remaining %d bytes after ANC parse\n", len(data)-offset)
	}
}
