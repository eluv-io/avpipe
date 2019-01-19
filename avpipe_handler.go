package avpipe

// #cgo CFLAGS: -I./libavpipe/include -I./utils/include
// #include <string.h>
// #include <stdlib.h>
// #include "avpipe_xc.h"
// #include "avpipe_handler.h"
import "C"
import (
	"eluvio/log"
	"sync"
	"unsafe"
)

type AVType int

const (
	DASHManifest AVType = iota
	DASHVideoInit
	DASHVideoSegment
	DASHAudioInit
	DASHAudioSegment
)

// AVPipeInputHandler the corresponding handlers will be called from the C interface functions
type AVPipeIOHandler interface {
	InReader(buf []byte) (int, error)
	InSeeker(offset C.int64_t, whence C.int) error
	InCloser() error
	OutWriter(fd C.int, buf []byte) (int, error)
	OutSeeker(fd C.int, offset C.int64_t, whence C.int) (int64, error)
	OutCloser(fd C.int) error
}

type AVPipeInputOpener interface {
	Open(url string) (AVPipeInputInterface, error)
}

type AVPipeInputInterface interface {
	Read(buf []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Close() error
}

type AVPipeOutputOpener interface {
	Open(stream_index, seg_index int, out_type AVType) (AVPipeOutputInterface, error)
}

type AVPipeOutputInterface interface {
	Write(buf []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Close() error
}

type etxAVPipeIOHandler struct {
	input     AVPipeInputInterface          // Input file
	filetable map[int]AVPipeOutputInterface // Map of output files
}

// Global table of handlers
var gHandlers map[int64]*etxAVPipeIOHandler = make(map[int64]*etxAVPipeIOHandler)
var gHandleNum int64
var gMutex sync.Mutex
var gInputOpener AVPipeInputOpener
var gOutputOpener AVPipeOutputOpener

func InitAVPipeIOHandler(inputOpener AVPipeInputOpener, outputOpener AVPipeOutputOpener) {
	gInputOpener = inputOpener
	gOutputOpener = outputOpener
}

//export NewAVPipeIOHandler
func NewAVPipeIOHandler(url *C.char) C.int64_t {
	if gInputOpener == nil || gOutputOpener == nil {
		log.Error("Input or output opener(s) are not set")
		return C.int64_t(-1)
	}
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	log.Debug("NewAVPipeIOHandler() url", filename)

	input, err := gInputOpener.Open(filename)
	if err != nil {
		return C.int64_t(-1)
	}

	h := &etxAVPipeIOHandler{input: input, filetable: make(map[int]AVPipeOutputInterface)}
	log.Debug("NewAVPipeIOHandler() url", filename, "h", h)

	gMutex.Lock()
	defer gMutex.Unlock()
	gHandleNum++
	gHandlers[gHandleNum] = h
	return C.int64_t(gHandleNum)
}

//export AVPipeReadInput
func AVPipeReadInput(handler C.int64_t, buf *C.char, sz C.int) C.int {
	h := gHandlers[int64(handler)]
	log.Debug("AVPipeReadInput()", "h", h, "buf", buf, "sz=", sz)

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

func (h *etxAVPipeIOHandler) InReader(buf []byte) (int, error) {
	n, err := h.input.Read(buf)
	log.Debug("InReader()", "buf_size", len(buf), "n", n, "error", err)
	return n, err
}

//export AVPipeSeekInput
func AVPipeSeekInput(handler C.int64_t, offset C.int64_t, whence C.int) C.int64_t {
	h := gHandlers[int64(handler)]
	log.Debug("AVPipeSeekInput()", "h", h)

	n, err := h.InSeeker(offset, whence)
	if err != nil {
		return -1
	}
	return C.int64_t(n)
}

func (h *etxAVPipeIOHandler) InSeeker(offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.input.Seek(int64(offset), int(whence))
	log.Debug("InSeeker() offset=%d, whence=%d, n=%d", offset, whence, n)
	return n, err
}

//export AVPipeCloseInput
func AVPipeCloseInput(handler C.int64_t) C.int {
	h := gHandlers[int64(handler)]
	err := h.InCloser()

	// Remove the handler from global table
	gHandlers[int64(handler)] = nil
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *etxAVPipeIOHandler) InCloser() error {
	err := h.input.Close()
	log.Error("InCloser() error", err)
	return err
}

//export AVPipeOpenOutput
func AVPipeOpenOutput(handler C.int64_t, stream_index, seg_index, stream_type C.int) C.int {
	var out_type AVType

	h := gHandlers[int64(handler)]
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
	log.Debug("AVPipeOpenOutput() fd=%d, stream_index=%d, seg_index=%d, out_type=%d", fd, stream_index, seg_index, out_type)
	h.filetable[fd] = etxOut

	return C.int(fd)
}

//export AVPipeWriteOutput
func AVPipeWriteOutput(handler C.int64_t, fd C.int, buf *C.char, sz C.int) C.int {
	h := gHandlers[int64(handler)]
	log.Debug("AVPipeWriteOutput", "h", h)

	if h.filetable[int(fd)] == nil {
		panic("OutWriterX filetable entry is NULL")
	}

	gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	n, err := h.OutWriter(fd, gobuf)
	if err != nil {
		return C.int(-1)
	}

	return C.int(n)
}

func (h *etxAVPipeIOHandler) OutWriter(fd C.int, buf []byte) (int, error) {
	n, err := h.filetable[int(fd)].Write(buf)
	log.Debug("OutWriter written", n, "error", err)
	return n, err
}

//export AVPipeSeekOutput
func AVPipeSeekOutput(handler C.int64_t, fd C.int, offset C.int64_t, whence C.int) C.int {
	h := gHandlers[int64(handler)]
	n, err := h.OutSeeker(fd, offset, whence)
	if err != nil {
		return C.int(-1)
	}
	return C.int(n)
}

func (h *etxAVPipeIOHandler) OutSeeker(fd C.int, offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.filetable[int(fd)].Seek(int64(offset), int(whence))
	log.Debug("OutSeeker err", err)
	return n, err
}

//export AVPipeCloseOutput
func AVPipeCloseOutput(handler C.int64_t, fd C.int) C.int {
	h := gHandlers[int64(handler)]
	err := h.OutCloser(fd)
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *etxAVPipeIOHandler) OutCloser(fd C.int) error {
	err := h.filetable[int(fd)].Close()
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
