package mvhevc

import (
	"bytes"
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

// Fix restores MV-HEVC sample-group and track-group boxes that FFmpeg's
// muxer drops when repackaging in bypass mode. The dropped boxes (oinf, linf,
// and trgr/cstg) can be reconstructed from VPS, which is expected to be present in the input.
//
// Only fmp4 input.
func Fix(inputPath, outputPath string) error {
	log.Info("fixing MV-HEVC boxes", "input", inputPath, "output", outputPath)

	in, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("could not open input: %w", err)
	}
	defer func() { _ = in.Close() }()

	parsedMP4, err := mp4.DecodeFile(in)
	if err != nil {
		return fmt.Errorf("decode MP4: %w", err)
	}
	if parsedMP4.Moov == nil {
		return fmt.Errorf("no moov box")
	}

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

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("could not create output: %w", err)
	}
	defer func() { _ = out.Close() }()

	// EncModeBoxTree preserves the raw top-level box sequence (ftyp, moov,
	// moof, mdat, moof, mdat, ...).
	// The moov is re-rendered with the new size.
	// The moof/mdat children are untouched, so internal offsets remain valid.
	parsedMP4.FragEncMode = mp4.EncModeBoxTree

	if err := parsedMP4.Encode(out); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	log.Info("wrote MV-HEVC MP4", "output", outputPath)
	return nil
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
		oinf, err := mp4.BuildOinfFromVPS(vps)
		if err != nil {
			return false, fmt.Errorf("build oinf from VPS: %w", err)
		}
		appendStblSgpd(stbl, "oinf", oinf)
		log.Info("added oinf sgpd", "trackID", trak.Tkhd.TrackID)
		changed = true
	}
	if !hasSgpd(stbl, "linf") {
		maxTids := make([]byte, vps.GetNumLayers())
		linf, err := mp4.BuildLinfFromVPS(vps, maxTids)
		if err != nil {
			return false, fmt.Errorf("build linf from VPS: %w", err)
		}
		appendStblSgpd(stbl, "linf", linf)
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
//
// mp4ff (>= v0.53.0) decodes dvcC/dvvC/dvwC into a typed DoViConfigurationBox
// that preserves the on-disk FourCC (its Type() returns the name it was decoded
// with), so a mislabeled box survives a round-trip unchanged. mp4ff's own
// profile→FourCC mapping also differs from the Dolby spec for profile 20
// (it emits dvwC, we want dvcC), so we re-wrap the encoded record under the
// spec-correct FourCC rather than reconstructing the box via mp4ff helpers.
func fixDVBoxType(vse *mp4.VisualSampleEntryBox, trackID uint32) bool {
	const op = "mvhevc.fixDVBoxType"
	const boxHdrSize = 8

	changed := false
	for i, c := range vse.Children {
		dcfg, ok := c.(*mp4.DoViConfigurationBox)
		if !ok {
			continue
		}
		profile := int(dcfg.DVProfile)

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

		// Re-encode the 24-byte DV configuration record and re-wrap it under the
		// spec-correct FourCC.
		var buf bytes.Buffer
		if err := dcfg.Encode(&buf); err != nil {
			log.Warn("failed to encode DV config box — skipping",
				"boxType", c.Type(), "trackID", trackID, "error", err, "op", op)
			continue
		}
		body := buf.Bytes()
		if len(body) <= boxHdrSize {
			log.Warn("DV config box unexpectedly short — skipping",
				"boxType", c.Type(), "trackID", trackID, "size", len(body), "op", op)
			continue
		}
		payload := body[boxHdrSize:]
		vse.Children[i] = mp4.CreateUnknownBox(want, uint64(boxHdrSize+len(payload)), payload)
		log.Info("corrected DV config box", "from", c.Type(), "to", want, "trackID", trackID, "profile", profile, "op", op)
		changed = true
	}
	return changed
}
