package transport

// This handles UDP (unicast and multicast), RTP decapsulation, MPEGTS basic processing
import (
	"encoding/binary"
	"errors"
	"fmt"
)

func StripRTP(data []byte) ([]byte, error) {
	hdr, err := ParseRTPHeader(data)
	if err != nil {
		return nil, err
	}
	if len(data) < hdr.ByteLength()+188 {
		return nil, fmt.Errorf("packet too short for RTP and TS")
	}
	return data[hdr.ByteLength():], nil
}

var ErrShortRTP = errors.New("RTP packet too short")

type RTPHeader struct {
	Version        uint8
	Padding        bool
	Extension      bool
	CSRCCount      uint8
	Marker         bool
	PayloadType    uint8
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	// PENDING(SS) CSRCs and extension not included
	ExtensionByteCount int // Number of bytes in the extension (header + payload), if present
}

func (h *RTPHeader) ByteLength() int {
	length := 12 // Base RTP header length
	if h.CSRCCount > 0 {
		length += int(h.CSRCCount) * 4
	}
	if h.Extension {
		length += h.ExtensionByteCount
	}
	return length
}

func ParseRTPHeader(data []byte) (*RTPHeader, error) {
	baseHeaderSize := 12 // Minimum size of RTP header
	if len(data) < baseHeaderSize {
		return nil, ErrShortRTP
	}

	b0 := data[0]
	b1 := data[1]

	header := &RTPHeader{
		Version:        b0 >> 6,
		Padding:        (b0>>5)&0x01 == 1,
		Extension:      (b0>>4)&0x01 == 1,
		CSRCCount:      b0 & 0x0F,
		Marker:         (b1>>7)&0x01 == 1,
		PayloadType:    b1 & 0x7F,
		SequenceNumber: binary.BigEndian.Uint16(data[2:4]),
		Timestamp:      binary.BigEndian.Uint32(data[4:8]),
		SSRC:           binary.BigEndian.Uint32(data[8:12]),
	}
	lenCSRC := 4 * int(header.CSRCCount)
	if len(data) < baseHeaderSize+lenCSRC {
		return nil, fmt.Errorf("RTP packet too short for CSRCs: expected at least %d bytes, got %d", baseHeaderSize+lenCSRC, len(data))
	}
	if header.Extension {
		extLen := binary.BigEndian.Uint16(data[baseHeaderSize+lenCSRC+2 : baseHeaderSize+lenCSRC+4]) // Read extension length
		header.ExtensionByteCount = (int(extLen) * 4) + 4                                            // 4 bytes for the extension header
		if len(data) < baseHeaderSize+lenCSRC+header.ExtensionByteCount {
			return nil, fmt.Errorf("RTP packet too short for extension: expected at least %d bytes, got %d", baseHeaderSize+lenCSRC+header.ExtensionByteCount, len(data))
		}
	}

	return header, nil
}
