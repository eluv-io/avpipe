package live

import (
	"io"
	"sync"

	elog "github.com/qluvio/content-fabric/log"
)

type RWBuffer struct {
	ch          [][]byte
	front       int
	rear        int    // rear-1 is the index of last element
	capacity    int    // Capacity of the queue (max number of elements in the queue)
	sz          int    // Total size of elements in the queue
	count       int    // Number of elements in the queue
	inReadBuf   []byte // Partially read packet by avpipe
	inReadIndex int    // Index of partially read data from a packet
	m           *sync.Mutex
	cond        *sync.Cond
	closed      RWBufferCloseState
}

type RWBufferCloseState int

const (
	RWBufferOpen RWBufferCloseState = iota
	RWBufferReadClosed
	RWBufferWriteClosed
	RWBufferClosed
)

var blog = elog.Get("/eluvio/avpipe/live/rwb")

/*
 * Creates a RWBuffer which is open for reading/writing.
 * The writer can write to the buffer until Close(RWBufferWriteClosed) is called.
 * The reader can read from the buffer until Close(RWBufferReadClosed) is called or
 * EOF is issued.
 * An EOF is issued for reader when the writer closed the buffer and there is no data
 * in the buffer.
 */
func NewRWBuffer(capacity int) io.ReadWriter {
	if capacity < 0 {
		return nil
	}

	rwb := &RWBuffer{
		ch:       make([][]byte, capacity),
		sz:       0,
		count:    0,
		front:    0,
		rear:     -1,
		capacity: capacity,
		closed:   0,
	}

	rwb.m = &sync.Mutex{}
	rwb.cond = sync.NewCond(rwb.m)
	return rwb
}

/*
 * It simply makes a copy from buf and enqueues the new copy of buf.
 * For more improvemnt it can avoid copying buffer buf by passing the ownership of buffer buf to rwb (RM).
 */
func (rwb *RWBuffer) Write(buf []byte) (n int, err error) {
	b := make([]byte, len(buf))
	copy(b, buf)

	rwb.m.Lock()
	defer rwb.m.Unlock()

	if rwb.closed&RWBufferWriteClosed != 0 {
		blog.Debug("Write RWBuffer WRITE closed")
		return 0, io.ErrClosedPipe
	}

	if rwb.closed&RWBufferReadClosed != 0 {
		blog.Debug("Write RWBuffer READ closed")
		return 0, io.ErrClosedPipe
	}

	if rwb.count >= rwb.capacity {
		blog.Warn("RWBuffer buffer queue is full", "capacity", rwb.capacity)
		rwb.cond.Wait()
	}

	rwb.sz += len(buf)
	rwb.count++
	rwb.rear = (rwb.rear + 1) % rwb.capacity
	rwb.ch[rwb.rear] = b
	rwb.cond.Broadcast()
	return len(buf), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

/*
 * Reads one packet at a time into buf.
 * It is possible avpipe asks for data even less than a packet size, which can cause a partial read.
 * RWBuffer can keep track of a partial read and will return the rest of the packet in next Read() call.
 */
func (rwb *RWBuffer) Read(buf []byte) (n int, err error) {
	rwb.m.Lock()
	defer rwb.m.Unlock()

	if rwb.closed&RWBufferReadClosed != 0 {
		return 0, io.ErrClosedPipe
	}

	nCopied := 0

	/*
	 * If there is a partially read packet by avpipe, continue reading the rest of the packet
	 * into buf. (The inReadBuf points to a partially read packet from buffer).
	 */
	if rwb.inReadBuf != nil {
		nCopied = min(len(buf), len(rwb.inReadBuf[rwb.inReadIndex:]))
		copy(buf, rwb.inReadBuf[rwb.inReadIndex:rwb.inReadIndex+nCopied])
		if nCopied == len(rwb.inReadBuf[rwb.inReadIndex:]) {
			rwb.inReadBuf = nil
			rwb.inReadIndex = 0
		} else {
			rwb.inReadIndex += nCopied
		}
		rwb.sz -= nCopied
		//fmt.Printf("Read partial len=%d, sz=%d, start=%d, end=%d, nCopied=%d, inReadIndex=%d\n",
		//	len(rwb.inReadBuf[rwb.inReadIndex:]), rwb.sz, rwb.front, rwb.rear, nCopied, rwb.inReadIndex)
		return nCopied, nil
	}

	for {
		if rwb.closed&RWBufferReadClosed != 0 {
			blog.Debug("Read reading from RWBuffer closed")
			break
		}

		if rwb.closed&RWBufferWriteClosed != 0 && rwb.count <= 0 {
			blog.Debug("Read writing to RWBuffer closed and no data")
			break
		}

		if nCopied == len(buf) {
			break
		}

		if rwb.count <= 0 {
			// Qeueue is empty, wait for a Write()
			rwb.cond.Wait()
		}

		if rwb.count <= 0 {
			continue
		}

		rwb.count--
		b := rwb.ch[rwb.front]
		nCopied = min(len(buf), len(b))
		copy(buf, b[:nCopied])
		if nCopied < len(b) {
			rwb.inReadBuf = b
			rwb.inReadIndex = nCopied
		}
		rwb.front = (rwb.front + 1) % rwb.capacity
		//fmt.Printf("Read len(buf)=%d, sz=%d, start=%d, end=%d, nCopied=%d, inReadIndex=%d\n",
		//	len(buf), rwb.sz, rwb.front, rwb.rear, nCopied, rwb.inReadIndex)
		rwb.sz -= nCopied
		break
	}
	rwb.cond.Broadcast()

	if rwb.closed&RWBufferWriteClosed != 0 && rwb.count <= 0 && nCopied == 0 {
		blog.Debug("Read RWBuffer EOF")
		return 0, io.EOF
	}

	return nCopied, nil
}

func (rwb *RWBuffer) Size() int {
	rwb.m.Lock()
	defer rwb.m.Unlock()
	return rwb.sz
}

func (rwb *RWBuffer) Len() int {
	rwb.m.Lock()
	defer rwb.m.Unlock()
	return rwb.count
}

func (rwb *RWBuffer) Close(state RWBufferCloseState) error {
	rwb.m.Lock()
	defer rwb.m.Unlock()

	rwb.closed |= state
	blog.Debug("Close RWBuffer", "state", state)
	rwb.cond.Broadcast()
	return nil
}
