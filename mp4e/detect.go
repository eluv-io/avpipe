package mp4e

import (
	"io"
	"path/filepath"
	"strings"
)

// This file centralizes detection of the MP4 / MOV (ISOBMFF) family

// mp4Extensions are the file extensions of the MP4 / MOV (ISOBMFF) family.
var mp4Extensions = map[string]bool{
	".mp4":  true,
	".m4v":  true,
	".m4a":  true,
	".mov":  true,
	".fmp4": true,
}

// IsMP4Extension reports whether path has an MP4/MOV-family file extension.
func IsMP4Extension(path string) bool {
	return mp4Extensions[strings.ToLower(filepath.Ext(path))]
}

// IsMP4FormatName reports whether the container format name reported by FFmpeg
// belongs to the MP4/MOV family.
// FFmpeg's demuxer reports a single comma-separated name ("mov,mp4,m4a,3gp,3g2,mj2")
func IsMP4FormatName(formatName string) bool {
	return strings.Contains(formatName, "mp4") || strings.Contains(formatName, "mov")
}

// HasFtypHeader reports whether r begins with an ISOBMFF/QuickTime "ftyp" box
// It doesn't match 'styp' or 'moof' so it will not match raw DASH/HLS segments.
// It reads the first 8 bytes and does not seek back.
func HasFtypHeader(r io.Reader) bool {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return false
	}
	return string(hdr[4:8]) == "ftyp"
}

// isobmffTopLevelBoxTypes are box types that may legitimately appear as the
// first box of an ISOBMFF stream or segment. It covers whole files/init segments
// (ftyp/moov), fragmented media segments (styp/moof/mdat), and the assorted
// filler/index boxes that can lead a file.
var isobmffTopLevelBoxTypes = map[string]bool{
	"ftyp": true, // file type (files, init segments)
	"styp": true, // segment type (fMP4 media segments)
	"moov": true, // movie box (some QuickTime .mov lead with this)
	"moof": true, // movie fragment (media segment with no styp)
	"mdat": true, // media data
	"free": true, // free space
	"skip": true, // free space
	"sidx": true, // segment index
	"pdin": true, // progressive download info
	"meta": true, // metadata
	"mfra": true, // movie fragment random access
	"wide": true, // QuickTime padding atom
	"pnot": true, // QuickTime preview atom
}

// HasISOBMFFHeader reports whether the first 8 bytes of r are a plausible
// top-level ISOBMFF box header (a recognized box type in bytes 4..8). Unlike
// HasFtypHeader it also accepts segment leaders (styp/moof), so it recognizes
// DASH/HLS fMP4 segments as well as whole files. It reads 8 bytes and does not
// seek back, so the caller must reposition the reader before decoding.
//
// Returns the header bytes read (up to 8)
func HasISOBMFFHeader(r io.Reader) (ok bool, hdr []byte) {
	var buf [8]byte
	n, err := io.ReadFull(r, buf[:])
	if err != nil {
		return false, buf[:n]
	}
	return isobmffTopLevelBoxTypes[string(buf[4:8])], buf[:n]
}
