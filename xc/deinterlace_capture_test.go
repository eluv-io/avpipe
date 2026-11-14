package xc_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/require"
)

// Replay captured TS bytes through custom I/O with a UDP URL.
func TestDeinterlaceLiveCaptures(t *testing.T) {
	if testing.Short() {
		t.Skip("five-minute 1080i capture transcodes")
	}
	sports := liveCaptureEncoding()
	sports.AudioIndex = []int32{1, 2}
	sports.SyncAudioToStreamId = 512
	sports.VideoTimeBase, sports.VideoFrameDurationTs = 60000, 1001
	sports.ForceKeyInt = 120
	sports.SegDuration = "30.03"
	sports.VideoSegDurationTs, sports.AudioSegDurationTs = 1801800, 1428480

	tv := liveCaptureEncoding()
	tv.AudioIndex = []int32{1}
	tv.SyncAudioToStreamId = 256
	tv.VideoTimeBase, tv.VideoFrameDurationTs = 12800, 256
	tv.ForceKeyInt = 128
	tv.ForceEqualFDuration = true // Normalize capture PTS jitter for exact part cuts.
	tv.SegDuration = "2.56"
	tv.VideoSegDurationTs, tv.AudioSegDurationTs = 32768, 122880

	for _, tc := range []struct {
		name, fixture, outputRate string
		encoding                  *goavpipe.XcParams
		minFrames, maxFrames      int
		equalAVParts              bool
	}{
		{"LiveSports_2997i", "live_sports_20261106_1080i_5min", "60000/1001", sports, 17400, 18000, false},
		{"LiveTV_25i", "live_tv_20261021_5min", "50/1", tv, 14500, 15000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join("../media", tc.fixture+".cap")
			skipIfFileMissing(t, capture)
			ffprobe, err := exec.LookPath("ffprobe")
			require.NoError(t, err, "ffprobe is required to verify output streams")
			verifyCaptureChecksum(t, capture, filepath.Join("../testdata", tc.fixture+".json"))
			require.NoError(t, os.MkdirAll(testOutBase, 0755))
			dir, err := os.MkdirTemp(testOutBase, "deinterlace-capture-")
			require.NoError(t, err)
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("retained failing capture output in %s", dir)
					return
				}
				os.RemoveAll(dir)
			})
			previousInput, previousOutput := goavpipe.GetGlobalInputOpener(), goavpipe.GetGlobalOutputOpener()
			t.Cleanup(func() { goavpipe.InitIOHandler(previousInput, previousOutput) })
			goavpipe.InitIOHandler(captureInput{capture}, &xc.FileOutputOpener{Dir: dir})

			params := *tc.encoding
			params.Url = "udp://" + tc.name + ":5000"
			params.InputCfg.BypassLibavReader = true
			framesPerPart := int(params.VideoSegDurationTs / int64(params.VideoFrameDurationTs))
			require.Zero(t, params.VideoSegDurationTs%int64(params.VideoFrameDurationTs))
			require.Zero(t, framesPerPart%int(params.ForceKeyInt), "part cuts must coincide with scheduled keyframes")
			if tc.equalAVParts {
				require.Equal(t, params.VideoSegDurationTs*int64(params.SampleRate),
					params.AudioSegDurationTs*int64(params.VideoTimeBase), "audio/video part durations must be equal")
			}
			require.NoError(t, avpipe.Xc(&params))

			parts := captureParts(t, dir, "vsegment-")
			require.GreaterOrEqual(t, len(parts), tc.minFrames/framesPerPart)
			totalFrames, extraKeys := 0, 0
			for partIndex, part := range parts {
				frames, _, err := xc.SourceMP4FrameTimings(part)
				require.NoError(t, err)
				require.NotEmpty(t, frames)
				isTail := partIndex == len(parts)-1
				if !isTail {
					require.Len(t, frames, framesPerPart, "full part: %s", part)
				} else {
					require.LessOrEqual(t, len(frames), framesPerPart)
				}
				var duration uint64
				lastKey := 0
				for n, frame := range frames {
					fd := uint32(params.VideoFrameDurationTs)
					if isTail && n >= len(frames)-8 {
						// PCAP prefixes can end mid-PES; decoder/filter draining
						// includes delayed frames at this EOF boundary only.
						require.Contains(t, []uint32{fd, 2 * fd}, frame.Duration)
					} else {
						require.Equal(t, fd, frame.Duration, "%s frame %d", part, n)
					}
					require.Equal(t, int64(duration), frame.PTS, "%s frame %d", part, n)
					require.Equal(t, duration, frame.DTS, "%s frame %d", part, n)
					duration += uint64(frame.Duration)
					if n%int(params.ForceKeyInt) == 0 {
						require.True(t, frame.Sync, "missing scheduled keyframe in %s at frame %d", part, n)
					} else if frame.Sync {
						extraKeys++ // Scene-cut keyframes are permitted.
					}
					if frame.Sync {
						require.LessOrEqual(t, n-lastKey, int(params.ForceKeyInt))
						lastKey = n
					}
				}
				stream := probeCapturePart(t, ffprobe, part, "v:0")
				require.Equal(t, int(params.EncWidth), stream.Width)
				require.Equal(t, int(params.EncHeight), stream.Height)
				require.Equal(t, "progressive", stream.FieldOrder)
				require.Equal(t, tc.outputRate, stream.FrameRate)
				require.Equal(t, fmt.Sprintf("1/%d", params.VideoTimeBase), stream.TimeBase)
				require.Equal(t, int64(duration), stream.Duration)
				if !isTail {
					require.Equal(t, params.VideoSegDurationTs, stream.Duration, "full video duration: %s", part)
				}
				totalFrames += len(frames)
			}
			require.Greater(t, totalFrames, tc.minFrames)
			require.LessOrEqual(t, totalFrames, tc.maxFrames)
			for audioIndex := range params.AudioIndex {
				audio := captureParts(t, dir, fmt.Sprintf("asegment%d-", audioIndex))
				require.NotEmpty(t, audio)
				if !tc.equalAVParts {
					continue
				}
				require.Equal(t, len(parts), len(audio), "audio/video part counts")
				for n, part := range audio {
					stream := probeCapturePart(t, ffprobe, part, "a:0")
					require.Equal(t, fmt.Sprint(params.SampleRate), stream.SampleRate)
					require.Equal(t, fmt.Sprintf("1/%d", params.SampleRate), stream.TimeBase)
					if n < len(audio)-1 {
						// This capture has audio durations 9604-9596 instead of 9600
						// which produces 1026/1022 ticks at 48,000.
						// Part 2 has 121 packets and lasts 2.581271 s instead of 120/2.56 s.
						// Allow +/-1 AAC packet and +/-50 ms, preserving source timing.
						require.InDelta(t, params.AudioSegDurationTs, stream.Duration,
							float64(params.SampleRate)*0.050, "full audio duration: %s", part)
						packets, err := strconv.ParseInt(stream.Packets, 10, 64)
						require.NoError(t, err)
						require.InDelta(t, params.AudioSegDurationTs/1024, packets, 1,
							"full AAC part packet count: %s", part)
					} else {
						require.Positive(t, stream.Duration)
						require.LessOrEqual(t, stream.Duration, params.AudioSegDurationTs)
					}
				}
			}
			t.Logf("%d frames, %d full video parts, %d tail frames, %d extra scene-cut keyframes",
				totalFrames, len(parts)-1, totalFrames-(len(parts)-1)*framesPerPart, extraKeys)
		})
	}
}

// Common encoding defaults; each table entry supplies its own XcParams so codec,
// bitrate, audio selection, GOP and segmentation settings can vary independently.
func liveCaptureEncoding() *goavpipe.XcParams {
	params := goavpipe.NewXcParams()
	params.Format = "fmp4-segment"
	params.XcType = goavpipe.XcAll
	params.Ecodec, params.Ecodec2 = "libx264", "aac"
	params.Preset, params.Profile = "medium", "high"
	params.EncWidth, params.EncHeight = 1920, 1080
	params.VideoBitrate, params.AudioBitrate = 9500000, 192000
	params.SampleRate = 48000
	params.Deinterlace = 1
	return params
}

func verifyCaptureChecksum(t *testing.T, capture, manifestPath string) {
	t.Helper()
	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	var manifest struct {
		SHA256 string `json:"sha256"`
	}
	require.NoError(t, json.Unmarshal(data, &manifest))
	input, err := os.Open(capture)
	require.NoError(t, err)
	defer input.Close()
	digest := sha256.New()
	_, err = io.Copy(digest, input)
	require.NoError(t, err)
	require.Equal(t, manifest.SHA256, fmt.Sprintf("%x", digest.Sum(nil)), "capture fixture changed")
}

func captureParts(t *testing.T, dir, prefix string) []string {
	t.Helper()
	parts, err := filepath.Glob(filepath.Join(dir, prefix+"*.mp4"))
	require.NoError(t, err)
	sort.Slice(parts, func(i, j int) bool {
		var a, b int
		fmt.Sscanf(filepath.Base(parts[i]), prefix+"%d.mp4", &a)
		fmt.Sscanf(filepath.Base(parts[j]), prefix+"%d.mp4", &b)
		return a < b
	})
	return parts
}

type captureStream struct {
	Width, Height int
	FieldOrder    string `json:"field_order"`
	FrameRate     string `json:"r_frame_rate"`
	TimeBase      string `json:"time_base"`
	SampleRate    string `json:"sample_rate"`
	Duration      int64  `json:"duration_ts"`
	Packets       string `json:"nb_read_packets"`
}

func probeCapturePart(t *testing.T, ffprobe, part, selector string) captureStream {
	t.Helper()
	output, err := exec.Command(ffprobe, "-v", "error", "-select_streams", selector, "-count_packets",
		"-show_entries", "stream=width,height,field_order,r_frame_rate,time_base,sample_rate,duration_ts,nb_read_packets", "-of", "json", part).CombinedOutput()
	require.NoError(t, err, "%s", output)
	var probe struct {
		Streams []captureStream `json:"streams"`
	}
	require.NoError(t, json.Unmarshal(output, &probe))
	require.Len(t, probe.Streams, 1)
	return probe.Streams[0]
}

type captureInput struct{ path string }

func (i captureInput) Open(_ int64, _ string) (goavpipe.InputHandler, error) {
	f, err := os.Open(i.path)
	if err != nil {
		return nil, err
	}
	r := &captureReader{file: f, reader: bufio.NewReaderSize(f, 256*1024)}
	var header [24]byte
	if _, err := io.ReadFull(r.reader, header[:]); err != nil {
		f.Close()
		return nil, err
	}
	if binary.LittleEndian.Uint32(header[:4]) != 0xa1b2c3d4 || binary.LittleEndian.Uint32(header[20:]) != 1 {
		f.Close()
		return nil, fmt.Errorf("expected microsecond little-endian Ethernet PCAP")
	}
	return r, nil
}

type captureReader struct {
	file    *os.File
	reader  *bufio.Reader
	packet  [65536]byte
	pending []byte
}

func (r *captureReader) Read(buf []byte) (int, error) {
	for len(r.pending) == 0 {
		var header [16]byte
		if _, err := io.ReadFull(r.reader, header[:]); err != nil {
			return 0, err
		}
		n := binary.LittleEndian.Uint32(header[8:])
		if n > uint32(len(r.packet)) || n != binary.LittleEndian.Uint32(header[12:]) {
			return 0, fmt.Errorf("invalid or truncated captured packet")
		}
		packet := r.packet[:n]
		if _, err := io.ReadFull(r.reader, packet); err != nil {
			return 0, err
		}
		if len(packet) < 42 || binary.BigEndian.Uint16(packet[12:14]) != 0x0800 || packet[23] != 17 {
			continue
		}
		udpOffset := 14 + int(packet[14]&15)*4
		if udpOffset < 34 || udpOffset+8 > len(packet) {
			return 0, fmt.Errorf("invalid IPv4 header")
		}
		udp := packet[udpOffset:]
		length := int(binary.BigEndian.Uint16(udp[4:6]))
		if length < 8 || length > len(udp) {
			return 0, fmt.Errorf("invalid UDP length")
		}
		payload := udp[8:length]
		if len(payload) == 0 || len(payload)%188 != 0 || payload[0] != 0x47 {
			continue
		}
		r.pending = payload
	}
	n := copy(buf, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *captureReader) Close() error { return r.file.Close() }
func (r *captureReader) Size() int64  { return -1 }
func (r *captureReader) Seek(int64, int) (int64, error) {
	return -1, fmt.Errorf("capture replay is not seekable")
}
func (r *captureReader) Stat(int, goavpipe.AVStatType, interface{}) error { return nil }
