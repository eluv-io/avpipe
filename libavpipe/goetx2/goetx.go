package main

// #cgo CFLAGS: -I../include -I../../utils/include
// #include <string.h>
// #include <stdlib.h>
// #include "avpipe_xc.h"
// #include "goetx.h"
import "C"
import (
	"flag"
	"fmt"
	"os"
	"sync"
	"unsafe"
)

// AVPipeInputHandler the corresponding handler will be called with the eventHandler function
type AVPipeIOHandler interface {
	InReader(buf []byte) (int, error)
	InSeeker(offset C.int64_t, whence C.int) error
	InCloser() error
	OutWriter(fd C.int, buf []byte) (int, error)
	OutSeeker(fd C.int, offset C.int64_t, whence C.int) (int64, error)
	OutCloser(fd C.int) error
}

type etxAVPipeIOHandler struct {
	file      *os.File           // Input file
	filetable map[C.int]*os.File // Map of output files
}

// Global table of handlers
var gHandlers map[int64]*etxAVPipeIOHandler = make(map[int64]*etxAVPipeIOHandler)
var gHandleNum int64
var gMutex sync.Mutex

//export NewAVPipeIOHandler
func NewAVPipeIOHandler(url *C.char) C.int64_t {
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	f, err := os.Open(filename)
	fmt.Fprintf(os.Stdout, "XXX2 InOpener Got filename=%s\n", filename)
	h := &etxAVPipeIOHandler{file: f, filetable: make(map[C.int]*os.File)}
	fmt.Println("newAVPipeInputHandler h=%v", h)

	if err != nil {
		return C.int64_t(-1)
	}

	gMutex.Lock()
	defer gMutex.Unlock()
	gHandleNum++
	gHandlers[gHandleNum] = h
	return C.int64_t(gHandleNum)
}

//export AVPipeReadInput
func AVPipeReadInput(handler C.int64_t, buf *C.char, sz C.int) C.int {
	h := gHandlers[int64(handler)]
	fmt.Println("InReaderX", "h", h, "buf", buf, "sz=", sz)

	//gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	gobuf := make([]byte, sz)

	n, _ := h.InReader(gobuf)
	if n > 0 {
		C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&gobuf[0]), C.size_t(n))
	}

	return C.int(n) // PENDING err
}

func (h *etxAVPipeIOHandler) InReader(buf []byte) (int, error) {
	n, err := h.file.Read(buf)
	fmt.Println("InReader", "buf_size", len(buf), "n", n, "err", err)
	return n, err
}

//export AVPipeSeekInput
func AVPipeSeekInput(handler C.int64_t, offset C.int64_t, whence C.int) C.int64_t {
	h := gHandlers[int64(handler)]
	fmt.Println("InSeekerX", "h", h)

	n, err := h.InSeeker(offset, whence)
	if err != nil {
		return -1
	}
	return C.int64_t(n)
}

func (h *etxAVPipeIOHandler) InSeeker(offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.file.Seek(int64(offset), int(whence))
	fmt.Fprintf(os.Stdout, "XXX InSeeker offset=%d, whence=%d, n=%d\n", offset, whence, n)
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
	fmt.Fprintf(os.Stdout, "XXX InCloser\n")
	err := h.file.Close()
	if err != nil {
		fmt.Fprintf(os.Stdout, "XXX InCloser error=%v", err)
	}
	return err
}

//export AVPipeOpenOutput
func AVPipeOpenOutput(handler C.int64_t, fd C.int, url *C.char) C.int {
	h := gHandlers[int64(handler)]
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	fmt.Fprintf(os.Stdout, "XXX2 OutOpener fd=%d, filename=%s\n", fd, filename)
	h.filetable[fd] = f

	if err != nil {
		return -1
	}

	return 0
}

//export AVPipeWriteOutput
func AVPipeWriteOutput(handler C.int64_t, fd C.int, buf *C.char, sz C.int) C.int {
	h := gHandlers[int64(handler)]
	fmt.Println("OutWriterX", "h", h)

	if h.filetable[fd] == nil {
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
	n, err := h.filetable[fd].Write(buf)
	fmt.Fprintf(os.Stdout, "XXX OutWriter written=%d\n", n)
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
	n, err := h.filetable[fd].Seek(int64(offset), int(whence))
	fmt.Fprintf(os.Stdout, "XXX OutSeeker\n")
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
	err := h.filetable[fd].Close()
	fmt.Fprintf(os.Stdout, "XXX OutCloser error=%v", err)
	return err
}

// TxParams should match with txparams_t in C library
type TxParams struct {
	startTimeTs        int32
	durationTs         int32
	startSegmentStr    []byte
	videoBitrate       int32
	audioBitrate       int32
	sampleRate         int32
	crfStr             []byte
	segDurationTs      int32
	segDurationFr      int32
	segDurationSecsStr []byte
	codec              []byte
	encHeight          int32
	encWidth           int32
}

type filenameFlag struct {
	set   bool
	value string
}

func (f *filenameFlag) Set(filename string) error {
	f.value = filename
	f.set = true
	return nil
}

func (f *filenameFlag) String() string {
	return f.value
}

func main() {
	var filename filenameFlag

	flag.Var(&filename, "filename", "filename for transcoding (output goes to ./O)")
	flag.Parse()

	if !filename.set {
		flag.Usage()
		return
	}

	params := &C.TxParams{
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

	err := C.tx((*C.txparams_t)(unsafe.Pointer(params)), C.CString(filename.value))
	if err != 0 {
		fmt.Fprintf(os.Stderr, "Failed transcoding %s\n", filename.value)
	}
}
