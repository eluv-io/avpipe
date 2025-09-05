package pdu

import (
	"encoding/binary"
	"fmt"

	"github.com/Comcast/gots/v2/packet"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
)

// PDU (Protocol Data Unit) definitions for MPEGTS over various transports.
// PDU encapsulation should only be used for serialization/deserialization of packetized data.

type PduType byte

const PDU_HEADER_LEN = 3

const (
	PduTypeUnknown PduType = iota
	PduTypeRtpTs
	PduTypeRawTs
)

var ErrUnknownPduType = fmt.Errorf("unknown PDU type")

func (pt PduType) String() string {
	switch pt {
	case PduTypeRtpTs:
		return "RTP-TS"
	case PduTypeRawTs:
		return "Raw-TS"
	default:
		return "Unknown"
	}
}

func ByteToPDUType(b byte) (PduType, error) {
	switch b {
	case 0x01:
		return PduTypeRtpTs, nil
	case 0x02:
		return PduTypeRawTs, nil
	}
	return PduTypeUnknown, ErrUnknownPduType
}

// ValidatePDU checks if the provided data is a valid PDU and returns its type.
// If the PDU type is not valid, the data is too short, or any other error occurs,
// it returns an error. It also validates the RTP header or raw TS packets based on the PDU type.
func ValidatePDU(data []byte) (PduType, error) {
	if len(data) < PDU_HEADER_LEN {
		return PduTypeUnknown, fmt.Errorf("data too short to contain PDU header")
	}
	pduType, err := ByteToPDUType(data[0])
	if err != nil {
		return PduTypeUnknown, fmt.Errorf("invalid PDU type: %w", err)
	}

	dataLen := binary.BigEndian.Uint16(data[1:3])
	if len(data)-PDU_HEADER_LEN < int(dataLen) {
		return PduTypeUnknown, fmt.Errorf("data length mismatch: expected %d, got %d", dataLen, len(data)-PDU_HEADER_LEN)
	}

	switch pduType {
	case PduTypeRtpTs:
		err = validateRtpTS(data[PDU_HEADER_LEN : PDU_HEADER_LEN+dataLen])
	case PduTypeRawTs:
		err = validateRawTS(data[PDU_HEADER_LEN : PDU_HEADER_LEN+dataLen])
	}

	return pduType, err
}

func validateRtpTS(data []byte) error {
	rtpOffset, err := transport.StripRTP(data)
	if err != nil {
		return err
	}

	mpegtsData := data[rtpOffset:]
	return validateRawTS(mpegtsData)
}

func validateRawTS(data []byte) error {
	if len(data)%188 != 0 {
		return fmt.Errorf("raw TS data length is not a multiple of 188 bytes: %d", len(data))
	}

	var pkt packet.Packet
	for offset := 0; offset < len(data); offset += 188 {
		copy(pkt[:], data[offset:offset+188])
		err := pkt.CheckErrors()
		if err != nil {
			return fmt.Errorf("invalid TS packet at index %d: %w", offset/188, err)
		}
	}

	return nil
}

/*


## case 1

params:
  input:
    copy_mode: raw
	copy_packaging: rtp
    // contains encapsulation details for mpegts stream
sources:
  mpegts:
    a
	...
  video:
    b
	...
  ...


## case 2

params:
  input:
    // contains encapsulation details for mpegts stream
sources:
  tlv-rtp-mpegts:
    a
	...
  video:
    b
	...
  ...



*/
