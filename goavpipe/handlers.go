package goavpipe

import (
	"sync"
)

const traceIo bool = false

type InputOpener interface {
	// fd determines uniquely opening input.
	// url determines input string for transcoding
	Open(fd int64, url string) (InputHandler, error)
}

type InputHandler interface {
	// Reads from input stream into buf.
	// Returns (0, nil) to indicate EOF.
	Read(buf []byte) (int, error)

	// Seeks to specific offset of the input.
	Seek(offset int64, whence int) (int64, error)

	// Closes the input.
	Close() error

	// Returns the size of input, if the size is not known returns 0 or -1.
	Size() int64

	// Reports some stats
	Stat(streamIndex int, statType AVStatType, statArgs interface{}) error
}

type OutputOpener interface {
	// h determines uniquely opening input.
	// fd determines uniquely opening output.
	Open(h, fd int64, stream_index, seg_index int, pts int64, out_type AVType) (OutputHandler, error)
}

type MuxOutputOpener interface {
	// url and fd determines uniquely opening output.
	Open(url string, fd int64, out_type AVType) (OutputHandler, error)
}

type OutputHandler interface {
	// Writes encoded stream to the output.
	Write(buf []byte) (int, error)

	// Seeks to specific offset of the output.
	Seek(offset int64, whence int) (int64, error)

	// Closes the output.
	Close() error

	// Reports some stats
	Stat(streamIndex int, avType AVType, statType AVStatType, statArgs interface{}) error
}

// Global table of handlers
var gHandlers map[int64]any = make(map[int64]any)
var gMuxHandlers map[int64]OutputHandler = make(map[int64]OutputHandler)
var gURLInputOpeners map[string]InputOpener = make(map[string]InputOpener)             // Keeps InputOpener for specific URL
var gURLOutputOpeners map[string]OutputOpener = make(map[string]OutputOpener)          // Keeps OutputOpener for specific URL
var gURLMuxOutputOpeners map[string]MuxOutputOpener = make(map[string]MuxOutputOpener) // Keeps MuxOutputOpener for specific URL
var gURLOutputOpenersByHandler map[int64]OutputOpener = make(map[int64]OutputOpener)   // Keeps OutputOpener for specific URL
var gHandleNum int64
var gFd int64
var gMutex sync.Mutex
var gInputOpener InputOpener
var gOutputOpener OutputOpener
var gMuxOutputOpener MuxOutputOpener

// Globals wraps the global variables used in avpipe. It should be gradually replaced in the
// future, but for now is a global singleton.
type GlobalsT struct{}

var Globals = &GlobalsT{}

func (g *GlobalsT) GetNextFD() int64 {
	gMutex.Lock()
	gHandleNum++
	fd := gHandleNum
	gMutex.Unlock()
	return fd
}

func (g *GlobalsT) AssignOutputOpener(opener OutputOpener) (fd int64) {
	gMutex.Lock()
	defer gMutex.Unlock()

	if opener == nil {
		return -1
	}

	gHandleNum++
	fd = gHandleNum
	gURLOutputOpenersByHandler[fd] = opener
	return fd
}

func (g *GlobalsT) PutCIOHandler(fd int64, h any) {
	gMutex.Lock()
	defer gMutex.Unlock()

	gHandlers[fd] = h
}

// GetCIOHandler returns an 'any' type so that it can exist with the rest of the globals, yet does
// not require C to be built.
func (g *GlobalsT) GetCIOHandler(fd int64) (any, bool) {
	gMutex.Lock()
	defer gMutex.Unlock()

	v, ok := gHandlers[fd]
	return v, ok
}

func (g *GlobalsT) DeleteCIOHandlerAndOutputOpeners(fd int64) {
	gMutex.Lock()
	defer gMutex.Unlock()

	delete(gHandlers, fd)
	delete(gURLOutputOpenersByHandler, fd)
}

func (g *GlobalsT) RemoveURLHandlers(url string) {
	gMutex.Lock()
	defer gMutex.Unlock()

	delete(gURLInputOpeners, url)
	delete(gURLOutputOpeners, url)
}

func (g *GlobalsT) GetMuxOutputHandler(fd int64) OutputHandler {
	gMutex.Lock()
	defer gMutex.Unlock()

	return gMuxHandlers[fd]
}

// This is used to set global input/output opener for avpipe
// If there is no specific input/output opener for a URL, the global
// input/output opener will be used.
func InitIOHandler(inputOpener InputOpener, outputOpener OutputOpener) {
	gInputOpener = inputOpener
	gOutputOpener = outputOpener
}

// Sets the global handlers for muxing (similar to InitIOHandler for transcoding)
func InitMuxIOHandler(inputOpener InputOpener, muxOutputOpener MuxOutputOpener) {
	gInputOpener = inputOpener
	gMuxOutputOpener = muxOutputOpener
}

// This is used to set input/output opener specific to a URL.
// The input/output opener set by this function, is only valid for the URL and will be unset after
// Xc() or Probe() is complete.
func InitUrlIOHandler(url string, inputOpener InputOpener, outputOpener OutputOpener) {
	if inputOpener != nil {
		gMutex.Lock()
		gURLInputOpeners[url] = inputOpener
		gMutex.Unlock()
	}

	if outputOpener != nil {
		gMutex.Lock()
		gURLOutputOpeners[url] = outputOpener
		gMutex.Unlock()
	}
}

// Sets specific IO handler for muxing a url/file (similar to InitUrlIOHandler)
func InitUrlMuxIOHandler(url string, inputOpener InputOpener, muxOutputOpener MuxOutputOpener) {
	if inputOpener != nil {
		gMutex.Lock()
		gURLInputOpeners[url] = inputOpener
		gMutex.Unlock()
	}

	if muxOutputOpener != nil {
		gMutex.Lock()
		gURLMuxOutputOpeners[url] = muxOutputOpener
		gMutex.Unlock()
	}
	Log.Debug("InitUrlMuxIOHandler", "url", url, "urlInputOpener", inputOpener == nil, "urlOutputOpener", muxOutputOpener == nil)
}

func GetInputOpener(url string) InputOpener {
	gMutex.Lock()
	defer gMutex.Unlock()
	if inputOpener, ok := gURLInputOpeners[url]; ok {
		return inputOpener
	}

	return gInputOpener
}

func GetOutputOpener(url string) OutputOpener {
	gMutex.Lock()
	defer gMutex.Unlock()
	if outputOpener, ok := gURLOutputOpeners[url]; ok {
		return outputOpener
	}

	return gOutputOpener
}

func GetMuxOutputOpener(url string) MuxOutputOpener {
	Log.Debug("getMuxOutputOpener", "url", url)
	gMutex.Lock()
	defer gMutex.Unlock()
	if muxOutputOpener, ok := gURLMuxOutputOpeners[url]; ok {
		return muxOutputOpener
	}

	return gMuxOutputOpener
}

func PutMuxOutputOpener(fd int64, muxOutputHandler OutputHandler) {
	gMutex.Lock()
	gMuxHandlers[fd] = muxOutputHandler
	gMutex.Unlock()
}

func GetOutputOpenerByHandler(h int64) OutputOpener {
	gMutex.Lock()
	defer gMutex.Unlock()
	if outputOpener, ok := gURLOutputOpenersByHandler[h]; ok {
		return outputOpener
	}

	return gOutputOpener
}
