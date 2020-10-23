package live

import (
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
)

var h1 = "hq__EhiwJsbi995znknYRErciysv75tEwXizGRjS5DzCNSxoRMgU11H5QSbrqZMhJEM6bebpHfqAwx"
var h2 = "hq__AfHsPTHjG3Hhs7bKYQzF5mENrRu4xc9ybNYPwEyZFH9YgxCMmU4FjXt6UUCyHxAYarMnCu3bpg"
var tok = "eyJxc3BhY2VfaWQiOiJpc3BjM0FOb1ZTek5BM1A2dDdhYkxSNjlobzVZUFBaVSIsImFkZHIiOiIweGVhM2Y3YmZkMTkyOWFlYTU1MWI0NThhZDQzNjA5NDNkZDRiNTJhYzkiLCJxbGliX2lkIjoiaWxpYjM4UmJTNzlraE5pUHRZVTk2UVRQOVNqWVI2eXYifQ==.RVMyNTZLX050RGhlZXNYdnBQR1lrZWdkVFJoZFNyN1djTG9aS3JXWW9Rc1pNTTZ6MThEMTVGQ1VoMndBcDNjTTdpYUZUNWhrVmhENlhDZ3RlTmtlWnlXcVlrY3Nlb1dB"
var urlBase = "https://host-66-220-3-86.contentfabric.io/q/"
var urlPath = "/rep/playout/default/hls-clear/video/720@14000000"
var audioPath = "/rep/playout/default/hls-clear/audio_2ch_eng/stereo@128000"

func TestLinearF1(t *testing.T) {
	setupLogging()
	fmt.Println("Linear F1")
	cfg := LinearConfig{ContentHashes: []string{h1, h2}, Tok: tok, UrlBase: urlBase, UrlPath: urlPath, AudioPath: audioPath}
	seq := NewSequence(&cfg)
	makeTestVod(seq)
	makeTestLive(seq)
}

func TestAuth(t *testing.T) {
	os.MkdirAll("./F1-VOD/video/720@14000000", 0755)
	query := url.Values{}
	query.Add("authorization", "test")
	cfg := LinearConfig{ContentHashes: []string{h1, h2}, Tok: tok, UrlBase: urlBase, UrlPath: urlPath, AudioPath: audioPath}
	seq := NewSequence(&cfg)
	mp := seq.MakeVodMasterPlaylist(query)
	_ = ioutil.WriteFile("./F1-VOD/playlist-auth.m3u8", []byte(mp), 0644)
}

func TestVideoOnly(t *testing.T) {
	os.MkdirAll("./F1-VOD/video/720@14000000", 0755)
	query := url.Values{}
	query.Add("video_only", "1")
	cfg := LinearConfig{ContentHashes: []string{h1, h2}, Tok: tok, UrlBase: urlBase, UrlPath: urlPath, AudioPath: audioPath}
	seq := NewSequence(&cfg)
	mp := seq.MakeVodMasterPlaylist(query)
	_ = ioutil.WriteFile("./F1-VOD/playlist-video.m3u8", []byte(mp), 0644)
}

func makeTestLive(seq *Sequence) {
	startTime := time.Now().Unix() - 300
	query := url.Values{}
	query.Add("requested_start_time_epoch_sec", strconv.FormatInt(startTime, 10))

	os.MkdirAll("./F1-Live/video/720@14000000", 0755)
	os.MkdirAll("./F1-Live/audio_2ch_eng/stereo@128000", 0755)

	mp := seq.MakeLiveMasterPlaylist(query)
	_ = ioutil.WriteFile("./F1-Live/playlist.m3u8", []byte(mp), 0644)

	vp := seq.MakeLiveMediaPlaylist(query, "video")
	_ = ioutil.WriteFile("./F1-Live/video/720@14000000/live.m3u8", []byte(vp), 0644)

	ap := seq.MakeLiveMediaPlaylist(query, "audio")
	_ = ioutil.WriteFile("./F1-Live/audio_2ch_eng/stereo@128000/live.m3u8", []byte(ap), 0644)
}

func makeTestVod(seq *Sequence) {
	query := url.Values{}
	os.MkdirAll("./F1-VOD/video/720@14000000", 0755)
	os.MkdirAll("./F1-VOD/audio_2ch_eng/stereo@128000", 0755)

	mp := seq.MakeVodMasterPlaylist(query)
	_ = ioutil.WriteFile("./F1-VOD/playlist.m3u8", []byte(mp), 0644)

	vp := seq.MakeVodMediaPlaylist(query, "video")
	_ = ioutil.WriteFile("./F1-VOD/video/720@14000000/vod.m3u8", []byte(vp), 0644)

	ap := seq.MakeVodMediaPlaylist(query, "audio")
	_ = ioutil.WriteFile("./F1-VOD/audio_2ch_eng/stereo@128000/vod.m3u8", []byte(ap), 0644)
}
