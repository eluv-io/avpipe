package live

import (
	"bytes"
	"io"
	"math/rand"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func randBuf(t *testing.T, sz int) []byte {
	buf := make([]byte, sz)
	r := rand.New(rand.NewSource(1))
	n, err := r.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, n, sz)
	return buf
}

func TestSimpleRWBuffer(t *testing.T) {
	rwb := NewRWBuffer(100)

	s1 := []byte("Hello")
	n, err := rwb.Write(s1)
	assert.NoError(t, err)
	assert.Equal(t, len(s1), n)

	s2 := []byte("My World")
	n, err = rwb.Write(s2)
	assert.NoError(t, err)
	assert.Equal(t, len(s2), n)

	buf := make([]byte, 100)

	// Read s1 and then s2 each in separate call
	n, err = rwb.Read(buf)
	assert.Equal(t, len(s1), n)
	assert.NoError(t, err)
	assert.Equal(t, true, bytes.Equal(buf[:len(s1)], s1))

	n, err = rwb.Read(buf)
	assert.Equal(t, len(s2), n)
	assert.NoError(t, err)
	assert.Equal(t, true, bytes.Equal(buf[:len(s2)], s2))
}

func TestSimpleRWBuffer2(t *testing.T) {
	rwb := NewRWBuffer(100)

	s1 := randBuf(t, 100)
	n, err := rwb.Write(s1)
	assert.NoError(t, err)
	assert.Equal(t, len(s1), n)

	s2 := randBuf(t, 200)
	n, err = rwb.Write(s2)
	assert.NoError(t, err)
	assert.Equal(t, len(s2), n)

	s3 := randBuf(t, 200)
	n, err = rwb.Write(s3)
	assert.NoError(t, err)
	assert.Equal(t, len(s3), n)

	buf := make([]byte, 500)

	toRead := 50
	n, err = rwb.Read(buf[:toRead])
	assert.Equal(t, toRead, n)
	assert.NoError(t, err)

	toRead = 450
	n, err = rwb.Read(buf[:toRead])
	// Still 50 bytes of s1 is in the buffer
	assert.Equal(t, 50, n)
	assert.NoError(t, err)

	toRead = 200
	n, err = rwb.Read(buf[:toRead])
	assert.Equal(t, toRead, n)
	assert.NoError(t, err)
	assert.Equal(t, true, bytes.Equal(buf[:len(s2)], s2))

}

func TestConcurrentRWBuffer(t *testing.T) {
	rwb := NewRWBuffer(1000)
	nRepeats := 10000
	nRead := 0

	wg := &sync.WaitGroup{}

	go func(rwb io.Writer) {
		for i:=0; i<nRepeats; i++ {
			b := make([]byte, 100)
			wg.Add(1)
			rwb.Write(b)
		}
	} (rwb)

	go func(rwb io.Reader) {
		b := make([]byte, 100)
		for i:=0; i<nRepeats; i++ {
			rwb.Read(b)
			nRead++
			wg.Done()
		}
	}(rwb)

	for {
		if nRead >= nRepeats {
			break
		}
		wg.Wait()
	}
}