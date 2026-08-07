package mpegts

import (
	"encoding/json"

	mio "github.com/eluv-io/common-go/media/io"
	"github.com/eluv-io/common-go/media/tracker"
	"github.com/eluv-io/common-go/util/jsonutil"
)

// populate fills e.TS and e.RTP from avpipe's own operational counters (ts, rtpStats) and e.Stream - the shared
// tracker's stats, which now owns most of the stream-integrity tracking - which the caller must already have set
// (e.g. via MpegtsPacketProcessor.refreshFullStats). It does not touch e.Stream or e.Srt. ExportedTSStats/
// ExportedRTPStats keep their original field names/shapes for backward JSON compatibility (see the legacy-field
// comments below); only their population source changed for the fields tracker.MediaTracker now computes.
func (e *ExportedStats) populate(ts *TSStats, rtpStats *RTPStats) {
	if ts != nil {
		e.TS.PacketsWritten = ts.PacketsWritten.Load()
		e.TS.PacketsDropped = ts.PacketsDropped.Load()
		e.TS.BytesWritten = ts.BytesWritten.Load()
		// BadPackets is computed by avpipe itself (not sourced from mpp.tracker), since mpp.tracker's own BadPackets
		// counts a different, RTP-only condition - see TSStats.BadPackets. FaultyPaddingPackets/StrippedPaddingPackets
		// are both byproducts of the padding-stripping operation itself (see stripTsPadding/RemoveTsPadding) - an
		// avpipe output-pipeline concern, not a stream-integrity stat, so they stay avpipe-managed too.
		e.TS.BadPackets = ts.BadPackets.Load()
		e.TS.FaultyPaddingPackets = ts.FaultyPaddingPackets.Load()
		e.TS.StrippedPaddingPackets = ts.StrippedPaddingPackets.Load()
		e.TS.MaxBufInPeriod = ts.MaxBufInPeriod.Load()
		e.TS.MinBufInPeriod = ts.MinBufInPeriod.Load()
		e.TS.NumSegments = uint64(ts.NumSegments.Load())
		e.TS.NumTimedRotate = uint64(ts.NumTimedRotate.Load())
		e.TS.ErrorsOther = ts.ErrorsOther.Load()
		e.TS.ErrorsOpeningOutput = ts.ErrorsOpeningOutput.Load()
		e.TS.ErrorsWriting = ts.ErrorsWriting.Load()
	}
	if rtpStats != nil {
		e.RTP.BadPackets = rtpStats.BadPackets.Load()
	}
	if stream := e.Stream; stream != nil {
		// PacketsReceived/BytesReceived count datagrams/datagram-bytes (network reads), matching PacketsDropped's
		// granularity (both feed the same "Recv/Drop %" report) - the pre-tracker code counted TS packets/TS-only
		// bytes here instead, which made that ratio meaningless. DiscardedPackets aggregates every condition under
		// which a whole datagram is rejected before its TS packets ever reach tsTracker: too small to plausibly
		// contain one (SmallPacketsDropped, which includes the RtcpPacketsDropped subset), a malformed/wrong-version
		// RTP header (BadPackets), or a TS payload that isn't a multiple of the TS packet size (IncompletePackets).
		// It does not include per-packet conditions (AdaptationFieldErrors, FaultyPaddingPackets, CC errors) since
		// those datagrams are still otherwise processed.
		e.TS.PacketsReceived = stream.Packets
		e.TS.BytesReceived = stream.Bytes
		e.TS.DiscardedPackets = stream.Errors.SmallPacketsDropped + stream.Errors.BadPackets + stream.Errors.IncompletePackets

		e.TS.SmallPacketsDropped = stream.Errors.SmallPacketsDropped
		e.TS.RtcpPacketsDropped = stream.Errors.RtcpPacketsDropped
		e.TS.ErrorsCC = uint64(stream.Errors.CcErrors)
		e.TS.ErrorsAdaptationField = stream.Errors.AdaptationFieldErrors
		e.TS.ErrorsIncompletePackets = stream.Errors.IncompletePackets

		if stream.Ts != nil {
			cat := stream.Ts.Categorize()
			e.TS.VideoPacketCount = uint64(cat.Video)
			e.TS.AudioPacketCount = uint64(cat.Audio)
			e.TS.DataPacketCount = uint64(cat.Other)
			e.TS.PaddingPackets = uint64(cat.Padding)
		}

		pcrPinned := false
		for _, c := range stream.Clocks {
			switch c.Source {
			case "rtp":
				e.RTP.LongHeaders = stream.Errors.LongHeaders
				e.RTP.SeqNumSkipCount = c.ErrorCount
				// c.Gaps is bounded by tracker.Config.MaxGaps (100 by default), so on a long-running stream with more
				// than MaxGaps gaps, SeqNumSkipTot undercounts - it only sums the retained gaps, not all of them.
				for _, g := range c.Gaps {
					e.RTP.SeqNumSkipTot += uint64(absInt64(g.SeqDiff))
				}
			case "pcr":
				if !pcrPinned {
					// NumWraps is pinned to the first PCR-bearing PID discovered, mirroring avpipe's pre-tracker
					// behavior. Clocks lists "pcr" entries in discovery order, so the first one found is that PID;
					// other programs' wraps remain visible via stream.Clocks directly.
					e.TS.NumWraps = uint64(c.NumWraps)
					pcrPinned = true
				}
			}
		}
	}
}

func absInt64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

type ExportedStats struct {
	TS     ExportedTSStats  `json:"ts,omitzero"`
	RTP    ExportedRTPStats `json:"rtp,omitzero"`
	Stream *tracker.Stats   `json:"stream,omitempty"`
	// Srt is the underlying connection's SRT protocol stats (RTT, bandwidth, retransmits, buffer levels - see
	// mio.SrtConnStats) - nil unless the source is an SRT pull or push connection. See MpegtsPacketProcessor's
	// connStats/SetConnStatsSource.
	Srt *mio.SrtConnStats `json:"srt,omitempty"`
}

// CopyInto deep-copies e into dst, reusing dst.Stream where possible instead of allocating a new one. Callers that
// retain an ExportedStats past a single Stat call (e.g. content-fabric's live-recorder) must use this instead of a
// plain assignment: MpegtsPacketProcessor reuses and mutates its Stream snapshot in place across PushStats calls
// (see refreshFullStats's field doc), so aliasing it would let a reader race that mutation.
//
// TS/RTP are plain value structs, safe to assign directly. Srt is rebuilt fresh by connStatsSource.ConnStats on
// every call (see NetReader.ConnStats and its StatsReporter chain), never reused, so it's also safe to assign
// directly - if that ever changes, the fix belongs here, next to the type that knows which of its fields are
// reused, not in a downstream caller guessing at avpipe's internals.
func (e *ExportedStats) CopyInto(dst *ExportedStats) {
	dst.TS = e.TS
	dst.RTP = e.RTP
	dst.Srt = e.Srt
	if e.Stream == nil {
		dst.Stream = nil
		return
	}
	if dst.Stream == nil {
		dst.Stream = &tracker.Stats{}
	}
	e.Stream.CopyInto(dst.Stream)
}

// Clone returns an independent copy of e, detached from any Stream MpegtsPacketProcessor may still be
// reusing/mutating in place - equivalent to CopyInto into a fresh, zero-value ExportedStats. Prefer this over
// CopyInto whenever the caller doesn't already hold a specific *ExportedStats to reuse as the destination (e.g. one
// whose Stream might otherwise still alias e's own, as when e itself came from a shallow struct copy) - CopyInto
// into such a destination would mistake that alias for a genuine, independent destination to reuse.
func (e *ExportedStats) Clone() ExportedStats {
	var dst ExportedStats
	e.CopyInto(&dst)
	return dst
}

func (e *ExportedStats) String() string {
	bb, err := json.Marshal(e)
	if err != nil {
		return jsonutil.MarshallingError("duration_histogram", err)
	}
	return string(bb)
}

type ExportedTSStats struct {
	PacketsReceived        uint64 `json:"packets_received"`
	PacketsWritten         uint64 `json:"packets_written"`
	PacketsDropped         uint64 `json:"packets_dropped"`
	DiscardedPackets       uint64 `json:"discarded_packets"`
	SmallPacketsDropped    uint64 `json:"small_packets_dropped"`
	RtcpPacketsDropped     uint64 `json:"rtcp_packets_dropped"`
	BadPackets             uint64 `json:"bad_packets"`
	BytesReceived          uint64 `json:"bytes_received"`
	BytesWritten           uint64 `json:"bytes_written"`
	PaddingPackets         uint64 `json:"padding_packets"`
	FaultyPaddingPackets   uint64 `json:"faulty_padding_packets"`
	StrippedPaddingPackets uint64 `json:"stripped_padding_packets"`

	MaxBufInPeriod uint64 `json:"max_buf_in_period"`
	MinBufInPeriod uint64 `json:"min_buf_in_period"`

	VideoPacketCount uint64 `json:"video_packet_count"`
	AudioPacketCount uint64 `json:"audio_packet_count"`
	DataPacketCount  uint64 `json:"data_packet_count"`

	NumSegments    uint64 `json:"num_segments"`
	NumWraps       uint64 `json:"num_wraps"`
	NumTimedRotate uint64 `json:"num_timed_rotate"`

	ErrorsCC                uint64 `json:"errors_cc"`
	ErrorsAdaptationField   uint64 `json:"errors_adaptation_field"`
	ErrorsOther             uint64 `json:"errors_other"`
	ErrorsIncompletePackets uint64 `json:"errors_incomplete_packets"`
	ErrorsOpeningOutput     uint64 `json:"errors_opening_output"`
	ErrorsWriting           uint64 `json:"errors_writing"`
	// ErrorsCCByPid           map[int]uint64
}

type ExportedRTPStats struct {
	SeqNumSkipTot   uint64 `json:"seq_num_skip_tot"`
	SeqNumSkipCount uint64 `json:"seq_num_skip_count"`

	BadPackets  uint64 `json:"bad_packets"`
	LongHeaders uint64 `json:"long_headers"`
}
