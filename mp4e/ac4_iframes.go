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
}

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

	IFrames []AC4SampleSync // true I-frames, up to the requested limit
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

func ac4FramesProgressive(file *mp4.File, rs io.ReadSeeker, max int) ([]AC4Track, error) {
	var out []AC4Track
	for _, trak := range file.Moov.Traks {
		if !isAC4Trak(trak) {
			continue
		}
		stbl := trak.Mdia.Minf.Stbl
		track := AC4Track{TrackID: int(trak.Tkhd.TrackID)}
		nrSamples := stbl.Stsz.GetNrSamples()
		var decodeTime uint64
		for nr := uint32(1); nr <= nrSamples; nr++ {
			// decodeTime must advance for every sample, including skipped ones, so
			// capture the duration before any continue.
			dur := uint64(stbl.Stts.GetDur(nr))

			data, err := readProgressiveSample(rs, trak, nr)
			if err != nil {
				track.FrameErrors++ // unreadable sample: skip, keep going
				decodeTime += dur
				continue
			}
			toc, err := ac4.ParseTOC(data)
			if err != nil {
				track.FrameErrors++ // unparseable ac4_toc: skip, keep going
				decodeTime += dur
				continue
			}
			containerSync := stbl.Stss == nil || stbl.Stss.IsSyncSample(nr)
			track.SamplesProcessed++
			ac4CountMismatch(&track, toc.IFrameGlobal, containerSync)
			if toc.IFrameGlobal {
				track.IFrames = append(track.IFrames, AC4SampleSync{
					SampleNumber:  int(nr),
					DecodeTime:    decodeTime,
					IFrameGlobal:  true,
					ContainerSync: containerSync,
				})
				if max > 0 && len(track.IFrames) >= max {
					break
				}
			}
			decodeTime += dur
		}
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
}

func ac4FramesFragmented(file *mp4.File, rs io.ReadSeeker, max int) ([]AC4Track, error) {
	var order []uint32
	trexByID := map[uint32]*mp4.TrexBox{}
	states := map[uint32]*ac4TrackState{}
	for _, trak := range file.Moov.Traks {
		if !isAC4Trak(trak) {
			continue
		}
		id := trak.Tkhd.TrackID
		order = append(order, id)
		states[id] = &ac4TrackState{track: AC4Track{TrackID: int(id)}, nextSample: 1}
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
		st.track.SamplesProcessed++
		ac4CountMismatch(&st.track, toc.IFrameGlobal, containerSync)
		if !toc.IFrameGlobal {
			continue
		}
		st.track.IFrames = append(st.track.IFrames, AC4SampleSync{
			SampleNumber:  nr,
			DecodeTime:    s.DecodeTime,
			IFrameGlobal:  true,
			ContainerSync: containerSync,
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
