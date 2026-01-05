package pktpool

import (
	"sync"
	"sync/atomic"

	"github.com/eluv-io/errors-go"
)

// NewPacketPool creates a new PacketPool with the given packet size.
func NewPacketPool(packetSize ...int) *PacketPool {
	var packetPool PacketPool
	packetPool.pool.New = func() interface{} {
		pktSize := 2048
		if len(packetSize) > 0 && packetSize[0] > 0 {
			pktSize = packetSize[0]
		}
		return &Packet{
			data: make([]byte, pktSize), // default larger than max MTU/SRT payload (1500)
			pool: &packetPool.pool,
		}
	}
	return &packetPool
}

// PacketPool is a pool for fixed-capacity byte slices that can be shared between multiple processes. To simplify usage,
// the byte slices are wrapped in a Packet struct that tracks the number of references to it. When the last reference is
// released, the packet is returned to the pool.
//
// Initially, a packet's ref count is 1 when it is retrieved from the pool with pool.GetPacket(). If you want to share
// the packet with another process, increase the ref count with p.Reference(count) before handing the packet over. The
// other process will then call p.Release() when finished with the packet.
type PacketPool struct {
	pool sync.Pool
}

// GetPacket returns a Packet from the packet pool. Its reference count is set to 1. The Data field byte slice is
// guaranteed to be of the same capacity as the pool's initial packet size, but it may contain data from a previous use
// (it is not zeroed).
func (p *PacketPool) GetPacket() *Packet {
	pkt := p.pool.Get().(*Packet)
	pkt.Data = pkt.data // reset to full capacity
	pkt.refs.Store(1)   // initialize ref count
	return pkt
}

// Packet is a wrapper around a byte slice that tracks the number of references to it and releases it back to the packet
// pool when the last reference is dropped.
type Packet struct {
	Data []byte
	data []byte // reference to original slice for resetting to full capacity
	refs atomic.Int32
	pool *sync.Pool
}

// Reference increments the reference count by one or the given number.
func (p *Packet) Reference(count ...uint32) {
	c := int32(1)
	if len(count) > 0 {
		c = int32(count[0])
	}
	p.refs.Add(c)
}

// Release decrements the reference count by one or the given number and returns the packet to the pool if the count
// reaches zero. Panics if the reference count drops below zero.
func (p *Packet) Release(count ...uint32) {
	c := int32(1)
	if len(count) > 0 {
		c = int32(count[0])
	}
	refs := p.refs.Add(-c)
	if refs == 0 {
		p.pool.Put(p)
	} else if refs < 0 {
		panic(errors.E("PacketPool.Release", errors.K.Invalid, "reason", "negative reference count!", "count", refs))
	}
}
