package avpipe

import (
	"io"
	"reflect"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/eluv-io/avpipe/mp4e"
	"github.com/eluv-io/errors-go"
)

// extractMovInfoForProbe extracts the MP4 track description from input and
// seeks it back to 0 so the caller can re-read from the beginning. The caller
// owns the handle and is responsible for opening and closing it.
func extractMovInfoForProbe(input goavpipe.InputHandler) (*avdesc.MP4MovInfo, error) {
	const op = "avpipe.extractMovInfoForProbe"
	e := errors.Template(op, errors.K.Invalid.Default())
	mov, extractErr := mp4e.ExtractMovInfoLazy(input) // Only loading MP4 box headers
	if _, seekErr := input.Seek(0, io.SeekStart); seekErr != nil {
		if extractErr != nil {
			goavpipe.Log.Error("seek back failed after failed extraction", "extract_error", extractErr, "op", op)
		}
		return nil, e("reason", "seek back after pre-extraction", "error", seekErr)
	}
	return mov, extractErr
}

// enhanceStreamInfo adds what the MP4 box layer knows to what FFmpeg reported.
//
// A stream is paired with its track by identity: StreamInfo.StreamId is the
// container's own stream identifier, which for MP4 is the tkhd track ID that
// MP4TrackInfo.TrackID carries. The two lists do not necessarily hold the same
// tracks - mp4ff describes a track only for the codecs it decodes - so neither
// order nor length can be relied on to line them up.
func enhanceStreamInfo(streams []goavpipe.StreamInfo, mov *avdesc.MP4MovInfo) {
	if mov == nil {
		return
	}
	byTrackID := make(map[int]*avdesc.MP4TrackInfo, len(mov.Tracks))
	for _, track := range mov.Tracks {
		if track != nil {
			byTrackID[track.TrackID] = track
		}
	}

	for i := range streams {
		track, ok := byTrackID[streams[i].StreamId]
		if !ok {
			// Ordinary: a data track, or one whose sample entry the box parser
			// does not decode. The stream keeps its FFmpeg-derived fields.
			continue
		}
		info := track.CodecInfo

		if info.CodecTagString != "" {
			if streams[i].CodecTagString != "" && streams[i].CodecTagString != info.CodecTagString {
				goavpipe.Log.Warn("Probe codec tag differs from MP4 sample entry; using MP4 value",
					"stream_index", streams[i].StreamIndex,
					"track_id", track.TrackID,
					"probe_codec_tag_string", streams[i].CodecTagString,
					"codec_info_codec_tag_string", info.CodecTagString)
			}
			streams[i].CodecTagString = info.CodecTagString
		}

		warnDOVIMismatch(streams[i].StreamIndex, streams[i].DOVI, info.DOVI)

		streams[i].MP4 = &info
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
