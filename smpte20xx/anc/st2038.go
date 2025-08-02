package anc

import (
	"fmt"
	"io"
)

type AncHeader struct {
	PacketStartCodePrefix  uint
	StreamID               uint8
	PESPacketLength        uint16
	Prefix2                uint8
	ScramblingControl      uint8
	PESPriority            uint8
	DataAlignmentIndicator uint8
	Copyright              uint8
	OriginalOrCopy         uint8
	PTSDTSFlags            uint8
	ESCRFlag               uint8
	ESRateFlag             uint8
	DSMTrickModeFlag       uint8
	AdditionalCopyInfoFlag uint8
	PESCRCFlag             uint8
	PESExtensionFlag       uint8
	PESHeaderDataLength    uint8

	PTS    uint64
	Anc    []Anc
	Errors []string
}

type Anc struct {
	Did  uint8
	Sdid uint8

	Description string
	Errors      []string

	Timecode *AncTimecode
}

type AncTimecode struct {
	Hours      int
	Minutes    int
	Seconds    int
	Frames     int
	DropFrame  bool
	ColorFrame bool
	BGF        [4]byte
}

// parseAncWord10 takes a 10-bit ANC word and returns:
// - value: the 8-bit data
// - valid: true if parity bits are correct
func parseAncWord10(word uint16) (value uint8, valid bool) {
	raw := uint8(word & 0xFF)       // bits 0–7
	parity := (word >> 8) & 0x01    // bit 8
	invParity := (word >> 9) & 0x01 // bit 9

	// Count bits set in raw value
	count := 0
	for i := 0; i < 8; i++ {
		if (raw>>i)&1 == 1 {
			count++
		}
	}
	expectedParity := uint8(count % 2) // even parity

	return raw, uint8(parity) == expectedParity && invParity == (parity^1)
}

func parseAncTimecode(words []uint16) (*AncTimecode, error) {
	if len(words) < 8 {
		return nil, fmt.Errorf("timecode ANC must have at least 8 data words")
	}
	tc := &AncTimecode{}

	// Unpack all 8-bit words from 10-bit values
	data := make([]uint8, 18)
	for i := 0; i < 18; i++ {
		val, ok := parseAncWord10(words[i])
		if !ok {
			return nil, fmt.Errorf("parity error in timecode word %d", i)
		}
		data[i] = val
	}

	off := 0

	// Parse BCD + flags
	tc.Frames = int((data[off+0]&0x30>>4)*10 + (data[off+0] & 0x0F))
	tc.DropFrame = (data[off+0] & 0x40) != 0
	tc.ColorFrame = (data[off+0] & 0x80) != 0

	tc.Seconds = int((data[off+1]&0x70>>4)*10 + (data[off+1] & 0x0F))
	tc.Minutes = int((data[off+2]&0x70>>4)*10 + (data[off+2] & 0x0F))
	tc.Hours = int((data[off+3]&0x30>>4)*10 + (data[off+3] & 0x0F))

	copy(tc.BGF[:], data[off+4:off+8])

	return tc, nil
}

func (tc *AncTimecode) String() string {
	df := ""
	if tc.DropFrame {
		df = " DF"
	}
	cf := ""
	if tc.ColorFrame {
		cf = " CF"
	}

	return fmt.Sprintf(
		"%02d:%02d:%02d:%02d%s%s BGF=[%02X %02X %02X %02X]",
		tc.Hours, tc.Minutes, tc.Seconds, tc.Frames,
		df, cf,
		tc.BGF[0], tc.BGF[1], tc.BGF[2], tc.BGF[3],
	)
}

func (h *AncHeader) String() string {
	var ancs string
	for i := range h.Anc {
		ancs = ancs + fmt.Sprintf("[%d] %s ", i, h.Anc[i].Description)
		if h.Anc[i].Timecode != nil {
			ancs = ancs + " Timecode=" + h.Anc[i].Timecode.String()
		}
		ancs = ancs + fmt.Sprintf(" Errors: %d ", len(h.Anc[i].Errors))
	}
	return fmt.Sprintf("Stream=0x%x Len=%d PTS=%d Num ANCs=%d ANCs=%s",
		h.StreamID, h.PESPacketLength, h.PTS, len(h.Anc), ancs)
}

type BitReader struct {
	buf []byte
	pos int // bit offset
}

// ReadBits reads n bits and returns them as a uint
func (r *BitReader) ReadBits(n int) (uint, error) {
	var out uint
	for i := 0; i < n; i++ {
		if r.pos >= len(r.buf)*8 {
			return 0, io.ErrUnexpectedEOF
		}
		byteIndex := r.pos / 8
		bitIndex := 7 - (r.pos % 8)
		bit := (r.buf[byteIndex] >> bitIndex) & 1
		out = (out << 1) | uint(bit)
		r.pos++
	}
	return out, nil
}

// PeekBits peeks ahead n bits without advancing the position
func (r *BitReader) PeekBits(n int) (uint, error) {
	oldPos := r.pos
	val, err := r.ReadBits(n)
	r.pos = oldPos
	return val, err
}

func (r *BitReader) ByteAlign() {
	if rem := r.pos % 8; rem != 0 {
		r.pos += (8 - rem)
	}
}

// parseAncPes parses out the PES header and each of the enclosed ANC packets
func ParseAncPes(pes []byte) (*AncHeader, error) {

	h := &AncHeader{}
	var err error

	br := BitReader{
		buf: pes,
		pos: 0,
	}

	if h.PacketStartCodePrefix, err = br.ReadBits(24); err != nil {
		return nil, fmt.Errorf("read PacketStartCodePrefix: %w", err)
	}
	if v, err := br.ReadBits(8); err != nil {
		return nil, fmt.Errorf("read StreamID: %w", err)
	} else {
		h.StreamID = uint8(v)
	}
	if v, err := br.ReadBits(16); err != nil {
		return nil, fmt.Errorf("read PESPacketLength: %w", err)
	} else {
		h.PESPacketLength = uint16(v)
	}
	if v, err := br.ReadBits(2); err != nil {
		return nil, fmt.Errorf("read Prefix2: %w", err)
	} else {
		h.Prefix2 = uint8(v)
	}
	if v, err := br.ReadBits(2); err != nil {
		return nil, fmt.Errorf("read ScramblingControl: %w", err)
	} else {
		h.ScramblingControl = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read PESPriority: %w", err)
	} else {
		h.PESPriority = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read DataAlignmentIndicator: %w", err)
	} else {
		h.DataAlignmentIndicator = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read Copyright: %w", err)
	} else {
		h.Copyright = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read OriginalOrCopy: %w", err)
	} else {
		h.OriginalOrCopy = uint8(v)
	}
	if v, err := br.ReadBits(2); err != nil {
		return nil, fmt.Errorf("read PTSDTSFlags: %w", err)
	} else {
		h.PTSDTSFlags = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read ESCRFlag: %w", err)
	} else {
		h.ESCRFlag = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read ESRateFlag: %w", err)
	} else {
		h.ESRateFlag = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read DSMTrickModeFlag: %w", err)
	} else {
		h.DSMTrickModeFlag = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read AdditionalCopyInfoFlag: %w", err)
	} else {
		h.AdditionalCopyInfoFlag = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read PESCRCFlag: %w", err)
	} else {
		h.PESCRCFlag = uint8(v)
	}
	if v, err := br.ReadBits(1); err != nil {
		return nil, fmt.Errorf("read PESExtensionFlag: %w", err)
	} else {
		h.PESExtensionFlag = uint8(v)
	}
	if v, err := br.ReadBits(8); err != nil {
		return nil, fmt.Errorf("read PESHeaderDataLength: %w", err)
	} else {
		h.PESHeaderDataLength = uint8(v)
	}

	if verbose {
		vPrintf("packet_start_code_prefix: 0x%06X\n", h.PacketStartCodePrefix)
		vPrintf("stream_id: 0x%02X\n", h.StreamID)
		vPrintf("PES_packet_length: %d\n", h.PESPacketLength)
		vPrintf("'10' prefix (should be 2): %d\n", h.Prefix2)
		vPrintf("scrambling_control: %d\n", h.ScramblingControl)
		vPrintf("PES_priority: %d\n", h.PESPriority)
		vPrintf("data_alignment_indicator: %d\n", h.DataAlignmentIndicator)
		vPrintf("copyright: %d\n", h.Copyright)
		vPrintf("original_or_copy: %d\n", h.OriginalOrCopy)
		vPrintf("PTS_DTS_flags: %d\n", h.PTSDTSFlags)
		vPrintf("ESCR_flag: %d\n", h.ESCRFlag)
		vPrintf("ES_rate_flag: %d\n", h.ESRateFlag)
		vPrintf("DSM_trick_mode_flag: %d\n", h.DSMTrickModeFlag)
		vPrintf("additional_copy_info_flag: %d\n", h.AdditionalCopyInfoFlag)
		vPrintf("PES_CRC_flag: %d\n", h.PESCRCFlag)
		vPrintf("PES_extension_flag: %d\n", h.PESExtensionFlag)
		vPrintf("PES_header_data_length: %d\n", h.PESHeaderDataLength)
	}

	// Parse PTS (5 bytes: '0010' + 3+1 + 15+1 + 15+1 bits)
	if prefix, err := br.ReadBits(4); err == nil {
		vPrintf("PTS prefix (should be 2): %d\n", prefix)
	}
	if v1, err := br.ReadBits(3); err == nil {
		exp0, _ := br.ReadBits(1) // marker
		if exp0 != 1 {
			fmt.Println("ERROR exp0 1")
		}
		if v2, err := br.ReadBits(15); err == nil {
			exp1, _ := br.ReadBits(1)
			if exp1 != 1 {
				fmt.Println("ERROR exp1 1")
			}
			if v3, err := br.ReadBits(15); err == nil {
				exp2, _ := br.ReadBits(1)
				if exp2 != 1 {
					fmt.Println("ERROR exp3 1")
				}

				h.PTS = (uint64(v1) << 30) | (uint64(v2) << 15) | uint64(v3)
				vPrintf("PTS: %d (0x%08X)\n", h.PTS, h.PTS)
			}
		}
	}

	index := 0
	for br.pos+72 <= len(pes)*8 { // enough for at least one ANC block header
		zeroBits, _ := br.ReadBits(6)
		if zeroBits != 0 {
			if zeroBits == 0x3f {
				// Reached padding - all 1s
			} else {
				fmt.Printf("Block %d: expected 6 zero bits, got %d\n", index, zeroBits)
			}
			break
		}
		cFlag, _ := br.ReadBits(1)
		line, _ := br.ReadBits(11)
		hoff, _ := br.ReadBits(12)
		did10, _ := br.ReadBits(10)
		sdid10, _ := br.ReadBits(10)
		dc10, _ := br.ReadBits(10)

		did, ok := parseAncWord10(uint16(did10))
		if !ok {
			fmt.Println("ERROR: failed ANC 10bit checksum")
		}
		sdid, ok := parseAncWord10(uint16(sdid10))
		if !ok {
			fmt.Println("ERROR: failed ANC 10bit checksum")
		}
		dc, ok := parseAncWord10(uint16(dc10))
		if !ok {
			fmt.Println("ERROR: failed ANC 10bit checksum")
		}

		anc := Anc{
			Did:         did,
			Sdid:        sdid,
			Description: ancDescription(did, sdid),
		}

		vPrintf("Block %d:\n", index)
		vPrintf("  c_not_y_channel_flag: %d\n", cFlag)
		vPrintf("  line_number: %d\n", line)
		vPrintf("  horizontal_offset: %d\n", hoff)
		vPrintf("  DID: 0x%02X\n", did)
		vPrintf("  SDID: 0x%02X\n", sdid)
		vPrintf("  data_count: %d\n", dc)
		vPrintf("  user_data_words: ...\n")

		w10s := make([]uint16, 1024) // Array of 10bit words - fix sizing
		for j := 0; j < int(dc); j++ {
			if br.pos+10 > len(pes)*8 {
				anc.Errors = append(anc.Errors, "ERROR: truncated checksum_word")
				break
			}
			udw, _ := br.ReadBits(10)
			udw8, _ := parseAncWord10(uint16(udw))
			w10s[j] = uint16(udw)
			vPrintf("    Word[%d] = 0x%03X (%08b)\n", j, udw8, udw8)
		}

		if did == 0x61 {
			anc.Timecode, err = parseAncTimecode(w10s)
			if err != nil {
				anc.Errors = append(anc.Errors, fmt.Sprintf("ERROR: failed to parse timecode (%v)", err))
			}
		}

		if br.pos+10 > len(pes)*8 {
			anc.Errors = append(anc.Errors, "ERROR: truncated checksum_word")
		}
		chk, _ := br.ReadBits(10)
		vPrintf("  checksum_word: 0x%03X\n", chk)

		// read '1' padding until byte-aligned
		for br.pos%8 != 0 {
			padBit, _ := br.ReadBits(1)
			if padBit != 1 {
				fmt.Println("WARNING: non-1 padding bit found")
			}
		}

		h.Anc = append(h.Anc, anc)
		index++
	}

	// optional: parse stuffing bytes
	for br.pos+8 <= len(pes)*8 {
		b, _ := br.ReadBits(8)
		if b == 0xFF {
			vPrintf("  stuffing_byte: 0xFF\n")
		} else {
			vPrintf("  remaining byte: 0x%02X (non-stuffing?)\n", b)
		}
	}

	return h, err
}

func ancDescription(did, sdid byte) string {
	switch {
	case did == 0x41 && sdid == 0x05:
		return "CEA-708 Closed Captions (HD)"
	case did == 0x61 && sdid == 0x01:
		return "ATC Timecode (LTC)"
	case did == 0x61 && sdid == 0x02:
		return "ATC Timecode (VITC1)"
	case did == 0x61 && sdid == 0x03:
		return "ATC Timecode (VITC2)"
	case did == 0x60 && sdid == 0x60:
		return "Legacy VITC/LTC Timecode"
	case did == 0x62 && sdid == 0x01:
		return "AFD (Active Format Description)"
	case did == 0x62 && sdid == 0x02:
		return "Bar Data"
	case did == 0x43 && sdid == 0x07:
		return "OP-47 Subtitles"
	case did == 0x45 && sdid == 0x01:
		return "SCTE-104 Splice Message"
	case did == 0x50 && sdid == 0x20:
		return "Dolby Metadata"
	default:
		return fmt.Sprintf("Unknown ANC (DID=0x%02X, SDID=0x%02X)", did, sdid)
	}
}

const verbose = false

// vPrintf behaves like fmt.Printf, but only prints if verbose is true
func vPrintf(format string, args ...interface{}) {
	if verbose {
		fmt.Printf(format, args...)
	}
}
