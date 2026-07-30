package transport

import (
	"io"
)

type Mock struct {
	Error     error
	Reader    io.ReadCloser
	Packaging TsPackagingMode
}

func (m *Mock) Open() (io.ReadCloser, error) {
	if m.Reader == nil {
		return nil, m.Error
	}
	return m.Reader, nil
}

func (m *Mock) URL() string {
	return "mock://localhost:1234"
}

func (m *Mock) Handler() string {
	return "mock"
}

func (m *Mock) PackagingMode() TsPackagingMode {
	return m.Packaging
}
