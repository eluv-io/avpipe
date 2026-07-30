package goavpipe

// ErrRetryField is the field name of an eluv-io/errors-go error that can be used to annotate an error as retryable.
// Currently only used in live-streaming when errors occur in input stream IO.
const ErrRetryField = "AVPIPE_RETRY"
