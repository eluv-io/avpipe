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
var globHandler *etxAVPipeInputHandler

//export InReaderX
func InReaderX(fd int, buf *C.char, sz C.int) C.int {

	//h := handler(fd)
	globex.Lock()
	h := globHandler
	fmt.Println("InReaderX", "h", h)
	globex.Unlock()

	gobuf := C.GoBytes(unsafe.Pointer(buf), sz)

	n, _ := h.inReader(gobuf)
	return C.int(n) // PENDING err
}

func (h *etxAVPipeInputHandler) inReader(buf []byte) (int, error) {
	n, err := h.file.Read(buf)
	fmt.Println("inReader", "buf_size", len(buf), "n", n, "err", err)
	return n, err
}

// AVPipeInputHandler the corresponding handler will be called with the eventHandler function
type AVPipeInputHandler interface {
	InWriter(msg *C.avpipe_msg_t) error
	InReader(msg *C.avpipe_msg_t, ch2 *C.elv_channel_t) error
	InSeeker(msg *C.avpipe_msg_t) error
	InCloser(msg *C.avpipe_msg_t) error
}

type etxAVPipeInputHandler struct {
	file *os.File
}

func newAVPipeInputHandler(msg *C.avpipe_msg_t) (AVPipeInputHandler, error) {

	globex.Lock()

	filename := C.GoString((*C.char)(unsafe.Pointer(msg.opaque)))
	f, err := os.Open(filename)
	fmt.Fprintf(os.Stdout, "XXX2 InOpener Got type=%d, filename=%s\n", msg.mtype, filename)
	h := &etxAVPipeInputHandler{file: f}

	globHandler = h
	fmt.Println("newAVPipeInputHandler", "h", globHandler)
	globex.Unlock()

	return h, err
}

func (h *etxAVPipeInputHandler) InCloser(msg *C.avpipe_msg_t) error {
	fmt.Fprintf(os.Stdout, "XXX InCloser type=%d\n", msg.mtype)
	err := h.file.Close()
	if err != nil {
		fmt.Fprintf(os.Stdout, "XXX InCloser error=%v", err)
	}
	return err
}

func (h *etxAVPipeInputHandler) InReader(msg *C.avpipe_msg_t, ch2 *C.elv_channel_t) error {
	buf := make([]byte, 64*1024)
	n, err := h.file.Read(buf)
	if n > 0 {
		C.memcpy(unsafe.Pointer(msg.buf), unsafe.Pointer(&buf[0]), C.size_t(n))
		msg.buf_size = C.int(n)
	}

	var reply *C.avpipe_msg_reply_t
	//reply = (*C.avpipe_msg_reply_t)(C.calloc(1, C.sizeof(C.avpipe_msg_reply_t)))
	reply = (*C.avpipe_msg_reply_t)(C.calloc(1, 4))
	reply.rc = C.int(n)
	C.elv_channel_send(ch2, unsafe.Pointer(reply))

	fmt.Fprintf(os.Stdout, "XXX InReader type=%d, buf_size=%d, n=%d\n", msg.mtype, msg.buf_size, n)
	return err
}

func (h *etxAVPipeInputHandler) InWriter(msg *C.avpipe_msg_t) error {
	fmt.Fprintf(os.Stdout, "XXX InWriter type=%d\n", msg.mtype)
	return nil
}

func (h *etxAVPipeInputHandler) InSeeker(msg *C.avpipe_msg_t) error {
	offset := int64(msg.offset)
	whence := int(msg.whence)
	_, err := h.file.Seek(offset, whence)
	fmt.Fprintf(os.Stdout, "XXX InSeeker type=%d\n", msg.mtype)
	return err
}

// AVPipeOutputHandler the corresponding handler will be called with the eventHandler function
type AVPipeOutputHandler interface {
	OutWriter(msg *C.avpipe_msg_t) error
	OutReader(msg *C.avpipe_msg_t) error
	OutSeeker(msg *C.avpipe_msg_t) error
	OutCloser(msg *C.avpipe_msg_t) error
}

type etxAVPipeOutputHandler struct {
	file *os.File
}

func newAVPipeOutputHandler(msg *C.avpipe_msg_t) (AVPipeOutputHandler, error) {
	filename := C.GoString((*C.char)(unsafe.Pointer(msg.opaque)))
	fmt.Fprintf(os.Stdout, "XXX OutOpener type=%d, filename=%s\n", msg.mtype, filename)
	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	return &etxAVPipeOutputHandler{file: f}, err
}

func (h *etxAVPipeOutputHandler) OutCloser(msg *C.avpipe_msg_t) error {
	fmt.Fprintf(os.Stdout, "XXX OutCloser type=%d\n", msg.mtype)
	err := h.file.Close()
	if err != nil {
		fmt.Fprintf(os.Stdout, "XXX OutCloser error=%v", err)
	}
	C.free(unsafe.Pointer(msg.buf))
	return err
}

func (h *etxAVPipeOutputHandler) OutReader(msg *C.avpipe_msg_t) error {
	fmt.Fprintf(os.Stdout, "XXX OutReader type=%d\n", msg.mtype)
	return nil
}

func (h *etxAVPipeOutputHandler) OutWriter(msg *C.avpipe_msg_t) error {
	buf_size := int(msg.buf_size)
	buf := make([]byte, buf_size)
	C.memcpy(unsafe.Pointer(&buf[0]), unsafe.Pointer(msg.buf), C.size_t(buf_size))
	n, err := h.file.Write(buf)
	fmt.Fprintf(os.Stdout, "XXX OutWriter type=%d, written=%d\n", msg.mtype, n)
	return err
}

func (h *etxAVPipeOutputHandler) OutSeeker(msg *C.avpipe_msg_t) error {
	offset := int64(msg.offset)
	whence := int(msg.whence)
	_, err := h.file.Seek(offset, whence)
	fmt.Fprintf(os.Stdout, "XXX OutSeeker type=%d\n", msg.mtype)
	return err
}

func eventHandler(ch *C.elv_channel_t, ch2 *C.elv_channel_t) error {
	var inputHandler AVPipeInputHandler
	var outputHandler AVPipeOutputHandler
	var err error

	for {
		var msg *C.avpipe_msg_t
		var ptr unsafe.Pointer

		//fmt.Fprintf(os.Stdout, "XXX1 Before call\n")
		ptr = C.elv_channel_receive(ch)
		msg = (*C.avpipe_msg_t)(ptr)

		switch msg.mtype {
		case C.avpipe_in_opener:
			inputHandler, err = newAVPipeInputHandler(msg)
		case C.avpipe_in_closer:
			inputHandler.InCloser(msg)
		case C.avpipe_in_reader:
			inputHandler.InReader(msg, ch2)
		case C.avpipe_in_writer:
			inputHandler.InWriter(msg)
		case C.avpipe_out_opener:
			outputHandler, err = newAVPipeOutputHandler(msg)
		case C.avpipe_out_closer:
			outputHandler.OutCloser(msg)
		case C.avpipe_out_reader:
			outputHandler.OutReader(msg)
		case C.avpipe_out_writer:
			outputHandler.OutWriter(msg)

		}

		C.free(ptr)
	}

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

	C.elv_channel_init(&C.chanReq, 10)
	C.elv_channel_init(&C.chanRep, 10)

	go eventHandler(C.chanReq, C.chanRep)

	err := C.tx((*C.txparams_t)(unsafe.Pointer(params)), C.CString(filename.value))
	if err != 0 {
		fmt.Fprintf(os.Stderr, "Failed transcoding %s\n", filename.value)
	}
}
