package mvhevc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/Eyevinn/mp4ff/mp4"
)

type PatchStatus int

const (
	StatusNeedMore    PatchStatus = iota // moov not yet fully buffered
	StatusPatched                        // moov found and modified
	StatusPassthrough                    // moov found but no changes required (can also be not MV-HEVC)
)

// PatchHeadBytes accepts the leading bytes of an FFmpeg-generated MV-HEVC
// fmp4 segment (ftyp + moov + start of first moof) and returns the same bytes
// with `oinf`, `linf`, and `trgr/cstg` injected into the moov, plus any bytes
// past the end of moov pass through verbatim.
//
// Returns the patched prefix, the number of input bytes consumed from buf,
// and whether a patch was applied.
//
// If the buffer doesn't yet contain a full moov (status == StatusNeedMore) the caller needs to keep buffering
// If the bytes turn out not to need patching (no MV-HEVC, or already complete) caller can stop buffering
func PatchHeadBytes(buf []byte) (out []byte, consumed int, status PatchStatus, err error) {
	moovEnd, ok := findMoovEnd(buf)
	if !ok {
		return nil, 0, StatusNeedMore, nil
	}

	head := buf[:moovEnd]
	patched, modified, err := patchMoovBytes(head)
	if err != nil {
		return nil, 0, StatusPassthrough, err
	}
	if !modified {
		return head, moovEnd, StatusPassthrough, nil
	}
	return patched, moovEnd, StatusPatched, nil
}

// findMoovEnd scans top-level boxes (ftyp, free, moov, ...) from the start
// of buf and returns the byte offset right after the moov box when it is
// fully present.
// If a moof or mdat appears before any moov, returns (0, false) since there is nothing to patch.
// Returns (0, false) if buf doesn't yet contain a complete moov.
func findMoovEnd(buf []byte) (int, bool) {
	pos := 0
	for pos+8 <= len(buf) {
		size := uint64(binary.BigEndian.Uint32(buf[pos:]))
		boxType := string(buf[pos+4 : pos+8])
		hdrLen := 8
		if size == 1 {
			if pos+16 > len(buf) {
				return 0, false
			}
			size = binary.BigEndian.Uint64(buf[pos+8:])
			hdrLen = 16
		} else if size == 0 {
			return 0, false
		}
		if size < uint64(hdrLen) {
			return 0, false
		}
		end := pos + int(size)
		if boxType == "moov" {
			if end > len(buf) {
				return 0, false
			}
			return end, true
		}
		if boxType == "moof" || boxType == "mdat" {
			// fragment data before any moov — nothing for us to patch
			return 0, false
		}
		if end > len(buf) {
			return 0, false
		}
		pos = end
	}
	return 0, false
}

// patchMoovBytes parses the head (ftyp + ... + moov) and injects MV-HEVC boxes
// if the video trak has a multi-layer VPS.
// Returns (newBytes, modified, err).
// When modified is false, newBytes is empty and the caller should emit the original head
// unchanged.
func patchMoovBytes(head []byte) (out []byte, modified bool, err error) {
	f, err := mp4.DecodeFile(bytes.NewReader(head))
	if err != nil {
		return nil, false, fmt.Errorf("decode head: %w", err)
	}
	if f.Moov == nil {
		return nil, false, nil
	}

	changed := false
	for _, trak := range f.Moov.Traks {
		if trak.Mdia == nil || trak.Mdia.Hdlr == nil || trak.Mdia.Hdlr.HandlerType != "vide" {
			continue
		}
		c, err := fixVideoTrak(trak, f)
		if err != nil {
			return nil, false, fmt.Errorf("trak %d: %w", trak.Tkhd.TrackID, err)
		}
		changed = changed || c
	}

	if !changed {
		return nil, false, nil
	}

	// We need to emit only ftyp+moov+anything that was before moov, which is
	// exactly what mp4ff already parsed into f.Children. Use EncModeBoxTree
	// so we render those top-level boxes in order. Any moof/mdat that appears
	// inside head will also be re-rendered verbatim.
	f.FragEncMode = mp4.EncModeBoxTree
	var w bytes.Buffer
	if err := f.Encode(&w); err != nil {
		return nil, false, fmt.Errorf("encode head: %w", err)
	}
	return w.Bytes(), true, nil
}

// StreamPatcher wraps a downstream io.Writer and applies PatchHeadBytes to
// the leading bytes of each segment that flows through it. After the moov is
// patched (or determined not to need patching), subsequent writes pass
// straight through.
//
// Not safe for concurrent Writes
type StreamPatcher struct {
	inner    io.Writer
	buf      bytes.Buffer
	settled  bool // moov has been handled (either patched or passthrough decided)
	maxBuf   int  // safety cap; once exceeded with no moov, flush as passthrough
	patchLog func(modified bool)
}

const defaultMaxHeadBuf = 1 << 20 // 1 MiB — moov for a single trak is typically a few KB

func NewStreamPatcher(inner io.Writer) *StreamPatcher {
	return &StreamPatcher{inner: inner, maxBuf: defaultMaxHeadBuf}
}

func (p *StreamPatcher) Write(buf []byte) (int, error) {
	if p.settled {
		return p.inner.Write(buf)
	}

	p.buf.Write(buf)
	patched, consumed, status, err := PatchHeadBytes(p.buf.Bytes())
	if err != nil {
		log.Warn("MV-HEVC stream patch failed; passing through",
			"error", err, "bufferedBytes", p.buf.Len())
		if _, werr := p.inner.Write(p.buf.Bytes()); werr != nil {
			return 0, werr
		}
		p.buf.Reset()
		p.settled = true
		return len(buf), nil
	}

	switch status {
	case StatusNeedMore:
		if p.buf.Len() > p.maxBuf {
			log.Warn("MV-HEVC stream patch buffer cap exceeded; passing through",
				"bufferedBytes", p.buf.Len(), "max", p.maxBuf)
			if _, werr := p.inner.Write(p.buf.Bytes()); werr != nil {
				return 0, werr
			}
			p.buf.Reset()
			p.settled = true
		}
		return len(buf), nil
	case StatusPatched:
		log.Info("MV-HEVC moov patched in stream",
			"originalHeadBytes", consumed, "patchedHeadBytes", len(patched))
		if _, werr := p.inner.Write(patched); werr != nil {
			return 0, werr
		}
		if consumed < p.buf.Len() {
			tail := p.buf.Bytes()[consumed:]
			if _, werr := p.inner.Write(tail); werr != nil {
				return 0, werr
			}
		}
		p.buf.Reset()
		p.settled = true
		return len(buf), nil
	case StatusPassthrough:
		if _, werr := p.inner.Write(p.buf.Bytes()); werr != nil {
			return 0, werr
		}
		p.buf.Reset()
		p.settled = true
		return len(buf), nil
	}
	return len(buf), nil
}

// Flush writes any buffered bytes to the downstream writer unchanged
func (p *StreamPatcher) Flush() error {
	if p.settled || p.buf.Len() == 0 {
		return nil
	}
	_, err := p.inner.Write(p.buf.Bytes())
	p.buf.Reset()
	p.settled = true
	return err
}
