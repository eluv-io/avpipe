package mpegts

import "time"

func exportStats(ts *TSStats, rtp *RTPStats) (res ExportedStats) {
	if ts != nil {
		res.TS.PacketsReceived = ts.PacketsReceived.Load()
		res.TS.PacketsWritten = ts.PacketsWritten.Load()
		res.TS.PacketsDropped = ts.PacketsDropped.Load()
		res.TS.SmallPacketsDropped = ts.SmallPacketsDropped.Load()
		res.TS.RtcpPacketsDropped = ts.RtcpPacketsDropped.Load()
		res.TS.BadPackets = ts.BadPackets.Load()
		res.TS.BytesReceived = ts.BytesReceived.Load()
		res.TS.BytesWritten = ts.BytesWritten.Load()
		res.TS.PaddingPackets = ts.PaddingPackets.Load()
		res.TS.FaultyPaddingPackets = ts.FaultyPaddingPackets.Load()
		res.TS.StrippedPaddingPackets = ts.StrippedPaddingPackets.Load()
		res.TS.MaxBufInPeriod = ts.MaxBufInPeriod.Load()
		res.TS.MinBufInPeriod = ts.MinBufInPeriod.Load()
		res.TS.VideoPacketCount = ts.VideoPacketCount.Load()
		res.TS.AudioPacketCount = ts.AudioPacketCount.Load()
		res.TS.DataPacketCount = ts.DataPacketCount.Load()
		res.TS.FirstPCR = ts.FirstPCR.Load()
		res.TS.LastPCR = ts.LastPCR.Load()
		res.TS.NumSegments = uint64(ts.NumSegments.Load())
		res.TS.NumWraps = uint64(ts.NumWraps.Load())
		res.TS.NumTimedRotate = uint64(ts.NumTimedRotate.Load())
		res.TS.ErrorsCC = ts.ErrorsCC.Load()
		res.TS.ErrorsAdapationField = ts.ErrorsAdapationField.Load()
		res.TS.ErrorsOther = ts.ErrorsOther.Load()
		res.TS.ErrorsIncompletePackets = ts.ErrorsIncompletePackets.Load()
		res.TS.ErrorsOpeningOutput = ts.ErrorsOpeningOutput.Load()
		res.TS.ErrorsWriting = ts.ErrorsWriting.Load()
	}
	if rtp != nil {
		res.RTP.FirstSeqNum = uint64(rtp.FirstSeqNum.Load())
		res.RTP.LastSeqNum = uint64(rtp.LastSeqNum.Load())
		res.RTP.SeqNumSkipTot = uint64(rtp.SeqNumSkipTot.Load())
		res.RTP.SeqNumSkipCount = rtp.SeqNumSkipCount.Load()
		res.RTP.FirstTimestamp = uint64(rtp.FirstTimestamp.Load())
		res.RTP.LastTimestamp = uint64(rtp.LastTimestamp.Load())
		res.RTP.RefTime = rtp.RefTime
		res.RTP.BadPackets = rtp.BadPackets.Load()
		res.RTP.LongHeaders = rtp.LongHeaders.Load()
	}
	return res
}

type ExportedStats struct {
	TS  ExportedTSStats  `json:"ts,omitzero"`
	RTP ExportedRTPStats `json:"rtp,omitzero"`
}

type ExportedTSStats struct {
	PacketsReceived        uint64 `json:"packets_received"`
	PacketsWritten         uint64 `json:"packets_written"`
	PacketsDropped         uint64 `json:"packets_dropped"`
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

	FirstPCR       uint64 `json:"first_pcr"`
	LastPCR        uint64 `json:"last_pcr"`
	NumSegments    uint64 `json:"num_segments"`
	NumWraps       uint64 `json:"num_wraps"`
	NumTimedRotate uint64 `json:"num_timed_rotate"`

	ErrorsCC                uint64 `json:"errors_cc"`
	ErrorsAdapationField    uint64 `json:"errors_adapation_field"`
	ErrorsOther             uint64 `json:"errors_other"`
	ErrorsIncompletePackets uint64 `json:"errors_incomplete_packets"`
	ErrorsOpeningOutput     uint64 `json:"errors_opening_output"`
	ErrorsWriting           uint64 `json:"errors_writing"`
	// ErrorsCCByPid           map[int]uint64
}

type ExportedRTPStats struct {
	FirstSeqNum     uint64 `json:"first_seq_num"`
	LastSeqNum      uint64 `json:"last_seq_num"`
	SeqNumSkipTot   uint64 `json:"seq_num_skip_tot"`
	SeqNumSkipCount uint64 `json:"seq_num_skip_count"`

	FirstTimestamp uint64    `json:"first_timestamp"`
	LastTimestamp  uint64    `json:"last_timestamp"`
	RefTime        time.Time `json:"ref_time"`

	BadPackets  uint64 `json:"bad_packets"`
	LongHeaders uint64 `json:"long_headers"`
}
