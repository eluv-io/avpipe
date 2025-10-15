package avpipe

// #cgo pkg-config: libavcodec
// #cgo pkg-config: libavfilter
// #cgo pkg-config: libavformat
// #cgo pkg-config: libavutil
// #cgo pkg-config: libswresample
// #cgo pkg-config: libavresample
// #cgo pkg-config: libavdevice
// #cgo pkg-config: libswscale
// #cgo pkg-config: libavutil
// #cgo pkg-config: libpostproc
// #cgo pkg-config: libcbor
// #cgo LDFLAGS: -lcbor
// #cgo netint pkg-config: xcoder
// #cgo pkg-config: srt
// #cgo CFLAGS: -I${SRCDIR}/libavpipe/include
// #cgo CFLAGS: -I${SRCDIR}/utils/include
// #cgo CFLAGS: -g
// #cgo LDFLAGS: -L${SRCDIR}
// #cgo linux LDFLAGS: -Wl,-rpath,$ORIGIN/../lib

// #include <string.h>
// #include <stdlib.h>
// #include "avpipe_xc.h"
// #include "avpipe.h"
// #include "elv_log.h"
import "C"
import (
	"fmt"
	"math/big"

	"github.com/fxamacker/cbor/v2"
)

// AVRational represents a rational number (fraction) with numerator and denominator
type AVRational struct {
	Num uint32 `cbor:"num" json:"num"`
	Den uint32 `cbor:"den" json:"den"`
}

// ToBigRat converts AVRational to *big.Rat
func (r *AVRational) ToBigRat() *big.Rat {
	if r.Den == 0 {
		return big.NewRat(0, 1)
	}
	return big.NewRat(int64(r.Num), int64(r.Den))
}

// SideDataCBOR represents stream side data from CBOR
type SideDataCBOR struct {
	DisplayMatrix SideDataDisplayMatrix `cbor:"display_matrix" json:"display_matrix"`
}

// StreamInfoCBOR represents the CBOR-serialized version of stream information
// This matches the structure packed by pack_stream_probes_to_cbor() in C
type StreamInfoCBOR struct {
	StreamIndex        uint32       `cbor:"stream_index" json:"stream_index"`
	StreamId           uint32       `cbor:"stream_id" json:"stream_id"`
	CodecType          uint32       `cbor:"codec_type" json:"codec_type"`
	CodecID            uint32       `cbor:"codec_id" json:"codec_id"`
	CodecName          string       `cbor:"codec_name" json:"codec_name"`
	DurationTs         uint64       `cbor:"duration_ts" json:"duration_ts"`
	TimeBase           AVRational   `cbor:"time_base" json:"time_base"`
	NBFrames           uint64       `cbor:"nb_frames" json:"nb_frames"`
	StartTime          uint64       `cbor:"start_time" json:"start_time"`
	AvgFrameRate       AVRational   `cbor:"avg_frame_rate" json:"avg_frame_rate"`
	FrameRate          AVRational   `cbor:"frame_rate" json:"frame_rate"`
	SampleRate         uint32       `cbor:"sample_rate" json:"sample_rate"`
	Channels           uint32       `cbor:"channels" json:"channels"`
	ChannelLayout      uint32       `cbor:"channel_layout" json:"channel_layout"`
	TicksPerFrame      uint32       `cbor:"ticks_per_frame" json:"ticks_per_frame"`
	BitRate            uint64       `cbor:"bit_rate" json:"bit_rate"`
	HasBFrames         uint32       `cbor:"has_b_frames" json:"has_b_frames"`
	Width              uint32       `cbor:"width" json:"width"`
	Height             uint32       `cbor:"height" json:"height"`
	PixFmt             uint32       `cbor:"pix_fmt" json:"pix_fmt"`
	SampleAspectRatio  AVRational   `cbor:"sample_aspect_ratio" json:"sample_aspect_ratio"`
	DisplayAspectRatio AVRational   `cbor:"display_aspect_ratio" json:"display_aspect_ratio"`
	FieldOrder         uint32       `cbor:"field_order" json:"field_order"`
	Profile            uint32       `cbor:"profile" json:"profile"`
	Level              uint32       `cbor:"level" json:"level"`
	SideData           SideDataCBOR `cbor:"side_data" json:"side_data"`
}

// ToStreamInfo converts StreamInfoCBOR to StreamInfo struct
func (s *StreamInfoCBOR) ToStreamInfo() StreamInfo {
	sideData := make([]interface{}, 0)

	// Add display matrix if it has data
	if s.SideData.DisplayMatrix.Rotation != 0 || s.SideData.DisplayMatrix.RotationCw != 0 {
		sideData = append(sideData, SideDataDisplayMatrix{
			Type:       "display_matrix",
			Rotation:   s.SideData.DisplayMatrix.Rotation,
			RotationCw: s.SideData.DisplayMatrix.RotationCw,
		})
	}

	return StreamInfo{
		StreamIndex:        int(s.StreamIndex),
		StreamId:           int32(s.StreamId),
		CodecType:          getCodecTypeName(s.CodecType),
		CodecID:            int(s.CodecID),
		CodecName:          s.CodecName,
		DurationTs:         int64(s.DurationTs),
		TimeBase:           s.TimeBase.ToBigRat(),
		NBFrames:           int64(s.NBFrames),
		StartTime:          int64(s.StartTime),
		AvgFrameRate:       s.AvgFrameRate.ToBigRat(),
		FrameRate:          s.FrameRate.ToBigRat(),
		SampleRate:         int(s.SampleRate),
		Channels:           int(s.Channels),
		ChannelLayout:      int(s.ChannelLayout),
		TicksPerFrame:      int(s.TicksPerFrame),
		BitRate:            int64(s.BitRate),
		Has_B_Frames:       s.HasBFrames != 0,
		Width:              int(s.Width),
		Height:             int(s.Height),
		PixFmt:             int(s.PixFmt),
		SampleAspectRatio:  s.SampleAspectRatio.ToBigRat(),
		DisplayAspectRatio: s.DisplayAspectRatio.ToBigRat(),
		FieldOrder:         getFieldOrderName(s.FieldOrder),
		Profile:            int(s.Profile),
		Level:              int(s.Level),
		SideData:           sideData,
		Tags:               make(map[string]string), // Tags are not packed in CBOR
	}
}

// UnpackStreamProbesFromCBOR unpacks a CBOR-encoded byte array back into stream information
// This is the counterpart to pack_stream_probes_to_cbor() in C
func UnpackStreamProbesFromCBOR(cborData []byte) ([]StreamInfo, error) {
	if len(cborData) == 0 {
		return nil, fmt.Errorf("empty CBOR data")
	}

	// The CBOR data is packed as an array of stream info maps
	var streamsCBOR []StreamInfoCBOR

	// Unmarshal the CBOR data
	if err := cbor.Unmarshal(cborData, &streamsCBOR); err != nil {
		return nil, fmt.Errorf("failed to unmarshal CBOR data: %w", err)
	}

	// Convert to StreamInfo structs
	streams := make([]StreamInfo, len(streamsCBOR))
	for i, streamCBOR := range streamsCBOR {
		streams[i] = streamCBOR.ToStreamInfo()
	}

	return streams, nil
}

// getCodecTypeName maps codec type integer to string name
// Based on AVMediaType enum from FFmpeg
func getCodecTypeName(codecType uint32) string {
	switch codecType {
	case 0:
		return "unknown"
	case 1:
		return "video"
	case 2:
		return "audio"
	case 3:
		return "data"
	case 4:
		return "subtitle"
	case 5:
		return "attachment"
	default:
		return fmt.Sprintf("unknown_%d", codecType)
	}
}

// getFieldOrderName maps field order integer to string name
// Based on AVFieldOrder enum from FFmpeg
func getFieldOrderName(fieldOrder uint32) string {
	switch fieldOrder {
	case 0:
		return "unknown"
	case 1:
		return "progressive"
	case 2:
		return "tt" // top coded_first, top displayed first
	case 3:
		return "bb" // bottom coded first, bottom displayed first
	case 4:
		return "tb" // top coded first, bottom displayed first
	case 5:
		return "bt" // bottom coded first, top displayed first
	default:
		return fmt.Sprintf("unknown_%d", fieldOrder)
	}
}

// UnpackStreamProbesFromCBORToProbeInfo is a convenience function that creates a ProbeInfo
// struct with the unpacked stream information (container info will be empty)
func UnpackStreamProbesFromCBORToProbeInfo(cborData []byte) (*ProbeInfo, error) {
	streams, err := UnpackStreamProbesFromCBOR(cborData)
	if err != nil {
		return nil, err
	}

	return &ProbeInfo{
		ContainerInfo: ContainerInfo{
			Duration:   0,  // Not available in CBOR data
			FormatName: "", // Not available in CBOR data
		},
		StreamInfo: streams,
	}, nil
}
