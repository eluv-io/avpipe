package transport

import "io"

// Transport defines the interface for transport protocols that wrap MPEGTS data.
type Transport interface {
	Open() (io.ReadCloser, error)
	URL() string
	Handler() string
}

// TsPackagingMode defines the desired packaging of the Transport Stream output
type TsPackagingMode int

const (
	RawTs TsPackagingMode = iota // Used in SMPTE ST2022
	RtpTs
)

// SS TODO configuration
const PackagingMode = RtpTs
