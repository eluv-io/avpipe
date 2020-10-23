package linear

import (
	"encoding/json"
	"io/ioutil"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qluvio/legacy_imf_dash_extract/abr"
	"github.com/qluvio/legacy_imf_dash_extract/conv_utils"
	"github.com/stretchr/testify/require"
)

// TODO test various cases to test partials (and that they are used only when needed)
// TODO handle duration < segDuration

func TestManifestMaker_makeSlicePlaylist(t *testing.T) {
	mm := ConstructManifestMaker(t, Vod, "flag")
	slice := MediaSlice{
		QHash:      "hq__4BKNw8XMejsabzixBo8C3eThtXPSyzZavNtKuv5pTkEeG5qkQq2dRgHU3hb3gQqx6zKGyzeJJk",
		Offering:   "default",
		StartPts:   0,
		DurationTs: 8571278765 - 8553957321,
		Type:       LivePlayable,
	}
	playlist, remainingSec, err := mm.makeSlicePlaylistForLivePlayable(
		slice, "video", "720@8000000", math.MaxFloat32)
	require.NoError(t, err)
	require.NotZero(t, len(playlist))
	require.Greater(t, remainingSec, float32(0))
	//println(playlist, remainingSec)
}

func TestManifestMaker_makeSlicePlaylistOpenEnd(t *testing.T) {
	mm := ConstructManifestMaker(t, Vod, "flag")
	slice := MediaSlice{
		QHash:      "hq__4BKNw8XMejsabzixBo8C3eThtXPSyzZavNtKuv5pTkEeG5qkQq2dRgHU3hb3gQqx6zKGyzeJJk",
		Offering:   "default",
		StartPts:   28660839,
		DurationTs: -1,
		Type:       LivePlayable,
	}
	playlist, remainingSec, err := mm.makeSlicePlaylistForLivePlayable(
		slice, "video", "720@8000000", math.MaxFloat32)
	require.NoError(t, err)
	require.NotZero(t, len(playlist))
	require.Greater(t, remainingSec, float32(0))
	//println(playlist, remainingSec)
}

func TestMockSequence(t *testing.T) {
	streamId := "video"
	licenseServers := make(map[string][]*url.URL, 0)
	encOverrides := abr.NewEncryptionOverrides()
	mm := ConstructManifestMaker(t, Vod, "flag")

	var sb strings.Builder
	for _, slice := range mm.MediaSequence.Slices {
		if slice.Type == LivePlayable {
			s, _, err := mm.makeSlicePlaylistForLivePlayable(*slice, streamId, "720@8000000", math.MaxFloat32)
			require.NoError(t, err)
			if len(s) > 0 {
				sb.WriteString(s)
				sb.WriteString(hlsDisc)
			}
			continue
		}

		offering := mm.Offerings[slice.QHash]
		require.NotNil(t, offering)
		// TODO this conversion is not safe
		sliceStart, _ := conv_utils.NewRatFromInts(int(slice.StartPts), 90000)
		sliceEnd, _ := conv_utils.NewRatFromInts(int(slice.StartPts+slice.DurationTs), 90000)
		playlist, err := offering.GetPlaylistSlice(
			sliceStart,
			sliceEnd,
			nil,
			slice.Offering,
			mm.Format,
			streamId,
			"video_1920x1080@9500000", // resolution ladder rung ID
			slice.QHash,
			licenseServers,
			encOverrides,
			mm.Query,
		)
		require.NoError(t, err)
		sb.WriteString(playlist)
		sb.WriteString(hlsDisc)
	}
	//fmt.Println(sb.String())
}

func TestManifestMaker_MakeVodMediaPlaylist(t *testing.T) {
	mm := ConstructManifestMaker(t, Vod, "flag")
	playlist, err := mm.MakeMediaPlaylist("video", "video_1280x720@4500000", "720@8000000")
	require.NoError(t, err)
	require.NotZero(t, len(playlist))
	//fmt.Println(playlist)
}

func TestManifestMaker_MakeMasterPlaylistVideoOnlyVod(t *testing.T) {
	mm := ConstructManifestMaker(t, Vod, "flag")
	mm.Query = url.Values{}
	mm.Query.Add("video_only", "1")
	playlist := mm.MakeMasterPlaylist("flag")
	require.NotZero(t, len(playlist))
	//fmt.Println(playlist)
}

func TestLive(t *testing.T) {
	mm := ConstructManifestMaker(t, Live, "flag")
	startTime := time.Now().Unix() - 220
	mm.Query.Add("requested_start_time_epoch_sec", strconv.FormatInt(startTime, 10))

	err := os.MkdirAll("./test_out", 0755)
	require.NoError(t, err)

	playlist := mm.MakeMasterPlaylist("flag")
	require.NotZero(t, len(playlist))
	err = ioutil.WriteFile("./test_out/live-master.m3u8", []byte(playlist), 0644)
	require.NoError(t, err)

	playlist, err = mm.MakeMediaPlaylist(
		"video", "video_1280x720@4500000", "720@8000000")
	require.NoError(t, err)
	require.NotZero(t, len(playlist))
	err = ioutil.WriteFile("./test_out/live-video.m3u8", []byte(playlist), 0644)
	require.NoError(t, err)

	mm.Query.Add("requested_start_time_epoch_sec", strconv.FormatInt(startTime, 10))
	playlist, err = mm.MakeMediaPlaylist(
		"audio", "audio@128000", "stereo@128000")
	require.NoError(t, err)
	require.NotZero(t, len(playlist))
	err = ioutil.WriteFile("./test_out/live-audio.m3u8", []byte(playlist), 0644)
	require.NoError(t, err)
}

func TestVod(t *testing.T) {
	mm := ConstructManifestMaker(t, Vod, "flag")
	mm.Query.Add("authorization", "badf00d")

	err := os.MkdirAll("./test_out", 0755)
	require.NoError(t, err)

	playlist := mm.MakeMasterPlaylist("flag")
	require.NotZero(t, len(playlist))
	err = ioutil.WriteFile("./test_out/vod-master.m3u8", []byte(playlist), 0644)
	require.NoError(t, err)

	playlist, err = mm.MakeMediaPlaylist(
		"video", "video_1280x720@4500000", "720@8000000")
	require.NoError(t, err)
	require.NotZero(t, len(playlist))
	err = ioutil.WriteFile("./test_out/vod-video.m3u8", []byte(playlist), 0644)
	require.NoError(t, err)

	playlist, err = mm.MakeMediaPlaylist(
		"audio", "audio@128000", "stereo@128000")
	require.NoError(t, err)
	require.NotZero(t, len(playlist))
	err = ioutil.WriteFile("./test_out/vod-audio.m3u8", []byte(playlist), 0644)
	require.NoError(t, err)
}

func TestIBC(t *testing.T) {
	offering := "female"
	mm := ConstructManifestMaker(t, Live, offering)
	mm.Query.Add("authorization", "badf00d")
	startTime := time.Now().Unix() - 50
	mm.Query.Add("requested_start_time_epoch_sec", strconv.FormatInt(startTime, 10))

	err := os.MkdirAll("./test_out", 0755)
	require.NoError(t, err)

	playlist := mm.MakeMasterPlaylist(offering)
	require.NotZero(t, len(playlist))
	err = ioutil.WriteFile("./test_out/ibc-master.m3u8", []byte(playlist), 0644)
	require.NoError(t, err)

	playlist, err = mm.MakeMediaPlaylist(
		"video", "video_1280x720@4500000", "720@8000000")
	require.NoError(t, err)
	require.NotZero(t, len(playlist))
	err = ioutil.WriteFile("./test_out/ibc-video.m3u8", []byte(playlist), 0644)
	require.NoError(t, err)

	mm.Query.Add("requested_start_time_epoch_sec", strconv.FormatInt(startTime, 10))
	playlist, err = mm.MakeMediaPlaylist(
		"audio", "audio@128000", "stereo@128000")
	require.NoError(t, err)
	require.NotZero(t, len(playlist))
	err = ioutil.WriteFile("./test_out/ibc-audio.m3u8", []byte(playlist), 0644)
	require.NoError(t, err)
}

func ConstructManifestMaker(t *testing.T, mode Mode, offering string) *ManifestMaker {
	mm := NewManifestMaker(mode)
	switch offering {
	case "flag":
		s, err := MakeMockScheduleFlagDay(true)
		require.NoError(t, err)
		seq, err := s.MakeSequence(nil, int64(8553957321+331984))
		require.NoError(t, err)
		mm.MediaSequence = *seq
	case "peter":
		mm.MediaSequence = MakeMockSequence()
	default:
		s, err := MakeMockScheduleIBCSmall("female")
		require.NoError(t, err)
		seq, err := s.MakeSequence(nil, 0)
		require.NoError(t, err)
		mm.MediaSequence = *seq
	}

	mm.Format = "hls-aes128"
	for _, slice := range mm.MediaSequence.Slices {
		if slice.Type == LivePlayable {
			mm.LiveOfferings[slice.QHash] =
				&OfferingInfo{
					StartPts:       0,        // 8553957321,
					EndPts:         35820000, //8607111930 - 8553957321,
					AudioTimescale: 48000,
					VideoTimescale: 90000,
					SegDurationTs:  180180,
				}
		} else {
			mm.Offerings[slice.QHash] = SampleOffering(t)
		}
	}
	return mm
}

func SampleOffering(t *testing.T) *abr.Offering {
	filePath, err := filepath.Abs("./offering_sample_aes.json")
	//filePath, err := filepath.Abs("./offering_trimmed.json")
	require.NoError(t, err)
	jsonBytes, err := ioutil.ReadFile(filePath)
	require.NoError(t, err)

	o := abr.NewOffering()
	err = json.Unmarshal(jsonBytes, &o)
	require.NoError(t, err)
	return o
}
