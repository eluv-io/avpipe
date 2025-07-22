package transport

// This handles UDP (unicast and multicast), RTP decapsulation, MPEGTS basic processing
import (
	"encoding/binary"
	"errors"
	"fmt"
)

func StripRTP(data []byte) ([]byte, error) {
	if len(data) < 12+188 {
		return nil, fmt.Errorf("packet too short for RTP and TS")
	}
	return data[12:], nil
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
}

func ParseRTPHeader(data []byte) (*RTPHeader, error) {
	if len(data) < 12 {
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

	return header, nil
}
