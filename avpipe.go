/*
 * avpipe.go
 *
 * This package has four main interfaces that has to be implemented by the client code:
 *
 * 1) InputOpener: is the input factory interface that needs an implementation to generate an InputHandler.
 *
 * 2) InputHandler: is the input handler with Read/Seek/Size/Close methods. An implementation of this
 *    interface is needed by ffmpeg to process input streams properly.
 *
 * 3) OutputOpener: is the output factory interface that needs an implementation to generate an OutputHandler.
 *
 * 4) OutputHandler: is the output handler with Write/Seek/Close methods. An implementation of this
 *    interface is needed by ffmpeg to write endoded streams properly.
 *
 */
package avpipe

// #cgo CFLAGS: -I./libavpipe/include -I./utils/include
// #include <string.h>
// #include <stdlib.h>
// #include "avpipe_xc.h"
// #include "avpipe.h"
import "C"
import (
	"eluvio/log"
	"sync"
	"unsafe"
)

type AVType int

const (
	Unknown AVType = iota
	DASHManifest
	DASHVideoInit
	DASHVideoSegment
	DASHAudioInit
	DASHAudioSegment
)

// IOHandler the corresponding handlers will be called from the C interface functions
type IOHandler interface {
	InReader(buf []byte) (int, error)
	InSeeker(offset C.int64_t, whence C.int) error
	InCloser() error
	OutWriter(fd C.int, buf []byte) (int, error)
	OutSeeker(fd C.int, offset C.int64_t, whence C.int) (int64, error)
	OutCloser(fd C.int) error
}

type InputOpener interface {
	Open(url string) (InputHandler, error)
}

type InputHandler interface {
	// Reads from input stream into buf
	Read(buf []byte) (int, error)

	// Seeks to specific offset of the input.
	Seek(offset int64, whence int) (int64, error)

	// Closes the input.
	Close() error

	// Returns the size of input, if the size is not known returns 0 or -1.
	Size() int64
}

type OutputOpener interface {
	Open(stream_index, seg_index int, out_type AVType) (OutputHandler, error)
}

type OutputHandler interface {
	// Writes encoded stream to the output.
	Write(buf []byte) (int, error)

	// Seeks to specific offset of the output.
	Seek(offset int64, whence int) (int64, error)

	// Closes the output.
	Close() error
}

// Implement IOHandler
type ioHandler struct {
	input    InputHandler          // Input file
	outTable map[int]OutputHandler // Map of integer handle to output interfaces
}

// Global table of handlers
var gHandlers map[int64]*ioHandler = make(map[int64]*ioHandler)
var gHandleNum int64
var gMutex sync.Mutex
var gInputOpener InputOpener
var gOutputOpener OutputOpener

func InitIOHandler(inputOpener InputOpener, outputOpener OutputOpener) {
	gInputOpener = inputOpener
	gOutputOpener = outputOpener
}

//export NewIOHandler
func NewIOHandler(url *C.char, size *C.int64_t) C.int64_t {
	if gInputOpener == nil || gOutputOpener == nil {
		log.Error("Input or output opener(s) are not set")
		return C.int64_t(-1)
	}
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	log.Debug("NewIOHandler()", "url", filename)

	input, err := gInputOpener.Open(filename)
	if err != nil {
		return C.int64_t(-1)
	}

	*size = C.int64_t(input.Size())

	h := &ioHandler{input: input, outTable: make(map[int]OutputHandler)}
	log.Debug("NewIOHandler()", "url", filename, "size", size)

	gMutex.Lock()
	defer gMutex.Unlock()
	gHandleNum++
	gHandlers[gHandleNum] = h
	return C.int64_t(gHandleNum)
}

//export AVPipeReadInput
func AVPipeReadInput(handler C.int64_t, buf *C.uint8_t, sz C.int) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	gMutex.Unlock()

	log.Debug("AVPipeReadInput()", "handler", handler, "buf", buf, "sz", sz)

	//gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	gobuf := make([]byte, sz)

	n, err := h.InReader(gobuf)
	if n > 0 {
		C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&gobuf[0]), C.size_t(n))
	}

	if err != nil {
		return C.int(-1)
	}

	return C.int(n) // PENDING err
}

func (h *ioHandler) InReader(buf []byte) (int, error) {
	n, err := h.input.Read(buf)
	log.Debug("InReader()", "buf_size", len(buf), "n", n, "error", err)
	return n, err
}

//export AVPipeSeekInput
func AVPipeSeekInput(handler C.int64_t, offset C.int64_t, whence C.int) C.int64_t {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	gMutex.Unlock()
	log.Debug("AVPipeSeekInput()", "h", h)

	n, err := h.InSeeker(offset, whence)
	if err != nil {
		return -1
	}
	return C.int64_t(n)
}

func (h *ioHandler) InSeeker(offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.input.Seek(int64(offset), int(whence))
	log.Debug("InSeeker()", "offset", offset, "whence", whence, "n", n)
	return n, err
}

//export AVPipeCloseInput
func AVPipeCloseInput(handler C.int64_t) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	err := h.InCloser()

	// Remove the handler from global table
	gHandlers[int64(handler)] = nil
	gMutex.Unlock()
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *ioHandler) InCloser() error {
	err := h.input.Close()
	log.Error("InCloser()", "error", err)
	return err
}

//export AVPipeOpenOutput
func AVPipeOpenOutput(handler C.int64_t, stream_index, seg_index, stream_type C.int) C.int {
	var out_type AVType

	gMutex.Lock()
	h := gHandlers[int64(handler)]
	gMutex.Unlock()
	switch stream_type {
	case C.avpipe_video_init_stream:
		out_type = DASHVideoInit
	case C.avpipe_audio_init_stream:
		out_type = DASHAudioInit
	case C.avpipe_manifest:
		out_type = DASHManifest
	case C.avpipe_video_segment:
		out_type = DASHVideoSegment
	case C.avpipe_audio_segment:
		out_type = DASHAudioSegment
	default:
		log.Error("AVPipeOpenOutput()", "invalid stream type", stream_type)
		return C.int(-1)
	}

	etxOut, err := gOutputOpener.Open(int(stream_index), int(seg_index), out_type)
	if err != nil {
		log.Error("AVPipeOpenOutput()", "out_type", out_type, "error", err)
		return C.int(-1)
	}

	fd := int(seg_index-1)*2 + int(stream_index)
	log.Debug("AVPipeOpenOutput()", "fd", fd, "stream_index", stream_index, "seg_index", seg_index, "out_type", out_type)
	h.outTable[fd] = etxOut

	return C.int(fd)
}

//export AVPipeWriteOutput
func AVPipeWriteOutput(handler C.int64_t, fd C.int, buf *C.uint8_t, sz C.int) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]

	gMutex.Unlock()
	log.Debug("AVPipeWriteOutput", "fd", fd, "sz", sz)

	if h.outTable[int(fd)] == nil {
		panic("OutWriterX outTable entry is NULL")
	}

	gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	n, err := h.OutWriter(fd, gobuf)
	if err != nil {
		return C.int(-1)
	}

	return C.int(n)
}

func (h *ioHandler) OutWriter(fd C.int, buf []byte) (int, error) {
	n, err := h.outTable[int(fd)].Write(buf)
	log.Debug("OutWriter written", "n", n, "error", err)
	return n, err
}

//export AVPipeSeekOutput
func AVPipeSeekOutput(handler C.int64_t, fd C.int, offset C.int64_t, whence C.int) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	gMutex.Unlock()
	n, err := h.OutSeeker(fd, offset, whence)
	if err != nil {
		return C.int(-1)
	}
	return C.int(n)
}

func (h *ioHandler) OutSeeker(fd C.int, offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.outTable[int(fd)].Seek(int64(offset), int(whence))
	log.Debug("OutSeeker", "err", err)
	return n, err
}

//export AVPipeCloseOutput
func AVPipeCloseOutput(handler C.int64_t, fd C.int) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	gMutex.Unlock()
	err := h.OutCloser(fd)
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *ioHandler) OutCloser(fd C.int) error {
	err := h.outTable[int(fd)].Close()
	log.Debug("OutCloser()", "fd", int(fd), "error", err)
	return err
}

func Tx(params *C.TxParams, url string) int {
	cparams := &C.TxParams{
		startTimeTs:        0,
		durationTs:         -1,
		startSegmentStr:    C.CString("1"),
		videoBitrate:       2560000,
		audioBitrate:       64000,
		sampleRate:         44100,
		crfStr:             C.CString("23"),
		segDurationTs:      1001 * 60,
		segDurationFr:      60,
		segDurationSecsStr: C.CString("2.002"),
		codec:              C.CString("libx264"),
		encHeight:          720,
		encWidth:           1280,
	}

	rc := C.tx((*C.txparams_t)(unsafe.Pointer(cparams)), C.CString(url))
	return int(rc)
}
