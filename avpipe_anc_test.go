package avpipe_test

import (
	"errors"
	"fmt"
	"os"
	"path"
	"sync"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/broadcastproto/anctest"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
	"github.com/eluv-io/common-go/media/anc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ancInputOpener opens a file and records every AV_IN_STAT_DATA_ANC the input receives.
type ancInputOpener struct {
	mu      sync.Mutex
	packets []goavpipe.AncPacket
}

func (o *ancInputOpener) Open(_ int64, url string) (goavpipe.InputHandler, error) {
	f, err := os.Open(url)
	if err != nil {
		return nil, err
	}
	return &ancInput{f: f, o: o}, nil
}

type ancInput struct {
	f *os.File
	o *ancInputOpener
}

func (i *ancInput) Read(b []byte) (int, error)                { return i.f.Read(b) }
func (i *ancInput) Seek(off int64, whence int) (int64, error) { return i.f.Seek(off, whence) }
func (i *ancInput) Close() error                              { return i.f.Close() }

func (i *ancInput) Size() int64 {
	fi, err := i.f.Stat()
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (i *ancInput) Stat(_ int, statType goavpipe.AVStatType, args interface{}) error {
	if statType != goavpipe.AV_IN_STAT_DATA_ANC {
		return nil
	}
	p := args.(*goavpipe.AncPacket)
	i.o.mu.Lock()
	defer i.o.mu.Unlock()
	i.o.packets = append(i.o.packets, *p)
	return nil
}

// TestAncStat builds a TS with an ST 2038 stream next to the video, checks that probe lists the stream, and that
// a transcode reports every ANC PES packet with its pts and payload through AV_IN_STAT_DATA_ANC.
func TestAncStat(t *testing.T) {
	src := "./media/bbb_1080p_30fps_10sec.mp4"
	checkFileExists(t, src)
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)
	tsPath := path.Join(outputDir, "anc.ts")

	// One ST 12-2 time code packet per frame, counting from 00:00:00:00 at 30 fps, and a second, user-defined
	// packet once a second
	written, err := anctest.WriteTS(src, tsPath, func(i int, pts int64) []byte {
		h, m, s, f := anctest.TimecodeAt(i, 30)
		pkts := []anc.Packet{{Line: 9, DID: 0x60, SDID: 0x60, UDW: anctest.TimecodeUDW(h, m, s, f, false, 0)}}
		if i%30 == 0 {
			pkts = append(pkts, anc.Packet{Line: 12, DID: 0x50, SDID: 0x01, UDW: []byte(fmt.Sprintf("frame %d", i))})
		}
		return anc.EncodePES(pkts)
	})
	require.NoError(t, err)
	require.Greater(t, len(written), 250, "expected one ANC packet per frame of the 10 s clip")

	// Probe lists the stream as data with the VANC registration tag; it has no decoder, so no codec name
	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: tsPath}, &xc.FileOutputOpener{Dir: outputDir})
	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: tsPath, Seekable: true})
	require.NoError(t, err)
	data := probe.StreamByCodecType("data")
	require.NotNil(t, data, "expected the ST 2038 stream in the probe")
	assert.Equal(t, "VANC", data.CodecTagString)
	assert.NotNil(t, probe.StreamByCodecType("video"))

	in := &ancInputOpener{}
	goavpipe.InitIOHandler(in, &xc.FileOutputOpener{Dir: outputDir})
	params := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		VideoBitrate:        1000000,
		Ecodec:              h264Codec,
		EncHeight:           360,
		EncWidth:            640,
		XcType:              goavpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 tsPath,
		DebugFrameLevel:     debugFrameLevel,
	}
	setFastEncodeParams(params, true)
	require.NoError(t, avpipe.Xc(params))

	in.mu.Lock()
	got := in.packets
	in.mu.Unlock()
	require.Equal(t, len(written), len(got), "one stat per ANC PES packet, in input order")
	for i := range got {
		assert.Equal(t, written[i].Pts, got[i].Pts, "packet %d pts", i)
		assert.Equal(t, written[i].Payload, got[i].Payload, "packet %d payload", i)
		// The payload parses back to the frame's time code and, once a second, the user packet
		pkts, err := anc.ParsePES(got[i].Payload)
		require.NoError(t, err, "packet %d", i)
		tc, ok := anc.FindTimecode(pkts)
		require.True(t, ok, "packet %d has a time code", i)
		h, m, s, f := anctest.TimecodeAt(i, 30)
		assert.Equal(t, fmt.Sprintf("%02d:%02d:%02d:%02d", h, m, s, f), tc.String(), "packet %d", i)
		if i%30 == 0 {
			require.Len(t, pkts, 2, "packet %d", i)
			assert.Equal(t, []byte(fmt.Sprintf("frame %d", i)), pkts[1].UDW, "packet %d", i)
		} else {
			require.Len(t, pkts, 1, "packet %d", i)
		}
		assert.Equal(t, 1, got[i].TimeBaseNum, "packet %d time base", i)
		assert.Equal(t, 90000, got[i].TimeBaseDen, "packet %d time base", i)
		if i > 0 {
			assert.Greater(t, got[i].Pts, got[i-1].Pts, "packet %d pts order", i)
		}
	}
}

// ancXcParams are the video transcode params the ANC tests use on a generated TS.
func ancXcParams(url string) *goavpipe.XcParams {
	params := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		VideoBitrate:        1000000,
		Ecodec:              h264Codec,
		EncHeight:           360,
		EncWidth:            640,
		XcType:              goavpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	setFastEncodeParams(params, true)
	return params
}

var errStopScan = errors.New("stop")

// TestScanANC reads a generated TS with ScanANC and gets the packets the stat reports, through the same demuxer.
func TestScanANC(t *testing.T) {
	src := "./media/bbb_1080p_30fps_10sec.mp4"
	checkFileExists(t, src)
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)
	tsPath := path.Join(outputDir, "anc.ts")
	written, err := anctest.WriteTS(src, tsPath, func(i int, pts int64) []byte {
		h, m, s, f := anctest.TimecodeAt(i, 30)
		return anc.EncodePES([]anc.Packet{{Line: 9, DID: 0x60, SDID: 0x60, UDW: anctest.TimecodeUDW(h, m, s, f, false, 0)}})
	})
	require.NoError(t, err)

	var got []goavpipe.AncPacket
	stream, err := avpipe.ScanANC(tsPath, 0, func(p *goavpipe.AncPacket) error {
		got = append(got, *p)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, stream.StreamIndex)
	assert.Equal(t, 0x101, stream.StreamID)
	assert.Equal(t, "smpte_2038", stream.CodecName)
	assert.Equal(t, "VANC", stream.CodecTag)
	require.Equal(t, len(written), len(got))
	for i := range got {
		assert.Equal(t, written[i].Pts, got[i].Pts, "packet %d pts", i)
		assert.Equal(t, written[i].Payload, got[i].Payload, "packet %d payload", i)
		assert.Equal(t, 90000, got[i].TimeBaseDen, "packet %d time base", i)
	}

	// The callback's error ends the scan and comes back
	n := 0
	_, err = avpipe.ScanANC(tsPath, 0, func(*goavpipe.AncPacket) error {
		if n++; n == 5 {
			return errStopScan
		}
		return nil
	})
	assert.ErrorIs(t, err, errStopScan)
	assert.Equal(t, 5, n)

	// An input that cannot be opened
	_, err = avpipe.ScanANC(path.Join(outputDir, "missing.ts"), 0, func(*goavpipe.AncPacket) error { return nil })
	assert.Error(t, err)
	assert.NotErrorIs(t, err, avpipe.ErrNoAncStream)

	// A plain clip has no such stream
	_, err = avpipe.ScanANC(src, 0, func(*goavpipe.AncPacket) error { return nil })
	assert.ErrorIs(t, err, avpipe.ErrNoAncStream)
}

// TestAncStreamId covers a stream the PMT does not mark as ST 2038, as an encoder with a vendor descriptor
// produces: neither the scan nor the stat sees it until AncStreamId names its PID.
func TestAncStreamId(t *testing.T) {
	src := "./media/bbb_1080p_30fps_10sec.mp4"
	checkFileExists(t, src)
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)
	tsPath := path.Join(outputDir, "unmarked.ts")
	written, err := anctest.WriteTSUnmarked(src, tsPath, func(i int, pts int64) []byte {
		return anc.EncodePES([]anc.Packet{{Line: 9, DID: 0x41, SDID: 0x07, UDW: []byte{byte(i)}}})
	})
	require.NoError(t, err)

	count := func(p *goavpipe.AncPacket) error { return nil }
	_, err = avpipe.ScanANC(tsPath, 0, count)
	assert.ErrorIs(t, err, avpipe.ErrNoAncStream)
	n := 0
	stream, err := avpipe.ScanANC(tsPath, 0x101, func(p *goavpipe.AncPacket) error {
		n++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, len(written), n)
	assert.Equal(t, 0x101, stream.StreamID)
	assert.Equal(t, "bin_data", stream.CodecName, "the demuxer files it as generic binary data")

	// The stat path: nothing without the id, every packet with it
	in := &ancInputOpener{}
	goavpipe.InitIOHandler(in, &xc.FileOutputOpener{Dir: outputDir})
	require.NoError(t, avpipe.Xc(ancXcParams(tsPath)))
	assert.Empty(t, in.packets, "an unmarked data stream is not reported")

	in = &ancInputOpener{}
	goavpipe.InitIOHandler(in, &xc.FileOutputOpener{Dir: outputDir})
	params := ancXcParams(tsPath)
	params.AncStreamId = 0x101
	require.NoError(t, avpipe.Xc(params))
	require.Equal(t, len(written), len(in.packets))
	for i := range in.packets {
		assert.Equal(t, written[i].Pts, in.packets[i].Pts, "packet %d pts", i)
		assert.Equal(t, written[i].Payload, in.packets[i].Payload, "packet %d payload", i)
	}
}
