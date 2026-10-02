package tlv

import (
	"encoding/binary"
	"testing"

	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

// makeTSPacket returns a single 188-byte TS packet that passes packet.CheckErrors: a valid sync byte, non-scrambled,
// and a payload-only adaptation field control.
func makeTSPacket() []byte {
	pkt := make([]byte, 188)
	pkt[0] = 0x47 // sync byte
	pkt[3] = 0x10 // adaptation field control = payload only (01), continuity counter 0
	return pkt
}

// makeTSDatagram returns a datagram of n concatenated valid TS packets.
func makeTSDatagram(n int) []byte {
	dg := make([]byte, 0, n*188)
	for i := 0; i < n; i++ {
		dg = append(dg, makeTSPacket()...)
	}
	return dg
}

// buildAtsTs builds a TlvTypeAtsTs part: TLV header, 8-byte big-endian arrival timestamp, then the raw TS datagram.
func buildAtsTs(t *testing.T, timestamp int64, datagram []byte) []byte {
	t.Helper()
	header, err := TlvHeader(AtsTimestampLen+len(datagram), TlvTypeAtsTs)
	require.NoError(t, err)

	out := make([]byte, 0, len(header)+AtsTimestampLen+len(datagram))
	out = append(out, header...)
	var ts [AtsTimestampLen]byte
	binary.BigEndian.PutUint64(ts[:], uint64(timestamp))
	out = append(out, ts[:]...)
	out = append(out, datagram...)
	return out
}

func TestAtsTs_TypeMapping(t *testing.T) {
	require.Equal(t, "ATS-TS", TlvTypeAtsTs.String())

	tlvType, err := ByteToTLVType(0x04)
	require.NoError(t, err)
	require.Equal(t, TlvTypeAtsTs, tlvType)
}

func TestAtsTs_ValidateRoundTrip(t *testing.T) {
	const arrival int64 = 1_700_000_000_123_456_789
	datagram := makeTSDatagram(3)
	part := buildAtsTs(t, arrival, datagram)

	tlvType, err := ValidateTLV(part)
	require.NoError(t, err)
	require.Equal(t, TlvTypeAtsTs, tlvType)

	// The timestamp must round-trip from the bytes following the TLV header.
	gotTs := int64(binary.BigEndian.Uint64(part[TLV_HEADER_LEN : TLV_HEADER_LEN+AtsTimestampLen]))
	require.Equal(t, arrival, gotTs)
}

func TestAtsTs_ValidateTooShortForTimestamp(t *testing.T) {
	// A value shorter than the timestamp prefix must be rejected. Length field reports the actual (too short) value
	// size so it passes the header/length check and reaches validateAtsTS.
	value := []byte{0x01, 0x02, 0x03}
	part := make([]byte, TLV_HEADER_LEN+len(value))
	part[0] = 0x04
	binary.BigEndian.PutUint16(part[1:3], uint16(len(value)))
	copy(part[TLV_HEADER_LEN:], value)

	_, err := ValidateTLV(part)
	require.Error(t, err)
}

func TestAtsTs_ValidateBadTSData(t *testing.T) {
	// Timestamp present, but the TS payload is not a multiple of 188 bytes.
	part := buildAtsTs(t, 1, []byte{0x47, 0x00, 0x00})
	_, err := ValidateTLV(part)
	require.Error(t, err)
}

// buildRtpTs builds an RTP-TS (or RTP-TS-NoPad) TLV part: TLV header followed by an RTP-marshaled datagram carrying
// the given raw TS payload.
func buildRtpTs(t *testing.T, tlvType TlvType, hdr pionrtp.Header, tsData []byte) []byte {
	t.Helper()
	rtpPkt := pionrtp.Packet{Header: hdr, Payload: tsData}
	rtpDatagram, err := rtpPkt.Marshal()
	require.NoError(t, err)

	header, err := TlvHeader(len(rtpDatagram), tlvType)
	require.NoError(t, err)

	return append(append([]byte{}, header...), rtpDatagram...)
}

func TestRtpTs_ValidateRoundTrip(t *testing.T) {
	for _, tlvType := range []TlvType{TlvTypeRtpTs, TlvTypeRtpTsNoPad} {
		t.Run(tlvType.String(), func(t *testing.T) {
			datagram := makeTSDatagram(3)
			part := buildRtpTs(t, tlvType, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, datagram)

			gotType, err := ValidateTLV(part)
			require.NoError(t, err)
			require.Equal(t, tlvType, gotType)
		})
	}
}

func TestRtpTs_ValidateExcludesRTPPadding(t *testing.T) {
	// RTP padding must not be treated as part of the TS payload - a 200-byte padding block (not a multiple of 188)
	// would otherwise make validateRawTS reject an otherwise-valid datagram.
	datagram := makeTSDatagram(1)
	hdr := pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1, Padding: true, PaddingSize: 200}
	part := buildRtpTs(t, TlvTypeRtpTs, hdr, datagram)

	_, err := ValidateTLV(part)
	require.NoError(t, err)
}

func TestRtpTs_ValidateRejectsMalformedPacket(t *testing.T) {
	value := []byte{0x01, 0x02} // too short to be a valid RTP header
	part := make([]byte, TLV_HEADER_LEN+len(value))
	part[0] = byte(TlvTypeRtpTs)
	binary.BigEndian.PutUint16(part[1:3], uint16(len(value)))
	copy(part[TLV_HEADER_LEN:], value)

	_, err := ValidateTLV(part)
	require.Error(t, err)
}

func TestRtpTs_ValidateRejectsNonVersion2Packet(t *testing.T) {
	datagram := makeTSDatagram(1)
	part := buildRtpTs(t, TlvTypeRtpTs, pionrtp.Header{Version: 1, SequenceNumber: 1, Timestamp: 1}, datagram)

	_, err := ValidateTLV(part)
	require.Error(t, err)
}

func TestRtpTs_ValidateBadTSData(t *testing.T) {
	// Valid RTP header, but the TS payload is not a multiple of 188 bytes.
	part := buildRtpTs(t, TlvTypeRtpTs, pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, []byte{0x47, 0x00, 0x00})

	_, err := ValidateTLV(part)
	require.Error(t, err)
}
