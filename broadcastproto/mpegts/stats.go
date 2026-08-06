package mpegts

import (
	"encoding/json"

	mio "github.com/eluv-io/common-go/media/io"
	"github.com/eluv-io/common-go/media/tracker"
	"github.com/eluv-io/common-go/util/jsonutil"
)

// exportStats builds the JSON-exported stats from avpipe's own operational counters (ts, rtpStats), the shared
// tracker's stats (stream), which now owns most of the stream-integrity tracking, and the underlying connection's
// SRT stats (srt), if any. ExportedTSStats/ExportedRTPStats keep their original field names/shapes for backward JSON
// compatibility (see the legacy-field comments below); only their population source changed for the fields
// tracker.MediaTracker now computes.
func exportStats(ts *TSStats, rtpStats *RTPStats, stream *tracker.Stats, srt *mio.SrtConnStats) (res ExportedStats) {
	if ts != nil {
		res.TS.PacketsWritten = ts.PacketsWritten.Load()
		res.TS.PacketsDropped = ts.PacketsDropped.Load()
		res.TS.BytesWritten = ts.BytesWritten.Load()
		// BadPackets is computed by avpipe itself (not sourced from mpp.tracker), since mpp.tracker's own BadPackets
		// counts a different, RTP-only condition - see TSStats.BadPackets. FaultyPaddingPackets/StrippedPaddingPackets
		// are both byproducts of the padding-stripping operation itself (see stripTsPadding/RemoveTsPadding) - an
		// avpipe output-pipeline concern, not a stream-integrity stat, so they stay avpipe-managed too.
		res.TS.BadPackets = ts.BadPackets.Load()
		res.TS.FaultyPaddingPackets = ts.FaultyPaddingPackets.Load()
		res.TS.StrippedPaddingPackets = ts.StrippedPaddingPackets.Load()
		res.TS.MaxBufInPeriod = ts.MaxBufInPeriod.Load()
		res.TS.MinBufInPeriod = ts.MinBufInPeriod.Load()
		res.TS.NumSegments = uint64(ts.NumSegments.Load())
		res.TS.NumTimedRotate = uint64(ts.NumTimedRotate.Load())
		res.TS.ErrorsOther = ts.ErrorsOther.Load()
		res.TS.ErrorsOpeningOutput = ts.ErrorsOpeningOutput.Load()
		res.TS.ErrorsWriting = ts.ErrorsWriting.Load()
	}
	if rtpStats != nil {
		res.RTP.BadPackets = rtpStats.BadPackets.Load()
	}
	if stream != nil {
		res.Stream = stream

		// PacketsReceived/BytesReceived count datagrams/datagram-bytes (network reads), matching PacketsDropped's
		// granularity (both feed the same "Recv/Drop %" report) - the pre-tracker code counted TS packets/TS-only
		// bytes here instead, which made that ratio meaningless. DiscardedPackets aggregates every condition under
		// which a whole datagram is rejected before its TS packets ever reach tsTracker: too small to plausibly
		// contain one (SmallPacketsDropped, which includes the RtcpPacketsDropped subset), a malformed/wrong-version
		// RTP header (BadPackets), or a TS payload that isn't a multiple of the TS packet size (IncompletePackets).
		// It does not include per-packet conditions (AdaptationFieldErrors, FaultyPaddingPackets, CC errors) since
		// those datagrams are still otherwise processed.
		res.TS.PacketsReceived = stream.Packets
		res.TS.BytesReceived = stream.Bytes
		res.TS.DiscardedPackets = stream.Errors.SmallPacketsDropped + stream.Errors.BadPackets + stream.Errors.IncompletePackets

		res.TS.SmallPacketsDropped = stream.Errors.SmallPacketsDropped
		res.TS.RtcpPacketsDropped = stream.Errors.RtcpPacketsDropped
		res.TS.ErrorsCC = uint64(stream.Errors.CcErrors)
		res.TS.ErrorsAdaptationField = stream.Errors.AdaptationFieldErrors
		res.TS.ErrorsIncompletePackets = stream.Errors.IncompletePackets

		if stream.Ts != nil {
			cat := stream.Ts.Categorize()
			res.TS.VideoPacketCount = uint64(cat.Video)
			res.TS.AudioPacketCount = uint64(cat.Audio)
			res.TS.DataPacketCount = uint64(cat.Other)
			res.TS.PaddingPackets = uint64(cat.Padding)
		}

		pcrPinned := false
		for _, c := range stream.Clocks {
			switch c.Source {
			case "rtp":
				res.RTP.LongHeaders = stream.Errors.LongHeaders
				res.RTP.SeqNumSkipCount = c.ErrorCount
				// c.Gaps is bounded by tracker.Config.MaxGaps (100 by default), so on a long-running stream with more
				// than MaxGaps gaps, SeqNumSkipTot undercounts - it only sums the retained gaps, not all of them.
				for _, g := range c.Gaps {
					res.RTP.SeqNumSkipTot += uint64(absInt64(g.SeqDiff))
				}
			case "pcr":
				if !pcrPinned {
					// NumWraps is pinned to the first PCR-bearing PID discovered, mirroring avpipe's pre-tracker
					// behavior. Clocks lists "pcr" entries in discovery order, so the first one found is that PID;
					// other programs' wraps remain visible via stream.Clocks directly.
					res.TS.NumWraps = uint64(c.NumWraps)
					pcrPinned = true
				}
			}
		}
	}
	res.Srt = srt
	return res
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
