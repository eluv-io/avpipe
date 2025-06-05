package goavpipe

type xcFnT = func(params *XcParams) error
type muxFnT = func(params *XcParams) error
type xcInitFnT = func(params *XcParams) (int32, error)
type xcRunFnT = func(handle int32) error
type xcCancelFnT = func(handle int32) error

// These various variables are used to allow mocking of the avpipe interface in tests
// They are _not_ thread-safe, so they should only be set in tests or in a controlled manner
// They are set to the default avpipe functions in avpipe's init(), so that if they are not set,
// the default behavior is used.
var xcFn xcFnT
var muxFn muxFnT
var xcInitFn xcInitFnT
var xcRunFn xcRunFnT
var xcCancelFn xcCancelFnT

func Xc(params *XcParams) error {
	return xcFn(params)
}

func Mux(params *XcParams) error {
	return muxFn(params)
}

func XcInit(params *XcParams) (int32, error) {
	return xcInitFn(params)
}

func XcRun(handle int32) error {
	return xcRunFn(handle)
}

func XcCancel(handle int32) error {
	return xcCancelFn(handle)
}

// SetXcFn sets the function to be used for transcoding, and returns the previous value.
func SetXcFn(fn xcFnT) xcFnT {
	oldXcFn := xcFn
	xcFn = fn
	return oldXcFn
}

// SetMuxFn sets the function to be used for muxing, and returns the previous value.
func SetMuxFn(fn muxFnT) muxFnT {
	oldMuxFn := muxFn
	muxFn = fn
	return oldMuxFn
}

// SetXcInitFn sets the function to be used for initializing transcoding, and returns the previous value.
func SetXcInitFn(fn xcInitFnT) xcInitFnT {
	oldXcInitFn := xcInitFn
	xcInitFn = fn
	return oldXcInitFn
}

// SetXcRunFn sets the function to be used for running transcoding, and returns the previous value.
func SetXcRunFn(fn xcRunFnT) xcRunFnT {
	oldXcRunFn := xcRunFn
	xcRunFn = fn
	return oldXcRunFn
}

// SetXcCancelFn sets the function to be used for canceling transcoding, and returns the previous value.
func SetXcCancelFn(fn xcCancelFnT) xcCancelFnT {
	oldXcCancelFn := xcCancelFn
	xcCancelFn = fn
	return oldXcCancelFn
}
