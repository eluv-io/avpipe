package main

// #cgo CFLAGS: -I../include -I../../utils/include
// #include <string.h>
// #include <stdlib.h>
// #include "avpipe_xc.h"
// #include "goetx.h"
import "C"
import (
	"eluvio/log"
	"flag"
	"fmt"
	"os"
	"sync"
	"unsafe"
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
	Open(stream_index, seg_index int, url string) (AVPipeOutputInterface, error)
}

type AVPipeOutputInterface interface {
	Write(buf []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Close() error
}

//Implement AVPipeInputOpener
type avPipeEtxInputOpener struct {
	url string
}

func (io *avPipeEtxInputOpener) Open(url string) (AVPipeInputInterface, error) {
	f, err := os.Open(url)
	if err != nil {
		return nil, err
	}

	etxInput := &avPipeEtxInput{
		file: f,
	}

	return etxInput, nil
}

// Implement AVPipeInputInterface
type avPipeEtxInput struct {
	file *os.File // Input file
}

func (i *avPipeEtxInput) Read(buf []byte) (int, error) {
	n, err := i.file.Read(buf)
	return n, err
}

func (i *avPipeEtxInput) Seek(offset int64, whence int) (int64, error) {
	n, err := i.file.Seek(int64(offset), int(whence))
	return n, err
}

func (i *avPipeEtxInput) Close() error {
	err := i.file.Close()
	return err
}

//Implement AVPipeOutputOpener
type avPipeEtxOutputOpener struct {
}

func (oo *avPipeEtxOutputOpener) Open(stream_index, seg_index int, url string) (AVPipeOutputInterface, error) {
	f, err := os.OpenFile(url, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}

	h := &avPipeEtxOutput{
		url:          url,
		stream_index: stream_index,
		seg_index:    seg_index,
		file:         f}

	return h, nil
}

// Implement AVPipeOutputInterface
type avPipeEtxOutput struct {
	url          string
	stream_index int
	seg_index    int
	file         *os.File
}

func (o *avPipeEtxOutput) Write(buf []byte) (int, error) {
	n, err := o.file.Write(buf)
	return n, err
}

func (o *avPipeEtxOutput) Seek(offset int64, whence int) (int64, error) {
	n, err := o.file.Seek(offset, whence)
	return n, err
}

func (o *avPipeEtxOutput) Close() error {
	err := o.file.Close()
	return err
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
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	log.Debug("NewAVPipeIOHandler() url", filename)

	/* TODO: these should be set by Init function */
	gInputOpener = &avPipeEtxInputOpener{url: filename}
	gOutputOpener = &avPipeEtxOutputOpener{}

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

	n, _ := h.InReader(gobuf)
	if n > 0 {
		C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&gobuf[0]), C.size_t(n))
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
func AVPipeOpenOutput(handler C.int64_t, stream_index, seg_index C.int, url *C.char) C.int {
	h := gHandlers[int64(handler)]
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	etxOut, err := gOutputOpener.Open(int(stream_index), int(seg_index), filename)
	if err != nil {
		log.Error("AVPipeOpenOutput()", "url", C.GoString((*C.char)(unsafe.Pointer(url))), "error", err)
		return C.int(-1)
	}

	fd := int(seg_index-1)*2 + int(stream_index)
	log.Debug("AVPipeOpenOutput() fd=%d, filename=%s", fd, filename)
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
