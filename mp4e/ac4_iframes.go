package mp4e

import (
	"io"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe/codec/ac4"
	"github.com/eluv-io/errors-go"
)

// tocReadBytes is how many leading bytes of a progressive AC-4 sample we read to parse
// the ac4_toc prefix. The prefix ends at b_iframe_global within ~3 bytes; 64 leaves ample
// room for the bitstream_version variable_bits escape while keeping per-sample reads tiny.
const tocReadBytes = 64

// AC4SampleSync is one AC-4 sample's I-frame status, pairing the authoritative bitstream
// flag against the container's view.
type AC4SampleSync struct {
	SampleNumber  int    // 1-based sample number
	DecodeTime    uint64 // absolute decode time in the track's mdhd timescale
	IFrameGlobal  bool   // ac4_toc.b_iframe_global (bitstream truth)
	ContainerSync bool   // container sync flag: stss membership / !sample_is_non_sync_sample

	// PresentationTime is DecodeTime mapped through the track's edit list — DecodeTime
	// minus AC4Edit.MediaTime — in the mdhd timescale. It equals DecodeTime when no edit
	// was applied. AC-4 is audio, so there are no composition offsets and this is the
	// whole of the media-to-presentation mapping; it is NOT mp4ff's
	// FullSample.PresentationTime (decode time plus composition offset, edit-unaware).
	//
	// It is negative for a sample the edit trims at the head, which is deliberate: it is
	// the same value the mov demuxer reports as the packet pts for such a sample, so the
	// two implementations can be compared directly.
	PresentationTime int64

	// InEdit reports whether any part of the sample is presented, i.e. whether the edit
	// list maps it into the presentation timeline at all. False means the sample is
	// trimmed away entirely — at the head (a priming frame) or past the edit's end.
	// Always true when no edit was applied.
	InEdit bool
}

// AC4Edit is a track's edit list reduced to the single head/tail trim that ISOBMFF audio
// uses in practice: media_time trims at the head, media_time + segment_duration bounds the
// tail. Nothing else is applied.
//
// The trim is what a Dolby encoder emits to remove the AC-4 priming frame (DEE's
// "offset": -2048 becomes elst {segment_duration=480000, media_time=2048}), so applying it
// is what makes the results describe presented audio rather than stored samples.
type AC4Edit struct {
	// Present reports that the track has an edts/elst holding at least one entry.
	Present bool

	// Applied reports that the edit list was a single simple forward mapping and was used
	// to compute AC4SampleSync.PresentationTime/InEdit and the track's trim counters.
	// When Present is true and Applied is false the edit was left unapplied and every
	// sample reads as in-edit — see Unapplied for why. Check Present && !Applied rather
	// than assuming an unapplied edit means the source had none.
	Applied bool

	// Unapplied says why a present edit list was not applied (empty when Applied). An
	// edit list this code declines to interpret is reported, never silently ignored:
	// treating it as absent would produce results that look clean but describe stored
	// samples while claiming to describe presented ones.
	Unapplied string

	// MediaTime is the elst entry's media_time in the mdhd timescale: the first presented
	// media time, and hence how much is trimmed at the head. 0 for movenc's identity edit.
	MediaTime int64

	// Duration is the elst entry's segment_duration converted from the movie (mvhd)
	// timescale to the mdhd timescale, i.e. the presented duration. 0 means unbounded: no
	// tail trim.
	//
	// 14496-12 does not define 0 as unbounded; the justification is that a fragmented
	// init segment cannot know its own duration, so a muxer writes 0 — movenc emits the
	// identity entry {segment_duration=0, media_time=0} for every fragmented output,
	// including every avpipe DASH init. Reading that 0 as a bound would trim the entire
	// track away.
	Duration uint64
}

// primingBasis values for AC4Track.PrimingBasis.
const (
	// PrimingFromEdit means PrimingSamples came from the edit list: definitive.
	PrimingFromEdit = "edit-list"

	// PrimingFromCadence means PrimingSamples came from the I-frame cadence heuristic
	// (see AC4Track.PrimingBasis): suggestive only.
	PrimingFromCadence = "iframe-cadence"
)

// AC4Track holds the I-frame results for one AC-4 track.
type AC4Track struct {
	TrackID int

	// SamplesProcessed is the number of samples parsed for this track before the
	// I-frame limit stopped the scan (every sample when maxIFramesPerTrack <= 0). The
	// mismatch counters below are over exactly these samples, not the whole track.
	SamplesProcessed int

	// ContainerSyncNotIFrame counts processed samples the container flags as sync
	// (stss membership / !sample_is_non_sync_sample) that are NOT real I-frames
	// (b_iframe_global == 0): container over-marking, e.g. a muxer flagging every audio
	// sample sync. This is the mismatch seen even in Dolby's own fragmented test asset.
	ContainerSyncNotIFrame int

	// IFrameNotContainerSync counts processed samples that ARE real I-frames
	// (b_iframe_global == 1) but the container does NOT flag as sync: a random access
	// point missing from the container signaling.
	IFrameNotContainerSync int

	// FrameErrors is the number of samples that could not be read or whose ac4_toc could
	// not be parsed. Such samples are skipped and the scan continues, so they are not
	// counted in SamplesProcessed or the mismatch counters. A whole track/file only fails
	// (non-nil error from AC4IFrames) when it cannot be decoded at all.
	FrameErrors int

	// IFrames holds the true I-frames, up to the requested limit. Samples the edit list
	// trims away are included with InEdit false rather than omitted, and they count
	// against the limit — the mismatch this package exists to find is between what a file
	// stores and what it claims, so nothing is filtered out of the raw results. Filter on
	// InEdit to get the presented I-frames.
	IFrames []AC4SampleSync

	// Edit is the track's edit list as interpreted, including whether it was applied.
	Edit AC4Edit

	// SamplesInEdit and SamplesTrimmed split SamplesProcessed by whether the edit list
	// presents the sample. SamplesTrimmed is 0 unless Edit.Applied and the edit actually
	// trims something; SamplesInEdit is the presented frame count, which is what a
	// segment's duration should be derived from.
	SamplesInEdit  int
	SamplesTrimmed int

	// PrimingSamples is the number of whole samples the head trim removes — the AC-4
	// priming frame(s). PrimingBasis says where the number came from: PrimingFromEdit
	// when the edit list gave it (definitive), PrimingFromCadence when it was inferred
	// from the I-frame cadence (suggestive; see below), and "" when no priming was found,
	// in which case PrimingSamples is 0.
	//
	// The cadence heuristic exists for sources that lost their edit list — notably an
	// avpipe fmp4-segment mez, which carries no edts at all, so a priming frame written
	// as ordinary content is otherwise indistinguishable from real audio. A Dolby encoder
	// places the cadence-anchoring I-frame immediately after the priming frame, so two
	// I-frames one sample apart followed by the real interval is the signature. It
	// requires at least three I-frames and a second gap greater than one, so a stream in
	// which every frame is an I-frame does not read as primed.
	PrimingSamples int
	PrimingBasis   string

	// PartialHeadTrim reports that MediaTime falls inside a sample rather than on a
	// boundary, so the head trim is sub-sample. That sample is reported in-edit (whole)
	// with a negative PresentationTime, because neither ISOBMFF output nor this scan can
	// express a partial sample. The mov demuxer takes the same position, keeping the
	// sample and attaching AV_PKT_DATA_SKIP_SAMPLES. A Dolby encoder does not produce it.
	PartialHeadTrim bool
}

// AC4IFrames parses ac4_toc per sample for every AC-4 track in an MP4/fMP4 file and
// returns, per track, the positions whose b_iframe_global is set — the true I-frames —
// plus counters of where the container sync signaling disagrees with the bitstream
// (AC4Track.ContainerSyncNotIFrame / IFrameNotContainerSync), tallied over the samples
// processed before the limit. maxIFramesPerTrack <= 0 means unlimited (whole track).
//
// It always runs the scan to completion when it can: a sample that cannot be read or
// parsed is counted in AC4Track.FrameErrors and skipped, never aborting the job. The
// returned error is non-nil only for a failure that prevents processing — the file
// cannot be decoded, or a fragment's data cannot be read — and even then the per-track
// results gathered so far are returned alongside it.
//
// It also applies the track's edit list, so the results describe presented audio and not
// merely stored samples: each AC4SampleSync carries a PresentationTime and an InEdit flag,
// and AC4Track reports the edit itself (AC4Track.Edit), the presented/trimmed sample split,
// and how many priming frames the track begins with (AC4Track.PrimingSamples). Samples the
// edit trims are flagged, never dropped from the results.
//
// It decodes with DecModeLazyMdat and reads sample bytes on demand from rs (progressive:
// per-sample ranges; fragmented: one fragment's mdat at a time), stopping each track once
// it reaches the limit, so it never loads the whole input into memory.
func AC4IFrames(rs io.ReadSeeker, maxIFramesPerTrack int) ([]AC4Track, error) {
	e := errors.T("mp4e.AC4IFrames", errors.K.Invalid.Default())
	file, err := mp4.DecodeFile(rs, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return nil, e(err, "reason", "decode ac4 file")
	}
	if file.Moov == nil {
		return nil, e("reason", "no moov box")
	}
	if file.IsFragmented() {
		return ac4FramesFragmented(file, rs, maxIFramesPerTrack)
	}
	return ac4FramesProgressive(file, rs, maxIFramesPerTrack)
}

// ac4SampleClass is how a track's edit list treats one sample.
type ac4SampleClass int

const (
	ac4InEdit      ac4SampleClass = iota // presented (at least partly)
	ac4TrimmedHead                       // wholly before the edit start: priming frame
	ac4TrimmedTail                       // at or past the edit end
)

// ac4SampleEdit is the edit list's verdict on one sample.
type ac4SampleEdit struct {
	presentationTime int64
	class            ac4SampleClass
	partialHead      bool // the edit start falls inside this sample
}

// ac4EditWindow is a parsed edit reduced to what classifying a sample needs.
type ac4EditWindow struct {
	applied   bool
	mediaTime int64
	editEnd   int64 // exclusive presented end, media timescale; valid only if bounded
	bounded   bool
}

// classify maps one sample's decode interval through the edit. A sample is presented if any
// part of it falls inside the edit, which is what both ISOBMFF and the mov demuxer do: a
// sample straddling either boundary is kept whole, since neither a container nor this scan
// can represent a fraction of a sample.
func (w ac4EditWindow) classify(decodeTime uint64, dur uint32) ac4SampleEdit {
	start := int64(decodeTime)
	if !w.applied {
		return ac4SampleEdit{presentationTime: start, class: ac4InEdit}
	}
	end := start + int64(dur)
	res := ac4SampleEdit{presentationTime: start - w.mediaTime}
	switch {
	case start < w.mediaTime && end <= w.mediaTime:
		res.class = ac4TrimmedHead
	case w.bounded && start >= w.editEnd:
		res.class = ac4TrimmedTail
	default:
		res.class = ac4InEdit
		res.partialHead = start < w.mediaTime // kept, but its head is trimmed
	}
	return res
}

// parseAC4Edit reads a track's edit list and reduces it to the head/tail trim this scan
// applies, returning both the reportable result and the classifier. An edit list it will
// not interpret comes back Present with Unapplied set and a zero (inactive) window.
func parseAC4Edit(trak *mp4.TrakBox, movieTimescale uint32) (AC4Edit, ac4EditWindow) {
	var edit AC4Edit
	if trak.Edts == nil {
		return edit, ac4EditWindow{}
	}
	var entries []mp4.ElstEntry
	for _, elst := range trak.Edts.Elst {
		entries = append(entries, elst.Entries...)
	}
	if len(entries) == 0 {
		return edit, ac4EditWindow{}
	}
	edit.Present = true

	if len(entries) > 1 {
		// Multi-entry lists express dwells, gaps and reordering; interpreting them as a
		// single trim would be wrong, so report rather than guess.
		edit.Unapplied = "multiple edit list entries"
		return edit, ac4EditWindow{}
	}
	en := entries[0]
	switch {
	case en.MediaTime < 0:
		// media_time -1 is an empty edit: it delays presentation, it does not trim.
		edit.Unapplied = "empty edit (media_time -1)"
	case en.MediaRateInteger != 1 || en.MediaRateFraction != 0:
		edit.Unapplied = "media rate is not 1.0"
	}
	if edit.Unapplied != "" {
		return edit, ac4EditWindow{}
	}

	var mediaTimescale uint32
	if trak.Mdia != nil && trak.Mdia.Mdhd != nil {
		mediaTimescale = trak.Mdia.Mdhd.Timescale
	}
	edit.MediaTime = en.MediaTime
	edit.Duration = ac4ScaleToMedia(en.SegmentDuration, movieTimescale, mediaTimescale)
	edit.Applied = true

	w := ac4EditWindow{applied: true, mediaTime: en.MediaTime}
	if edit.Duration > 0 { // 0 = unbounded, per ISOBMFF and movenc's identity edit
		w.bounded = true
		w.editEnd = en.MediaTime + int64(edit.Duration)
	}
	return edit, w
}

// ac4ScaleToMedia converts an elst segment_duration from the movie (mvhd) timescale to the
// track's media (mdhd) timescale, rounding to nearest so an inexact ratio cannot trim a
// sample that should be presented.
//
// No asset on hand exercises the conversion itself: the two files that carry an edit list
// have mvhd timescale == mdhd timescale == 48000, making this a no-op. It is not
// hypothetical, though — media/Audio_ID_720p_50fps_h264_514ch_192kbps_ac4_fra.mp4 has mvhd
// 1000 against mdhd 48000 and would be trimmed 48x early if the timescales were conflated;
// it simply has no edts. Hence the unit test on this function rather than on an asset.
func ac4ScaleToMedia(d uint64, movieTimescale, mediaTimescale uint32) uint64 {
	if d == 0 || movieTimescale == 0 || mediaTimescale == 0 ||
		movieTimescale == mediaTimescale {
		return d
	}
	return (d*uint64(mediaTimescale) + uint64(movieTimescale)/2) / uint64(movieTimescale)
}

// ac4RecordSampleEdit tallies one processed sample against the edit. PrimingSamples counts
// the head-trimmed samples directly; ac4FinalizePriming turns that into a basis. Only
// processed samples are tallied, so SamplesInEdit + SamplesTrimmed == SamplesProcessed and
// a sample counted in FrameErrors appears in none of the three.
func ac4RecordSampleEdit(track *AC4Track, res ac4SampleEdit) {
	if res.partialHead {
		track.PartialHeadTrim = true
	}
	switch res.class {
	case ac4InEdit:
		track.SamplesInEdit++
	default:
		track.SamplesTrimmed++
		if res.class == ac4TrimmedHead {
			track.PrimingSamples++
		}
	}
}

// ac4FinalizePriming settles how many priming frames the track begins with, preferring the
// edit list and falling back to the I-frame cadence. Call once per track, after the scan.
func ac4FinalizePriming(track *AC4Track) {
	if track.PrimingSamples > 0 {
		track.PrimingBasis = PrimingFromEdit // the edit list said so: definitive
		return
	}
	if ac4CadencePriming(track.IFrames) {
		track.PrimingSamples = 1
		track.PrimingBasis = PrimingFromCadence
	}
}

// ac4CadencePriming reports the priming signature in a track's I-frame list: sample 1 is an
// I-frame, sample 2 is the next one, and the I-frame after that is more than one sample
// later. The middle condition is the priming frame's cadence anchor; the last one keeps a
// stream whose every frame is an I-frame from reading as primed. Requiring three I-frames
// also keeps a limit-truncated scan from firing it on too little evidence.
//
// Suggestive only — it is a cadence, not a signal. Use it just for sources whose edit list
// is gone (an avpipe fmp4-segment mez writes no edts), never in place of one that is there.
func ac4CadencePriming(iframes []AC4SampleSync) bool {
	if len(iframes) < 3 || iframes[0].SampleNumber != 1 {
		return false
	}
	return iframes[1].SampleNumber-iframes[0].SampleNumber == 1 &&
		iframes[2].SampleNumber-iframes[1].SampleNumber > 1
}

func ac4FramesProgressive(file *mp4.File, rs io.ReadSeeker, max int) ([]AC4Track, error) {
	var movieTimescale uint32
	if file.Moov.Mvhd != nil {
		movieTimescale = file.Moov.Mvhd.Timescale
	}
	var out []AC4Track
	for _, trak := range file.Moov.Traks {
		if !isAC4Trak(trak) {
			continue
		}
		stbl := trak.Mdia.Minf.Stbl
		track := AC4Track{TrackID: int(trak.Tkhd.TrackID)}
		var window ac4EditWindow
		track.Edit, window = parseAC4Edit(trak, movieTimescale)
		nrSamples := stbl.Stsz.GetNrSamples()
		var decodeTime uint64
		for nr := uint32(1); nr <= nrSamples; nr++ {
			// decodeTime must advance for every sample, including skipped ones, so
			// capture the duration before any continue.
			dur := stbl.Stts.GetDur(nr)

			data, err := readProgressiveSample(rs, trak, nr)
			if err != nil {
				track.FrameErrors++ // unreadable sample: skip, keep going
				decodeTime += uint64(dur)
				continue
			}
			toc, err := ac4.ParseTOC(data)
			if err != nil {
				track.FrameErrors++ // unparseable ac4_toc: skip, keep going
				decodeTime += uint64(dur)
				continue
			}
			containerSync := stbl.Stss == nil || stbl.Stss.IsSyncSample(nr)
			edit := window.classify(decodeTime, dur)
			track.SamplesProcessed++
			ac4CountMismatch(&track, toc.IFrameGlobal, containerSync)
			ac4RecordSampleEdit(&track, edit)
			if toc.IFrameGlobal {
				track.IFrames = append(track.IFrames, AC4SampleSync{
					SampleNumber:     int(nr),
					DecodeTime:       decodeTime,
					IFrameGlobal:     true,
					ContainerSync:    containerSync,
					PresentationTime: edit.presentationTime,
					InEdit:           edit.class == ac4InEdit,
				})
				if max > 0 && len(track.IFrames) >= max {
					break
				}
			}
			decodeTime += uint64(dur)
		}
		ac4FinalizePriming(&track)
		out = append(out, track)
	}
	return out, nil
}

// readProgressiveSample reads up to tocReadBytes of sample nr's data from a progressive
// track via the ReadSeeker (bounded, on-demand).
func readProgressiveSample(rs io.ReadSeeker, trak *mp4.TrakBox, nr uint32) ([]byte, error) {
	e := errors.T("mp4e.readProgressiveSample", errors.K.Invalid.Default())
	ranges, err := trak.GetRangesForSampleInterval(nr, nr)
	if err != nil {
		return nil, e(err, "reason", "sample data ranges", "sample", nr)
	}
	if len(ranges) == 0 {
		return nil, e("reason", "no data range for sample", "sample", nr)
	}
	readLen := int(ranges[0].Size)
	if readLen > tocReadBytes {
		readLen = tocReadBytes
	}
	return readRange(rs, int64(ranges[0].Offset), readLen)
}

type ac4TrackState struct {
	track      AC4Track
	nextSample int // 1-based running sample number across fragments
	window     ac4EditWindow
}

func ac4FramesFragmented(file *mp4.File, rs io.ReadSeeker, max int) ([]AC4Track, error) {
	var movieTimescale uint32
	if file.Moov.Mvhd != nil {
		movieTimescale = file.Moov.Mvhd.Timescale
	}
	var order []uint32
	trexByID := map[uint32]*mp4.TrexBox{}
	states := map[uint32]*ac4TrackState{}
	for _, trak := range file.Moov.Traks {
		if !isAC4Trak(trak) {
			continue
		}
		id := trak.Tkhd.TrackID
		order = append(order, id)
		st := &ac4TrackState{track: AC4Track{TrackID: int(id)}, nextSample: 1}
		// A fragmented file's edit list lives in the init segment's trak, so it applies
		// across every fragment; sample decode times are absolute (tfdt/trun), so the
		// same classifier works unchanged.
		st.track.Edit, st.window = parseAC4Edit(trak, movieTimescale)
		states[id] = st
		if file.Moov.Mvex != nil {
			trexByID[id], _ = file.Moov.Mvex.GetTrex(id)
		}
	}
	if len(order) == 0 {
		return nil, nil
	}

	e := errors.T("mp4e.ac4FramesFragmented", errors.K.Invalid.Default())
	var firstErr error // first structural (whole-fragment) failure; results still returned
	done := map[uint32]bool{}
	remaining := len(order)
	for _, seg := range file.Segments {
		for _, frag := range seg.Fragments {
			if frag.Mdat == nil || frag.Moof == nil {
				continue
			}
			mdatLoaded := false
			for _, id := range order {
				if done[id] || !fragHasTrack(frag, id) {
					continue
				}
				// Ensure the fragment's mdat payload is in memory before reading samples
				// (lazy decode left it on disk). Load it at most once per fragment. A
				// fragment we cannot read is skipped (its frames are omitted) so the scan
				// completes the rest of the file; the first such error is returned.
				if !mdatLoaded && frag.Mdat.IsLazy() {
					data, err := frag.Mdat.ReadData(
						int64(frag.Mdat.PayloadAbsoluteOffset()),
						int64(frag.Mdat.GetLazyDataSize()), rs)
					if err != nil {
						if firstErr == nil {
							firstErr = e(err, "reason", "read fragment mdat")
						}
						break // cannot read this fragment for any track
					}
					frag.Mdat.SetData(data)
					mdatLoaded = true
				}
				samples, err := frag.GetFullSamples(trexByID[id])
				if err != nil {
					if firstErr == nil {
						firstErr = e(err, "reason", "read fragment samples", "track", id)
					}
					continue
				}
				if ac4AppendFragmentSamples(states[id], samples, max) {
					done[id] = true
					remaining--
				}
			}
			if remaining == 0 {
				break
			}
		}
		if remaining == 0 {
			break
		}
	}

	out := make([]AC4Track, 0, len(order))
	for _, id := range order {
		ac4FinalizePriming(&states[id].track)
		out = append(out, states[id].track)
	}
	return out, firstErr
}

// fragHasTrack reports whether a fragment carries samples for trackID, checked from the
// moof traf headers only (no sample-data read).
func fragHasTrack(frag *mp4.Fragment, id uint32) bool {
	for _, traf := range frag.Moof.Trafs {
		if traf.Tfhd != nil && traf.Tfhd.TrackID == id {
			return true
		}
	}
	return false
}

// ac4AppendFragmentSamples appends the I-frames from one fragment's samples, advancing the
// track's running sample number. It returns true once the track reaches the limit.
func ac4AppendFragmentSamples(st *ac4TrackState, samples []mp4.FullSample, max int) bool {
	for i := range samples {
		s := &samples[i]
		nr := st.nextSample
		st.nextSample++
		toc, err := ac4.ParseTOC(s.Data)
		if err != nil {
			// A malformed frame should not silently drop the whole track; count and skip.
			st.track.FrameErrors++
			continue
		}
		// Container sync is the raw sample_is_non_sync_sample bit, NOT Sample.IsSync().
		// Sample.IsSync() also requires sample_depends_on == 2 (the video "I-picture,
		// depends on nothing" code), but AC-4 audio muxers set the sync bit while leaving
		// sample_depends_on == 0 ("unknown"). So Sample.IsSync() reports false for every
		// audio sample — including the over-marked ones this tool exists to catch — which
		// would invert the bitstream-vs-container comparison.
		containerSync := !mp4.DecodeSampleFlags(s.Flags).SampleIsNonSync
		edit := st.window.classify(s.DecodeTime, s.Dur)
		st.track.SamplesProcessed++
		ac4CountMismatch(&st.track, toc.IFrameGlobal, containerSync)
		ac4RecordSampleEdit(&st.track, edit)
		if !toc.IFrameGlobal {
			continue
		}
		st.track.IFrames = append(st.track.IFrames, AC4SampleSync{
			SampleNumber:     nr,
			DecodeTime:       s.DecodeTime,
			IFrameGlobal:     true,
			ContainerSync:    containerSync,
			PresentationTime: edit.presentationTime,
			InEdit:           edit.class == ac4InEdit,
		})
		if max > 0 && len(st.track.IFrames) >= max {
			return true
		}
	}
	return false
}

// ac4CountMismatch updates a track's sync-signaling mismatch counters for one processed
// sample: container over-marking (container says sync, bitstream says not an I-frame) and
// the reverse (a real I-frame the container does not flag as sync).
func ac4CountMismatch(track *AC4Track, iframeGlobal, containerSync bool) {
	switch {
	case containerSync && !iframeGlobal:
		track.ContainerSyncNotIFrame++
	case iframeGlobal && !containerSync:
		track.IFrameNotContainerSync++
	}
}

func isAC4Trak(trak *mp4.TrakBox) bool {
	if trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil ||
		trak.Mdia.Minf.Stbl.Stsd == nil {
		return false
	}
	for _, c := range trak.Mdia.Minf.Stbl.Stsd.Children {
		if ase, ok := c.(*mp4.AudioSampleEntryBox); ok && ase.Type() == "ac-4" {
			return true
		}
	}
	return false
}

func readRange(rs io.ReadSeeker, offset int64, size int) ([]byte, error) {
	if _, err := rs.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(rs, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
