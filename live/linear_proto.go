package live

/* Linear channel composition prototype
 * Implements a 'media Sequence' based on a simple spec
 */

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type LinearConfig struct {
	ContentHashes []string
	Tok           string
	UrlBase       string
	UrlPath       string
	AudioPath     string
}

type Sequence struct {
	startTime   time.Time
	Config      *LinearConfig
	slices      []slice
	audioSlices []slice
}

type slice struct {
	config           *LinearConfig
	startNano        int64
	durationNano     int64
	segDurationNano  int64
	hash             string
	firstFrame       int
	lastFrame        int
	numSegs          int
	firstSeg         int
	firstSegFraction string
	lastSeg          int
	lastSegFraction  string
	mediaType        string
}

type segment struct {
	slice_idx int
	slice_num int
	num       int // From the beginning of the Sequence
}

func NewSequence(cfg *LinearConfig) *Sequence {
	seq := &Sequence{Config: cfg, startTime: time.Now()}
	seq.slices = makeScheduleF1(seq.Config)
	seq.audioSlices = makeAudioScheduleF1(seq.Config)
	return seq
}

func makeScheduleF1(cfg *LinearConfig) []slice {
	s1 := slice{
		config:           cfg,
		startNano:        0,
		durationNano:     1,
		segDurationNano:  2002000000,
		hash:             cfg.ContentHashes[0],
		numSegs:          0,
		firstFrame:       0,
		lastFrame:        13917,
		firstSeg:         1,
		firstSegFraction: "",
		lastSeg:          115 + 1,
		lastSegFraction:  "-0-175675", // subtracted 0.5
		mediaType:        "video",
	}
	s2 := slice{
		config:           cfg,
		startNano:        0,
		durationNano:     0,
		hash:             cfg.ContentHashes[1],
		numSegs:          0,
		firstFrame:       13150,
		lastFrame:        18901,
		firstSeg:         109 + 1,
		firstSegFraction: "-105105-180180",
		lastSeg:          157 + 1,
		lastSegFraction:  "-0-150100",
		mediaType:        "video",
	}
	s3 := slice{
		config:           cfg,
		startNano:        0,
		durationNano:     0,
		hash:             cfg.ContentHashes[0],
		numSegs:          0,
		firstFrame:       19669,
		lastFrame:        34080, // this is a little short of full length
		firstSeg:         164 + 1,
		firstSegFraction: "-60060-180180",
		lastSeg:          284,
		lastSegFraction:  "",
		mediaType:        "video",
	}

	return []slice{s1, s2, s3}
}

// timescale: 48000
// segment duration: 2.002 seconds == 96096 ts
func makeAudioScheduleF1(cfg *LinearConfig) []slice {
	s1 := slice{
		config:           cfg,
		startNano:        0,
		durationNano:     1,
		segDurationNano:  2002000000,
		hash:             cfg.ContentHashes[0],
		numSegs:          0,
		firstFrame:       0,
		lastFrame:        13917, // 115.975 video segments
		firstSeg:         1,
		firstSegFraction: "",
		lastSeg:          115 + 1,
		lastSegFraction:  "-0-93694", // 93693.6
		mediaType:        "audio",
	}
	s2 := slice{
		config:           cfg,
		startNano:        0,
		durationNano:     0,
		hash:             cfg.ContentHashes[1],
		numSegs:          0,
		firstFrame:       13150, // 109.583333333333333
		lastFrame:        18901, // 157.508333333333333
		firstSeg:         109 + 1,
		firstSegFraction: "-56056-96096",
		lastSeg:          157 + 1,
		lastSegFraction:  "-0-80080", // 48848.8
		mediaType:        "audio",
	}
	s3 := slice{
		config:           cfg,
		startNano:        0,
		durationNano:     0,
		hash:             cfg.ContentHashes[0],
		numSegs:          0,
		firstFrame:       19669, // 163.908333333333333
		lastFrame:        34080, // this is a little short of full length
		firstSeg:         164 + 1,
		firstSegFraction: "-32032-96096",
		lastSeg:          284,
		lastSegFraction:  "",
		mediaType:        "audio",
	}

	return []slice{s1, s2, s3}
}

// nil cfg to not add authorization token for dev localhost fabric testing
func makeQueryParams(query url.Values, cfg *LinearConfig) string {
	tok := query.Get("authorization")
	if tok == "" && cfg != nil && len(cfg.Tok) > 0 {
		query.Del("authorization")
		query.Add("authorization", cfg.Tok)
	}
	s := ""
	if len(query) > 0 {
		s = "?" + query.Encode()
	}
	return s
}

func (seq *Sequence) segByTime(t time.Time) *segment {
	return nil
}

// Returns segment and discontinuity (0 or 1)
func (seq *Sequence) next(seg segment) (*segment, int) {
	return nil, 0
}

// segCount is the number of segments to write
// returns the number of segments remaining
func (sl *slice) makeSlicePlaylist(segCount int, query url.Values) (string, int) {
	queryParams := makeQueryParams(query, sl.config)
	urlPath := sl.config.UrlPath
	if sl.mediaType == "audio" {
		urlPath = sl.config.AudioPath
	}

	p := sl.makeInitLine(query) + "\n"
	for s := sl.firstSeg; s <= sl.lastSeg; s++ {
		// Determine the partial spec
		partial := ""
		if s == sl.firstSeg {
			partial = sl.firstSegFraction
		} else if s == sl.lastSeg {
			partial = sl.lastSegFraction
		}

		// Build segment URL - including 'partial spec'
		segstr := fmt.Sprintf("%05d%s.m4s", s, partial)
		l := "#EXTINF:2.002000,\n" + sl.config.UrlBase + sl.hash + urlPath +
			"/" + segstr + queryParams
		p = p + l + "\n"

		segCount--
		if segCount <= 0 {
			break
		}
	}
	return p, segCount
}

func (sl *slice) makeInitLine(query url.Values) string {
	urlPath := sl.config.UrlPath
	if sl.mediaType == "audio" {
		urlPath = sl.config.AudioPath
	}
	l := "#EXT-X-MAP:URI=" + "\"" + sl.config.UrlBase + sl.hash + urlPath +
		"/" + "init.m4s" + makeQueryParams(query, sl.config) + "\""
	return l
}

func (seq *Sequence) MakeVodMediaPlaylist(query url.Values, mediaType string) string {
	p := `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-DISCONTINUITY-SEQUENCE:0
#EXT-X-TARGETDURATION:3
#EXT-X-PLAYLIST-TYPE:VOD

`
	disc := "\n#EXT-X-DISCONTINUITY\n"
	slices := seq.slices
	if strings.Contains(mediaType, "audio") {
		slices = seq.audioSlices
	}
	for _, sl := range slices {
		psl, _ := sl.makeSlicePlaylist(math.MaxInt32, query)
		p = p + psl + "\n" + disc
	}
	p = p + "\n#EXT-X-ENDLIST\n"
	return p
}

func (seq *Sequence) MakeLiveMediaPlaylist(query url.Values, mediaType string) string {
	p := `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-TARGETDURATION:3

`
	disc := "\n#EXT-X-DISCONTINUITY\n"

	s := query.Get("requested_start_time_epoch_sec")
	query.Del("requested_start_time_epoch_sec") // assume caller no longer needs query
	startTime, _ := strconv.ParseInt(s, 10, 64)
	durationSec := time.Now().Unix() - startTime
	segCount := int(float32(durationSec) / 2.002)
	slices := seq.slices
	if strings.Contains(mediaType, "audio") {
		slices = seq.audioSlices
	}
	for _, sl := range slices {
		if segCount <= 0 {
			break
		}
		var psl string
		psl, segCount = sl.makeSlicePlaylist(segCount, query)
		p = p + psl + "\n" + disc
	}
	if segCount > 0 {
		p = p + "\n#EXT-X-ENDLIST\n"
	}
	return p
}

func (seq *Sequence) MakeVodMasterPlaylist(query url.Values) string {
	videoOnly := query.Get("video_only") != ""
	query.Del("video_only")

	qp := makeQueryParams(query, nil)

	if videoOnly {
		return `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=14000000,CODECS="avc1.640028",RESOLUTION=1280x720
video/720@14000000/vod.m3u8` + qp + "\n"
	}

	p := `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=14000000,CODECS="avc1.640028,mp4a.40.2",RESOLUTION=1280x720,AUDIO="audio"
video/720@14000000/vod.m3u8` + qp +
		"\n#EXT-X-MEDIA:NAME=\"audio_2ch_eng\",GROUP-ID=\"audio\",TYPE=AUDIO,CHANNELS=\"2\",URI=\"audio_2ch_eng/stereo@128000/vod.m3u8" + qp + "\"\n"
	return p
}

func (seq *Sequence) MakeLiveMasterPlaylist(query url.Values) string {
	videoOnly := query.Get("video_only") != ""
	query.Del("video_only")

	qp := makeQueryParams(query, nil)

	if videoOnly {
		return `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=14000000,CODECS="avc1.640028",RESOLUTION=1280x720
video/720@14000000/live.m3u8` + qp + "\n"
	}

	p := `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=14000000,CODECS="avc1.640028,mp4a.40.2",RESOLUTION=1280x720,AUDIO="audio"
video/720@14000000/live.m3u8` + qp +
		"\n#EXT-X-MEDIA:NAME=\"audio_2ch_eng\",GROUP-ID=\"audio\",TYPE=AUDIO,CHANNELS=\"2\",URI=\"audio_2ch_eng/stereo@128000/live.m3u8" + qp + "\"\n"
	return p
}
