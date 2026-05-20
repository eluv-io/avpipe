// Package mp4e bypass-mode (no transcoding) repackaging APIs.
//
// MakeMezPart and MakeAbrSegments mirror the BypassTranscoding=true flows of
// avpipe.Xc but are implemented in pure Go on top of github.com/Eyevinn/mp4ff.
// They accept the same *goavpipe.XcParams as Xc and resolve input/output
// streams through the same goavpipe InputOpener / OutputOpener registration
// (goavpipe.InitUrlIOHandler), so existing IO implementations such as
// xc.FileInputOpener / xc.FileOutputOpener work unchanged.
//
// Scope (v1): video only (AVC and HEVC), progressive or fragmented MP4 input,
// single video track output per file. Audio, multiplexed output, encryption,
// and manifest generation are out of scope.

package mp4e

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/errors-go"
)

// HEVCProfileMultiviewMain is the HEVC general_profile_idc value that
// identifies MV-HEVC Multiview Main content (ISO/IEC 23008-2 Annex G).
const HEVCProfileMultiviewMain = 6

// IsMVHEVC reports whether the MP4 read from rs contains an HEVC video track
// encoded with the Multiview Main profile (profile_idc 6). This is the
// indicator avpipe uses to identify MV-HEVC content for bypass dispatch.
//
// The reader must support seeking; decoding uses DecModeLazyMdat so mdat
// payload data is not loaded.
func IsMVHEVC(rs io.ReadSeeker) (bool, error) {
	f, err := mp4.DecodeFile(rs, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return false, err
	}
	var moov *mp4.MoovBox
	if f.Init != nil {
		moov = f.Init.Moov
	} else if f.Moov != nil {
		moov = f.Moov
	}
	if moov == nil {
		return false, nil
	}
	for _, t := range moov.Traks {
		if t.Mdia == nil || t.Mdia.Hdlr == nil || t.Mdia.Hdlr.HandlerType != "vide" {
			continue
		}
		if t.Mdia.Minf == nil || t.Mdia.Minf.Stbl == nil || t.Mdia.Minf.Stbl.Stsd == nil {
			continue
		}
		hvcX := t.Mdia.Minf.Stbl.Stsd.HvcX
		if hvcX == nil || hvcX.HvcC == nil {
			continue
		}
		for _, arr := range hvcX.HvcC.NaluArrays {
			if arr.NaluType() != hevc.NALU_SPS || len(arr.Nalus) == 0 {
				continue
			}
			sps, err := hevc.ParseSPSNALUnit(arr.Nalus[0])
			if err != nil {
				continue
			}
			if int(sps.ProfileTierLevel.GeneralProfileIDC) == HEVCProfileMultiviewMain {
				return true, nil
			}
		}
	}
	return false, nil
}

// IsMVHEVCUrl opens the input via the goavpipe IO opener registered for url
// (see goavpipe.InitUrlIOHandler / InitIOHandler) and reports whether the MP4
// is MV-HEVC. The input is opened only for detection and closed before
// returning.
func IsMVHEVCUrl(url string) (bool, error) {
	rsc, err := openInput(url)
	if err != nil {
		return false, err
	}
	defer rsc.Close()
	return IsMVHEVC(rsc)
}

// MakeMezPart packages a source MP4 into one or more fragmented MP4 "mez"
// parts of approximately params.SegDuration seconds each.
//
// Mirrors avpipe.Xc(format="fmp4-segment", bypass=true). Each output segment
// is a self-contained fragmented MP4 (ftyp+moov+styp+moof+mdat) delivered
// through goavpipe's per-URL OutputOpener with AVType=FMP4VideoSegment.
//
// Required params: Url, XcType=XcVideo, SegDuration (seconds, e.g. "30").
// Optional: StartTimeTs, DurationTs, StartSegmentStr, StreamId.
func MakeMezPart(params *goavpipe.XcParams) error {
	e := errors.Template("mp4e.MakeMezPart", errors.K.Invalid.Default())
	if err := validateParams(params); err != nil {
		return e(err)
	}
	segDurSec, err := strconv.ParseFloat(params.SegDuration, 64)
	if err != nil || segDurSec <= 0 {
		return e("reason", "invalid seg_duration", "seg_duration", params.SegDuration)
	}
	startSegNr, err := startSegmentNr(params.StartSegmentStr, 1)
	if err != nil {
		return e(err)
	}

	rsc, err := openInput(params.Url)
	if err != nil {
		return e(err)
	}
	defer rsc.Close()

	file, err := mp4.DecodeFile(rsc)
	if err != nil {
		return e(err, "reason", "DecodeFile failed")
	}

	out := goavpipe.GetOutputOpener(params.Url)
	if out == nil {
		return e("reason", "no output opener registered for url", "url", params.Url)
	}

	if file.IsFragmented() {
		return makeMezFromFragmented(file, params, segDurSec, startSegNr, out)
	}
	return makeMezFromProgressive(file, rsc, params, segDurSec, startSegNr, out)
}

// MakeAbrSegments re-segments a fragmented MP4 mez part into short DASH-style
// ABR segments and a separate init segment.
//
// Mirrors avpipe.Xc(format="dash", bypass=true). Emits one init via AVType
// DASHVideoInit and N media segments via DASHVideoSegment. No DASH manifest
// is produced in v1.
//
// Required params: Url, XcType=XcVideo, and one of VideoSegDurationTs (track
// timescale ticks) or SegDuration (seconds).
// Optional: StartFragmentIndex.
func MakeAbrSegments(params *goavpipe.XcParams) error {
	e := errors.Template("mp4e.MakeAbrSegments", errors.K.Invalid.Default())
	if err := validateParams(params); err != nil {
		return e(err)
	}
	startSegNr := int(params.StartFragmentIndex)
	if startSegNr <= 0 {
		startSegNr = 1
	}

	rsc, err := openInput(params.Url)
	if err != nil {
		return e(err)
	}
	defer rsc.Close()

	file, err := mp4.DecodeFile(rsc)
	if err != nil {
		return e(err, "reason", "DecodeFile failed")
	}
	if !file.IsFragmented() || file.Init == nil {
		return e("reason", "input is not a fragmented MP4 with an init segment")
	}

	trak, err := pickVideoTrak(file.Init.Moov, params.StreamId)
	if err != nil {
		return e(err)
	}
	timescale := trak.Mdia.Mdhd.Timescale
	chunkDur, err := abrChunkDurTicks(params, timescale)
	if err != nil {
		return e(err)
	}

	out := goavpipe.GetOutputOpener(params.Url)
	if out == nil {
		return e("reason", "no output opener registered for url", "url", params.Url)
	}

	// Collect all samples in input order across all fragments for the chosen track.
	var trex *mp4.TrexBox
	if file.Init.Moov.Mvex != nil {
		// Pick trex matching trak.Tkhd.TrackID if present.
		if t, ok := file.Init.Moov.Mvex.GetTrex(trak.Tkhd.TrackID); ok {
			trex = t
		} else {
			trex = file.Init.Moov.Mvex.Trex
		}
	}

	samples := make([]mp4.FullSample, 0, 1024)
	for _, seg := range file.Segments {
		for _, frag := range seg.Fragments {
			if frag.Moof == nil || frag.Moof.Traf == nil {
				continue
			}
			if frag.Moof.Traf.Tfhd != nil && frag.Moof.Traf.Tfhd.TrackID != trak.Tkhd.TrackID {
				continue
			}
			fs, gErr := frag.GetFullSamples(trex)
			if gErr != nil {
				return e(gErr, "reason", "GetFullSamples failed")
			}
			samples = append(samples, fs...)
		}
	}
	if len(samples) == 0 {
		return e("reason", "no samples in input")
	}

	// Write the init segment first.
	h := goavpipe.Globals.GetNextFD()
	if err = writeOutput(out, h, 0, 0, int64(samples[0].PresentationTime()),
		goavpipe.DASHVideoInit, func(w io.Writer) error {
			return file.Init.Encode(w)
		}); err != nil {
		return e(err, "reason", "write init segment")
	}

	// Pick a styp for media segments (reuse first input styp if present).
	var styp *mp4.StypBox
	if len(file.Segments) > 0 {
		styp = file.Segments[0].Styp
	}

	// Walk samples, breaking at sync samples whose PTS >= chunkDur*currOutSeqNr.
	segNr := startSegNr
	chunkBoundary := uint64(chunkDur)
	startIdx := 0
	flush := func(endExclusive int) error {
		if endExclusive <= startIdx {
			return nil
		}
		first := samples[startIdx]
		return writeOutput(out, h, 0, segNr, int64(first.PresentationTime()),
			goavpipe.DASHVideoSegment, func(w io.Writer) error {
				return encodeMediaSegment(w, styp, uint32(segNr), trak.Tkhd.TrackID,
					samples[startIdx:endExclusive])
			})
	}

	for i, s := range samples {
		if i == 0 {
			continue
		}
		if !mp4.IsSyncSampleFlags(s.Flags) {
			continue
		}
		if uint64(s.PresentationTime()) < chunkBoundary {
			continue
		}
		if err = flush(i); err != nil {
			return e(err, "reason", "write segment")
		}
		startIdx = i
		segNr++
		// Advance chunk boundary past current PTS so that one missed boundary
		// (e.g. sparse keyframes) does not produce a zero-length segment.
		for uint64(s.PresentationTime()) >= chunkBoundary {
			chunkBoundary += uint64(chunkDur)
		}
	}
	if err = flush(len(samples)); err != nil {
		return e(err, "reason", "write final segment")
	}
	return nil
}

// --- progressive mez ---------------------------------------------------------

func makeMezFromProgressive(file *mp4.File, rs io.ReadSeeker,
	params *goavpipe.XcParams, segDurSec float64, startSegNr int,
	out goavpipe.OutputOpener) error {

	e := errors.Template("mp4e.MakeMezPart", errors.K.Invalid.Default())
	if file.Moov == nil {
		return e("reason", "no moov in progressive input")
	}
	trak, err := pickVideoTrak(file.Moov, params.StreamId)
	if err != nil {
		return e(err)
	}
	stbl := trak.Mdia.Minf.Stbl
	if stbl == nil || stbl.Stts == nil || stbl.Stsz == nil || stbl.Stsc == nil {
		return e("reason", "incomplete stbl in input track")
	}
	if stbl.Stss == nil {
		return e("reason", "no stss in input track (cannot find IDR boundaries)")
	}

	timescale := trak.Mdia.Mdhd.Timescale
	segDurTicks := uint64(segDurSec * float64(timescale))

	// Apply optional [StartTimeTs, StartTimeTs+DurationTs) window over PTS.
	winStart, winEnd := windowTicks(params, timescale)

	// Compute segment start sync samples in input PTS order.
	syncStarts := segmentSyncStarts(stbl, segDurTicks, winStart, winEnd)
	if len(syncStarts) == 0 {
		return e("reason", "no sync samples in requested window")
	}

	totSamples := stbl.Stsz.SampleNumber
	// Build [startSampleNr, endSampleNr] intervals (1-based, inclusive).
	intervals := make([][2]uint32, len(syncStarts))
	for i, sn := range syncStarts {
		startNr := sn
		var endNr uint32
		if i == len(syncStarts)-1 {
			endNr = totSamples
			if winEnd > 0 {
				// Trim by window end: find last sample with PTS < winEnd.
				endNr = lastSampleBeforePts(stbl, winEnd, totSamples)
			}
		} else {
			endNr = syncStarts[i+1] - 1
		}
		if endNr < startNr {
			endNr = startNr
		}
		intervals[i] = [2]uint32{startNr, endNr}
	}

	mdat := file.Mdat
	if mdat == nil {
		return e("reason", "no mdat in progressive input")
	}
	mdatPayloadStart := mdat.PayloadAbsoluteOffset()
	lazy := mdat.GetLazyDataSize() > 0

	streamIdx := 0
	h := goavpipe.Globals.GetNextFD()
	for i, iv := range intervals {
		segIdx := startSegNr + i
		fullSamples, err := readSamplesProgressive(stbl, mdat, mdatPayloadStart, lazy, rs, iv[0], iv[1])
		if err != nil {
			return e(err, "reason", "read samples for segment", "seg", segIdx)
		}
		if len(fullSamples) == 0 {
			continue
		}
		init, err := buildInitFromProgressive(file, trak)
		if err != nil {
			return e(err)
		}
		first := fullSamples[0]
		if err = writeOutput(out, h, streamIdx, segIdx, int64(first.PresentationTime()),
			goavpipe.FMP4VideoSegment, func(w io.Writer) error {
				if err := init.Encode(w); err != nil {
					return err
				}
				return encodeMediaSegment(w, nil, uint32(segIdx), init.Moov.Trak.Tkhd.TrackID, fullSamples)
			}); err != nil {
			return e(err, "reason", "write mez segment", "seg", segIdx)
		}
	}
	return nil
}

func segmentSyncStarts(stbl *mp4.StblBox, segDurTicks, winStart, winEnd uint64) []uint32 {
	stts := stbl.Stts
	stss := stbl.Stss
	ctts := stbl.Ctts
	var nextStart uint64
	if winStart > 0 {
		nextStart = winStart
	}
	out := make([]uint32, 0, len(stss.SampleNumber))
	for _, sampleNr := range stss.SampleNumber {
		decTime, _ := stts.GetDecodeTime(sampleNr)
		pts := int64(decTime)
		if ctts != nil {
			pts += int64(ctts.GetCompositionTimeOffset(sampleNr))
		}
		if pts < 0 {
			pts = 0
		}
		uPts := uint64(pts)
		if uPts < winStart {
			continue
		}
		if winEnd > 0 && uPts >= winEnd {
			break
		}
		if uPts >= nextStart {
			out = append(out, sampleNr)
			if segDurTicks == 0 {
				nextStart = uPts + 1
			} else {
				for uPts >= nextStart {
					nextStart += segDurTicks
				}
			}
		}
	}
	return out
}

func lastSampleBeforePts(stbl *mp4.StblBox, ptsEnd uint64, totSamples uint32) uint32 {
	stts := stbl.Stts
	ctts := stbl.Ctts
	var last uint32 = totSamples
	for n := uint32(1); n <= totSamples; n++ {
		dec, _ := stts.GetDecodeTime(n)
		pts := int64(dec)
		if ctts != nil {
			pts += int64(ctts.GetCompositionTimeOffset(n))
		}
		if pts < 0 {
			pts = 0
		}
		if uint64(pts) >= ptsEnd {
			return last
		}
		last = n
	}
	return last
}

func readSamplesProgressive(stbl *mp4.StblBox, mdat *mp4.MdatBox, mdatPayloadStart uint64,
	lazy bool, rs io.ReadSeeker, startNr, endNr uint32) ([]mp4.FullSample, error) {

	if endNr < startNr {
		return nil, nil
	}
	samples := make([]mp4.FullSample, 0, endNr-startNr+1)
	for sampleNr := startNr; sampleNr <= endNr; sampleNr++ {
		chunkNr, sampleNrAtChunkStart, err := stbl.Stsc.ChunkNrFromSampleNr(int(sampleNr))
		if err != nil {
			return nil, err
		}
		var offset int64
		if stbl.Stco != nil {
			offset = int64(stbl.Stco.ChunkOffset[chunkNr-1])
		} else if stbl.Co64 != nil {
			offset = int64(stbl.Co64.ChunkOffset[chunkNr-1])
		} else {
			return nil, fmt.Errorf("no stco/co64 in stbl")
		}
		for sNr := sampleNrAtChunkStart; sNr < int(sampleNr); sNr++ {
			offset += int64(stbl.Stsz.GetSampleSize(sNr))
		}
		size := stbl.Stsz.GetSampleSize(int(sampleNr))
		decTime, dur := stbl.Stts.GetDecodeTime(sampleNr)
		var cto int32
		if stbl.Ctts != nil {
			cto = stbl.Ctts.GetCompositionTimeOffset(sampleNr)
		}
		var data []byte
		if lazy {
			if _, err = rs.Seek(offset, io.SeekStart); err != nil {
				return nil, err
			}
			data = make([]byte, size)
			if _, err = io.ReadFull(rs, data); err != nil {
				return nil, err
			}
		} else {
			off := uint64(offset) - mdatPayloadStart
			data = mdat.Data[off : off+uint64(size)]
		}

		samples = append(samples, mp4.FullSample{
			Sample: mp4.Sample{
				Flags:                 translateSampleFlags(stbl, sampleNr),
				Size:                  size,
				Dur:                   dur,
				CompositionTimeOffset: cto,
			},
			DecodeTime: decTime,
			Data:       data,
		})
	}
	return samples, nil
}

func translateSampleFlags(stbl *mp4.StblBox, sampleNr uint32) uint32 {
	var sf mp4.SampleFlags
	if stbl.Stss != nil {
		isSync := stbl.Stss.IsSyncSample(sampleNr)
		sf.SampleIsNonSync = !isSync
		if isSync {
			sf.SampleDependsOn = 2 // does not depend on others
		}
	}
	if stbl.Sdtp != nil && int(sampleNr-1) < len(stbl.Sdtp.Entries) {
		entry := stbl.Sdtp.Entries[sampleNr-1]
		sf.IsLeading = entry.IsLeading()
		sf.SampleDependsOn = entry.SampleDependsOn()
		sf.SampleHasRedundancy = entry.SampleHasRedundancy()
		sf.SampleIsDependedOn = entry.SampleIsDependedOn()
	}
	return sf.Encode()
}

func buildInitFromProgressive(file *mp4.File, trak *mp4.TrakBox) (*mp4.InitSegment, error) {
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = file.Moov.Mvhd.Timescale
	init.Moov.Mvex.AddChild(&mp4.MehdBox{FragmentDuration: int64(file.Moov.Mvhd.Duration)})
	outTrak := init.AddEmptyTrack(trak.Mdia.Mdhd.Timescale, "video", trak.Mdia.Mdhd.GetLanguage())
	inStsd := trak.Mdia.Minf.Stbl.Stsd
	outStsd := outTrak.Mdia.Minf.Stbl.Stsd
	switch {
	case inStsd.AvcX != nil:
		outStsd.AddChild(inStsd.AvcX)
	case inStsd.HvcX != nil:
		outStsd.AddChild(inStsd.HvcX)
	default:
		return nil, fmt.Errorf("unsupported video sample entry; expected avc1/avc3 or hvc1/hev1")
	}
	// Copy width/height from input tkhd into the new trak.
	outTrak.Tkhd.Width = trak.Tkhd.Width
	outTrak.Tkhd.Height = trak.Tkhd.Height
	return init, nil
}

// --- fragmented mez ----------------------------------------------------------

func makeMezFromFragmented(file *mp4.File, params *goavpipe.XcParams,
	segDurSec float64, startSegNr int, out goavpipe.OutputOpener) error {

	e := errors.Template("mp4e.MakeMezPart", errors.K.Invalid.Default())
	if file.Init == nil {
		return e("reason", "fragmented input is missing an init segment")
	}
	trak, err := pickVideoTrak(file.Init.Moov, params.StreamId)
	if err != nil {
		return e(err)
	}
	timescale := trak.Mdia.Mdhd.Timescale
	segDurTicks := uint64(segDurSec * float64(timescale))
	winStart, winEnd := windowTicks(params, timescale)

	var trex *mp4.TrexBox
	if file.Init.Moov.Mvex != nil {
		if t, ok := file.Init.Moov.Mvex.GetTrex(trak.Tkhd.TrackID); ok {
			trex = t
		} else {
			trex = file.Init.Moov.Mvex.Trex
		}
	}

	samples := make([]mp4.FullSample, 0, 1024)
	for _, seg := range file.Segments {
		for _, frag := range seg.Fragments {
			if frag.Moof == nil || frag.Moof.Traf == nil {
				continue
			}
			if frag.Moof.Traf.Tfhd != nil && frag.Moof.Traf.Tfhd.TrackID != trak.Tkhd.TrackID {
				continue
			}
			fs, gErr := frag.GetFullSamples(trex)
			if gErr != nil {
				return e(gErr, "reason", "GetFullSamples failed")
			}
			samples = append(samples, fs...)
		}
	}
	if len(samples) == 0 {
		return e("reason", "no samples in input")
	}

	// Window-trim samples by PTS.
	if winStart > 0 || winEnd > 0 {
		filtered := samples[:0]
		for _, s := range samples {
			pts := uint64(s.PresentationTime())
			if pts < winStart {
				continue
			}
			if winEnd > 0 && pts >= winEnd {
				break
			}
			filtered = append(filtered, s)
		}
		samples = filtered
		if len(samples) == 0 {
			return e("reason", "no samples in requested window")
		}
	}

	// Group into ~segDurTicks blocks, breaking at sync samples.
	streamIdx := 0
	segIdx := startSegNr
	startSampleIdx := 0
	chunkBoundary := uint64(samples[0].PresentationTime()) + segDurTicks
	h := goavpipe.Globals.GetNextFD()

	flush := func(endExclusive int) error {
		if endExclusive <= startSampleIdx {
			return nil
		}
		first := samples[startSampleIdx]
		return writeOutput(out, h, streamIdx, segIdx, int64(first.PresentationTime()),
			goavpipe.FMP4VideoSegment, func(w io.Writer) error {
				if err := file.Init.Encode(w); err != nil {
					return err
				}
				return encodeMediaSegment(w, nil, uint32(segIdx), trak.Tkhd.TrackID,
					samples[startSampleIdx:endExclusive])
			})
	}

	for i, s := range samples {
		if i == 0 {
			continue
		}
		if !mp4.IsSyncSampleFlags(s.Flags) {
			continue
		}
		if uint64(s.PresentationTime()) < chunkBoundary {
			continue
		}
		if err = flush(i); err != nil {
			return e(err, "reason", "write mez segment", "seg", segIdx)
		}
		startSampleIdx = i
		segIdx++
		for uint64(s.PresentationTime()) >= chunkBoundary {
			chunkBoundary += segDurTicks
		}
	}
	if err = flush(len(samples)); err != nil {
		return e(err, "reason", "write final mez segment")
	}
	return nil
}

// --- shared helpers ----------------------------------------------------------

func validateParams(params *goavpipe.XcParams) error {
	if params == nil {
		return fmt.Errorf("params is nil")
	}
	if params.Url == "" {
		return fmt.Errorf("url is empty")
	}
	if params.XcType != goavpipe.XcVideo {
		return fmt.Errorf("unsupported xc_type %v; only XcVideo is supported in v1", params.XcType)
	}
	return nil
}

func startSegmentNr(s string, dflt int) (int, error) {
	if s == "" {
		return dflt, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid start_segment_str %q: %w", s, err)
	}
	if n <= 0 {
		return dflt, nil
	}
	return n, nil
}

func windowTicks(params *goavpipe.XcParams, _ uint32) (start, end uint64) {
	if params.StartTimeTs > 0 {
		start = uint64(params.StartTimeTs)
	}
	if params.DurationTs > 0 {
		end = start + uint64(params.DurationTs)
	}
	return
}

func abrChunkDurTicks(params *goavpipe.XcParams, timescale uint32) (uint64, error) {
	if params.VideoSegDurationTs > 0 {
		return uint64(params.VideoSegDurationTs), nil
	}
	if params.SegDuration != "" {
		secs, err := strconv.ParseFloat(params.SegDuration, 64)
		if err != nil || secs <= 0 {
			return 0, fmt.Errorf("invalid seg_duration %q", params.SegDuration)
		}
		return uint64(secs * float64(timescale)), nil
	}
	return 0, fmt.Errorf("neither video_seg_duration_ts nor seg_duration is set")
}

func pickVideoTrak(moov *mp4.MoovBox, streamID int32) (*mp4.TrakBox, error) {
	if moov == nil {
		return nil, fmt.Errorf("no moov")
	}
	var candidates []*mp4.TrakBox
	for _, t := range moov.Traks {
		if t.Mdia == nil || t.Mdia.Hdlr == nil {
			continue
		}
		if t.Mdia.Hdlr.HandlerType != "vide" {
			continue
		}
		if streamID > 0 && t.Tkhd != nil && int32(t.Tkhd.TrackID) != streamID {
			continue
		}
		candidates = append(candidates, t)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no video track found (streamID=%d)", streamID)
	}
	if len(candidates) > 1 && streamID <= 0 {
		// Pick the first by trackID for determinism.
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Tkhd.TrackID < candidates[j].Tkhd.TrackID
		})
	}
	return candidates[0], nil
}

func encodeMediaSegment(w io.Writer, styp *mp4.StypBox, seqNr, trackID uint32,
	samples []mp4.FullSample) error {

	var seg *mp4.MediaSegment
	if styp != nil {
		seg = mp4.NewMediaSegmentWithStyp(styp)
	} else {
		seg = mp4.NewMediaSegment()
	}
	frag, err := mp4.CreateFragment(seqNr, trackID)
	if err != nil {
		return err
	}
	seg.AddFragment(frag)
	for i := range samples {
		if err = frag.AddFullSampleToTrack(samples[i], trackID); err != nil {
			return err
		}
	}
	return seg.Encode(w)
}

// --- IO adapters -------------------------------------------------------------

// inputReadSeekCloser adapts a goavpipe.InputHandler to io.ReadSeeker + Close.
// It translates the avpipe EOF convention (n=0, err=nil) to io.EOF so that
// mp4ff's box decoder terminates correctly.
type inputReadSeekCloser struct {
	h goavpipe.InputHandler
}

func (a *inputReadSeekCloser) Read(p []byte) (int, error) {
	n, err := a.h.Read(p)
	if err == nil && n == 0 && len(p) > 0 {
		return 0, io.EOF
	}
	return n, err
}

func (a *inputReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	return a.h.Seek(offset, whence)
}

func (a *inputReadSeekCloser) Close() error { return a.h.Close() }

func openInput(url string) (*inputReadSeekCloser, error) {
	opener := goavpipe.GetInputOpener(url)
	if opener == nil {
		return nil, fmt.Errorf("no input opener registered for url %q", url)
	}
	fd := goavpipe.Globals.GetNextFD()
	h, err := opener.Open(fd, url)
	if err != nil {
		return nil, fmt.Errorf("open input %q: %w", url, err)
	}
	return &inputReadSeekCloser{h: h}, nil
}

// writeOutput opens a fresh OutputHandler for the given segment and writes to
// it through the user-supplied callback. The handler is closed at the end.
func writeOutput(opener goavpipe.OutputOpener, h int64, streamIdx, segIdx int,
	pts int64, outType goavpipe.AVType, write func(io.Writer) error) error {

	fd := goavpipe.Globals.GetNextFD()
	oh, err := opener.Open(h, fd, streamIdx, segIdx, pts, outType)
	if err != nil {
		return fmt.Errorf("open output %v seg %d: %w", outType, segIdx, err)
	}
	defer oh.Close()
	// Buffer the segment so a single Write call hits the output handler with a
	// full payload (mp4ff writes many small boxes; this avoids per-box syscalls
	// and gives stream handlers the freedom to treat each segment atomically).
	var buf bytes.Buffer
	if err = write(&buf); err != nil {
		return fmt.Errorf("encode output %v seg %d: %w", outType, segIdx, err)
	}
	if _, err = oh.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write output %v seg %d: %w", outType, segIdx, err)
	}
	return nil
}
