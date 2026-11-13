package anc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParity10(t *testing.T) {
	assert.Equal(t, uint32(0x200), parity10(0x00)) // no bits: even parity, bit 8 clear, bit 9 set
	assert.Equal(t, uint32(0x101), parity10(0x01)) // one bit: odd, bit 8 set, bit 9 clear
	assert.Equal(t, uint32(0x260), parity10(0x60)) // two bits: even
	assert.Equal(t, uint32(0x2ff), parity10(0xff)) // eight bits: even
	for v := 0; v < 256; v++ {
		w := parity10(uint8(v))
		assert.True(t, parityOK(w), "%#x", v)
		assert.False(t, parityOK(w^0x100), "%#x bit 8 flipped", v)
		assert.False(t, parityOK(w^0x200), "%#x bit 9 flipped", v)
	}
}
