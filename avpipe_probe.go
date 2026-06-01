package avpipe

import (
	"fmt"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/mp4e"
)

func extractCodecInfoForProbe(url string) ([]*mp4e.CodecInfo, error) {
	inputOpener := goavpipe.GetInputOpener(url)
	if inputOpener == nil {
		return nil, fmt.Errorf("input opener not set")
	}

	input, err := inputOpener.Open(goavpipe.Globals.GetNextFD(), url)
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}
	defer func() {
		if err := input.Close(); err != nil {
			goavpipe.Log.Warn("Probe codec info enhancement failed to close input", "url", url, "error", err)
		}
	}()

	return mp4e.ExtractCodecInfo(input)
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

		streams[i].Mp4Info = convertMp4Info(info)
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
		EC3:                   convertEC3Info(info.EC3),
		VideoLayout:           goavpipe.VideoLayout(info.VideoLayout),
		EnhancementProfileIDC: info.EnhancementProfileIDC,
	}
}

func convertEC3Info(info *mp4e.EC3Info) *goavpipe.EC3Info {
	if info == nil {
		return nil
	}
	return &goavpipe.EC3Info{
		ChanMap:         info.ChanMap,
		JOC:             info.JOC,
		ComplexityIndex: info.ComplexityIndex,
	}
}
