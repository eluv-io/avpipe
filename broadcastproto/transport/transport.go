package transport

import "io"

// Transport defines the interface for transport protocols that wrap MPEGTS data.
type Transport interface {
	Open() (io.ReadCloser, error)
	URL() string
	Handler() string
	PackagingMode() TsPackagingMode
}

// TsPackagingMode defines the desired packaging of the Transport Stream output
type TsPackagingMode string

const (
	UnknownPackagingMode TsPackagingMode = ""
	RawTs                TsPackagingMode = "raw_ts" // Used in SMPTE ST2022
	RtpTs                TsPackagingMode = "rtp_ts"
)

// SS TODO configuration
const PackagingMode = RtpTs
