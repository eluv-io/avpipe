package mp4e

import (
	"math/big"

	"github.com/Eyevinn/mp4ff/mp4"
)

// frameRateMaxSamples bounds how many samples the frame-rate derivation reads
// before giving up on identifying a standard rate.
//
// It is a cap, not a target: most streams resolve in two samples, because the
// bound below narrows fast. avpipe muxes live video with +frag_every_frame, so
// a sample is a fragment, and the cap only binds when the track timescale is
// too coarse to tell two broadcast rates apart - in which case no number of
// samples would help.
const frameRateMaxSamples = 8

// standardFrameRates are the broadcast rates a measurement is resolved to.
// The NTSC rates sit 0.1% from their integer neighbours, which is why
// identifying one takes a bound rather than a nearest-match.
var standardFrameRates = []*big.Rat{
	big.NewRat(24000, 1001), // 23.976
	big.NewRat(24, 1),
	big.NewRat(25, 1),
	big.NewRat(30000, 1001), // 29.97
	big.NewRat(30, 1),
	big.NewRat(48, 1),
	big.NewRat(50, 1),
	big.NewRat(60000, 1001), // 59.94
	big.NewRat(60, 1),
}

// trackFrameRate derives a track's frame rate from the first fragments of a
// fragmented file, or nil when there are none.
//
// Nil is the answer for an init segment, for a track with no fragment of its
// own, and for a zero timescale - all ordinary, none an error: frame rate is an
// enrichment, and a caller that needs one has other sources.
func trackFrameRate(mp4Data *mp4.File, trackID, timescale int) *big.Rat {
	if timescale <= 0 || trackID <= 0 {
		return nil
	}

	var totalDur, samples uint64
	for _, seg := range mp4Data.Segments {
		for _, frag := range seg.Fragments {
			traf := fragTraf(frag, trackID)
			if traf == nil || traf.Trun == nil {
				continue
			}
			for _, sample := range traf.Trun.Samples {
				dur := uint64(sample.Dur)
				if dur == 0 && traf.Tfhd != nil {
					// The trun may omit per-sample durations, in which case the
					// tfhd default applies. avpipe's output takes this branch.
					dur = uint64(traf.Tfhd.DefaultSampleDuration)
				}
				if dur == 0 {
					continue
				}
				totalDur += dur
				samples++

				if rate, resolved := resolveFrameRate(timescale, samples, totalDur); resolved {
					return rate
				}
				if samples >= frameRateMaxSamples {
					// Two standard rates remain consistent with the data and
					// more samples will not separate them. Report what was
					// measured rather than picking one.
					return measuredFrameRate(timescale, samples, totalDur)
				}
			}
		}
	}

	if samples == 0 || totalDur == 0 {
		return nil
	}
	return measuredFrameRate(timescale, samples, totalDur)
}

// resolveFrameRate reports the frame rate when the samples read so far
// determine it, and resolved=false when reading more would help.
//
// Note the durations themselves are exact - each is the true duration of that
// sample. What a single one cannot do is express the frame interval, which is
// often not a whole number of ticks: 59.94 fps at timescale 90000 is 1501.5,
// so the muxer writes 1501, 1502, 1501, 1502 and the cumulative stays true.
// One sample therefore reads 59.96 and two read 59.94.
//
// The reasoning is a bound rather than a nearest-match. After n samples the
// cumulative duration the muxer wrote differs from the ideal n*timescale/rate
// by less than one tick, so the true rate lies in
//
//	( n*timescale/(total+1), n*timescale/(total-1) )
//
// and any standard rate outside that interval is excluded - provably, not
// probably. One standard rate left means the rate is identified; none means the
// stream does not run at a standard rate; several means keep reading.
//
// The bound assumes nothing about which integer the muxer picks when the ideal
// falls between two, and that independence is load-bearing rather than
// decorative: real output does not use one convention. testdata/vfsegment.mp4
// writes 1501 where an ideal 1501.5 rounds to 1502, while a 24 fps track at
// timescale 10000 writes 417 for an ideal 416.67, where flooring would give
// 416. Within-one-tick covers both, and ceiling too.
//
// A nearest-match or an equality test would be wrong in a way that looks right.
// Quantization can land a short measurement exactly on a neighbouring standard
// rate - 24000/59.94 is 400.4 ticks, and 24000/400 is exactly 60 - so an exact
// hit is evidence of nothing. Against every standard rate at the timescales
// avpipe produces, nearest-match misidentifies three and an equality test
// misidentifies ten; the bound misidentifies none.
func resolveFrameRate(timescale int, samples, totalDur uint64) (*big.Rat, bool) {
	if totalDur <= 1 {
		return nil, false
	}
	// Lower and upper bounds on the true rate. Both ends are exclusive, but
	// treating them as inclusive only keeps a candidate that is exactly at the
	// bound, which costs a sample rather than an answer.
	lo := ratio(timescale, samples, totalDur+1)
	hi := ratio(timescale, samples, totalDur-1)

	var match *big.Rat
	found := 0
	for _, std := range standardFrameRates {
		if std.Cmp(lo) >= 0 && std.Cmp(hi) <= 0 {
			found++
			match = std
		}
	}

	switch found {
	case 1:
		return new(big.Rat).Set(match), true
	case 0:
		// No standard rate is consistent with the data, so the stream runs at
		// some other rate and more samples cannot change that.
		return measuredFrameRate(timescale, samples, totalDur), true
	default:
		return nil, false
	}
}

// measuredFrameRate is the rate implied by the samples read, unreduced by any
// judgement about what the encoder meant.
func measuredFrameRate(timescale int, samples, totalDur uint64) *big.Rat {
	if totalDur == 0 {
		return nil
	}
	return ratio(timescale, samples, totalDur)
}

func ratio(timescale int, samples, denom uint64) *big.Rat {
	num := new(big.Int).Mul(
		big.NewInt(int64(timescale)),
		new(big.Int).SetUint64(samples))
	return new(big.Rat).SetFrac(num, new(big.Int).SetUint64(denom))
}

// fragTraf returns the fragment's track fragment box for trackID, or nil.
func fragTraf(frag *mp4.Fragment, trackID int) *mp4.TrafBox {
	if frag == nil || frag.Moof == nil {
		return nil
	}
	for _, traf := range frag.Moof.Trafs {
		if traf.Tfhd != nil && int(traf.Tfhd.TrackID) == trackID {
			return traf
		}
	}
	return nil
}
