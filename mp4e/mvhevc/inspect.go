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
				vpsNalus := vse.HvcC.DecConfRec.GetNalusForType(hevc.NALU_VPS)
				if len(vpsNalus) > 0 {
					if vps, err := hevc.ParseVPSNALUnit(vpsNalus[0]); err == nil {
						out.HasMultiLayerVPS = vps.IsMultiLayer()
					}
				}
			}
			if vse.LhvC != nil {
				out.HasLhvC = true
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
