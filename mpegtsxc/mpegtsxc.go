// Package mpegtsxc transcodes the video PID of an MPEGTS stream while preserving all
// other PIDs (audio, data, PSI) byte-exact. Primarily used to compress/downscale video.
//
// Two modes are supported:
//
//   - Live mode (LiveTranscoder): raw-TS datagrams in, a continuous TS packet stream out,
//     optionally paced to a constant bitrate on the wall clock with the output phase-locked
//     to the source clock (PLL). This is the mode used by the mpegts-xc CLI for UDP/RTP
//     input and UDP output.
//
//   - Parts mode (PartsTranscoder): RTP datagrams in (e.g. read from recorded rtp_ts
//     MPEGTS parts), RTP datagrams out with synthesized RTP timestamps on a CBR virtual
//     clock — no wall clock involved, throughput is governed entirely by the caller and
//     the encoder (natural catch-up behavior).
//
// Known limitation (both modes): the source PCR must ride on the video PID (the common
// case). If the PCR PID differs from the video PID, PCR packets are passed through
// byte-exact but do not reach the video transcode leg (see processor.go).
package mpegtsxc

import (
	"time"

	elog "github.com/eluv-io/log-go"
)

var log = elog.Get("/avpipe/mpegtsxc")

const tsPacketSize = 188

const (
	pcrClockHz = 27_000_000

	// defaultPcrLead - how much regenerated PCR leads DTS (approx. decoder buffer delay)
	defaultPcrLead = 300 * time.Millisecond
)

func ticks27(d time.Duration) int64 { return int64(d.Seconds() * pcrClockHz) }
