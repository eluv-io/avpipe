package mp4e

import (
	"bytes"

	mp4bits "github.com/Eyevinn/mp4ff/bits"
	"github.com/eluv-io/avpipe/goavpipe/avdesc"
)

// parseAC4PresentationDSI decodes a single ac4_presentation_v1_dsi (ETSI TS
// 103 190-2 §E.10; presentation_version 2 is the Dolby Delivery Kit IMS extension)
// from a presentation's raw payload. version is AC4Presentation.PresentationVersion
// and data is AC4Presentation.PresentationData (which begins at
// presentation_config_v1). It returns the fully-decoded (public) AC4Presentation
// plus an ok flag: ok is true only when the parse stayed within the payload and hit
// no bitstream error — callers must treat the deep fields as valid only when ok is
// true (Version/Config/MDCompat, read from the first byte, are set regardless).
//
// The full raw content is captured; nothing structurally meaningful is discarded
// (only reserved/opaque byte regions — filter_data, add-EMDF substream ids — are
// consumed without being stored). Interpreted values (Atmos/immersive/IMS) are
// derived by the AC4Presentation methods, never stored here.
//
// The bit layout is ported from Bento4's AP4_Dac4Atom constructor
// (Source/C++/Core/Ap4Dac4Atom.cpp), matched by mp4dump, and cross-checked
// against ETSI TS 103 190-2 §E.10/§E.11/§E.12. Only presentation_version 1 and 2
// are decoded here; version gating/errors live in buildAC4Info.
func parseAC4PresentationDSI(version int, data []byte) (avdesc.AC4Presentation, bool) {
	p := avdesc.AC4Presentation{}
	p.Version = version
	if version != 1 && version != 2 {
		return p, false
	}
	if len(data) < 1 {
		return p, false
	}
	r := mp4bits.NewReader(bytes.NewReader(data))
	nbits := len(data) * 8

	p.Config = int(r.Read(5))
	bAddEmdfSubstreams := false
	if p.Config == 0x06 {
		bAddEmdfSubstreams = true
	} else {
		p.MDCompat = int(r.Read(3))
		if r.ReadFlag() { // b_presentation_id
			p.HasPresentationID = true
			p.PresentationID = int(r.Read(5))
		}
		p.FrameRateMultiply = int(r.Read(2)) // dsi_frame_rate_multiply_info
		p.FrameRateFraction = int(r.Read(2)) // dsi_frame_rate_fraction_info
		p.EMDFVersion = int(r.Read(5))       // presentation_emdf_version
		p.KeyID = int(r.Read(10))            // presentation_key_id
		p.ChannelCoded = r.ReadFlag()
		if p.ChannelCoded {
			chMode := int(r.Read(5))
			p.ChMode = &chMode
			if chMode >= 11 && chMode <= 14 {
				p.BackChannelsPresent = r.ReadFlag() // pres_b_4_back_channels_present
				p.TopChannelPairs = int(r.Read(2))
			}
			p.ChannelMask = uint32(r.Read(24)) // reserved(6)+presentation_v1_channel_groups(18)
		} else {
			p.ChannelMask = 0x800000
		}
		if r.ReadFlag() { // b_presentation_core_differs
			p.CoreDiffers = true
			if r.ReadFlag() { // b_presentation_core_channel_coded
				p.CoreChannelCoded = true
				p.CoreChMode = int(r.Read(2))
			}
		}
		if r.ReadFlag() { // b_presentation_filter
			p.HasFilter = true
			p.EnablePresentation = r.ReadFlag()
			nFilterBytes := r.Read(8)
			for i := uint(0); i < nFilterBytes; i++ {
				r.Read(8) // filter_data — reserved/opaque, not retained
			}
		}

		nSubstreamGroups := 0
		parseSubstreamGroups := true
		if p.Config == 0x1f {
			nSubstreamGroups = 1
		} else {
			p.MultiPID = r.ReadFlag() // b_multi_pid
			switch p.Config {
			case 0, 1, 2:
				nSubstreamGroups = 2
			case 3, 4:
				nSubstreamGroups = 3
			case 5:
				nSubstreamGroups = int(r.Read(3)) + 2
			default:
				parseSubstreamGroups = false
				nSkipBytes := r.Read(7)
				for i := uint(0); i < nSkipBytes; i++ {
					r.Read(8)
				}
			}
		}
		if parseSubstreamGroups {
			for g := 0; g < nSubstreamGroups; g++ {
				p.SubstreamGroups = append(p.SubstreamGroups, parseSubstreamGroupDSI(r))
			}
		}
		p.PreVirtualized = r.ReadFlag()
		bAddEmdfSubstreams = r.ReadFlag()
	}

	if bAddEmdfSubstreams {
		n := r.Read(7) // n_add_emdf_substreams
		for i := uint(0); i < n; i++ {
			r.Read(5)  // substream_emdf_version — reserved/opaque, not retained
			r.Read(10) // substream_key_id — reserved/opaque, not retained
		}
	}
	if r.ReadFlag() { // b_presentation_bitrate_info
		p.HasBitrateInfo = true
		p.BitrateMode = int(r.Read(2))
		p.Bitrate = uint32(r.Read(32))
		p.BitratePrecision = uint32(r.Read(32))
	}
	if r.ReadFlag() { // b_alternative
		p.Alternative = true
		byteAlign(r)
		nameLen := r.Read(16)
		name := make([]byte, 0, nameLen)
		for i := uint(0); i < nameLen; i++ {
			name = append(name, byte(r.Read(8)))
		}
		p.PresentationName = string(name)
		nTargets := r.Read(5)
		for i := uint(0); i < nTargets; i++ {
			p.Targets = append(p.Targets, avdesc.AC4Target{
				MDCompat:       int(r.Read(3)), // target_md_compat
				DeviceCategory: int(r.Read(8)), // target_device_category
			})
		}
	}
	byteAlign(r)
	// The trailing block is guarded in ETSI by
	// if (bits_read() <= (pres_bytes - 1) * 8) — it may be absent when the payload
	// ends early. len(data) is pres_bytes for this presentation.
	if r.NrBitsRead() <= (len(data)-1)*8 {
		p.DEIndicator = r.ReadFlag()             // de_indicator
		p.ImmersiveAudioIndicator = r.ReadFlag() // immersive_audio_indicator (pv1) / dolby_atmos_indicator (pv2)
		r.Read(4)                                // reserved
		if r.ReadFlag() {                        // b_extended_presentation_id
			p.HasExtendedPresentationID = true
			p.ExtendedPresentationID = int(r.Read(9))
		} else {
			r.Read(1) // reserved
		}
	}

	// A trailing bitstream error, or reading past the presentation payload, means
	// the layout diverged from what we parsed — report the deep fields as
	// untrustworthy. Version/Config/MDCompat (read from the first byte) are still
	// returned.
	ok := r.AccError() == nil && r.NrBitsRead() <= nbits
	return p, ok
}

// parseSubstreamGroupDSI decodes one ac4_substream_group_dsi (ETSI TS 103 190-2
// §E.11) at the reader's current position.
func parseSubstreamGroupDSI(r *mp4bits.Reader) avdesc.AC4SubstreamGroup {
	g := avdesc.AC4SubstreamGroup{}
	g.SubstreamsPresent = r.ReadFlag()
	g.HSFExt = r.ReadFlag()
	g.ChannelCoded = r.ReadFlag()
	nSubstreams := int(r.Read(8))
	for s := 0; s < nSubstreams; s++ {
		sub := avdesc.AC4Substream{}
		sub.SFMultiplier = int(r.Read(2))
		if r.ReadFlag() { // b_substream_bitrate_indicator
			bi := int(r.Read(5))
			sub.BitrateIndicator = &bi
		}
		if g.ChannelCoded {
			sub.ChannelGroups = uint32(r.Read(24)) // reserved(6)+dsi_substream_channel_groups(18)
		} else {
			sub.Ajoc = r.ReadFlag()
			if sub.Ajoc {
				sub.StaticDmx = r.ReadFlag()
				if !sub.StaticDmx {
					sub.NDmxObjects = int(r.Read(4)) + 1 // n_dmx_objects_minus1
				}
				sub.NUmxObjects = int(r.Read(6)) + 1 // n_umx_objects_minus1
			}
			sub.BedObjects = r.ReadFlag()
			sub.DynamicObjects = r.ReadFlag()
			sub.ISFObjects = r.ReadFlag()
			r.Read(1) // reserved
		}
		g.Substreams = append(g.Substreams, sub)
	}
	if r.ReadFlag() { // b_content_type
		g.HasContentType = true
		g.ContentClassifier = int(r.Read(3))
		if r.ReadFlag() { // b_language_indicator
			nLang := r.Read(6)
			lang := make([]byte, 0, nLang)
			for l := uint(0); l < nLang; l++ {
				lang = append(lang, byte(r.Read(8)))
			}
			g.Language = string(lang)
		}
	}
	return g
}

// byteAlign advances r to the next byte boundary relative to the start of the
// presentation payload (which is itself byte-aligned within the dac4 box).
func byteAlign(r *mp4bits.Reader) {
	if rem := r.NrBitsRead() % 8; rem != 0 {
		r.Read(8 - rem)
	}
}
