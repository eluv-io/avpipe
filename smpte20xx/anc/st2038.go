package anc

import (
	"encoding/binary"
	"fmt"
)

// ANC packet structure constants
const (
	MinANCPacketLen = 5 // DID + SDID + DC + 1 data byte + checksum
)

// AncPacket represents one ANC data packet.
type AncPacket struct {
	CNotY            bool
	LineNumber       uint16
	HorizontalOffset uint16
	DID              byte
	SDID             byte
	DataCount        byte
	UserDataWords    []byte
	Checksum         byte
}

// ParseSMPTE2038PES parses the SMPTE 2038 PES payload and extracts ANC packets.
func ParseSMPTE2038PES(data []byte) ([]AncPacket, error) {
	if len(data) < 14 || data[0] != 0x00 || data[1] != 0x00 || data[2] != 0x01 || data[3] != 0xBD {
		return nil, fmt.Errorf("not a valid SMPTE 2038 PES packet")
	}

	fmt.Printf("DATA PROCESS % x\n", data)

	pesHeaderLen := 9 + int(data[8]) // basic PES + PES_header_data_length
	if len(data) < pesHeaderLen {
		return nil, fmt.Errorf("incomplete PES header")
	}

	payload := data[pesHeaderLen:]
	var packets []AncPacket

	i := 0
	for i+6 <= len(payload) {
		// Look for the 6 zero bits start code
		if payload[i] != 0x00 {
			break
		}
		// Each ANC data packet starts with 3 bytes:
		//  - c_not_y (1 bit), line_number (11 bits), horizontal_offset (12 bits)
		if i+6 > len(payload) {
			break
		}
		word1 := binary.BigEndian.Uint16(payload[i+1 : i+3])
		cNotY := (word1 & 0x0800) != 0
		lineNumber := (word1 >> 4) & 0x07FF
		hOffset := ((word1 & 0x000F) << 8) | uint16(payload[i+3])

		did := payload[i+4] >> 2
		sdid := ((payload[i+4] & 0x03) << 6) | (payload[i+5] >> 2)
		dataCount := ((payload[i+5] & 0x03) << 4) | (payload[i+6] >> 4)

		dataWords := make([]byte, dataCount)
		if i+7+int(dataCount) >= len(payload) {
			break
		}
		copy(dataWords, payload[i+7:i+7+int(dataCount)])
		checksum := payload[i+7+int(dataCount)]

		packet := AncPacket{
			CNotY:            cNotY,
			LineNumber:       lineNumber,
			HorizontalOffset: hOffset,
			DID:              did,
			SDID:             sdid,
			DataCount:        dataCount,
			UserDataWords:    dataWords,
			Checksum:         checksum,
		}
		packet.Print()
		packets = append(packets, packet)

		// Move to next ANC data packet (align to next byte boundary)
		i += 7 + int(dataCount) + 1
		for i < len(payload) && payload[i] == 0xFF {
			i++
		}
	}

	return packets, nil
}

func (a *AncPacket) Print() {
	fmt.Println("Ancillary Packet:")
	fmt.Printf("  C Not Y Flag     : %v\n", a.CNotY)
	fmt.Printf("  Line Number      : %d\n", a.LineNumber)
	fmt.Printf("  Horizontal Offset: %d\n", a.HorizontalOffset)
	fmt.Printf("  DID              : 0x%02X\n", a.DID)
	fmt.Printf("  SDID             : 0x%02X\n", a.SDID)
	fmt.Printf("  Data Count       : %d\n", a.DataCount)
	fmt.Printf("  User Data Words  :")
	for i, word := range a.UserDataWords {
		if i%8 == 0 {
			fmt.Printf("\n    ")
		}
		fmt.Printf("0x%03X ", word)
	}
	fmt.Printf("\n  Checksum         : 0x%03X\n", a.Checksum)
}
