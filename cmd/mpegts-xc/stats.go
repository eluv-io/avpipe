package main

import (
	"fmt"
	"sort"
	"sync"

	"github.com/Comcast/gots/v2/pes"
	"go.uber.org/atomic"
)

// Stats is the running stats record for the run and holds counters for each phase
// - input
// - classification
// - transcoding
// - muxing/interleave
// - output
type Stats struct {
	// input counters
	datagrams atomic.Uint64
	tsVideo   atomic.Uint64
	tsOther   atomic.Uint64 // non-video packets (other + PSI)

	// interleave (PES PTS, 90 kHz), guarded by mu
	mu       sync.Mutex
	videoPTS int64         // -1 until first video PES PTS seen
	otherPTS map[int]int64 // pid -> last PES PTS
}

func newStats() *Stats {
	return &Stats{videoPTS: -1, otherPTS: map[int]int64{}}
}

// addDatagram records one input datagram and its per-class TS packet counts.
func (s *Stats) addDatagram(c dgCounts) {
	s.datagrams.Inc()
	s.tsVideo.Add(c.video)
	s.tsOther.Add(c.other + c.psi)
}

func (s *Stats) Datagrams() uint64 { return s.datagrams.Load() }

func (s *Stats) setVideoPTS(pts int64) {
	s.mu.Lock()
	s.videoPTS = pts
	s.mu.Unlock()
}

func (s *Stats) setOtherPTS(pid int, pts int64) {
	s.mu.Lock()
	s.otherPTS[pid] = pts
	s.mu.Unlock()
}

// Log formats and logs the periodic stats
func (s *Stats) Log(videoPID, fifoLen int, fifoDropped uint64) {
	video := s.tsVideo.Load()
	other := s.tsOther.Load()
	log.Info("mpegts-xc stats input",
		"videoPID", videoPID, "datagrams", s.datagrams.Load(), "tsVideo", video, "tsOther", other,
		"tsTotal", video+other, "fifoLen", fifoLen, "fifoDropped", fifoDropped)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.videoPTS < 0 {
		return
	}
	pids := make([]int, 0, len(s.otherPTS))
	for pid := range s.otherPTS {
		if pid == videoPID {
			continue // never compare the video PID against itself
		}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		aheadMs := float64(s.otherPTS[pid]-s.videoPTS) / 90.0
		log.Info("stats aheadOfVideo",
			"pid", pid, "otherPTS", s.otherPTS[pid], "videoPTS", s.videoPTS,
			"aheadMs", fmt.Sprintf("%.1f", aheadMs))
	}
}

// pesPTS returns the PTS (90 kHz) from a TS payload that begins a PES packet, or
// ok=false if the payload is not a PES start or carries no PTS.
func pesPTS(payload []byte) (int64, bool) {
	ph, err := pes.NewPESHeader(payload)
	if err != nil || !ph.HasPTS() {
		return 0, false
	}
	return int64(ph.PTS()), true
}
