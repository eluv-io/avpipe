package mvhevc

import (
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

// Inspection summarizes MP4 items relevant to MV-HEVC
type Inspection struct {
	HasVideoTrak     bool
	Width, Height    uint16
	HasHvcC          bool
	HasLhvC          bool
	HasMultiLayerVPS bool
	HasOinfSgpd      bool
	HasLinfSgpd      bool
	HasTrgrCstg      bool

	// HDR-relevant fields, populated when the visual sample entry / hvcC / SPS
	// supplies them. Zero values are returned for streams that omit them.
	BitDepthLuma            byte // luma bit depth from hvcC (8, 10, 12, ...)
	BitDepthChroma          byte // chroma bit depth from hvcC
	ColourPrimaries         byte // SPS VUI: 9 = BT.2020
	TransferCharacteristics byte // SPS VUI: 16 = SMPTE 2084 (PQ), 18 = HLG
	MatrixCoefficients      byte // SPS VUI: 9 = BT.2020-NCL
	HasMdcv                 bool // visual sample entry has 'mdcv' (mastering display)
	HasClli                 bool // visual sample entry has 'clli' (content light level)
}

// Inspect opens an MP4 file or fmp4 init segment and reports which
// MV-HEVC structural boxes are present in the moov.
func Inspect(inputPath string) (*Inspection, error) {
	f, err := os.Open(inputPath)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	parsedMP4, err := mp4.DecodeFile(f, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if parsedMP4.Moov == nil {
		return &Inspection{}, nil
	}

	var trak *mp4.TrakBox
	for _, t := range parsedMP4.Moov.Traks {
		if t.Mdia != nil && t.Mdia.Hdlr != nil && t.Mdia.Hdlr.HandlerType == "vide" {
			trak = t
			break
		}
	}
	if trak == nil {
		return &Inspection{}, nil
	}

	out := &Inspection{HasVideoTrak: true}

	stbl := trak.Mdia.Minf.Stbl
	if stbl != nil && stbl.Stsd != nil {
		for _, c := range stbl.Stsd.Children {
			vse, ok := c.(*mp4.VisualSampleEntryBox)
			if !ok {
				continue
			}
			out.Width = vse.Width
			out.Height = vse.Height
			if vse.HvcC != nil {
				out.HasHvcC = true
				out.BitDepthLuma = vse.HvcC.DecConfRec.BitDepthLumaMinus8 + 8
				out.BitDepthChroma = vse.HvcC.DecConfRec.BitDepthChromaMinus8 + 8
				vpsNalus := vse.HvcC.DecConfRec.GetNalusForType(hevc.NALU_VPS)
				if len(vpsNalus) > 0 {
					if vps, err := hevc.ParseVPSNALUnit(vpsNalus[0]); err == nil {
						out.HasMultiLayerVPS = vps.IsMultiLayer()
					}
				}
				// VUI color triplet from the base-layer SPS. Used to distinguish HDR10 from SDR.
				spsNalus := vse.HvcC.DecConfRec.GetNalusForType(hevc.NALU_SPS)
				if len(spsNalus) > 0 {
					if sps, err := hevc.ParseSPSNALUnit(spsNalus[0]); err == nil && sps.VUI != nil {
						out.ColourPrimaries = sps.VUI.ColourPrimaries
						out.TransferCharacteristics = sps.VUI.TransferCharacteristics
						out.MatrixCoefficients = sps.VUI.MatrixCoefficients
					}
				}
			}
			if vse.LhvC != nil {
				out.HasLhvC = true
			}
			if vse.Mdcv != nil {
				out.HasMdcv = true
			}
			if vse.Clli != nil {
				out.HasClli = true
			}
			break
		}
		for _, c := range stbl.Children {
			if sgpd, ok := c.(*mp4.SgpdBox); ok {
				switch sgpd.GroupingType {
				case "oinf":
					out.HasOinfSgpd = true
				case "linf":
					out.HasLinfSgpd = true
				}
			}
		}
	}

	if trak.Trgr != nil {
		for _, c := range trak.Trgr.Children {
			if tg, ok := c.(*mp4.TrackGroupTypeBox); ok && tg.Type() == "cstg" {
				out.HasTrgrCstg = true
				break
			}
		}
	}

	return out, nil
}
