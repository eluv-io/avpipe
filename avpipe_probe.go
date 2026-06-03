package avpipe

import (
	"fmt"
	"io"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/eluv-io/avpipe/mp4e"
)

// extractCodecInfoForProbe opens the input for url, extracts MP4 codec info, seeks back
// to 0, and returns both the codec infos and the open handle. The caller owns the handle.
func extractCodecInfoForProbe(url string) ([]*mp4e.CodecInfo, goavpipe.InputHandler, error) {
	inputOpener := goavpipe.GetInputOpener(url)
	if inputOpener == nil {
		return nil, nil, fmt.Errorf("input opener not set")
	}

	input, err := inputOpener.Open(goavpipe.Globals.GetNextFD(), url)
	if err != nil {
		return nil, nil, fmt.Errorf("open input: %w", err)
	}

	infos, err := mp4e.ExtractCodecInfo(input)
	if err != nil {
		_ = input.Close()
		return nil, nil, err
	}

	if _, seekErr := input.Seek(0, io.SeekStart); seekErr != nil {
		_ = input.Close()
		return nil, nil, fmt.Errorf("seek back after pre-extraction: %w", seekErr)
	}

	return infos, input, nil
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

		streams[i].Mp4Info = convertMp4Info(info)
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
		if probeDOVI.Profile != mp4DOVI.Profile ||
			probeDOVI.Level != mp4DOVI.Level ||
			probeDOVI.BLSignalCompatibilityID != mp4DOVI.BLSignalCompatibilityID ||
			probeDOVI.RPUPresent != mp4DOVI.RPUPresent ||
			probeDOVI.ELPresent != mp4DOVI.ELPresent ||
			probeDOVI.BLPresent != mp4DOVI.BLPresent {
			goavpipe.Log.Warn("Probe DOVI mismatch between side data and MP4 box",
				"stream_index", streamIndex,
				"probe_dovi", probeDOVI,
				"mp4_dovi", mp4DOVI)
		}
	}
}

func convertMp4Info(info *mp4e.CodecInfo) *goavpipe.Mp4Info {
	if info == nil {
		return nil
	}
	return &goavpipe.Mp4Info{
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

