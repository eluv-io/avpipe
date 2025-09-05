package tlv

import (
	"encoding/binary"
	"fmt"

	"github.com/Comcast/gots/v2/packet"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
)

// TLV (Type Length Value) definitions for MPEGTS over various transports.

type TlvType byte

const TLV_HEADER_LEN = 3

const U16MAX = 0xFFFF

const (
	TlvTypeUnknown TlvType = iota
	TlvTypeRtpTs
	TlvTypeRawTs
)

var ErrUnknownTlvType = fmt.Errorf("unknown TLV type")

func (pt TlvType) String() string {
	switch pt {
	case TlvTypeRtpTs:
		return "RTP-TS"
	case TlvTypeRawTs:
		return "Raw-TS"
	default:
		return "Unknown"
	}
}

func TlvHeader(length int, tlvType TlvType) ([]byte, error) {
	if length > U16MAX || length < 0 {
		// TODO: Truncate this to a more reasonable length, 64k is probably still way too big
		return nil, fmt.Errorf("bad length for TLV: %d", length)
	}
	var header [TLV_HEADER_LEN]byte
	header[0] = byte(tlvType)
	binary.BigEndian.PutUint16(header[1:3], uint16(length))
	return header[:], nil
}

func ByteToTLVType(b byte) (TlvType, error) {
	switch b {
	case 0x01:
		return TlvTypeRtpTs, nil
	case 0x02:
		return TlvTypeRawTs, nil
	}
	return TlvTypeUnknown, ErrUnknownTlvType
}

// ValidateTLV checks if the provided data is a valid TLV and returns its type.
// If the TLV type is not valid, the data is too short, or any other error occurs,
// it returns an error. It also validates the RTP header or raw TS packets based on the TLV type.
func ValidateTLV(data []byte) (TlvType, error) {
	if len(data) < TLV_HEADER_LEN {
		return TlvTypeUnknown, fmt.Errorf("data too short to contain TLV header")
	}
	tlvType, err := ByteToTLVType(data[0])
	if err != nil {
		return TlvTypeUnknown, fmt.Errorf("invalid TLV type: %w", err)
	}

	dataLen := binary.BigEndian.Uint16(data[1:3])
	if len(data)-TLV_HEADER_LEN < int(dataLen) {
		return TlvTypeUnknown, fmt.Errorf("data length mismatch: expected %d, got %d", dataLen, len(data)-TLV_HEADER_LEN)
	}

	switch tlvType {
	case TlvTypeRtpTs:
		err = validateRtpTS(data[TLV_HEADER_LEN : TLV_HEADER_LEN+dataLen])
	case TlvTypeRawTs:
		err = validateRawTS(data[TLV_HEADER_LEN : TLV_HEADER_LEN+dataLen])
	}

	return tlvType, err
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
