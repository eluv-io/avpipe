package anc

import (
	"errors"
	"fmt"
)

// Timecode is SMPTE ST 12-2 ancillary time code (ATC), DID 0x60 / SDID 0x60: the 64-bit ST 12-1 time code four
// bits per word in the high nibbles of the 16 user data words, so the time digits and flags are in the even
// words and the binary groups in the odd ones, and the two distributed binary bit groups in bit 3 of each word.
type Timecode struct {
	Hours, Minutes, Seconds, Frames int
	DropFrame                       bool
	ColorFrame                      bool
	// Flags are bits 27, 43, 58 and 59 of the ST 12-1 code: the field (or phase-correction) mark and the binary
	// group flags. Which is which depends on the video standard (525-line: field mark in bit 27; 625-line: in
	// bit 43), so they are kept raw.
	Flags        [4]bool
	BinaryGroups [8]uint8 // BG1 to BG8, 4 bits each
	DBB1         uint8    // payload type: 0 LTC, 1 VITC field 1, 2 VITC field 2, others as ST 12-2 assigns them
	DBB2         uint8    // VITC line select and the time code validity and process bits
}

var ErrTimecode = errors.New("anc: not an ST 12-2 time code")

// ParseTimecode decodes the user data words of a time code packet (KindTimecode).
func ParseTimecode(udw []byte) (Timecode, error) {
	var t Timecode
	if len(udw) != 16 {
		return t, fmt.Errorf("%w: %d words, want 16", ErrTimecode, len(udw))
	}
	nib := func(i int) int { return int(udw[i] >> 4) }
	t.Frames = nib(0) + 10*(nib(2)&3)
	t.DropFrame = udw[2]&0x40 != 0
	t.ColorFrame = udw[2]&0x80 != 0
	t.Seconds = nib(4) + 10*(nib(6)&7)
	t.Flags[0] = udw[6]&0x80 != 0
	t.Minutes = nib(8) + 10*(nib(10)&7)
	t.Flags[1] = udw[10]&0x80 != 0
	t.Hours = nib(12) + 10*(nib(14)&3)
	t.Flags[2] = udw[14]&0x40 != 0
	t.Flags[3] = udw[14]&0x80 != 0
	for i := 0; i < 8; i++ {
		t.BinaryGroups[i] = udw[2*i+1] >> 4
		t.DBB1 |= (udw[i] >> 3 & 1) << i
		t.DBB2 |= (udw[8+i] >> 3 & 1) << i
	}
	if nib(0) > 9 || nib(4) > 9 || nib(8) > 9 || nib(12) > 9 || t.Seconds > 59 || t.Minutes > 59 || t.Hours > 23 {
		return t, fmt.Errorf("%w: digits out of range (%s)", ErrTimecode, t)
	}
	return t, nil
}

// UDW returns the 16 user data words of a time code packet carrying t, the inverse of ParseTimecode. The time
// fields are written as two decimal digits each and the binary groups in their four bits; values out of range
// are truncated.
func (t Timecode) UDW() []byte {
	udw := make([]byte, 16)
	digits := [8]int{
		t.Frames % 10, t.Frames / 10 % 4,
		t.Seconds % 10, t.Seconds / 10 % 8,
		t.Minutes % 10, t.Minutes / 10 % 8,
		t.Hours % 10, t.Hours / 10 % 4,
	}
	for i, d := range digits {
		udw[2*i] = byte(d) << 4
		udw[2*i+1] = t.BinaryGroups[i] << 4
	}
	flag := func(i int, set bool, bit byte) {
		if set {
			udw[i] |= bit
		}
	}
	flag(2, t.DropFrame, 0x40)
	flag(2, t.ColorFrame, 0x80)
	flag(6, t.Flags[0], 0x80)
	flag(10, t.Flags[1], 0x80)
	flag(14, t.Flags[2], 0x40)
	flag(14, t.Flags[3], 0x80)
	for i := 0; i < 8; i++ {
		udw[i] |= (t.DBB1 >> i & 1) << 3
		udw[8+i] |= (t.DBB2 >> i & 1) << 3
	}
	return udw
}

// String formats the time as hh:mm:ss:ff, with ';' before the frames for drop frame.
func (t Timecode) String() string {
	sep := ":"
	if t.DropFrame {
		sep = ";"
	}
	return fmt.Sprintf("%02d:%02d:%02d%s%02d", t.Hours, t.Minutes, t.Seconds, sep, t.Frames)
}

// Source names the payload type in DBB1.
func (t Timecode) Source() string {
	switch t.DBB1 {
	case 0:
		return "LTC"
	case 1:
		return "VITC1"
	case 2:
		return "VITC2"
	default:
		return fmt.Sprintf("DBB1 0x%02x", t.DBB1)
	}
}

// FindTimecode returns the first time code packet among pkts, decoded.
func FindTimecode(pkts []Packet) (Timecode, bool) {
	for _, p := range pkts {
		if p.Kind() == KindTimecode {
			if t, err := ParseTimecode(p.UDW); err == nil {
				return t, true
			}
		}
	}
	return Timecode{}, false
}
