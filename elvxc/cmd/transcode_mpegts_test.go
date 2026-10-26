package cmd

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/spf13/cobra"
)

func TestParseMPEGTSSelection(t *testing.T) {
	t.Run("programs decimal and hex", func(t *testing.T) {
		got, err := parseMPEGTSSelection([]string{"101", "0x66"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := []uint16{101, 102}; !reflect.DeepEqual(got.ProgramIDs, want) {
			t.Fatalf("program IDs = %v, want %v", got.ProgramIDs, want)
		}
	})

	t.Run("pids decimal and hex", func(t *testing.T) {
		got, err := parseMPEGTSSelection(nil, []string{"33", "0x22"})
		if err != nil {
			t.Fatal(err)
		}
		if want := []uint16{33, 34}; !reflect.DeepEqual(got.PIDs, want) {
			t.Fatalf("PIDs = %v, want %v", got.PIDs, want)
		}
	})

	t.Run("mutually exclusive", func(t *testing.T) {
		if _, err := parseMPEGTSSelection([]string{"101"}, []string{"33"}); err == nil {
			t.Fatal("expected mutually-exclusive selection error")
		}
	})

	t.Run("invalid PID", func(t *testing.T) {
		if _, err := parseMPEGTSSelection(nil, []string{"0x1fff"}); err == nil {
			t.Fatal("expected invalid null PID error")
		}
	})
}

func TestTranscodeMPEGTSOptions(t *testing.T) {
	root := &cobra.Command{Use: "test"}
	if err := InitTranscodeMPEGTS(root); err != nil {
		t.Fatal(err)
	}
	cmd, _, err := root.Find([]string{"transcode-mpegts"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Flags().Lookup("mpegts-program-id") == nil {
		t.Fatal("missing singular --mpegts-program-id flag")
	}
	if cmd.Flags().Lookup("mpegts-program-ids") != nil {
		t.Fatal("unexpected plural --mpegts-program-ids flag")
	}
	for name, value := range map[string]string{
		"input":          "udp://127.0.0.1:9000",
		"output":         "out.ts",
		"mpegts-pids":    "0x21,0x22",
		"video-bitrate":  "4000000",
		"stream-bitrate": "8000000",
		"max-datagrams":  "5",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}

	opts, err := transcodeMPEGTSOptionsFromCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if opts.input != "udp://127.0.0.1:9000" || opts.streamBitrate != 8_000_000 {
		t.Fatalf("unexpected options: %+v", opts)
	}
	if want := []uint16{0x21, 0x22}; !reflect.DeepEqual(opts.selection.PIDs, want) {
		t.Fatalf("PIDs=%v, want %v", opts.selection.PIDs, want)
	}
	if !reflect.DeepEqual(opts.outputs, []string{"out.ts"}) {
		t.Fatalf("outputs=%v", opts.outputs)
	}
}

func TestTranscodeMPEGTSOptionsDefaultsAndValidation(t *testing.T) {
	valid := transcodeMPEGTSOptions{
		input:          "udp://127.0.0.1:9000",
		inputPackaging: "auto",
		outputs:        []string{"O/O1/output.ts", "udp://127.0.0.1:6100"},
		encWidth:       -1,
		encHeight:      -1,
		encoder:        "libx264",
		videoBitrate:   4_000_000,
		crf:            23,
		bitDepth:       8,
		streamBitrate:  8_000_000,
		maxLead:        time.Second,
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*transcodeMPEGTSOptions){
		"non UDP input":        func(o *transcodeMPEGTSOptions) { o.input = "file.ts" },
		"bad packaging":        func(o *transcodeMPEGTSOptions) { o.inputPackaging = "srt" },
		"mux too small":        func(o *transcodeMPEGTSOptions) { o.streamBitrate = int(o.videoBitrate) },
		"unsupported output":   func(o *transcodeMPEGTSOptions) { o.outputs = []string{"rtp://127.0.0.1:6100"} },
		"multiple programs":    func(o *transcodeMPEGTSOptions) { o.selection = mustSelection(t, []string{"101", "102"}, nil) },
		"negative datagrams":   func(o *transcodeMPEGTSOptions) { o.maxInputDatagrams = -1 },
		"invalid output depth": func(o *transcodeMPEGTSOptions) { o.bitDepth = 9 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			opts := valid
			mutate(&opts)
			if err := opts.validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestNewMPEGTSInputTransport(t *testing.T) {
	for _, tc := range []struct {
		url       string
		packaging string
		handler   string
	}{
		{url: "udp://127.0.0.1:9000", packaging: "auto", handler: "udp"},
		{url: "rtp://127.0.0.1:9000", packaging: "auto", handler: "rtp"},
		{url: "udp://127.0.0.1:9000", packaging: "rtp_ts", handler: "rtp"},
	} {
		tp, err := newMPEGTSInputTransport(tc.url, tc.packaging)
		if err != nil {
			t.Fatal(err)
		}
		if got := tp.Handler(); got != tc.handler {
			t.Errorf("%s/%s handler=%q, want %q", tc.url, tc.packaging, got, tc.handler)
		}
	}
}

func TestMPEGTSUDPWriterPacketizesOutput(t *testing.T) {
	conn := &recordingConn{}
	w := &mpegtsUDPWriter{conn: conn, buf: make([]byte, 0, mpegtsUDPDatagramSize)}
	input := make([]byte, 8*mpegtsPacketSize)
	for off := 0; off < len(input); off += mpegtsPacketSize {
		input[off] = 0x47
	}
	if n, err := w.Write(input); err != nil || n != len(input) {
		t.Fatalf("Write n=%d err=%v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := packetLengths(conn.writes), []int{7 * mpegtsPacketSize, mpegtsPacketSize}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UDP datagram lengths=%v, want %v", got, want)
	}
}

func TestMPEGTSUDPWriterSendErrorsAreNonFatal(t *testing.T) {
	conn := &recordingConn{writeErr: errors.New("send failed")}
	w := &mpegtsUDPWriter{conn: conn, buf: make([]byte, 0, mpegtsUDPDatagramSize), destination: "udp://example"}
	input := make([]byte, mpegtsUDPDatagramSize)
	if n, err := w.Write(input); err != nil || n != len(input) {
		t.Fatalf("Write n=%d err=%v", n, err)
	}
	if w.sendErrors != 1 {
		t.Fatalf("sendErrors=%d, want 1", w.sendErrors)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMPEGTSMultiWriterWritesEveryOutput(t *testing.T) {
	first, second := &recordingConn{}, &recordingConn{}
	w := &mpegtsMultiWriteCloser{writers: []io.WriteCloser{first, second}}
	p := make([]byte, mpegtsPacketSize)
	if n, err := w.Write(p); err != nil || n != len(p) {
		t.Fatalf("Write n=%d err=%v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if len(first.writes) != 1 || len(second.writes) != 1 || !first.closed || !second.closed {
		t.Fatalf("outputs not written and closed: first=%+v second=%+v", first, second)
	}
}

func TestOpenMPEGTSFileOutputCreatesParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "output.ts")
	w, err := openMPEGTSOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, mpegtsPacketSize)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != mpegtsPacketSize {
		t.Fatalf("file size=%d, want %d", info.Size(), mpegtsPacketSize)
	}
}

func mustSelection(t *testing.T, programs, pids []string) *goavpipe.MPEGTSSelection {
	t.Helper()
	selection, err := parseMPEGTSSelection(programs, pids)
	if err != nil {
		t.Fatal(err)
	}
	return selection
}

func packetLengths(packets [][]byte) []int {
	lengths := make([]int, len(packets))
	for i := range packets {
		lengths[i] = len(packets[i])
	}
	return lengths
}

type recordingConn struct {
	writes   [][]byte
	closed   bool
	writeErr error
}

func (c *recordingConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *recordingConn) Write(p []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (c *recordingConn) Close() error                     { c.closed = true; return nil }
func (c *recordingConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *recordingConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *recordingConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

type stubAddr string

func (a stubAddr) Network() string { return string(a) }
func (a stubAddr) String() string  { return string(a) }
