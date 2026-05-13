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
