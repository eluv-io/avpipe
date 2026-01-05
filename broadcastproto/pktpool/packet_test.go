package pktpool

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPacket(t *testing.T) {
	pool := NewPacketPool(1024)
	pkt := pool.GetPacket()
	require.EqualValues(t, 1, pkt.refs.Load())
	require.Len(t, pkt.Data, 1024)

	pkt.Data = pkt.Data[10:20]
	pkt.Data[0] = 1
	pkt.Data[9] = 10

	pkt.Reference()
	require.EqualValues(t, 2, pkt.refs.Load())
	pkt.Release()
	require.EqualValues(t, 1, pkt.refs.Load())
	pkt.Reference(2)
	require.EqualValues(t, 3, pkt.refs.Load())
	pkt.Release(3)

	require.Panics(t, func() { pkt.Release() })

	pkt2 := pool.GetPacket()
	require.EqualValues(t, 1, pkt2.refs.Load())
	require.Len(t, pkt.Data, 1024)

	fmt.Println(pkt.Data[10:20]) // should be [1 0 0 0 0 0 0 0 0 10], but that's not guaranteed...
}
