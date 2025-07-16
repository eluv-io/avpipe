package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Part of ISO/IEC 13818-1 Amendment for JPEG XS - encapsulation of JPEG XS in MPEGTS

const StreamTypeJpegXS = 0x32

// JXESHeader represents the parsed JPEG XS elementary stream header (jxes)
type JXESHeader struct {
	BoxCode            uint32
	Bitrate            uint32
	FrameRate          uint32
	SampleChar         uint32
	Profile            uint16
	Level              uint16
	ColorPrimaries     uint8
	TransferChar       uint8
	MatrixCoefficients uint8
	FullRangeFlag      bool
	Timecode           uint32
}

// ParseJXESHeader parses a 28-byte jxes header into a JXESHeader struct
func ParseJXESHeader(data []byte) (*JXESHeader, error) {
	if len(data) < 28 {
		return nil, fmt.Errorf("input too short: expected 28 bytes, got %d", len(data))
	}

	boxCode := binary.BigEndian.Uint32(data[0:4])
	if boxCode != 0x6a786573 { // 'jxes'
		return nil, fmt.Errorf("invalid box code: 0x%x", boxCode)
	}

	header := &JXESHeader{
		BoxCode:            boxCode,
		Bitrate:            binary.BigEndian.Uint32(data[4:8]),
		FrameRate:          binary.BigEndian.Uint32(data[8:12]),
		SampleChar:         binary.BigEndian.Uint32(data[12:16]),
		Profile:            binary.BigEndian.Uint16(data[16:18]),
		Level:              binary.BigEndian.Uint16(data[18:20]),
		ColorPrimaries:     data[20],
		TransferChar:       data[21],
		MatrixCoefficients: data[22],
		FullRangeFlag:      (data[23] & 0x80) != 0,
		Timecode:           binary.BigEndian.Uint32(data[24:28]),
	}
	return header, nil
}

// Print prints the values of the JXESHeader fields
func (h *JXESHeader) Print() {
	fmt.Println("=== JXES Header ===")
	fmt.Printf("BoxCode (should be 'jxes'):     0x%x\n", h.BoxCode)
	fmt.Printf("Bitrate (brat):                 %d Mbps\n", h.Bitrate)
	fmt.Printf("FrameRate (frat):               0x%x\n", h.FrameRate)
	fmt.Printf("Sample Characteristics (schar): 0x%x\n", h.SampleChar)
	fmt.Printf("Profile (Ppih):                 %d\n", h.Profile)
	fmt.Printf("Level/Sublevel (Plev):          %d\n", h.Level)
	fmt.Printf("Color Primaries:                %d\n", h.ColorPrimaries)
	fmt.Printf("Transfer Characteristics:       %d\n", h.TransferChar)
	fmt.Printf("Matrix Coefficients:            %d\n", h.MatrixCoefficients)
	fmt.Printf("Full Range Flag:                %v\n", h.FullRangeFlag)
	fmt.Printf("Timecode (tcod):                0x%x\n", h.Timecode)
	fmt.Println("====================")
}

// StripJXESHeader checks for a valid 28-byte jxes header and returns the codestream
func StripJXESHeader(data []byte) ([]byte, error) {
	if len(data) < 30 {
		return nil, fmt.Errorf("data too short: got %d bytes, need at least 30", len(data))
	}

	// Check jxes magic box code: 0x6A786573 ('jxes')
	boxCode := binary.BigEndian.Uint32(data[0:4])
	if boxCode != 0x6A786573 {
		return nil, fmt.Errorf("invalid jxes box code: expected 0x6A786573, got 0x%X", boxCode)
	}

	const codestreamStartMarker = "\xff\x10"

	index := bytes.Index(data, []byte(codestreamStartMarker))
	if index == -1 {
		return nil, errors.New("codestream start marker 0xFF 0x10 not found")
	}
	if index+1 >= len(data) {
		return nil, errors.New("found 0xFF without complete codestream marker")
	}

	return data[index:], nil
}
