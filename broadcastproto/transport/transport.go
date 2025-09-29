package transport

import "io"

// Transport defines the interface for transport protocols that wrap MPEGTS data.
type Transport interface {
	Open() (io.ReadCloser, error)
	URL() string
	Handler() string
}
