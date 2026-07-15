package mvhevc

import (
	"fmt"
	"io"
	"math"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/eluv-io/avpipe/mp4e"
)

// Fix restores MV-HEVC sample-group and track-group boxes that FFmpeg's
// muxer drops when repackaging in bypass mode. The dropped boxes (oinf, linf,
// and trgr/cstg) can be reconstructed from VPS, which is expected to be present in the input.
func Fix(inputPath, outputPath string) error {
	log.Info("fixing MV-HEVC boxes", "input", inputPath, "output", outputPath)

	in, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("could not open input: %w", err)
	}
	defer func() { _ = in.Close() }()

	if err := ensureDifferentFiles(in, outputPath); err != nil {
		return err
	}

	// DecModeLazyMdat parses the box tree but seeks over media payloads. This
	// keeps memory use proportional to metadata size, rather than file size.
	parsedMP4, err := mp4.DecodeFile(in, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return fmt.Errorf("decode MP4: %w", err)
	}
	if parsedMP4.Moov == nil {
		return fmt.Errorf("no moov box")
	}
	originalMoovSize := parsedMP4.Moov.Size()

	changed := false
	for _, trak := range parsedMP4.Moov.Traks {
		if trak.Mdia == nil || trak.Mdia.Hdlr == nil || trak.Mdia.Hdlr.HandlerType != "vide" {
			continue
		}
		c, err := fixVideoTrak(trak, parsedMP4)
		if err != nil {
			return fmt.Errorf("trak %d: %w", trak.Tkhd.TrackID, err)
		}
		changed = changed || c
	}

	if !changed {
		log.Info("no missing MV-HEVC boxes; copying input through unchanged")
	}
	if err := adjustChunkOffsetsForMoovResize(parsedMP4, originalMoovSize); err != nil {
		return fmt.Errorf("adjust chunk offsets: %w", err)
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("could not create output: %w", err)
	}
	defer func() { _ = out.Close() }()

	// A lazy mdat's Encode method emits only its header. Walk the original
	// top-level box order and stream every lazy payload from the input file.
	if err := encodeFileWithLazyMdat(parsedMP4, in, out); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	log.Info("wrote MV-HEVC MP4", "output", outputPath)
	return nil
}

func ensureDifferentFiles(input *os.File, outputPath string) error {
	inputInfo, err := input.Stat()
	if err != nil {
		return fmt.Errorf("stat input: %w", err)
	}
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat output: %w", err)
	}
	if os.SameFile(inputInfo, outputInfo) {
		return fmt.Errorf("input and output must be different files")
	}
	return nil
}

// encodeFileWithLazyMdat preserves the decoded top-level box sequence. Normal
// boxes are encoded from the parsed tree; lazy mdat payloads are copied from
// their original absolute offsets without being materialized in memory.
func encodeFileWithLazyMdat(file *mp4.File, input io.ReadSeeker, output io.Writer) error {
	for i, box := range file.Children {
		mdat, lazy := box.(*mp4.MdatBox)
		if !lazy || !mdat.IsLazy() {
			if err := box.Encode(output); err != nil {
				return fmt.Errorf("top-level box %d (%s): %w", i, box.Type(), err)
			}
			continue
		}

		payloadOffset := mdat.PayloadAbsoluteOffset()
		payloadSize := mdat.GetLazyDataSize()
		if payloadOffset > math.MaxInt64 || payloadSize > math.MaxInt64 {
			return fmt.Errorf("top-level box %d (mdat): payload range exceeds int64", i)
		}
		if err := mdat.Encode(output); err != nil {
			return fmt.Errorf("top-level box %d (mdat) header: %w", i, err)
		}
		written, err := mdat.CopyData(int64(payloadOffset), int64(payloadSize), input, output)
		if err != nil {
			return fmt.Errorf("top-level box %d (mdat) payload: %w", i, err)
		}
		if uint64(written) != payloadSize {
			return fmt.Errorf("top-level box %d (mdat) payload: copied %d of %d bytes", i, written, payloadSize)
		}
	}
	return nil
}

// adjustChunkOffsetsForMoovResize handles progressive fast-start files, where
// moov precedes mdat. Re-encoding a larger repaired moov shifts all later media
// chunks, whose stco/co64 values are absolute file offsets. Files with mdat
// before moov (the usual AVAssetWriter layout) need no offset changes.
func adjustChunkOffsetsForMoovResize(file *mp4.File, originalMoovSize uint64) error {
	if file.Moov == nil || file.Mdat == nil || file.Moov.StartPos >= file.Mdat.StartPos {
		return nil
	}

	newMoovSize := file.Moov.Size()
	if newMoovSize == originalMoovSize {
		return nil
	}
	if file.Moov.StartPos > math.MaxUint64-originalMoovSize {
		return fmt.Errorf("original moov end overflows uint64")
	}
	originalMoovEnd := file.Moov.StartPos + originalMoovSize

	for _, trak := range file.Moov.Traks {
		if trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
			continue
		}
		stbl := trak.Mdia.Minf.Stbl
		if stbl.Stco != nil {
			for i, offset := range stbl.Stco.ChunkOffset {
				if uint64(offset) < originalMoovEnd {
					continue
				}
				adjusted, err := resizeOffset(uint64(offset), originalMoovSize, newMoovSize)
				if err != nil {
					return fmt.Errorf("trak %d stco[%d]: %w", trak.Tkhd.TrackID, i, err)
				}
				if adjusted > math.MaxUint32 {
					return fmt.Errorf("trak %d stco[%d]: adjusted offset %d exceeds uint32; co64 conversion required",
						trak.Tkhd.TrackID, i, adjusted)
				}
				stbl.Stco.ChunkOffset[i] = uint32(adjusted)
			}
		}
		if stbl.Co64 != nil {
			for i, offset := range stbl.Co64.ChunkOffset {
				if offset < originalMoovEnd {
					continue
				}
				adjusted, err := resizeOffset(offset, originalMoovSize, newMoovSize)
				if err != nil {
					return fmt.Errorf("trak %d co64[%d]: %w", trak.Tkhd.TrackID, i, err)
				}
				stbl.Co64.ChunkOffset[i] = adjusted
			}
		}
	}
	return nil
}

func resizeOffset(offset, oldSize, newSize uint64) (uint64, error) {
	if newSize >= oldSize {
		growth := newSize - oldSize
		if offset > math.MaxUint64-growth {
			return 0, fmt.Errorf("offset overflow")
		}
		return offset + growth, nil
	}

	shrink := oldSize - newSize
	if offset < shrink {
		return 0, fmt.Errorf("offset underflow")
	}
	return offset - shrink, nil
}

func fixVideoTrak(trak *mp4.TrakBox, _ *mp4.File) (bool, error) {
	stbl := trak.Mdia.Minf.Stbl
	if stbl == nil || stbl.Stsd == nil {
		return false, nil
	}

	var vse *mp4.VisualSampleEntryBox
	for _, c := range stbl.Stsd.Children {
		if v, ok := c.(*mp4.VisualSampleEntryBox); ok && v.HvcC != nil {
			vse = v
			break
		}
	}
	if vse == nil {
		return false, nil
	}

	vpsNalus := vse.HvcC.DecConfRec.GetNalusForType(hevc.NALU_VPS)
	if len(vpsNalus) == 0 {
		return false, nil
	}
	vps, err := hevc.ParseVPSNALUnit(vpsNalus[0])
	if err != nil {
		return false, fmt.Errorf("parse VPS: %w", err)
	}
	if !vps.IsMultiLayer() {
		return false, nil
	}

	changed := false
	if ensureColrFromSPSVUI(vse, trak.Tkhd.TrackID) {
		changed = true
	}
	if fixDVBoxType(vse, trak.Tkhd.TrackID) {
		changed = true
	}
	if !hasSgpd(stbl, "oinf") {
		appendStblSgpd(stbl, "oinf", mp4.BuildOinfFromVPS(vps))
		log.Info("added oinf sgpd", "trackID", trak.Tkhd.TrackID)
		changed = true
	}
	if !hasSgpd(stbl, "linf") {
		maxTids := make([]byte, vps.GetNumLayers())
		appendStblSgpd(stbl, "linf", mp4.BuildLinfFromVPS(vps, maxTids))
		log.Info("added linf sgpd", "trackID", trak.Tkhd.TrackID)
		changed = true
	}
	if trak.Trgr == nil {
		trgr := &mp4.TrgrBox{}
		trgr.AddChild(mp4.CreateTrackGroupTypeBox("cstg", 1001))
		trak.AddChild(trgr)
		log.Info("added trgr/cstg", "trackID", trak.Tkhd.TrackID)
		changed = true
	}
	return changed, nil
}

func ensureColrFromSPSVUI(vse *mp4.VisualSampleEntryBox, trackID uint32) bool {
	spsNalus := vse.HvcC.DecConfRec.GetNalusForType(hevc.NALU_SPS)
	if len(spsNalus) == 0 {
		log.Info("not adding colr; hvcC has no SPS", "trackID", trackID)
		return false
	}

	sps, err := hevc.ParseSPSNALUnit(spsNalus[0])
	if err != nil {
		log.Warn("could not parse SPS for colr repair", "trackID", trackID, "err", err)
		return false
	}
	if sps.VUI == nil || !sps.VUI.ColourDescriptionFlag {
		log.Info("not adding colr; SPS has no VUI colour description", "trackID", trackID)
		return false
	}

	vui := sps.VUI
	colr := findColr(vse)
	if colr != nil && colr.ColorType == mp4.ColorTypeOnScreenColors &&
		colr.ColorPrimaries == uint16(vui.ColourPrimaries) &&
		colr.TransferCharacteristics == uint16(vui.TransferCharacteristics) &&
		colr.MatrixCoefficients == uint16(vui.MatrixCoefficients) &&
		colr.FullRangeFlag == vui.VideoFullRangeFlag {
		return false
	}

	action := "updated colr from SPS VUI"
	if colr == nil {
		action = "added colr from SPS VUI"
		colr = &mp4.ColrBox{}
		vse.AddChild(colr)
	}
	colr.ColorType = mp4.ColorTypeOnScreenColors
	colr.ICCProfile = nil
	colr.ColorPrimaries = uint16(vui.ColourPrimaries)
	colr.TransferCharacteristics = uint16(vui.TransferCharacteristics)
	colr.MatrixCoefficients = uint16(vui.MatrixCoefficients)
	colr.FullRangeFlag = vui.VideoFullRangeFlag
	colr.UnknownPayload = nil

	log.Info(action,
		"trackID", trackID,
		"type", mp4.ColorTypeOnScreenColors,
		"primaries", vui.ColourPrimaries,
		"transfer", vui.TransferCharacteristics,
		"matrix", vui.MatrixCoefficients,
		"fullRange", vui.VideoFullRangeFlag)
	return true
}

func findColr(vse *mp4.VisualSampleEntryBox) *mp4.ColrBox {
	for _, c := range vse.Children {
		if colr, ok := c.(*mp4.ColrBox); ok {
			return colr
		}
	}
	return nil
}

func hasSgpd(stbl *mp4.StblBox, groupingType string) bool {
	for _, c := range stbl.Children {
		if sgpd, ok := c.(*mp4.SgpdBox); ok && sgpd.GroupingType == groupingType {
			return true
		}
	}
	return false
}

func appendStblSgpd(stbl *mp4.StblBox, groupingType string, entry mp4.SampleGroupEntry) {
	stbl.AddChild(&mp4.SgpdBox{
		Version:            2,
		GroupingType:       groupingType,
		DefaultLength:      uint32(entry.Size()),
		SampleGroupEntries: []mp4.SampleGroupEntry{entry},
	})
}

// fixDVBoxType corrects the DV configuration box FourCC for any child of vse
// that carries a DOVIDecoderConfigurationRecord (dvcC, dvvC, or dvwC).
//
// Per Dolby Vision ISOBMFF spec v2.7.1:
//
//	dvcC — profiles ≤ 7 and profile 20
//	dvvC — profiles 8–10
//	dvwC — reserved for future use
//
// FFmpeg uses dvwC for all profiles > 10 (a bug for profile 20), and content
// tools sometimes mislabel the box. This function re-checks every DV config
// box and renames it to the spec-correct FourCC if it differs.
func fixDVBoxType(vse *mp4.VisualSampleEntryBox, trackID uint32) bool {
	const op = "mvhevc.fixDVBoxType"

	changed := false
	for i, c := range vse.Children {
		if !mp4e.IsDOVIBoxType(c.Type()) {
			continue
		}
		// If mp4ff adds support for parsing DOVI, revisit this code
		ub, ok := c.(*mp4.UnknownBox)
		if !ok {
			log.Warn("DV config box has unexpected Go type, skipping",
				"boxType", c.Type(), "trackID", trackID, "op", op)
			continue
		}
		dovi, err := mp4e.ParseDOVIBox(ub.Payload())
		if err != nil {
			log.Warn("failed to parse DV config box payload — skipping",
				"boxType", c.Type(), "trackID", trackID, "error", err, "op", op)
			continue
		}
		profile := dovi.Profile

		var want string
		switch {
		case profile <= 7 || profile == 20:
			want = "dvcC"
		case profile <= 10: // && profile >= 8
			want = "dvvC"
		default:
			want = "dvwC" // genuinely future profile — leave as-is
		}

		if c.Type() == want {
			continue
		}
		const boxHdrSize = 8
		payload := ub.Payload()
		vse.Children[i] = mp4.CreateUnknownBox(want, uint64(boxHdrSize+len(payload)), payload)
		log.Info("corrected DV config box", "from", c.Type(), "to", want, "trackID", trackID, "profile", profile, "op", op)
		changed = true
	}
	return changed
}
