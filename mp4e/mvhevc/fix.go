package mvhevc

import (
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

// Fix restores MV-HEVC sample-group and track-group boxes that FFmpeg's
// muxer drops when repackaging in bypass mode. The dropped boxes (oinf, linf,
// and trgr/cstg) can be reconstructed from VPS which is expected to be present in the input.
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
	changed := false
	for i, c := range vse.Children {
		if c.Type() != "dvcC" && c.Type() != "dvvC" && c.Type() != "dvwC" {
			continue
		}
		ub, ok := c.(*mp4.UnknownBox)
		if !ok {
			continue
		}
		payload := ub.Payload()
		if len(payload) < 4 {
			continue
		}
		// dv_profile occupies bits 15-9 of the big-endian uint16 at bytes 2-3
		// (matches the DOVIDecoderConfigurationRecord layout in parseDOVIBox).
		word := uint16(payload[2])<<8 | uint16(payload[3])
		profile := int((word >> 9) & 0x7f)

		var want string
		switch {
		case profile <= 7 || profile == 20:
			want = "dvcC"
		case profile >= 8 && profile <= 10:
			want = "dvvC"
		default:
			want = "dvwC" // genuinely future profile — leave as-is
		}

		if c.Type() == want {
			continue
		}
		const boxHdrSize = 8
		vse.Children[i] = mp4.CreateUnknownBox(want, uint64(boxHdrSize+len(payload)), payload)
		log.Info("corrected DV config box", "from", c.Type(), "to", want, "trackID", trackID, "profile", profile)
		changed = true
	}
	return changed
}
