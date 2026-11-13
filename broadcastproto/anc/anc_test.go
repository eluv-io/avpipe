package anc_test

import (
	"testing"

	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handVector is one SCTE-104 packet (DID 0x41 / SDID 0x07) on line 9 with no data words, packed by hand:
//
//	000000 0 00000001001 000000000000   header: zero bits, Y, line 9, offset 0
//	10 0100 0001  DID 0x41  (two bits set: even parity 0, bit 9 = 1)  -> 0x241
//	01 0000 0111  SDID 0x07 (three bits: odd parity 1, bit 9 = 0)     -> 0x107
//	10 0000 0000  DC 0                                                -> 0x200
//	checksum: (0x241+0x107+0x200) & 0x1ff = 0x148, bit 8 = 1 -> bit 9 = 0 -> 0x148
//	11            stuffing, 72 bits in all
var handVector = []byte{0x00, 0x02, 0x40, 0x02, 0x41, 0x41, 0xe0, 0x05, 0x23}

func TestParsePESHandVector(t *testing.T) {
	// Lay the bits out explicitly to be sure of the vector above: 30 header bits, four words, two stuffing bits
	bits := "000000" + "0" + "00000001001" + "000000000000" +
		"1001000001" + "0100000111" + "1000000000" + "0101001000" + "11"
	require.Len(t, bits, 72)
	var b []byte
	for i := 0; i < len(bits); i += 8 {
		var v byte
		for _, c := range bits[i : i+8] {
			v = v<<1 | byte(c-'0')
		}
		b = append(b, v)
	}
	require.Equal(t, handVector, b, "the hand vector is the bits above")

	pkts, err := anc.ParsePES(b)
	require.NoError(t, err)
	require.Len(t, pkts, 1)
	p := pkts[0]
	assert.False(t, p.CNotY)
	assert.Equal(t, 9, p.Line)
	assert.Equal(t, 0, p.HOffset)
	assert.Equal(t, uint8(0x41), p.DID)
	assert.Equal(t, uint8(0x07), p.SDID)
	assert.Empty(t, p.UDW)
	assert.True(t, p.ParityOK)
	assert.True(t, p.ChecksumOK)
	assert.Equal(t, anc.KindSCTE104, p.Kind())

	// The encoder produces the same bytes
	assert.Equal(t, b, anc.EncodePES([]anc.Packet{{Line: 9, DID: 0x41, SDID: 0x07}}))
}

func TestEncodePES(t *testing.T) {
	// One packet, DID 0x41 / SDID 0x07, no data words: 6+1+11+12 = 30 header bits, three words and the checksum
	// = 40 bits, 70 bits in all -> 9 bytes, the last two bits stuffing
	one := anc.EncodePES([]anc.Packet{{Line: 9, DID: 0x41, SDID: 0x07}})
	assert.Len(t, one, 9)
	assert.Equal(t, byte(0), one[0]&0xfc, "six zero bits first")
	assert.Equal(t, byte(0x03), one[8]&0x03, "stuffing bits are ones")
	line := uint32(one[0]&0x01)<<10 | uint32(one[1])<<2 | uint32(one[2])>>6
	assert.Equal(t, uint32(9), line)

	// Packets are concatenated, each starting on a byte boundary, so the first packet's bytes are unchanged
	second := anc.Packet{DID: 0x60, SDID: 0x60, UDW: make([]byte, 16)}
	two := anc.EncodePES([]anc.Packet{{Line: 9, DID: 0x41, SDID: 0x07}, second})
	assert.Equal(t, one, two[:9])
	assert.Equal(t, anc.EncodePES([]anc.Packet{second}), two[9:])
	// 30 + (3+16+1)*10 = 230 bits -> 29 bytes
	assert.Len(t, two, 9+29)

	// More than 255 words: the first 255 are written
	pkts, err := anc.ParsePES(anc.EncodePES([]anc.Packet{{DID: 0x50, SDID: 0x01, UDW: make([]byte, 300)}}))
	require.NoError(t, err)
	require.Len(t, pkts, 1)
	assert.Len(t, pkts[0].UDW, 255)
	assert.True(t, pkts[0].ParityOK && pkts[0].ChecksumOK)
}

func TestParsePESRoundTrip(t *testing.T) {
	in := []anc.Packet{
		{Line: 9, DID: 0x60, SDID: 0x60, UDW: anc.Timecode{Hours: 1, Minutes: 2, Seconds: 3, Frames: 4, DBB1: 1}.UDW()},
		{CNotY: true, Line: 12, HOffset: 1920, DID: 0x50, SDID: 0x01, UDW: []byte("score 1-0")},
		{Line: 13, DID: 0x61, SDID: 0x01, UDW: make([]byte, 255)},
	}
	pkts, err := anc.ParsePES(anc.EncodePES(in))
	require.NoError(t, err)
	require.Len(t, pkts, 3)
	for i, p := range pkts {
		assert.Equal(t, in[i].CNotY, p.CNotY, "packet %d", i)
		assert.Equal(t, in[i].Line, p.Line, "packet %d", i)
		assert.Equal(t, in[i].HOffset, p.HOffset, "packet %d", i)
		assert.Equal(t, in[i].DID, p.DID, "packet %d", i)
		assert.Equal(t, in[i].SDID, p.SDID, "packet %d", i)
		assert.Equal(t, in[i].UDW, p.UDW, "packet %d", i)
		assert.True(t, p.ParityOK && p.ChecksumOK, "packet %d", i)
	}
	assert.Equal(t, anc.KindTimecode, pkts[0].Kind())
	assert.Equal(t, anc.KindPrivateUserData, pkts[1].Kind())
	assert.Equal(t, anc.KindCaptions708, pkts[2].Kind())

	tc, ok := anc.FindTimecode(pkts)
	require.True(t, ok)
	assert.Equal(t, "01:02:03:04", tc.String())
	assert.Equal(t, "VITC1", tc.Source())
}

func TestParsePESErrors(t *testing.T) {
	good := anc.EncodePES([]anc.Packet{{Line: 9, DID: 0x41, SDID: 0x07, UDW: []byte{1, 2, 3}}})

	// Trailing stuffing bytes are not packets
	pkts, err := anc.ParsePES(append(append([]byte{}, good...), 0xff, 0xff, 0xff))
	require.NoError(t, err)
	assert.Len(t, pkts, 1)

	// Empty payload: no packets, no error
	pkts, err = anc.ParsePES(nil)
	require.NoError(t, err)
	assert.Empty(t, pkts)

	// Cut inside the words
	_, err = anc.ParsePES(good[:len(good)-2])
	assert.ErrorIs(t, err, anc.ErrShort)

	// A second packet that does not start with zero bits: the first is still returned
	bad := append(append([]byte{}, good...), 0x40, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	pkts, err = anc.ParsePES(bad)
	assert.ErrorIs(t, err, anc.ErrHeader)
	assert.Len(t, pkts, 1)

	// A flipped data bit (bit 79, the low bit of the second user data word) breaks parity and checksum; the
	// packet is still delivered
	flipped := append([]byte{}, good...)
	flipped[9] ^= 0x01
	pkts, err = anc.ParsePES(flipped)
	require.NoError(t, err)
	require.Len(t, pkts, 1)
	assert.False(t, pkts[0].ParityOK)
	assert.False(t, pkts[0].ChecksumOK)
}

func TestParseTimecode(t *testing.T) {
	// 23:59:58;29 drop frame, LTC, by hand: digits in the high nibbles of the even words, drop bit in word 2
	udw := []byte{0x90, 0, 0x20 | 0x40, 0, 0x80, 0, 0x50, 0, 0x90, 0, 0x50, 0, 0x30, 0, 0x20, 0}
	tc, err := anc.ParseTimecode(udw)
	require.NoError(t, err)
	assert.Equal(t, "23:59:58;29", tc.String())
	assert.True(t, tc.DropFrame)
	assert.Equal(t, "LTC", tc.Source())
	assert.Equal(t, udw, anc.Timecode{Hours: 23, Minutes: 59, Seconds: 58, Frames: 29, DropFrame: true}.UDW())

	// Binary groups are the odd words, DBB2 bit 3 of words 8-15, the field mark bit 7 of word 6
	udw[1] = 0xA0  // BG1 = 0xA
	udw[15] = 0x50 // BG8 = 0x5
	udw[8] |= 0x08 // DBB2 bit 0
	udw[15] |= 0x08
	udw[6] |= 0x80
	tc, err = anc.ParseTimecode(udw)
	require.NoError(t, err)
	assert.Equal(t, uint8(0xA), tc.BinaryGroups[0])
	assert.Equal(t, uint8(0x5), tc.BinaryGroups[7])
	assert.Equal(t, uint8(0x81), tc.DBB2)
	assert.True(t, tc.Flags[0])
	assert.Equal(t, "23:59:58;29", tc.String(), "flags and groups leave the time alone")
	assert.Equal(t, udw, tc.UDW(), "UDW is the inverse of ParseTimecode")

	// Every field survives a round trip
	full := anc.Timecode{Hours: 12, Minutes: 34, Seconds: 56, Frames: 7, ColorFrame: true,
		Flags: [4]bool{true, false, true, true}, BinaryGroups: [8]uint8{1, 2, 3, 4, 5, 6, 7, 8}, DBB1: 2, DBB2: 0x5a}
	tc, err = anc.ParseTimecode(full.UDW())
	require.NoError(t, err)
	assert.Equal(t, full, tc)

	_, err = anc.ParseTimecode(udw[:15])
	assert.ErrorIs(t, err, anc.ErrTimecode)
	_, err = anc.ParseTimecode(anc.Timecode{Hours: 24}.UDW())
	assert.ErrorIs(t, err, anc.ErrTimecode, "hours 24")
}
