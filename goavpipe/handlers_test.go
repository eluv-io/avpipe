package goavpipe

import "testing"

// stubOutputHandler is a no-op OutputHandler used only for exercising the
// gMuxHandlers Put/Delete bookkeeping. The methods are never called.
type stubOutputHandler struct{}

func (stubOutputHandler) Write(buf []byte) (int, error)                   { return len(buf), nil }
func (stubOutputHandler) Seek(offset int64, whence int) (int64, error)    { return 0, nil }
func (stubOutputHandler) Close() error                                    { return nil }
func (stubOutputHandler) Stat(int, AVType, AVStatType, interface{}) error { return nil }

// stubInputOpener / stubOutputOpener — interface-satisfying no-ops, used
// only to exercise the per-URL and per-handler bookkeeping maps.
type stubInputOpener struct{}

func (stubInputOpener) Open(int64, string) (InputHandler, error) { return nil, nil }

type stubOutputOpener struct{}

func (stubOutputOpener) Open(int64, int64, int, int, int64, AVType) (OutputHandler, error) {
	return nil, nil
}

// TestMuxOutputHandlerPutDelete verifies that DeleteMuxOutputHandler removes
// the entry PutMuxOutputOpener adds. Without the delete, gMuxHandlers would
// grow unboundedly across mux operations.
func TestMuxOutputHandlerPutDelete(t *testing.T) {
	// Use a large negative fd unlikely to collide with any concurrent state.
	// gMutex guarantees our Put/Delete are atomic, but we still avoid stomping.
	const fd int64 = -424242

	gMutex.Lock()
	_, existedBefore := gMuxHandlers[fd]
	gMutex.Unlock()
	if existedBefore {
		t.Fatalf("test fd %d already present in gMuxHandlers — pick a different sentinel", fd)
	}

	h := stubOutputHandler{}
	PutMuxOutputOpener(fd, h)

	gMutex.Lock()
	got, ok := gMuxHandlers[fd]
	gMutex.Unlock()
	if !ok {
		t.Fatalf("PutMuxOutputOpener did not insert entry for fd %d", fd)
	}
	if got != h {
		t.Fatalf("PutMuxOutputOpener stored %v, want %v", got, h)
	}

	DeleteMuxOutputHandler(fd)

	gMutex.Lock()
	_, stillPresent := gMuxHandlers[fd]
	gMutex.Unlock()
	if stillPresent {
		t.Fatalf("DeleteMuxOutputHandler left entry for fd %d in gMuxHandlers", fd)
	}
}

// TestMuxOutputHandlerDeleteMissingIsSafe verifies that DeleteMuxOutputHandler
// on a never-inserted fd is a no-op (important because AVPipeCloseMuxOutput
// defers the delete even on the error path).
func TestMuxOutputHandlerDeleteMissingIsSafe(t *testing.T) {
	const fd int64 = -424243

	gMutex.Lock()
	_, existedBefore := gMuxHandlers[fd]
	lenBefore := len(gMuxHandlers)
	gMutex.Unlock()
	if existedBefore {
		t.Fatalf("test fd %d already present in gMuxHandlers — pick a different sentinel", fd)
	}

	DeleteMuxOutputHandler(fd) // must not panic or mutate other entries

	gMutex.Lock()
	lenAfter := len(gMuxHandlers)
	gMutex.Unlock()
	if lenAfter != lenBefore {
		t.Fatalf("DeleteMuxOutputHandler on missing fd changed map size: before=%d after=%d",
			lenBefore, lenAfter)
	}
}

// TestIOHandlerPutDelete verifies the gHandlers Put/Delete symmetry used by
// the non-mux transcode path. DeleteCIOHandlerAndOutputOpeners must drop the
// entry that PutCIOHandler inserted.
func TestIOHandlerPutDelete(t *testing.T) {
	const fd int64 = -525252

	gMutex.Lock()
	_, existedBefore := gHandlers[fd]
	gMutex.Unlock()
	if existedBefore {
		t.Fatalf("test fd %d already present in gHandlers — pick a different sentinel", fd)
	}

	sentinel := &struct{ name string }{name: "ioh"}
	Globals.PutCIOHandler(fd, sentinel)

	got, ok := Globals.GetCIOHandler(fd)
	if !ok {
		t.Fatalf("GetCIOHandler did not find entry for fd %d", fd)
	}
	if got != sentinel {
		t.Fatalf("GetCIOHandler returned %v, want %v", got, sentinel)
	}

	Globals.DeleteCIOHandlerAndOutputOpeners(fd)

	if _, stillPresent := Globals.GetCIOHandler(fd); stillPresent {
		t.Fatalf("DeleteCIOHandlerAndOutputOpeners left entry for fd %d in gHandlers", fd)
	}
}

// TestAssignOutputOpenerAndDelete verifies that AssignOutputOpener inserts
// into gURLOutputOpenersByHandler and DeleteCIOHandlerAndOutputOpeners (which
// also handles gHandlers) clears it.
func TestAssignOutputOpenerAndDelete(t *testing.T) {
	opener := stubOutputOpener{}
	fd := Globals.AssignOutputOpener(opener)
	if fd <= 0 {
		t.Fatalf("AssignOutputOpener returned invalid fd: %d", fd)
	}

	gMutex.Lock()
	_, ok := gURLOutputOpenersByHandler[fd]
	gMutex.Unlock()
	if !ok {
		t.Fatalf("AssignOutputOpener did not insert entry for fd %d", fd)
	}

	if got := GetOutputOpenerByHandler(fd); got != opener {
		t.Fatalf("GetOutputOpenerByHandler returned %v, want %v", got, opener)
	}

	Globals.DeleteCIOHandlerAndOutputOpeners(fd)

	gMutex.Lock()
	_, stillPresent := gURLOutputOpenersByHandler[fd]
	gMutex.Unlock()
	if stillPresent {
		t.Fatalf("DeleteCIOHandlerAndOutputOpeners left entry for fd %d in gURLOutputOpenersByHandler", fd)
	}
}

// TestAssignOutputOpenerNilReturnsSentinel verifies the documented contract
// that a nil opener yields fd == -1 and no map mutation.
func TestAssignOutputOpenerNilReturnsSentinel(t *testing.T) {
	gMutex.Lock()
	lenBefore := len(gURLOutputOpenersByHandler)
	gMutex.Unlock()

	fd := Globals.AssignOutputOpener(nil)
	if fd != -1 {
		t.Fatalf("AssignOutputOpener(nil) returned fd=%d, want -1", fd)
	}

	gMutex.Lock()
	lenAfter := len(gURLOutputOpenersByHandler)
	gMutex.Unlock()
	if lenAfter != lenBefore {
		t.Fatalf("AssignOutputOpener(nil) mutated map: before=%d after=%d", lenBefore, lenAfter)
	}
}

// TestRemoveURLHandlers verifies RemoveURLHandlers clears both per-URL maps
// set by InitUrlIOHandler.
func TestRemoveURLHandlers(t *testing.T) {
	const url = "test://removeURLHandlers/sentinel"

	gMutex.Lock()
	_, inExists := gURLInputOpeners[url]
	_, outExists := gURLOutputOpeners[url]
	gMutex.Unlock()
	if inExists || outExists {
		t.Fatalf("test url %q already present — pick a different sentinel", url)
	}

	InitUrlIOHandler(url, stubInputOpener{}, stubOutputOpener{})

	gMutex.Lock()
	_, inOk := gURLInputOpeners[url]
	_, outOk := gURLOutputOpeners[url]
	gMutex.Unlock()
	if !inOk || !outOk {
		t.Fatalf("InitUrlIOHandler did not populate both maps: in=%v out=%v", inOk, outOk)
	}

	Globals.RemoveURLHandlers(url)

	gMutex.Lock()
	_, inStill := gURLInputOpeners[url]
	_, outStill := gURLOutputOpeners[url]
	gMutex.Unlock()
	if inStill || outStill {
		t.Fatalf("RemoveURLHandlers left entries: in=%v out=%v", inStill, outStill)
	}
}

// TestRemoveUrlMuxIOHandler verifies the mux-side per-URL cleanup added in
// this branch clears both gURLInputOpeners and gURLMuxOutputOpeners.
func TestRemoveUrlMuxIOHandler(t *testing.T) {
	const url = "test://removeUrlMuxIOHandler/sentinel"

	gMutex.Lock()
	_, inExists := gURLInputOpeners[url]
	_, muxExists := gURLMuxOutputOpeners[url]
	gMutex.Unlock()
	if inExists || muxExists {
		t.Fatalf("test url %q already present — pick a different sentinel", url)
	}

	InitUrlMuxIOHandler(url, stubInputOpener{}, nil) // mux opener nil — tolerated by InitUrlMuxIOHandler
	// Populate gURLMuxOutputOpeners directly since InitUrlMuxIOHandler skips nil openers;
	// a stub MuxOutputOpener would add more interface-method boilerplate for little value.
	gMutex.Lock()
	gURLMuxOutputOpeners[url] = nil
	gMutex.Unlock()

	RemoveUrlMuxIOHandler(url)

	gMutex.Lock()
	_, inStill := gURLInputOpeners[url]
	_, muxStill := gURLMuxOutputOpeners[url]
	gMutex.Unlock()
	if inStill || muxStill {
		t.Fatalf("RemoveUrlMuxIOHandler left entries: in=%v mux=%v", inStill, muxStill)
	}
}
