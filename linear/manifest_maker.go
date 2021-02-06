package linear

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qluvio/legacy_imf_dash_extract/abr"

	elog "github.com/qluvio/content-fabric/log"
)

var log = elog.Get("/eluvio/avpipe/linear")

const urlBase = "../../../../../../.."
const hlsDisc = "\n#EXT-X-DISCONTINUITY\n"

type Mode int

const (
	Vod Mode = iota
	Live
)

type ManifestMaker struct {
	Format        string
	LiveOfferings map[string]*OfferingInfo
	Offerings     map[string]*abr.Offering
	Query         url.Values
	MediaSequence MediaSequence
	Mode          Mode
}

type OfferingInfo struct {
	StartPts       int64
	EndPts         int64
	AudioTimescale int64
	VideoTimescale int64
	SegDurationTs  int64
}

func NewManifestMaker(mode Mode) *ManifestMaker {
	return &ManifestMaker{
		Format:        "hls-clear",
		LiveOfferings: make(map[string]*OfferingInfo),
		Offerings:     make(map[string]*abr.Offering),
		Query:         url.Values{},
		MediaSequence: MediaSequence{},
		Mode:          mode,
	}
}

func (mm *ManifestMaker) MakeMasterPlaylist(offering string) string {
	fileName := "vod.m3u8"
	if mm.Mode == Live {
		fileName = "live.m3u8"
	}
	videoOnly := mm.Query.Get("video_only") != ""
	mm.Query.Del("video_only")
	audioOnly := mm.Query.Get("audio_only") != ""
	mm.Query.Del("audio_only")
	query := mm.makeQueryParams()

	codecAudio := "mp4a.40.2"
	codecVideo1080 := "avc1.640032" // profile 'high' level 5
	codecVideo720 := "avc1.64002A"  // profile 'high' level 4.2

	if audioOnly {

		return `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=128000,CODECS="` + codecAudio + `"
audio/stereo@128000/` + fileName + query + "\n"
	}

	if offering != "flag" {
		if videoOnly {
			return `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=9400000,CODECS="` + codecVideo1080 + `",RESOLUTION=1920x1080
video/1080@9400000/` + fileName + query + "\n"
		}
		return `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=9400000,CODECS="` + codecVideo1080 + `,` + codecAudio + `",RESOLUTION=1920x1080,AUDIO="audio"
video/1080@9400000/` + fileName + query +
			"\n#EXT-X-MEDIA:NAME=\"audio_2ch_eng\",GROUP-ID=\"audio\",TYPE=AUDIO,CHANNELS=\"2\",URI=\"audio/stereo@128000/" + fileName + query + "\"\n"
	} else {
		if videoOnly {
			return `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=13900000,CODECS="` + codecVideo720 + `",RESOLUTION=1280x720
video/720@13900000/` + fileName + query + "\n"
		}
		return `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=13900000,CODECS="` + codecVideo720 + `,` + codecAudio + `",RESOLUTION=1280x720,AUDIO="audio"
video/720@13900000/` + fileName + query +
			"\n#EXT-X-MEDIA:NAME=\"audio_2ch_eng\",GROUP-ID=\"audio\",TYPE=AUDIO,CHANNELS=\"2\",URI=\"audio/stereo@128000/" + fileName + query + "\"\n"
	}
}

func (mm *ManifestMaker) MakeMediaPlaylist(streamId string, repId string, liveRepId string) (
	playlist string, err error) {

	if mm.Mode == Live {
		return mm.makeLiveMediaPlaylist(streamId, repId, liveRepId)
	} else {
		return mm.makeVodMediaPlaylist(streamId, repId, liveRepId)
	}
}

func (mm *ManifestMaker) makeLiveMediaPlaylist(streamId string, repId string, liveRepId string) (
	playlist string, err error) {

	var sb strings.Builder
	sb.WriteString(`#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-DISCONTINUITY-SEQUENCE:0
#EXT-X-TARGETDURATION:3

`)
	s := mm.Query.Get("requested_start_time_epoch_sec")
	mm.Query.Del("requested_start_time_epoch_sec") // assume caller no longer needs query
	startTime, _ := strconv.ParseInt(s, 10, 64)
	remainingSec := float32(time.Now().Unix() - startTime)

	for i, slice := range mm.MediaSequence.Slices {
		if remainingSec <= 0 {
			break
		}
		var spl string
		if slice.Type == LivePlayable {
			spl, remainingSec, err = mm.makeSlicePlaylistForLivePlayable(*slice, streamId, liveRepId, remainingSec)
		} else {
			spl, remainingSec, err = mm.makeSlicePlaylistForVodPlayable(*slice, streamId, repId, remainingSec)
		}
		if err != nil {
			return sb.String(), err
		}
		if i > 0 {
			sb.WriteString(hlsDisc)
		}
		sb.WriteString(spl)
		sb.WriteString("\n")
	}
	if remainingSec > 0 {
		sb.WriteString("#EXT-X-ENDLIST\n")
	}
	playlist = sb.String()
	return
}

func (mm *ManifestMaker) makeVodMediaPlaylist(streamId string, repId string, liveRepId string) (
	playlist string, err error) {

	var sb strings.Builder
	sb.WriteString(`#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-DISCONTINUITY-SEQUENCE:0
#EXT-X-TARGETDURATION:3
#EXT-X-PLAYLIST-TYPE:VOD

`)
	for i, slice := range mm.MediaSequence.Slices {
		var spl string
		if slice.Type == LivePlayable {
			spl, _, err = mm.makeSlicePlaylistForLivePlayable(*slice, streamId, liveRepId, math.MaxFloat32)
		} else {
			spl, _, err = mm.makeSlicePlaylistForVodPlayable(*slice, streamId, repId, math.MaxFloat32)
		}
		if err != nil {
			return sb.String(), err
		}
		if i > 0 {
			sb.WriteString(hlsDisc)
		}
		sb.WriteString(spl)
		sb.WriteString("\n")
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	playlist = sb.String()
	return
}

// #EXT-X-MAP:URI="https://host-38-142-50-110.contentfabric.io/q/hq__EhiwJsbi995znknYRErciysv75tEwXizGRjS5DzCNSxoRMgU11H5QSbrqZMhJEM6bebpHfqAwx/rep/playout/default/hls-clear/video/720@13900000/init.m4s?authorization=eyJxc3BhY2VfaWQiOiJpc3BjM0FOb1ZTek5BM1A2dDdhYkxSNjlobzVZUFBaVSIsImFkZHIiOiIweGVhM2Y3YmZkMTkyOWFlYTU1MWI0NThhZDQzNjA5NDNkZDRiNTJhYzkiLCJxbGliX2lkIjoiaWxpYjM4UmJTNzlraE5pUHRZVTk2UVRQOVNqWVI2eXYifQ%3D%3D.RVMyNTZLX050RGhlZXNYdnBQR1lrZWdkVFJoZFNyN1djTG9aS3JXWW9Rc1pNTTZ6MThEMTVGQ1VoMndBcDNjTTdpYUZUNWhrVmhENlhDZ3RlTmtlWnlXcVlrY3Nlb1dB"
// #EXT-X-MAP:URI="../../hq_xx/rep/playout/default/hls-clear/video/720@13900000/init.m4s?authorization=eyJxx"
func (mm *ManifestMaker) makeInit(slice MediaSlice, streamId string, repId string) string {
	url := mm.buildLiveUrl(slice, streamId, repId)
	query := mm.makeQueryParams()
	s := fmt.Sprintf("#EXT-X-MAP:URI=\"%s/init.m4s%s\"\n", url, query)
	return s
}

// http://localhost:8008/qlibs/ilib4WQZhWEPmRT8rq5DLSJxBMnRhBAk/q/hq__8h2jP7xPvcqygHegTbc6Atwmw5UHrXH47XfWrfG6eE3nntuctTt4BeajFm7v1SoaKs33fNLevR/rep/live/default/hls-clear/video/720@8000000/00064.m4s
// ../../hq__xx/rep/playout/default/hls-clear/video/720@8000000
func (mm *ManifestMaker) buildLiveUrl(slice MediaSlice, streamId string, repId string) string {
	return fmt.Sprintf("%s/%s/rep/live/%s/%s/%s/%s",
		urlBase, slice.QHash, slice.Offering, mm.Format, streamId, repId)
}

func (mm *ManifestMaker) makeQueryParams() string {
	s := ""
	if len(mm.Query) > 0 {
		s = "?" + mm.Query.Encode()
	}
	return s
}

// segCount is the number of segments to write
// returns the number of segments remaining
func (mm *ManifestMaker) makeSlicePlaylistForLivePlayable(
	slice MediaSlice, streamId string, repId string, remainingSec float32) (string, float32, error) {

	query := mm.makeQueryParams()
	base := mm.buildLiveUrl(slice, streamId, repId)
	offering := mm.LiveOfferings[slice.QHash]
	if offering == nil {
		return "", remainingSec, errors.New("offering not found for qhash " + slice.QHash)
	}
	adj := TsAdjuster{
		audioTimescale: offering.AudioTimescale,
		isAudio:        strings.Contains(strings.ToLower(streamId), "audio"),
		videoTimescale: offering.VideoTimescale,
	}
	segTs := adj.calc(offering.SegDurationTs)
	duration := adj.calc(slice.DurationTs)
	startPts := adj.calc(slice.StartPts)
	if duration <= 0 {
		duration = adj.calc(offering.EndPts - slice.StartPts)
	}
	if duration <= segTs {
		return "", remainingSec, errors.New("duration must be greater than segment length")
	}

	var sb strings.Builder
	sb.WriteString(mm.makeInit(slice, streamId, repId))
	t := adj.calc(offering.StartPts)
	seg := 1
	for {
		partial := ""
		currentSegTs := segTs
		if t < startPts {
			t += segTs
			if t <= startPts {
				seg++
				continue
			}
			currentSegTs = t - startPts
			partial = fmt.Sprintf("-%d-%d", segTs-currentSegTs, segTs)
		} else {
			t += segTs
			if t > startPts+duration {
				currentSegTs = startPts + duration + segTs - t
				partial = fmt.Sprintf("-0-%d", currentSegTs)
			}
		}
		currentSegSec := float32(currentSegTs) / float32(adj.timescale())
		sb.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n%s/%05d%s.m4s%s\n",
			currentSegSec, base, seg, partial, query))
		if t >= startPts+duration {
			break
		}
		seg++

		remainingSec = remainingSec - currentSegSec
		if remainingSec <= 0 {
			break
		}
	}

	return sb.String(), remainingSec, nil
}

func (mm *ManifestMaker) makeSlicePlaylistForVodPlayable(
	slice MediaSlice, streamId string, repId string, remainingSec float32) (
	playlist string, remainingSecOut float32, err error) {

	licenseServers := make(map[string][]*url.URL, 0)
	encOverrides := abr.NewEncryptionOverrides()
	remainingSecOut = remainingSec
	isAudio := strings.Contains(strings.ToLower(streamId), "audio")

	offering := mm.Offerings[slice.QHash]
	if offering == nil {
		err = errors.New("offering not found for qhash " + slice.QHash)
		return
	}

	vs, err := offering.MediaStruct.VideoStreamFirst()
	if err != nil {
		return
	}
	ts, err := vs.Timescale()
	if err != nil {
		return
	}
	videoTs := int64(ts)
	timescale := videoTs
	audioTs := videoTs
	if isAudio {
		as, er := offering.MediaStruct.AudioStreamFirst()
		if er != nil {
			return
		}
		ts, er = as.Timescale()
		if er != nil {
			return
		}
		audioTs = int64(ts)
		timescale = audioTs
	}

	// adjust ts if audio
	sliceStart := big.NewRat(slice.StartPts*audioTs, timescale*videoTs)
	var sliceEnd *big.Rat
	if slice.DurationTs == -1 {
		sliceEnd = big.NewRat((slice.StartPts+vs.Duration.Ts())*audioTs, timescale*videoTs)
	} else {
		sliceEnd = big.NewRat((slice.StartPts+slice.DurationTs)*audioTs, timescale*videoTs)
	}
	sliceDuration := big.NewRat(0, 1)
	sliceDuration.Sub(sliceEnd, sliceStart)
	sliceDurationF, _ := sliceDuration.Float32()
	var sliceTruncate *big.Rat
	if sliceDurationF > remainingSec {
		remainingSecRat := big.NewRat(0, 1)
		remainingSecRat.SetFloat64(float64(remainingSec))
		sliceTruncate = big.NewRat(0, 1)
		sliceTruncate.Add(sliceStart, remainingSecRat)
		remainingSecOut = 0
	} else {
		remainingSecOut = remainingSec - sliceDurationF
	}

	playlist, err = offering.GetPlaylistSlice(
		sliceStart,
		sliceEnd,
		sliceTruncate,
		slice.Offering,
		mm.Format,
		streamId,
		repId,
		slice.QHash,
		licenseServers,
		encOverrides,
		mm.Query,
	)
	if err != nil {
		log.Error("failed get slice playlist", err, "qhash", slice.QHash, "sliceStart", sliceStart, "sliceEnd", sliceEnd)
	}
	//playlist = regexp.MustCompile("videovideo_[[:digit:]]*x").ReplaceAllString(playlist, "")
	return
}

type TsAdjuster struct {
	audioTimescale int64
	isAudio        bool
	videoTimescale int64
}

func (t TsAdjuster) calc(ts int64) int64 {
	if t.isAudio {
		return ts * t.audioTimescale / t.videoTimescale
	}
	return ts
}

func (t TsAdjuster) timescale() int64 {
	if t.isAudio {
		return t.audioTimescale
	} else {
		return t.videoTimescale
	}
}
