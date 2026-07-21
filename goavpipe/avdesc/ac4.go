package avdesc

import (
	"fmt"
	"math/big"

	"github.com/eluv-io/avpipe/goavpipe/util"
)

// AC4Info holds AC-4 (ETSI TS 103 190-2) fields parsed from the MP4 dac4 box.
// The top-level fields describe the first (default) presentation — enough to
// identify the stream and build the RFC 6381 codec string. Presentations carries
// the fully-decoded per-presentation DSI for every presentation in the box.
//
// Whether the stream is Dolby Atmos / immersive / immersive-stereo is NOT stored
// as a field: those are *derived* from the raw data (see IsDolbyAtmos,
// IsImmersive, IsIMS) because the same bits mean different things at different
// presentation versions. In particular the trailing DSI bit is
// immersive_audio_indicator for presentation_version 1 (informative, set even for
// a channel-based 5.1.4 bed) and dolby_atmos_indicator for presentation_version 2
// (IMS) — the same physical bit, so it must be interpreted, not read as "Atmos".
type AC4Info struct {
	// AC4DSIVersion is the ac4_dsi_version of the dac4 box. avpipe only supports
	// version 1 (ac4_dsi_v1); parsing errors otherwise.
	AC4DSIVersion int `json:"ac4_dsi_version"`

	// BitstreamVersion is the ac4_dsi bitstream_version.
	BitstreamVersion int `json:"bitstream_version"`

	// PresentationVersion is the presentation_version of the first (default)
	// presentation.
	PresentationVersion int `json:"presentation_version"`

	// MDCompat is the metadata-compatibility field (mdcompat) of the first
	// presentation, used as the third component of the codec string.
	MDCompat int `json:"mdcompat"`

	// FSIndex is the dac4 fs_index: the base-sampling-frequency selector
	// (0 = 44.1 kHz, 1 = 48 kHz; ETSI TS 103 190-1 Table 77). This is the raw
	// field; the output rate (which also depends on the per-substream
	// dsi_sf_multiplier) is computed by SampleRate().
	FSIndex int `json:"fs_index"`

	// FrameRateIndex is the raw dac4 frame_rate_index (ETSI TS 103 190-1 Table 83).
	// It is the lossless frame-rate identity used to derive the AC-4 frame duration
	// for segmentation; FrameRate() turns it into the exact rate. No omitempty:
	// index 0 (23.976 fps) is valid and must survive the round-trip.
	FrameRateIndex int `json:"frame_rate_index"`

	// NPresentations is the number of presentations declared in the dac4 box.
	NPresentations int `json:"n_presentations,omitempty"`

	// PresentationChannelMask is presentation_channel_mask_v1, the 24-bit
	// speaker-group bit field (ETSI TS 103 190-2 §E.10.14) of the first
	// presentation. 0x800000 for object-only (non-channel-coded) presentations.
	PresentationChannelMask uint32 `json:"presentation_channel_mask,omitempty"`

	// ChMode is dsi_presentation_ch_mode of the first presentation. It is a
	// pointer so nil (omitted) means "not channel-coded / not signaled" (object
	// audio), distinct from a real *0 == Mono. See AC4Presentation.ChMode.
	ChMode *int `json:"ch_mode,omitempty"`

	// TopChannelPairs is pres_top_channel_pairs of the first presentation
	// (number of height-channel pairs; non-zero implies immersive height content).
	TopChannelPairs int `json:"top_channel_pairs,omitempty"`

	// Presentations holds the decoded DSI for every presentation in the dac4
	// box (len == NPresentations when all parse). Presentations[0] is the
	// default presentation the top-level fields above are derived from.
	Presentations []AC4Presentation `json:"presentations,omitempty"`
}

// AC4Presentation holds the fields decoded from one ac4_presentation_v1_dsi
// (ETSI TS 103 190-2 §E.10; presentation_version 2 is the Dolby Delivery Kit IMS
// extension, identical except the trailing bit is dolby_atmos_indicator). Every
// field here is *raw* bitstream data; interpreted questions ("is this Atmos?")
// are answered by the Is* / Ajoc methods, never stored.
//
// JSON note: a small identity core (version, config, mdcompat, channel_coded,
// ch_mode, channel_mask) is always emitted; every other field is omitempty, so an
// unset/default field is not serialized. Absence in JSON therefore means "default
// / not signaled", which keeps probe output readable.
type AC4Presentation struct {
	// Version is the presentation_version (1, or 2 for IMS).
	Version int `json:"version"`

	// Config is presentation_config_v1 (0x1f == single substream group).
	Config int `json:"config"`

	// MDCompat is the metadata-compatibility field.
	MDCompat int `json:"mdcompat"`

	// HasPresentationID is b_presentation_id.
	HasPresentationID bool `json:"has_presentation_id,omitempty"`
	// PresentationID is presentation_id (valid when HasPresentationID).
	PresentationID int `json:"presentation_id,omitempty"`

	// FrameRateMultiply is dsi_frame_rate_multiply_info.
	FrameRateMultiply int `json:"frame_rate_multiply,omitempty"`
	// FrameRateFraction is dsi_frame_rate_fraction_info.
	FrameRateFraction int `json:"frame_rate_fraction,omitempty"`
	// EMDFVersion is presentation_emdf_version.
	EMDFVersion int `json:"emdf_version,omitempty"`
	// KeyID is presentation_key_id.
	KeyID int `json:"key_id,omitempty"`

	// ChannelCoded is b_presentation_channel_coded (false for object-based).
	ChannelCoded bool `json:"channel_coded"`

	// ChMode is dsi_presentation_ch_mode. Pointer so that nil (omitted) means
	// "not channel-coded" (object audio) — distinct from a real *0 == Mono, which
	// a plain int + omitempty could not preserve.
	ChMode *int `json:"ch_mode,omitempty"`

	// BackChannelsPresent is pres_b_4_back_channels_present (only for ch_mode 11-14).
	BackChannelsPresent bool `json:"back_channels_present,omitempty"`

	// TopChannelPairs is pres_top_channel_pairs (height-channel pairs).
	TopChannelPairs int `json:"top_channel_pairs,omitempty"`

	// ChannelMask is presentation_channel_mask_v1 / presentation_v1_channel_groups,
	// the 24-bit speaker-group bit field (0x800000 for object-only presentations).
	ChannelMask uint32 `json:"channel_mask"`

	// CoreDiffers is b_presentation_core_differs.
	CoreDiffers bool `json:"core_differs,omitempty"`
	// CoreChannelCoded is b_presentation_core_channel_coded.
	CoreChannelCoded bool `json:"core_channel_coded,omitempty"`
	// CoreChMode is dsi_presentation_channel_mode_core (0/omitted when absent, i.e.
	// only meaningful when CoreChannelCoded).
	CoreChMode int `json:"core_ch_mode,omitempty"`

	// HasFilter is b_presentation_filter; EnablePresentation is b_enable_presentation.
	HasFilter          bool `json:"has_filter,omitempty"`
	EnablePresentation bool `json:"enable_presentation,omitempty"`

	// MultiPID is b_multi_pid (presentation spans more than one elementary stream).
	MultiPID bool `json:"multi_pid,omitempty"`

	// SubstreamGroups holds the decoded ac4_substream_group_dsi() entries. This is
	// where object coding lives: a group with ChannelCoded=false and a substream
	// with Ajoc=true is the reliable in-band A-JOC (non-IMS Atmos) signal.
	SubstreamGroups []AC4SubstreamGroup `json:"substream_groups,omitempty"`

	// PreVirtualized is b_pre_virtualized (headphone/stereo pre-rendering; set on
	// IMS presentations).
	PreVirtualized bool `json:"pre_virtualized,omitempty"`

	// HasBitrateInfo is b_presentation_bitrate_info; the following three are the
	// ac4_bitrate_dsi() values when present.
	HasBitrateInfo   bool   `json:"has_bitrate_info,omitempty"`
	BitrateMode      int    `json:"bitrate_mode,omitempty"`
	Bitrate          uint32 `json:"bitrate,omitempty"`
	BitratePrecision uint32 `json:"bitrate_precision,omitempty"`

	// Alternative is b_alternative; PresentationName and Targets are the
	// alternative_info() contents (the human-readable name and target-device tunings).
	Alternative      bool        `json:"alternative,omitempty"`
	PresentationName string      `json:"presentation_name,omitempty"`
	Targets          []AC4Target `json:"targets,omitempty"`

	// DEIndicator is de_indicator (dialogue enhancement available).
	DEIndicator bool `json:"de_indicator,omitempty"`

	// ImmersiveAudioIndicator is the raw byte-aligned bit immediately after
	// de_indicator. Its meaning is version-dependent: immersive_audio_indicator
	// for Version 1 (ETSI, informative — set for channel-based height beds too) and
	// dolby_atmos_indicator for Version 2 (Delivery Kit IMS). It is the SAME
	// physical bit. Do NOT read it as "Atmos" directly — use IsDolbyAtmos.
	ImmersiveAudioIndicator bool `json:"immersive_audio_indicator,omitempty"`

	// HasExtendedPresentationID is b_extended_presentation_id; ExtendedPresentationID
	// overrides PresentationID when present (presentation_id >= 32).
	HasExtendedPresentationID bool `json:"has_extended_presentation_id,omitempty"`
	ExtendedPresentationID    int  `json:"extended_presentation_id,omitempty"`
}

// AC4SubstreamGroup is one ac4_substream_group_dsi() (ETSI TS 103 190-2 §E.11).
// channel_coded is always emitted (the object-vs-channel discriminator); the rest
// is omitempty.
type AC4SubstreamGroup struct {
	SubstreamsPresent bool           `json:"substreams_present,omitempty"` // b_substreams_present
	HSFExt            bool           `json:"hsf_ext,omitempty"`            // b_hsf_ext
	ChannelCoded      bool           `json:"channel_coded"`                // b_channel_coded
	Substreams        []AC4Substream `json:"substreams,omitempty"`
	HasContentType    bool           `json:"has_content_type,omitempty"`   // b_content_type
	ContentClassifier int            `json:"content_classifier,omitempty"` // content_classifier
	Language          string         `json:"language,omitempty"`           // language_tag_bytes (UTF-8)
}

// AC4Substream is one substream entry inside an ac4_substream_group_dsi().
// Channel-coded substreams carry ChannelGroups; object-coded substreams carry the
// A-JOC / object fields. All fields are omitempty (a substream renders as only the
// fields relevant to its coding).
type AC4Substream struct {
	SFMultiplier int `json:"sf_multiplier,omitempty"` // dsi_sf_multiplier
	// BitrateIndicator is substream_bitrate_indicator. Pointer so nil (omitted)
	// means "absent" (b_substream_bitrate_indicator == 0), distinct from a real
	// *0 — there is no companion presence flag, so a plain int would lose that.
	BitrateIndicator *int `json:"bitrate_indicator,omitempty"`

	// Channel-coded (group.ChannelCoded == true):
	ChannelGroups uint32 `json:"channel_groups,omitempty"` // reserved(6)+dsi_substream_channel_groups(18)

	// Object-coded (group.ChannelCoded == false):
	Ajoc           bool `json:"ajoc,omitempty"`            // b_ajoc — A-JOC object coding
	StaticDmx      bool `json:"static_dmx,omitempty"`      // b_static_dmx
	NDmxObjects    int  `json:"n_dmx_objects,omitempty"`   // n_dmx_objects_minus1+1 (0 when static_dmx)
	NUmxObjects    int  `json:"n_umx_objects,omitempty"`   // n_umx_objects_minus1+1
	BedObjects     bool `json:"bed_objects,omitempty"`     // b_substream_contains_bed_objects
	DynamicObjects bool `json:"dynamic_objects,omitempty"` // b_substream_contains_dynamic_objects
	ISFObjects     bool `json:"isf_objects,omitempty"`     // b_substream_contains_ISF_objects
}

// AC4Target is one target-device entry from alternative_info().
type AC4Target struct {
	MDCompat       int `json:"md_compat"`       // target_md_compat
	DeviceCategory int `json:"device_category"` // target_device_category
}

// Ajoc reports whether the presentation carries A-JOC object audio: any
// object-coded substream group (b_channel_coded==0) with a substream whose
// b_ajoc is set. Per ETSI TS 103 190-2, (b_channel_coded==0 && b_ajoc==1) denotes
// A-JOC coded content — the reliable in-band signal for non-IMS Dolby Atmos.
func (p AC4Presentation) Ajoc() bool {
	for _, g := range p.SubstreamGroups {
		if g.ChannelCoded {
			continue
		}
		for _, s := range g.Substreams {
			if s.Ajoc {
				return true
			}
		}
	}
	return false
}

// IsDolbyAtmos reports whether the presentation carries Dolby Atmos. For an IMS
// presentation (Version 2) this is the dolby_atmos_indicator bit; otherwise it is
// A-JOC object coding. Channel-based immersive beds (e.g. 5.1.4) are NOT Atmos,
// even though their immersive_audio_indicator bit is set.
func (p AC4Presentation) IsDolbyAtmos() bool {
	if p.Version == 2 {
		return p.ImmersiveAudioIndicator // dolby_atmos_indicator for IMS
	}
	return p.Ajoc()
}

// IsImmersive reports whether the presentation carries immersive audio: the ETSI
// immersive_audio_indicator, A-JOC objects, height-channel pairs, or IMS
// pre-virtualization.
func (p AC4Presentation) IsImmersive() bool {
	return p.ImmersiveAudioIndicator || p.Ajoc() || p.TopChannelPairs > 0 || p.PreVirtualized
}

// ChannelLayout returns a human-readable speaker layout ("stereo", "5.1",
// "5.1.4", …) derived from the channel-coded DSI fields — the layout ffmpeg
// cannot report for AC-4 (it decodes channel_layout as "unknown"). It returns ""
// for object-based (A-JOC) presentations, which carry no fixed speaker layout.
//
// ChMode maps via ETSI TS 103 190-2 Table 56 (dsi_presentation_ch_mode). Modes
// 11-14 are immersive "container" modes whose actual layout is refined by
// BackChannelsPresent (Table 57: when false, the back channels Lb/Rb are silent,
// so a 7.x container is really 5.x) and TopChannelPairs (height channels =
// 2×pairs). The three 7.x variants (modes 5-10, differing only in speaker
// placement) all render as "7.0"/"7.1" by channel count; the exact variant is
// preserved in the raw ChMode.
func (p AC4Presentation) ChannelLayout() string {
	if !p.ChannelCoded || p.ChMode == nil {
		return "" // object-based: no fixed speaker layout
	}
	chMode := *p.ChMode
	switch chMode {
	case 0:
		return "mono"
	case 1:
		return "stereo"
	case 2:
		return "3.0"
	case 3:
		return "5.0"
	case 4:
		return "5.1"
	case 5, 7, 9:
		return "7.0"
	case 6, 8, 10:
		return "7.1"
	case 11, 12, 13, 14:
		floor := 9 // modes 13/14: 9.x base (wide channels)
		if chMode == 11 || chMode == 12 {
			// 7.x.4 container → 5.x when the back channels are silent.
			if p.BackChannelsPresent {
				floor = 7
			} else {
				floor = 5
			}
		}
		lfe := 0
		if chMode == 12 || chMode == 14 { // .1 (LFE-bearing) container modes
			lfe = 1
		}
		height := 2 * p.TopChannelPairs
		if height == 0 {
			return fmt.Sprintf("%d.%d", floor, lfe)
		}
		return fmt.Sprintf("%d.%d.%d", floor, lfe, height)
	case 15:
		return "22.2"
	}
	return "" // reserved / unknown ch_mode
}

// defaultPresentation returns the first (default) presentation, or a zero value.
func (a AC4Info) defaultPresentation() AC4Presentation {
	if len(a.Presentations) == 0 {
		return AC4Presentation{}
	}
	return a.Presentations[0]
}

// Ajoc reports whether the default presentation carries A-JOC object audio.
func (a AC4Info) Ajoc() bool { return a.defaultPresentation().Ajoc() }

// IsDolbyAtmos reports whether the default presentation carries Dolby Atmos.
func (a AC4Info) IsDolbyAtmos() bool { return a.defaultPresentation().IsDolbyAtmos() }

// IsImmersive reports whether the default presentation carries immersive audio.
func (a AC4Info) IsImmersive() bool { return a.defaultPresentation().IsImmersive() }

// IsIMS reports whether the stream is immersive stereo: a presentation_version 2
// (Delivery Kit IMS) default presentation.
func (a AC4Info) IsIMS() bool { return a.defaultPresentation().Version == 2 }

// ChannelLayout returns the default presentation's speaker layout (e.g. "5.1"),
// or "" for object-based audio. See AC4Presentation.ChannelLayout.
func (a AC4Info) ChannelLayout() string { return a.defaultPresentation().ChannelLayout() }

// SampleRate returns the output sampling frequency in Hz. It is NOT a raw dac4
// field: per ETSI TS 103 190-1 Table E.5d / TS 103 190-2 §G.2.2 the
// @audioSamplingRate is derived from BOTH fs_index and the per-substream
// dsi_sf_multiplier. At a 48 kHz base (fs_index=1) the default presentation's
// dsi_sf_multiplier 0/1/2 selects 48/96/192 kHz; a 44.1 kHz base (fs_index=0)
// has no multiplier. Returns 0 for an unrecognized combination.
func (a AC4Info) SampleRate() int {
	if a.FSIndex == 0 {
		return 44100 // 44.1 kHz base has no sampling-frequency multiplier
	}
	switch a.defaultPresentation().sfMultiplier() { // dsi_sf_multiplier, Table E.5d
	case 0:
		return 48000
	case 1:
		return 96000
	case 2:
		return 192000
	default:
		return 0 // 3 is reserved
	}
}

// FrameRate returns the exact AC-4 frame rate in fps as a rational, derived from
// FrameRateIndex (ETSI TS 103 190-1 Table 83). A rational rather than a float
// because the five NTSC rates have no exact float form (e.g. 23.976 = 24000/1001).
// Index 13 is special — its rate is base_sampling_frequency/2048, so it depends on
// FSIndex: 48000/2048 (= 375/16 = 23.4375) at a 48 kHz base, 44100/2048
// (= 11025/512 ≈ 21.53) at 44.1 kHz. Returns nil for a reserved/invalid index
// (14, 15). The returned value is a fresh copy the caller may mutate freely.
func (a AC4Info) FrameRate() *big.Rat {
	switch {
	case a.FrameRateIndex == 13:
		base := int64(48000)
		if a.FSIndex == 0 {
			base = 44100
		}
		return big.NewRat(base, 2048)
	case a.FrameRateIndex >= 0 && a.FrameRateIndex < len(ac4FrameRates):
		return new(big.Rat).Set(ac4FrameRates[a.FrameRateIndex]) // copy: big.Rat is mutable
	default:
		return nil
	}
}

// ac4FrameRates maps frame_rate_index 0-12 to the exact AC-4 frame rate in fps
// (ETSI TS 103 190-1 Table 83). The five NTSC rates (0, 3, 5, 8, 11) are exactly
// 1000/1001 of their integer neighbours. Index 13 is base-dependent (see
// FrameRate); 14-15 are reserved.
var ac4FrameRates = [13]*big.Rat{
	big.NewRat(24000, 1001),  // 0: 23.976
	big.NewRat(24, 1),        // 1
	big.NewRat(25, 1),        // 2
	big.NewRat(30000, 1001),  // 3: 29.97
	big.NewRat(30, 1),        // 4
	big.NewRat(48000, 1001),  // 5: 47.95
	big.NewRat(48, 1),        // 6
	big.NewRat(50, 1),        // 7
	big.NewRat(60000, 1001),  // 8: 59.94
	big.NewRat(60, 1),        // 9
	big.NewRat(100, 1),       // 10
	big.NewRat(120000, 1001), // 11: 119.88
	big.NewRat(120, 1),       // 12
}

// sfMultiplier returns the presentation's dsi_sf_multiplier, read from its first
// substream (the constant-configuration constraint means all substreams of a
// deliverable stream share it). Returns 0 (no multiplier) when the presentation
// carries no decoded substreams.
func (p AC4Presentation) sfMultiplier() int {
	for _, g := range p.SubstreamGroups {
		if len(g.Substreams) > 0 {
			return g.Substreams[0].SFMultiplier
		}
	}
	return 0
}

// MimeCodecString returns the RFC 6381 codec string for AC-4:
// "ac-4.<bitstream_version>.<presentation_version>.<mdcompat>", three
// dot-separated, zero-padded hexadecimal byte pairs (e.g. "ac-4.02.01.01").
// No object type indicator (OTI) is appended — it is undefined for AC-4.
func (a AC4Info) MimeCodecString() string {
	return fmt.Sprintf("ac-4.%02x.%02x.%02x",
		a.BitstreamVersion, a.PresentationVersion, a.MDCompat)
}

// String returns the AC4Info as a JSON string, satisfying fmt.Stringer.
func (a AC4Info) String() string { return util.JSONString(a) }
