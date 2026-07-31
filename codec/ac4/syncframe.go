package ac4

import (
	"bufio"
	"io"

	"github.com/eluv-io/errors-go"
)

// SyncFrame is one ac4_syncframe located in an AC-4 elementary stream (ETSI TS
// 103 190-1 §4.3): sync_word (0xAC40 no-CRC / 0xAC41 CRC-follows), frame_size, the
// raw_ac4_frame payload, and an optional trailing crc_word.
type SyncFrame struct {
	Index  int    // 0-based frame index within the stream
	Offset int64  // byte offset of the sync_word from the start of the stream
	Raw    []byte // the raw_ac4_frame payload (suitable for ParseTOC)
	HasCRC bool   // sync_word == 0xAC41 (a crc_word follows the payload)
}

// ScanSyncFrames walks a byte-aligned AC-4 sync-frame elementary stream incrementally,
// invoking visit for each frame. It reads through a bufio.Reader and never buffers more
// than one frame at a time, so it does not load the whole stream into memory. visit may
// return stop=true to end the scan early (e.g. once an I-frame limit is reached).
func ScanSyncFrames(r io.Reader, visit func(SyncFrame) (stop bool, err error)) error {
	e := errors.T("ac4.ScanSyncFrames", errors.K.Invalid.Default())
	br := bufio.NewReader(r)
	var offset int64
	hdr := make([]byte, 4)
	for index := 0; ; index++ {
		n, err := io.ReadFull(br, hdr)
		if n == 0 && err == io.EOF {
			return nil // clean end of stream on a frame boundary
		}
		if err == io.ErrUnexpectedEOF || (err == io.EOF && n > 0) {
			return e("reason", "truncated sync-frame header", "offset", offset)
		}
		if err != nil {
			return e(err, "reason", "read sync-frame header", "offset", offset)
		}

		syncWord := uint16(hdr[0])<<8 | uint16(hdr[1])
		var hasCRC bool
		switch syncWord {
		case 0xAC40:
			hasCRC = false
		case 0xAC41:
			hasCRC = true
		default:
			return e("reason", "invalid sync word", "sync_word", syncWord, "offset", offset)
		}

		frameSize := int(uint16(hdr[2])<<8 | uint16(hdr[3]))
		headerLen := int64(4)
		if frameSize == 0xFFFF { // 24-bit extended frame_size escape
			ext := make([]byte, 3)
			if _, err := io.ReadFull(br, ext); err != nil {
				return e(err, "reason", "truncated 24-bit frame_size", "offset", offset)
			}
			frameSize = int(ext[0])<<16 | int(ext[1])<<8 | int(ext[2])
			headerLen += 3
		}

		raw := make([]byte, frameSize)
		if _, err := io.ReadFull(br, raw); err != nil {
			return e(err, "reason", "truncated frame payload", "frame_size", frameSize, "offset", offset)
		}

		crcLen := int64(0)
		if hasCRC {
			if _, err := io.CopyN(io.Discard, br, 2); err != nil {
				return e(err, "reason", "truncated crc_word", "offset", offset)
			}
			crcLen = 2
		}

		stop, err := visit(SyncFrame{Index: index, Offset: offset, Raw: raw, HasCRC: hasCRC})
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
		offset += headerLen + int64(frameSize) + crcLen
	}
}

// IFrameIndices scans an AC-4 sync-frame elementary stream and returns the 0-based
// indices of frames whose ac4_toc has b_iframe_global set. It stops after max I-frames
// are found (max <= 0 means unlimited) and reads incrementally.
func IFrameIndices(r io.Reader, max int) ([]int, error) {
	e := errors.T("ac4.IFrameIndices", errors.K.Invalid.Default())
	var out []int
	err := ScanSyncFrames(r, func(sf SyncFrame) (bool, error) {
		toc, err := ParseTOC(sf.Raw)
		if err != nil {
			return false, e(err, "frame", sf.Index, "offset", sf.Offset)
		}
		if toc.IFrameGlobal {
			out = append(out, sf.Index)
			if max > 0 && len(out) >= max {
				return true, nil
			}
		}
		return false, nil
	})
	return out, err
}
