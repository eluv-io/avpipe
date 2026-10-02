package avpipe

import (
	"io"
	"reflect"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/eluv-io/avpipe/mp4e"
	"github.com/eluv-io/errors-go"
)

// extractCodecInfoForProbe extracts MP4 codec info from input and seeks it back
// to 0 so the caller can re-read from the beginning. The caller owns the handle
// and is responsible for opening and closing it.
func extractCodecInfoForProbe(input goavpipe.InputHandler) ([]*mp4e.CodecInfo, error) {
	const op = "avpipe.extractCodecInfoForProbe"
	e := errors.Template(op, errors.K.Invalid.Default())
	infos, extractErr := mp4e.ExtractCodecInfoLazy(input) // Only loading MP4 box headers
	if _, seekErr := input.Seek(0, io.SeekStart); seekErr != nil {
		if extractErr != nil {
			goavpipe.Log.Error("seek back failed after failed extraction", "extract_error", extractErr, "op", op)
		}
		return nil, e("reason", "seek back after pre-extraction", "error", seekErr)
	}
	return infos, extractErr
}

func enhanceStreamInfo(streams []goavpipe.StreamInfo, codecInfos []*mp4e.CodecInfo) {
	codecInfoIdx := 0
	for i := range streams {
		if streams[i].CodecType != "audio" && streams[i].CodecType != "video" {
			continue
		}
		if codecInfoIdx >= len(codecInfos) {
			return
		}
		info := codecInfos[codecInfoIdx]
		codecInfoIdx++
		if info == nil {
			continue
		}

		if info.CodecTagString != "" {
			if streams[i].CodecTagString != "" && streams[i].CodecTagString != info.CodecTagString {
				goavpipe.Log.Warn("Probe codec tag differs from MP4 sample entry; using MP4 value",
					"stream_index", streams[i].StreamIndex,
					"probe_codec_tag_string", streams[i].CodecTagString,
					"codec_info_codec_tag_string", info.CodecTagString)
			}
			streams[i].CodecTagString = info.CodecTagString
		}

		warnDOVIMismatch(streams[i].StreamIndex, streams[i].DOVI, info.DOVI)

		streams[i].MP4 = convertMP4Info(info)
	}
}

func warnDOVIMismatch(streamIndex int, probeDOVI, mp4DOVI *avdesc.DOVIInfo) {
	if mp4DOVI != nil && probeDOVI == nil {
		goavpipe.Log.Warn("Probe DOVI mismatch: MP4 box has DOVI config but side data is absent",
			"stream_index", streamIndex,
			"mp4_dovi", mp4DOVI)
	} else if probeDOVI != nil && mp4DOVI == nil {
		goavpipe.Log.Warn("Probe DOVI mismatch: side data has DOVI config but MP4 box is absent",
			"stream_index", streamIndex,
			"probe_dovi", probeDOVI)
	} else if probeDOVI != nil { // && mp4DOVI != nil
		// Compare all fields except BoxType, which is empty in the probe/side-data
		// path and set only in the MP4 box path — that difference is by design.
		probe := *probeDOVI
		mp4 := *mp4DOVI
		probe.BoxType = ""
		mp4.BoxType = ""
		if !reflect.DeepEqual(probe, mp4) {
			goavpipe.Log.Warn("Probe DOVI mismatch between side data and MP4 box",
				"stream_index", streamIndex,
				"probe_dovi", probeDOVI,
				"mp4_dovi", mp4DOVI)
		}
	}
}

func convertMP4Info(info *mp4e.CodecInfo) *goavpipe.MP4Info {
	if info == nil {
		return nil
	}
	return &goavpipe.MP4Info{
		CodecTagString:        info.CodecTagString,
		MimeCodecString:       info.MimeCodecString,
		ProfileIDC:            info.ProfileIDC,
		Level:                 info.Level,
		Channels:              info.Channels,
		EC3:                   info.EC3,
		DOVI:                  info.DOVI,
		VideoLayout:           goavpipe.VideoLayout(info.VideoLayout),
		EnhancementProfileIDC: info.EnhancementProfileIDC,
	}
}
