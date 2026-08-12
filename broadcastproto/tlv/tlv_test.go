package tlv

import (
	"encoding/binary"
	"testing"

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
