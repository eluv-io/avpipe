package avpipe

import (
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
	fields = append(fields, logHandleIfKnown()...)
	l.log.Warn(msg, fields...)
}

func (l *logWrapper) Error(msg string, fields ...interface{}) {
	fields = append(fields, logHandleIfKnown()...)
	l.log.Error(msg, fields...)
}

func (l *logWrapper) Fatal(msg string, fields ...interface{}) {
	fields = append(fields, logHandleIfKnown()...)
	l.log.Fatal(msg, fields...)
}

var log = logWrapper{log: elog.Get("/eluvio/avpipe")}

var gidHandleMap sync.Map = sync.Map{}

func AssociateGIDWithHandle(handle int32) {
	gidHandleMap.Store(gls.GoID(), handle)
}

func DissociateGIDWithHandle() {
	gidHandleMap.Delete(gls.GoID())
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
		return []interface{}{"av_handle", handle}
	}
	return nil
}
