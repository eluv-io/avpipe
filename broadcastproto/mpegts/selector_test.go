package mpegts

import (
	"encoding/binary"
	"reflect"
	"sort"
	"testing"

	"github.com/Comcast/gots/v2"
	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/psi"

	"github.com/eluv-io/avpipe/goavpipe"
)

type testES struct {
	streamType byte
	pid        uint16
	descriptor []byte
}

func TestSelectorByProgram(t *testing.T) {
	selector, err := NewSelector(&goavpipe.MPEGTSSelection{ProgramIDs: []uint16{101}})
	if err != nil {
		t.Fatal(err)
	}
	pat := testPAT(map[uint16]uint16{101: 0x100, 102: 0x101})
	pmt101 := testPMT(101, 0x21, []testES{
		{streamType: 0x24, pid: 0x21},
		{streamType: 0x81, pid: 0x22},
		{streamType: 0x86, pid: 0x60},
	})
	pmt102 := testPMT(102, 0x71, []testES{
		{streamType: 0x24, pid: 0x71},
		{streamType: 0x81, pid: 0x72},
	})

	if out := pushSection(t, selector, 0, pat); len(out) != 0 {
		t.Fatalf("PAT emitted before selected PMT: %d packets", len(out))
	}
	if out := pushSection(t, selector, 0x101, pmt102); len(out) != 0 {
		t.Fatalf("unselected PMT emitted: %d packets", len(out))
	}
	out := pushSection(t, selector, 0x100, pmt101)
	assertSnapshot(t, selector.Snapshot(), SelectionSnapshot{
		Ready:        true,
		ProgramIDs:   []uint16{101},
		PMTPIDs:      []uint16{0x100},
		PCRPIDs:      []uint16{0x21},
		VideoPIDs:    []uint16{0x21},
		SelectedPIDs: []uint16{0x21, 0x22, 0x60},
	})
	assertOutputTables(t, out, map[int]int{101: 0x100}, 0x100, []int{0x21, 0x22, 0x60}, 0x21)

	selectedVideo := testPayloadPacket(0x21)
	if got, err := selector.Push(&selectedVideo); err != nil || len(got) != 1 {
		t.Fatalf("selected video: packets=%d err=%v", len(got), err)
	}
	unselectedVideo := testPayloadPacket(0x71)
	if got, err := selector.Push(&unselectedVideo); err != nil || len(got) != 0 {
		t.Fatalf("unselected video: packets=%d err=%v", len(got), err)
	}
}

func TestSelectorByExactPIDKeepsPCR(t *testing.T) {
	selector, err := NewSelector(&goavpipe.MPEGTSSelection{PIDs: []uint16{0x21, 0x22}})
	if err != nil {
		t.Fatal(err)
	}
	pat := testPAT(map[uint16]uint16{101: 0x100})
	pmt := testPMT(101, 0x30, []testES{
		{streamType: 0x24, pid: 0x21},
		{streamType: 0x81, pid: 0x22},
		{streamType: 0x81, pid: 0x23},
		{streamType: 0x86, pid: 0x60},
	})
	pushSection(t, selector, 0, pat)
	out := pushSection(t, selector, 0x100, pmt)
	assertSnapshot(t, selector.Snapshot(), SelectionSnapshot{
		Ready:        true,
		ProgramIDs:   []uint16{101},
		PMTPIDs:      []uint16{0x100},
		PCRPIDs:      []uint16{0x30},
		VideoPIDs:    []uint16{0x21},
		SelectedPIDs: []uint16{0x21, 0x22, 0x30},
	})
	assertOutputTables(t, out, map[int]int{101: 0x100}, 0x100, []int{0x21, 0x22}, 0x30)

	for _, tc := range []struct {
		pid  uint16
		keep bool
	}{{0x21, true}, {0x22, true}, {0x30, true}, {0x23, false}, {0x60, false}, {0x1fff, false}} {
		pkt := testPayloadPacket(tc.pid)
		got, err := selector.Push(&pkt)
		if err != nil {
			t.Fatal(err)
		}
		if (len(got) == 1) != tc.keep {
			t.Errorf("PID 0x%x keep=%v, got %d packets", tc.pid, tc.keep, len(got))
		}
	}
}

func TestSelectorExactPIDsCanResolveMultiplePrograms(t *testing.T) {
	selector, err := NewSelector(&goavpipe.MPEGTSSelection{PIDs: []uint16{0x21, 0x71}})
	if err != nil {
		t.Fatal(err)
	}
	pushSection(t, selector, 0, testPAT(map[uint16]uint16{101: 0x100, 102: 0x101}))
	pushSection(t, selector, 0x100, testPMT(101, 0x21, []testES{{streamType: 0x24, pid: 0x21}}))
	out := pushSection(t, selector, 0x101, testPMT(102, 0x71, []testES{{streamType: 0x24, pid: 0x71}}))
	assertSnapshot(t, selector.Snapshot(), SelectionSnapshot{
		Ready:        true,
		ProgramIDs:   []uint16{101, 102},
		PMTPIDs:      []uint16{0x100, 0x101},
		PCRPIDs:      []uint16{0x21, 0x71},
		VideoPIDs:    []uint16{0x21, 0x71},
		SelectedPIDs: []uint16{0x21, 0x71},
	})
	sections := outputSections(t, out)
	pat, err := psi.NewPAT(append([]byte{0}, sections[0][0]...))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int]int{101: 0x100, 102: 0x101}; !reflect.DeepEqual(pat.ProgramMap(), want) {
		t.Fatalf("PAT map=%v, want %v", pat.ProgramMap(), want)
	}
}

func TestSelectorAccumulatesMultiPacketPMT(t *testing.T) {
	selector, err := NewSelector(&goavpipe.MPEGTSSelection{PIDs: []uint16{0x21}})
	if err != nil {
		t.Fatal(err)
	}
	pushSection(t, selector, 0, testPAT(map[uint16]uint16{101: 0x100}))
	largeDescriptor := make([]byte, 300)
	for i := range largeDescriptor {
		largeDescriptor[i] = byte(i)
	}
	pmt := testPMT(101, 0x21, []testES{
		{streamType: 0x24, pid: 0x21},
		{streamType: 0x81, pid: 0x22, descriptor: largeDescriptor},
	})
	if packets := testPacketize(0x100, pmt); len(packets) < 2 {
		t.Fatal("test PMT did not span packets")
	}
	out := pushSection(t, selector, 0x100, pmt)
	assertOutputTables(t, out, map[int]int{101: 0x100}, 0x100, []int{0x21}, 0x21)
}

func TestSelectorRejectsUnknownProgramAndPID(t *testing.T) {
	t.Run("program", func(t *testing.T) {
		selector, err := NewSelector(&goavpipe.MPEGTSSelection{ProgramIDs: []uint16{999}})
		if err != nil {
			t.Fatal(err)
		}
		pkt := testPacketize(0, testPAT(map[uint16]uint16{101: 0x100}))[0]
		if _, err := selector.Push(&pkt); err == nil {
			t.Fatal("expected unknown program error")
		}
	})

	t.Run("pid after all PMTs", func(t *testing.T) {
		selector, err := NewSelector(&goavpipe.MPEGTSSelection{PIDs: []uint16{0x999}})
		if err != nil {
			t.Fatal(err)
		}
		pushSection(t, selector, 0, testPAT(map[uint16]uint16{101: 0x100}))
		pkt := testPacketize(0x100, testPMT(101, 0x21, []testES{{streamType: 0x24, pid: 0x21}}))[0]
		if _, err := selector.Push(&pkt); err == nil {
			t.Fatal("expected unknown PID error")
		}
	})
}

func assertSnapshot(t *testing.T, got, want SelectionSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot=\n%+v\nwant=\n%+v", got, want)
	}
}

func assertOutputTables(t *testing.T, packets []packet.Packet, wantPAT map[int]int, pmtPID uint16, wantPIDs []int, wantPCR uint16) {
	t.Helper()
	sections := outputSections(t, packets)
	patSections := sections[0]
	if len(patSections) != 1 {
		t.Fatalf("PAT sections=%d, want 1", len(patSections))
	}
	if err := validateSection(patSections[0], 0x00, 12); err != nil {
		t.Fatalf("generated PAT: %v", err)
	}
	pat, err := psi.NewPAT(append([]byte{0}, patSections[0]...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pat.ProgramMap(), wantPAT) {
		t.Fatalf("PAT map=%v, want %v", pat.ProgramMap(), wantPAT)
	}
	pmtSections := sections[pmtPID]
	if len(pmtSections) != 1 {
		t.Fatalf("PMT sections=%d, want 1", len(pmtSections))
	}
	if err := validateSection(pmtSections[0], 0x02, 16); err != nil {
		t.Fatalf("generated PMT: %v", err)
	}
	pmtPayload := append([]byte{0}, pmtSections[0]...)
	pmt, err := psi.NewPMT(pmtPayload)
	if err != nil {
		t.Fatal(err)
	}
	gotPIDs := pmt.Pids()
	sort.Ints(gotPIDs)
	sort.Ints(wantPIDs)
	if !reflect.DeepEqual(gotPIDs, wantPIDs) {
		t.Fatalf("PMT PIDs=%v, want %v", gotPIDs, wantPIDs)
	}
	section := pmtSections[0]
	gotPCR := uint16(section[8]&0x1f)<<8 | uint16(section[9])
	if gotPCR != wantPCR {
		t.Fatalf("PCR PID=0x%x, want 0x%x", gotPCR, wantPCR)
	}
}

func outputSections(t *testing.T, packets []packet.Packet) map[uint16][][]byte {
	t.Helper()
	assemblers := make(map[uint16]*sectionAssembler)
	res := make(map[uint16][][]byte)
	for i := range packets {
		pid := uint16(packets[i].PID())
		if assemblers[pid] == nil {
			assemblers[pid] = &sectionAssembler{}
		}
		sections, err := assemblers[pid].Push(&packets[i])
		if err != nil {
			t.Fatal(err)
		}
		res[pid] = append(res[pid], sections...)
	}
	return res
}

func pushSection(t *testing.T, selector *Selector, pid uint16, section []byte) []packet.Packet {
	t.Helper()
	var out []packet.Packet
	for _, pkt := range testPacketize(pid, section) {
		selected, err := selector.Push(&pkt)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, selected...)
	}
	return out
}

func testPAT(programs map[uint16]uint16) []byte {
	ids := make([]uint16, 0, len(programs))
	for id := range programs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	sectionLength := 9 + 4*len(ids)
	section := []byte{0, 0xb0 | byte(sectionLength>>8), byte(sectionLength), 0, 1, 0xc1, 0, 0}
	for _, id := range ids {
		pid := programs[id]
		section = append(section, byte(id>>8), byte(id), 0xe0|byte(pid>>8), byte(pid))
	}
	return append(section, gots.ComputeCRC(section)...)
}

func testPMT(programID, pcrPID uint16, streams []testES) []byte {
	section := []byte{
		0x02, 0, 0,
		byte(programID >> 8), byte(programID),
		0xc1, 0, 0,
		0xe0 | byte(pcrPID>>8), byte(pcrPID),
		0xf0, 0,
	}
	for _, es := range streams {
		infoLen := len(es.descriptor)
		section = append(section,
			es.streamType,
			0xe0|byte(es.pid>>8), byte(es.pid),
			0xf0|byte(infoLen>>8), byte(infoLen),
		)
		section = append(section, es.descriptor...)
	}
	sectionLength := len(section) - 3 + 4
	section[1] = 0xb0 | byte(sectionLength>>8)
	section[2] = byte(sectionLength)
	return append(section, gots.ComputeCRC(section)...)
}

func testPacketize(pid uint16, section []byte) []packet.Packet {
	data := append([]byte{0}, section...)
	var packets []packet.Packet
	cc := 0
	first := true
	for len(data) > 0 {
		var pkt packet.Packet
		for i := range pkt {
			pkt[i] = 0xff
		}
		pkt[0], pkt[1], pkt[2], pkt[3] = 0x47, byte(pid>>8)&0x1f, byte(pid), 0x10|byte(cc)
		if first {
			pkt[1] |= 0x40
		}
		n := copy(pkt[4:], data)
		data = data[n:]
		packets = append(packets, pkt)
		cc = (cc + 1) & 0x0f
		first = false
	}
	return packets
}

func testPayloadPacket(pid uint16) packet.Packet {
	var pkt packet.Packet
	for i := range pkt {
		pkt[i] = 0xff
	}
	pkt[0], pkt[1], pkt[2], pkt[3] = 0x47, byte(pid>>8)&0x1f, byte(pid), 0x10
	return pkt
}

func TestGeneratedSectionLengths(t *testing.T) {
	for _, section := range [][]byte{
		testPAT(map[uint16]uint16{101: 0x100}),
		testPMT(101, 0x21, []testES{{streamType: 0x24, pid: 0x21}}),
	} {
		want := 3 + int(binary.BigEndian.Uint16(section[1:3])&0x0fff)
		if len(section) != want {
			t.Fatalf("section len=%d, header=%d", len(section), want)
		}
	}
}
