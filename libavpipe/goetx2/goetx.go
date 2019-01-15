package main

// #cgo CFLAGS: -I../include -I../../utils/include
// #include <string.h>
// #include <stdlib.h>
// #include "avpipe_xc.h"
// #include "goetx.h"
// #include "elv_channel.h"
// extern elv_channel_t *chanReq;
// extern elv_channel_t *chanRep;
import "C"
import (
	"flag"
	"fmt"
	"os"
	"sync"
	"unsafe"
)

var globex sync.Mutex
var gInHandler *etxAVPipeInputHandler
var gOutHandler *etxAVPipeOutputHandler

//export InOpenerX
func InOpenerX(url *C.char) C.int {

	globex.Lock()

	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	f, err := os.Open(filename)
	fmt.Fprintf(os.Stdout, "XXX2 InOpener Got filename=%s\n", filename)
	h := &etxAVPipeInputHandler{file: f}

	gInHandler = h
	fmt.Println("newAVPipeInputHandler", "h", gInHandler)
	globex.Unlock()

	if err != nil {
		return -1
	}

	return 0
}

//export InReaderX
func InReaderX(buf *C.char, sz C.int) C.int {

	//h := handler(fd)
	globex.Lock()
	h := gInHandler
	fmt.Println("InReaderX", "h", h, "buf", buf, "sz=", sz)
	globex.Unlock()

	//gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	//gobuf := (*[1 << 30]C.char)(unsafe.Pointer(buf))[:sz:sz]
	gobuf := make([]byte, sz)

	n, _ := h.InReader(gobuf)
	if n > 0 {
		C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&gobuf[0]), C.size_t(n))
	}
	fmt.Println("InReaderX gobuf=", gobuf[0:10])
	return C.int(n) // PENDING err
}

func (h *etxAVPipeInputHandler) InReader(buf []byte) (int, error) {
	n, err := h.file.Read(buf)
	fmt.Println("InReader", "buf_size", len(buf), "n", n, "err", err)
	return n, err
}

func (h *etxAVPipeInputHandler) InWriter(buf []byte) (int, error) {
	fmt.Fprintf(os.Stdout, "ERROR unexpected InWriter\n")
	return 0, nil
}

//export InSeekerX
func InSeekerX(offset C.int64_t, whence C.int) C.int64_t {
	globex.Lock()
	h := gInHandler
	fmt.Println("InSeekerX", "h", h)
	globex.Unlock()

	n, err := h.InSeeker(offset, whence)
	if err != nil {
		return -1
	}
	return C.int64_t(n)
}

func (h *etxAVPipeInputHandler) InSeeker(offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.file.Seek(int64(offset), int(whence))
	fmt.Fprintf(os.Stdout, "XXX InSeeker offset=%d, whence=%d, n=%d\n", offset, whence, n)
	return n, err
}

//export InCloserX
func InCloserX() C.int {
	h := gInHandler
	err := h.InCloser()
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *etxAVPipeInputHandler) InCloser() error {
	fmt.Fprintf(os.Stdout, "XXX InCloser\n")
	err := h.file.Close()
	if err != nil {
		fmt.Fprintf(os.Stdout, "XXX InCloser error=%v", err)
	}
	return err
}

// AVPipeInputHandler the corresponding handler will be called with the eventHandler function
type AVPipeInputHandler interface {
	InWriter(buf []byte) (int, error)
	InReader(buf []byte) (int, error)
	InSeeker(offset C.int64_t, whence C.int) error
	InCloser() error
}

type etxAVPipeInputHandler struct {
	file *os.File
}

//export OutOpenerX
func OutOpenerX(url *C.char) C.int {

	globex.Lock()

	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	fmt.Fprintf(os.Stdout, "XXX2 OutOpener Got filename=%s\n", filename)
	h := &etxAVPipeOutputHandler{file: f}

	gOutHandler = h
	globex.Unlock()

	if err != nil {
		return -1
	}

	return 0
}

func (h *etxAVPipeOutputHandler) OutReader(buf []byte) (int, error) {
	fmt.Fprintf(os.Stdout, "XXX OutReader\n")
	return 0, nil
}

//export OutWriterX
func OutWriterX(buf *C.char, sz C.int) C.int {

	//h := handler(fd)
	globex.Lock()
	h := gOutHandler
	fmt.Println("OutWriterX", "h", h)
	globex.Unlock()

	gobuf := C.GoBytes(unsafe.Pointer(buf), sz)

	n, err := h.OutWriter(gobuf)
	if err != nil {
		return -1
	}

	return C.int(n)
}

func (h *etxAVPipeOutputHandler) OutWriter(buf []byte) (int, error) {
	n, err := h.file.Write(buf)
	fmt.Fprintf(os.Stdout, "XXX OutWriter written=%d\n", n)
	return n, err
}

//export OutSeekerX
func OutSeekerX(offset C.int64_t, whence C.int) C.int {
	h := gOutHandler
	n, err := h.OutSeeker(offset, whence)
	if err != nil {
		return -1
	}
	return C.int(n)
}

func (h *etxAVPipeOutputHandler) OutSeeker(offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.file.Seek(int64(offset), int(whence))
	fmt.Fprintf(os.Stdout, "XXX OutSeeker\n")
	return n, err
}

//export OutCloserX
func OutCloserX() C.int {
	h := gOutHandler
	err := h.OutCloser()
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *etxAVPipeOutputHandler) OutCloser() error {
	fmt.Fprintf(os.Stdout, "XXX OutCloser\n")
	err := h.file.Close()
	if err != nil {
		fmt.Fprintf(os.Stdout, "XXX OutCloser error=%v", err)
	}
	return err
}

// AVPipeOutputHandler the corresponding handler will be called with the eventHandler function
type AVPipeOutputHandler interface {
	OutWriter(buf []byte) (int, error)
	OutReader(buf []byte) (int, error)
	OutSeeker(offset C.int64_t, whence C.int) (int64, error)
	OutCloser() error
}

type etxAVPipeOutputHandler struct {
	file *os.File
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
