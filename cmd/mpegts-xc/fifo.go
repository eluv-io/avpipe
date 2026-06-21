package main

import (
	"github.com/Comcast/gots/v2/packet"
	"go.uber.org/atomic"
)

// tsItem is a 188-byte TS packet queued for passthrough, tagged with most recent PCR
type tsItem struct {
	data packet.Packet
	pcr  uint64
}

// PassthroughFifo is the bounded FIFO of passthrough TS packets
// Push is non-blocking - reader can never block. On overflow we drop and count
type PassthroughFifo struct {
	ch      chan tsItem
	dropped atomic.Uint64
}

func NewPassthroughFifo(capacity int) *PassthroughFifo {
	return &PassthroughFifo{ch: make(chan tsItem, capacity)}
}

// Push enqueues item, dropping and counting if the FIFO is full.
func (f *PassthroughFifo) Push(item tsItem) {
	select {
	case f.ch <- item:
	default:
		f.dropped.Inc()
	}
}

// Chan exposes the receving end so downstream muxer can select()
func (f *PassthroughFifo) Chan() <-chan tsItem { return f.ch }

func (f *PassthroughFifo) Close()          { close(f.ch) }
func (f *PassthroughFifo) Len() int        { return len(f.ch) }
func (f *PassthroughFifo) Dropped() uint64 { return f.dropped.Load() }
