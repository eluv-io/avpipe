package avpipe

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/modern-go/gls"

	elog "github.com/eluv-io/log-go"
)

// logWrapper is used to wrap the standard log-go logger to include the handle of the AVPipe job in
// question, if it is known
type logWrapper struct {
	log *elog.Log
}

func (l *logWrapper) Trace(msg string, fields ...interface{}) {
	fields = append(fields, logHandleIfKnown()...)
	l.log.Trace(msg, fields...)
}

func (l *logWrapper) Debug(msg string, fields ...interface{}) {
	fields = append(fields, logHandleIfKnown()...)
	l.log.Debug(msg, fields...)
}

func (l *logWrapper) Info(msg string, fields ...interface{}) {
	fields = append(fields, logHandleIfKnown()...)
	l.log.Info(msg, fields...)
}

func (l *logWrapper) Warn(msg string, fields ...interface{}) {
	dispatchToChannelIfPresent("WARN", msg, fields...)
	fields = append(fields, logHandleIfKnown()...)
	l.log.Warn(msg, fields...)
}

func (l *logWrapper) Error(msg string, fields ...interface{}) {
	dispatchToChannelIfPresent("ERROR", msg, fields...)
	fields = append(fields, logHandleIfKnown()...)
	l.log.Error(msg, fields...)
}

func (l *logWrapper) Fatal(msg string, fields ...interface{}) {
	fields = append(fields, logHandleIfKnown()...)
	l.log.Fatal(msg, fields...)
}

var log = logWrapper{log: elog.Get("/avpipe")}

var gidHandleMap sync.Map = sync.Map{}
var gidChanMap sync.Map = sync.Map{}

var handleChanMap map[int32]chan string = make(map[int32]chan string)
var handleChanMapMu sync.Mutex

func AssociateGIDWithHandle(handle int32) {
	gid := gls.GoID()
	gidHandleMap.Store(gid, handle)
	if ch, ok := gidChanMap.LoadAndDelete(gid); ok {
		handleChanMapMu.Lock()
		handleChanMap[handle] = ch.(chan string)
		handleChanMapMu.Unlock()
	}
}

// XCEnded releases resources associated with the handle
func XCEnded() {
	handleUntyped, ok := gidHandleMap.LoadAndDelete(gls.GoID())
	if !ok {
		return
	}
	handle := handleUntyped.(int32)
	handleChanMapMu.Lock()
	delete(handleChanMap, handle)
	handleChanMapMu.Unlock()
}

// RegisterWarnErrChanForHandle registers a channel to send error logs to for a given handle.
// If handle is nil, then channel will be registered with a handle that is created on this
// goroutine.
func RegisterWarnErrChanForHandle(handle *int32, errChan chan string) {
	if handle == nil {
		gidChanMap.Store(gls.GoID(), errChan)
		return
	}

	gid := gls.GoID()
	gidHandleMap.Store(gid, *handle)
	handleChanMapMu.Lock()
	if _, ok := handleChanMap[*handle]; ok {
		log.Warn("RegisterErrorChanForHandle: handle already registered with channel", "handle", *handle)
	}
	handleChanMap[*handle] = errChan
	handleChanMapMu.Unlock()
}

func GIDHandle() (int32, bool) {
	gid := gls.GoID()
	handle, ok := gidHandleMap.Load(gid)
	if !ok {
		return 0, false
	}
	return handle.(int32), true
}

func logHandleIfKnown() []interface{} {
	if handle, ok := GIDHandle(); ok {
		buf := &bytes.Buffer{}
		binary.Write(buf, binary.BigEndian, handle)
		return []interface{}{"avp", hex.EncodeToString(buf.Bytes())}
	}
	return nil
}

func dispatchToChannelIfPresent(level string, msg string, fields ...interface{}) {
	if handle, ok := GIDHandle(); ok {
		if ch, ok := handleChanMap[handle]; ok {
			//space-combine fields
			strs := []string{level, msg}
			for _, field := range fields {
				strs = append(strs, fmt.Sprint(field))
			}
			select {
			case ch <- strings.Join(strs, " "):
			default:
			}
		}
	}
}
