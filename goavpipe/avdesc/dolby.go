package avdesc

import "fmt"

// DOVIInfo holds the Dolby Vision configuration from a dvcC/dvvC box or probe
// side data (AV_PKT_DATA_DOVI_CONF).
type DOVIInfo struct {
	// VersionMajor is the major version of the Dolby Vision specification used
	// to encode the content (e.g. 1 for Dolby Vision specification version 1.x).
	VersionMajor int `json:"version_major"`

	// VersionMinor is the minor version of the Dolby Vision specification.
	VersionMinor int `json:"version_minor"`

	// Profile is the Dolby Vision profile number (e.g. 5 = single-layer HDR,
	// 8 = single-layer cross-compatible with HDR10/HLG base layer).
	Profile int `json:"profile"`

	// Level specifies the maximum content parameters (resolution, frame rate)
	// allowed by this Dolby Vision stream; e.g. 6 = up to 1080p60, 9 = up to 4K60.
	Level int `json:"level"`

	// RPUPresent indicates that this stream carries a Reference Processing Unit
	// (RPU), which holds the per-frame Dolby Vision metadata.
	RPUPresent bool `json:"rpu_present"`

	// ELPresent indicates that this stream contains an Enhancement Layer (EL),
	// present in dual-layer profiles (e.g. Profile 7) but not in single-layer
	// profiles (e.g. Profile 5 or 8).
	ELPresent bool `json:"el_present"`

	// BLPresent indicates that this stream contains a Base Layer (BL).
	// False only in a standalone EL-only track.
	BLPresent bool `json:"bl_present"`

	// BLSignalCompatibilityID describes the HDR signaling of the base layer,
	// allowing non-DV decoders to render the stream without Dolby Vision support.
	// 0 = no backward compatibility (Dolby Vision only), 1 = HDR10, 4 = HLG.
	BLSignalCompatibilityID int `json:"bl_signal_compatibility_id"`

	// FourCC is the Dolby Vision sample entry FourCC derived from the enclosing
	// HEVC codec tag: "hvc1" → "dvh1", "hev1" → "dvhe". Empty when DOVIInfo
	// originates from a probe (AV_PKT_DATA_DOVI_CONF) rather than mp4e parsing.
	FourCC string `json:"fourcc,omitempty"`
}

// CodecString returns the Dolby Vision codec string, e.g. "dvh1.08.01",
// using FourCC. Returns "" if FourCC is empty (e.g. DOVIInfo from a probe).
func (d DOVIInfo) CodecString() string {
	if d.FourCC == "" {
		return ""
	}
	return fmt.Sprintf("%s.%02d.%02d", d.FourCC, d.Profile, d.Level)
}
