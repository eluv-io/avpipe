// Package anc encodes and parses SMPTE ST 2038 ancillary data. The payload of one ST 2038 PES packet, as avpipe
// reports it in AV_IN_STAT_DATA_ANC, holds the ST 291 ancillary data packets of one video frame, bit-packed.
// ParsePES splits the payload into packets with the 10-bit word parity stripped and checked, EncodePES is its
// inverse, and the payload decoders, such as ParseTimecode, read a packet's user data words.
package anc

import (
	"errors"
	"fmt"
)

// Packet is one ST 291 ancillary data packet as ST 2038 carries it: where it sat in the SDI frame and its words
// with the parity bits removed.
type Packet struct {
	CNotY      bool   // true: the packet was in the chroma (C) data stream, false: luma (Y)
	Line       int    // line number in the frame (11 bits)
	HOffset    int    // horizontal offset within the line (12 bits)
	DID        uint8  // data identifier
	SDID       uint8  // secondary data identifier (the data block number for a type 1 DID, 0x80 and above)
	UDW        []byte // user data words, 8 bits each
	ParityOK   bool   // every 10-bit word had consistent parity bits (bit 8 even parity, bit 9 its complement)
	ChecksumOK bool   // the checksum word matched the sum of the words
}

// Kind returns what the DID/SDID pair carries, for the common registered pairs.
func (p Packet) Kind() Kind { return KindOf(p.DID, p.SDID) }

func (p Packet) String() string {
	s := fmt.Sprintf("line %d off %d DID 0x%02x SDID 0x%02x %s %d words", p.Line, p.HOffset, p.DID, p.SDID,
		p.Kind(), len(p.UDW))
	if !p.ParityOK {
		s += " BAD PARITY"
	}
	if !p.ChecksumOK {
		s += " BAD CHECKSUM"
	}
	return s
}

// Kind names the registered uses of a DID/SDID pair that the live pipeline cares about.
type Kind int

const (
	KindUnknown         Kind = iota
	KindPayloadID            // ST 352 payload identifier, 0x41/0x01
	KindAFD                  // ST 2016-3 active format description and bar data, 0x41/0x05
	KindSCTE104              // ST 2010 SCTE-104 messages, 0x41/0x07
	KindTeletextOP47         // RDD 8 (OP-47) teletext subtitles, 0x43/0x02
	KindAudioMetadata        // ST 2020 audio metadata, 0x45/0x01-0x09
	KindTimecode             // ST 12-2 ancillary time code, 0x60/0x60
	KindCaptions708          // ST 334-1 CEA-708 captions (CDP), 0x61/0x01
	KindCaptions608          // ST 334-1 CEA-608 captions, 0x61/0x02
	KindPrivateUserData      // ST 291 private user data, 0x50/0x01: the venue clock feed (see package venueclock)
)

var kindNames = map[Kind]string{
	KindUnknown:         "unknown",
	KindPayloadID:       "payload-id",
	KindAFD:             "afd",
	KindSCTE104:         "scte-104",
	KindTeletextOP47:    "op-47",
	KindAudioMetadata:   "audio-metadata",
	KindTimecode:        "timecode",
	KindCaptions708:     "cea-708",
	KindCaptions608:     "cea-608",
	KindPrivateUserData: "private-user-data",
}

func (k Kind) String() string {
	if s, ok := kindNames[k]; ok {
		return s
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// KindOf maps a DID/SDID pair to its Kind.
func KindOf(did, sdid uint8) Kind {
	switch did {
	case 0x41:
		switch sdid {
		case 0x01:
			return KindPayloadID
		case 0x05:
			return KindAFD
		case 0x07:
			return KindSCTE104
		}
	case 0x43:
		if sdid == 0x02 {
			return KindTeletextOP47
		}
	case 0x45:
		if sdid >= 0x01 && sdid <= 0x09 {
			return KindAudioMetadata
		}
	case 0x50:
		if sdid == 0x01 {
			return KindPrivateUserData
		}
	case 0x60:
		if sdid == 0x60 {
			return KindTimecode
		}
	case 0x61:
		switch sdid {
		case 0x01:
			return KindCaptions708
		case 0x02:
			return KindCaptions608
		}
	}
	return KindUnknown
}

var (
	ErrShort  = errors.New("anc: payload ends inside a packet")
	ErrHeader = errors.New("anc: packet does not start with six zero bits")
)

// minPacketBits is the smallest packet: header (6+1+11+12) and DID, SDID, data count and checksum words.
const minPacketBits = 30 + 4*10

// ParsePES parses one ST 2038 PES payload into its ancillary data packets. Per packet the payload holds six
// zero bits, c_not_y, the line number (11 bits), the horizontal offset (12 bits), then DID, SDID, the data count,
// the user data words and the checksum as 10-bit words, followed by one bits to the next byte boundary; one bits
// after the last packet are stuffing. On an error the packets parsed before it are returned with the error.
func ParsePES(b []byte) ([]Packet, error) {
	r := &bitReader{b: b}
	var pkts []Packet
	for r.left() >= minPacketBits {
		if b[r.pos>>3] == 0xff {
			break // stuffing after the last packet
		}
		p, err := parsePacket(r)
		if err != nil {
			return pkts, fmt.Errorf("packet %d: %w", len(pkts), err)
		}
		pkts = append(pkts, p)
		r.align()
	}
	if left := r.left(); left > 0 && left < minPacketBits && b[r.pos>>3] != 0xff {
		return pkts, fmt.Errorf("packet %d: %w", len(pkts), ErrShort)
	}
	return pkts, nil
}

func parsePacket(r *bitReader) (Packet, error) {
	var p Packet
	if r.read(6) != 0 {
		return p, ErrHeader
	}
	p.CNotY = r.read(1) == 1
	p.Line = int(r.read(11))
	p.HOffset = int(r.read(12))
	did, sdid, dc := r.read(10), r.read(10), r.read(10)
	p.DID, p.SDID = uint8(did), uint8(sdid)
	count := int(dc & 0xff)
	if r.left() < (count+1)*10 {
		return p, ErrShort
	}
	sum := did + sdid + dc
	p.ParityOK = parityOK(did) && parityOK(sdid) && parityOK(dc)
	p.UDW = make([]byte, count)
	for i := range p.UDW {
		w := r.read(10)
		sum += w
		p.ParityOK = p.ParityOK && parityOK(w)
		p.UDW[i] = uint8(w)
	}
	cs := r.read(10)
	// Checksum: the nine low bits of the sum of the words, bit 9 the complement of bit 8
	p.ChecksumOK = cs&0x1ff == sum&0x1ff && (cs>>9)&1 == (cs>>8)&1^1
	return p, nil
}

// EncodePES returns the ST 2038 PES payload carrying pkts in order, in the layout ParsePES reads: the parity bits
// and the checksum of every packet are computed, so ParityOK and ChecksumOK of the input play no part. Line and
// HOffset are written in their 11 and 12 bits, and only the first 255 user data words are written.
func EncodePES(pkts []Packet) []byte {
	w := &bitWriter{}
	for _, p := range pkts {
		w.put(0, 6)
		if p.CNotY {
			w.put(1, 1)
		} else {
			w.put(0, 1)
		}
		w.put(uint32(p.Line), 11)
		w.put(uint32(p.HOffset), 12)
		udw := p.UDW
		if len(udw) > 255 {
			udw = udw[:255]
		}
		words := append([]uint8{p.DID, p.SDID, uint8(len(udw))}, udw...)
		sum := uint32(0)
		for _, v := range words {
			word := parity10(v)
			w.put(word, 10)
			sum += word
		}
		// Checksum: the nine low bits of the sum of the words, bit 9 the complement of bit 8
		cs := sum & 0x1ff
		if cs&0x100 == 0 {
			cs |= 0x200
		}
		w.put(cs, 10)
		w.alignOnes()
	}
	return w.buf
}

// parityOK checks an ST 291 10-bit word: bit 8 is the even parity of bits 0-7 and bit 9 its complement.
func parityOK(w uint32) bool {
	return w>>8 == parity10(uint8(w))>>8
}

// parity10 returns the ST 291 10-bit word for an 8-bit value: bit 8 is the even parity of bits 0-7 and bit 9
// its complement.
func parity10(v uint8) uint32 {
	p := uint32(0)
	for x := v; x != 0; x &= x - 1 {
		p ^= 1
	}
	return uint32(v) | p<<8 | (p^1)<<9
}

// bitReader reads most significant bit first. Callers check left() before reading; a read past the end returns
// zero bits.
type bitReader struct {
	b   []byte
	pos uint // in bits
}

func (r *bitReader) left() int { return len(r.b)*8 - int(r.pos) }

func (r *bitReader) read(n uint) uint32 {
	var v uint32
	for i := uint(0); i < n; i++ {
		var bit uint32
		if int(r.pos) < len(r.b)*8 {
			bit = uint32(r.b[r.pos>>3]>>(7-r.pos&7)) & 1
		}
		v = v<<1 | bit
		r.pos++
	}
	return v
}

func (r *bitReader) align() { r.pos = (r.pos + 7) &^ 7 }

// bitWriter packs values most significant bit first.
type bitWriter struct {
	buf []byte
	acc uint64
	n   uint
}

func (w *bitWriter) put(v uint32, bits uint) {
	w.acc = w.acc<<bits | uint64(v)&(1<<bits-1)
	w.n += bits
	for w.n >= 8 {
		w.n -= 8
		w.buf = append(w.buf, byte(w.acc>>w.n))
	}
	w.acc &= 1<<w.n - 1
}

// alignOnes pads with one bits to the next byte boundary.
func (w *bitWriter) alignOnes() {
	if w.n > 0 {
		w.put(1<<(8-w.n)-1, 8-w.n)
	}
}
