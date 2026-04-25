package goavpipe

import (
	"sync"
)

const traceIo bool = false

type InputOpener interface {
	// Open returns an InputHandler or an error
	// fd determines uniquely opening input.
	// url determines input string for transcoding
	Open(fd int64, url string) (InputHandler, error)
}

type InputHandler interface {
	// Read reads from input stream into buf.
	// Returns (0, nil) to indicate EOF.
	Read(buf []byte) (int, error)

	// Seek to specific offset of the input.
	Seek(offset int64, whence int) (int64, error)

	// Close the input.
	Close() error

	// Size returns the size of input, if the size is not known returns 0 or -1.
	Size() int64

	// Stat reports some stats
	Stat(streamIndex int, statType AVStatType, statArgs interface{}) error
}

type OutputOpener interface {
	// Open returns an OutputHandler or an error
	// h determines uniquely opening input.
	// fd determines uniquely opening output.
	Open(h, fd int64, streamIndex, segIndex int, pts int64, outType AVType) (OutputHandler, error)
}

type MuxOutputOpener interface {
	// Open returns an OutputHandler or an error
	// url and fd determines uniquely opening output.
	Open(url string, fd int64, outType AVType) (OutputHandler, error)
}

type OutputHandler interface {
	// Write writes encoded stream to the output.
	Write(buf []byte) (int, error)

	// Seek to specific offset of the output.
	Seek(offset int64, whence int) (int64, error)

	// Close the output.
	Close() error

	// Stat reports some stats
	Stat(streamIndex int, avType AVType, statType AVStatType, statArgs interface{}) error
}

// Global table of handlers
var gHandlers = make(map[int64]any)
var gMuxHandlers = make(map[int64]OutputHandler)
var gURLInputOpeners = make(map[string]InputOpener)           // Keeps InputOpener for specific URL
var gURLOutputOpeners = make(map[string]OutputOpener)         // Keeps OutputOpener for specific URL
var gURLMuxOutputOpeners = make(map[string]MuxOutputOpener)   // Keeps MuxOutputOpener for specific URL
var gURLOutputOpenersByHandler = make(map[int64]OutputOpener) // Keeps OutputOpener for specific URL
// gHandleNum is used for both FDs and handles so that they will not collide if misused.
var gHandleNum int64
var gMutex sync.Mutex
var gInputOpener InputOpener
var gOutputOpener OutputOpener
var gMuxOutputOpener MuxOutputOpener

// GlobalsT wraps the global variables used in avpipe. It should be gradually replaced in the
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

// InitIOHandler is used to set global input/output opener for avpipe
// If there is no specific input/output opener for a URL, the global
// input/output opener will be used.
func InitIOHandler(inputOpener InputOpener, outputOpener OutputOpener) {
	gInputOpener = inputOpener
	gOutputOpener = outputOpener
}

// InitMuxIOHandler sets the global handlers for muxing (similar to InitIOHandler for transcoding)
func InitMuxIOHandler(inputOpener InputOpener, muxOutputOpener MuxOutputOpener) {
	gInputOpener = inputOpener
	gMuxOutputOpener = muxOutputOpener
}

// InitUrlIOHandler is used to set input/output opener specific to a URL.
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

func InitUrlIOHandlerIfNotPresent(url string, inputOpener InputOpener, outputOpener OutputOpener) (inSet bool, outSet bool) {
	gMutex.Lock()
	defer gMutex.Unlock()
	if inputOpener != nil {
		if _, ok := gURLInputOpeners[url]; !ok {
			gURLInputOpeners[url] = inputOpener
			inSet = true
		}
	}

	if outputOpener != nil {
		if _, ok := gURLOutputOpeners[url]; !ok {
			gURLOutputOpeners[url] = outputOpener
			outSet = true
		}
	}

	Log.Debug("InitUrlIOHandlerIfNotPresent", "url", url, "urlInputOpener", inSet, "urlOutputOpener", outSet)

	return
}

// InitUrlMuxIOHandler sets specific IO handler for muxing a url/file (similar to InitUrlIOHandler)
// To clean up - call RemoveUrlMuxIOHandler.
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

// RemoveUrlMuxIOHandler removes IO handlers used for muxing a url/file
func RemoveUrlMuxIOHandler(url string) {
	gMutex.Lock()
	delete(gURLInputOpeners, url)
	delete(gURLMuxOutputOpeners, url)
	gMutex.Unlock()

	Log.Debug("RemoveUrlMuxIOHandler", "url", url)
}

func GetInputOpener(url string) InputOpener {
	gMutex.Lock()
	defer gMutex.Unlock()
	if inputOpener, ok := gURLInputOpeners[url]; ok {
		Log.Debug("GetInputOpener getting custom opener", "url", url)
		return inputOpener
	}

	Log.Debug("GetInputOpener falling back to global opener", "url", url)

	return gInputOpener
}

func GetGlobalInputOpener() InputOpener {
	gMutex.Lock()
	defer gMutex.Unlock()
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

// PutMuxOutputOpener registers a mux output handler keyed by fd.
// To clean up - call DeleteMuxOutputHandler.
func PutMuxOutputOpener(fd int64, muxOutputHandler OutputHandler) {
	gMutex.Lock()
	gMuxHandlers[fd] = muxOutputHandler
	gMutex.Unlock()
}

// DeleteMuxOutputHandler removes the mux output handler entry for fd.
// Called from AVPipeCloseMuxOutput.
func DeleteMuxOutputHandler(fd int64) {
	gMutex.Lock()
	delete(gMuxHandlers, fd)
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
