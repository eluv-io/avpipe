package mpegts

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/Comcast/gots/v2"
	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/psi"

	"github.com/eluv-io/avpipe/goavpipe"
)

// SelectionSnapshot is the resolved state of an MPEG-TS selection. SelectedPIDs
// contains elementary PIDs plus mandatory PCR PIDs; PAT and PMT PIDs are listed
// separately. VideoPIDs contains only videos explicitly selected by the config
// (all videos for program selection, or requested video PIDs for PID selection).
type SelectionSnapshot struct {
	Ready        bool
	ProgramIDs   []uint16
	PMTPIDs      []uint16
	PCRPIDs      []uint16
	VideoPIDs    []uint16
	SelectedPIDs []uint16
}

type elementaryStream struct {
	pid        uint16
	streamType uint8
	raw        []byte
}

type programTable struct {
	id          uint16
	pmtPID      uint16
	pcrPID      uint16
	versionByte byte
	programInfo []byte
	streams     []elementaryStream
}

// Selector incrementally parses PAT/PMT sections and emits a valid transport
// stream containing only the configured programs/PIDs. It is packaging agnostic:
// callers feed individual 188-byte TS packets and decide whether omitted packets
// are dropped or replaced with null packets.
type Selector struct {
	cfg goavpipe.MPEGTSSelection

	patAssembler *sectionAssembler
	pmtAssembler map[uint16]*sectionAssembler

	patVersion     int
	transportID    uint16
	patLastSection uint8
	patSections    map[uint8]bool
	programPMTPIDs map[uint16]uint16
	programs       map[uint16]*programTable

	ready            bool
	selectedPrograms map[uint16]*programTable
	selectedPIDs     map[uint16]bool
	videoPIDs        map[uint16]bool
	psiCC            map[uint16]uint8
}

// NewSelector constructs an MPEG-TS selector. A nil selection disables filtering
// and returns nil, nil so callers can pass configuration through directly.
func NewSelector(cfg *goavpipe.MPEGTSSelection) (*Selector, error) {
	if cfg == nil {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cpy := goavpipe.MPEGTSSelection{
		ProgramIDs: append([]uint16(nil), cfg.ProgramIDs...),
		PIDs:       append([]uint16(nil), cfg.PIDs...),
	}
	return &Selector{
		cfg:              cpy,
		patAssembler:     &sectionAssembler{},
		pmtAssembler:     make(map[uint16]*sectionAssembler),
		patVersion:       -1,
		patSections:      make(map[uint8]bool),
		programPMTPIDs:   make(map[uint16]uint16),
		programs:         make(map[uint16]*programTable),
		selectedPrograms: make(map[uint16]*programTable),
		selectedPIDs:     make(map[uint16]bool),
		videoPIDs:        make(map[uint16]bool),
		psiCC:            make(map[uint16]uint8),
	}, nil
}

// Push consumes one TS packet and returns zero or more selected packets. PSI is
// buffered until a complete section is available, then rewritten and packetized.
func (s *Selector) Push(pkt *packet.Packet) ([]packet.Packet, error) {
	if s == nil {
		return []packet.Packet{*pkt}, nil
	}
	pid := uint16(pkt.PID())
	if pid == 0 {
		sections, err := s.patAssembler.Push(pkt)
		if err != nil {
			return nil, fmt.Errorf("mpegts selector PAT: %w", err)
		}
		var out []packet.Packet
		for _, section := range sections {
			emit, err := s.consumePAT(section)
			if err != nil {
				return nil, err
			}
			if emit {
				out = append(out, s.outputPAT()...)
			}
		}
		return out, nil
	}

	assembler := s.pmtAssembler[pid]
	if assembler != nil {
		sections, err := assembler.Push(pkt)
		if err != nil {
			return nil, fmt.Errorf("mpegts selector PMT PID %d: %w", pid, err)
		}
		var out []packet.Packet
		for _, section := range sections {
			packets, err := s.consumePMT(pid, section)
			if err != nil {
				return nil, err
			}
			out = append(out, packets...)
		}
		return out, nil
	}

	if s.ready && s.selectedPIDs[pid] {
		return []packet.Packet{*pkt}, nil
	}
	return nil, nil
}

// Snapshot returns a copy of the selector's currently resolved state.
func (s *Selector) Snapshot() SelectionSnapshot {
	if s == nil {
		return SelectionSnapshot{}
	}
	res := SelectionSnapshot{Ready: s.ready}
	for id, p := range s.selectedPrograms {
		res.ProgramIDs = append(res.ProgramIDs, id)
		res.PMTPIDs = append(res.PMTPIDs, p.pmtPID)
		if p.pcrPID != 0x1fff {
			res.PCRPIDs = append(res.PCRPIDs, p.pcrPID)
		}
	}
	for pid := range s.selectedPIDs {
		res.SelectedPIDs = append(res.SelectedPIDs, pid)
	}
	for pid := range s.videoPIDs {
		res.VideoPIDs = append(res.VideoPIDs, pid)
	}
	sortUint16s(res.ProgramIDs)
	sortUint16s(res.PMTPIDs)
	sortUint16s(res.PCRPIDs)
	sortUint16s(res.VideoPIDs)
	sortUint16s(res.SelectedPIDs)
	res.PMTPIDs = compactUint16s(res.PMTPIDs)
	res.PCRPIDs = compactUint16s(res.PCRPIDs)
	return res
}

func (s *Selector) consumePAT(section []byte) (bool, error) {
	if err := validateSection(section, 0x00, 12); err != nil {
		return false, fmt.Errorf("mpegts selector PAT: %w", err)
	}
	if section[5]&1 == 0 { // current_next_indicator
		return false, nil
	}
	version := int((section[5] >> 1) & 0x1f)
	transportID := binary.BigEndian.Uint16(section[3:5])
	if version != s.patVersion || transportID != s.transportID {
		s.patVersion = version
		s.transportID = transportID
		s.patLastSection = section[7]
		s.patSections = make(map[uint8]bool)
		s.programPMTPIDs = make(map[uint16]uint16)
		s.programs = make(map[uint16]*programTable)
		s.pmtAssembler = make(map[uint16]*sectionAssembler)
		s.clearResolved()
	}
	s.patLastSection = section[7]
	s.patSections[section[6]] = true
	for off := 8; off+4 <= len(section)-4; off += 4 {
		programID := binary.BigEndian.Uint16(section[off : off+2])
		pid := uint16(section[off+2]&0x1f)<<8 | uint16(section[off+3])
		if programID == 0 { // NIT PID, not a program PMT
			continue
		}
		s.programPMTPIDs[programID] = pid
		if s.pmtAssembler[pid] == nil {
			s.pmtAssembler[pid] = &sectionAssembler{}
		}
	}
	s.recompute()
	if s.patComplete() && len(s.cfg.ProgramIDs) > 0 {
		for _, id := range s.cfg.ProgramIDs {
			if _, exists := s.programPMTPIDs[id]; !exists {
				return false, fmt.Errorf("mpegts selector: program ID %d is not present in the PAT", id)
			}
		}
	}
	return section[6] == 0 && s.ready, nil
}

func (s *Selector) consumePMT(pid uint16, section []byte) ([]packet.Packet, error) {
	if err := validateSection(section, 0x02, 16); err != nil {
		return nil, fmt.Errorf("mpegts selector PMT PID %d: %w", pid, err)
	}
	if section[5]&1 == 0 {
		return nil, nil
	}
	programID := binary.BigEndian.Uint16(section[3:5])
	if expected, ok := s.programPMTPIDs[programID]; !ok || expected != pid {
		return nil, nil
	}
	programInfoLen := int(section[10]&0x0f)<<8 | int(section[11])
	streamOffset := 12 + programInfoLen
	if streamOffset > len(section)-4 {
		return nil, fmt.Errorf("mpegts selector PMT PID %d has invalid program_info_length", pid)
	}
	p := &programTable{
		id:          programID,
		pmtPID:      pid,
		pcrPID:      uint16(section[8]&0x1f)<<8 | uint16(section[9]),
		versionByte: section[5],
		programInfo: append([]byte(nil), section[12:streamOffset]...),
	}
	for off := streamOffset; off < len(section)-4; {
		if off+5 > len(section)-4 {
			return nil, fmt.Errorf("mpegts selector PMT PID %d has truncated elementary stream", pid)
		}
		esInfoLen := int(section[off+3]&0x0f)<<8 | int(section[off+4])
		end := off + 5 + esInfoLen
		if end > len(section)-4 {
			return nil, fmt.Errorf("mpegts selector PMT PID %d has invalid ES_info_length", pid)
		}
		p.streams = append(p.streams, elementaryStream{
			pid:        uint16(section[off+1]&0x1f)<<8 | uint16(section[off+2]),
			streamType: section[off],
			raw:        append([]byte(nil), section[off:end]...),
		})
		off = end
	}

	wasReady := s.ready
	s.programs[programID] = p
	s.recompute()
	if !s.ready {
		if len(s.cfg.PIDs) > 0 && s.allPMTsParsed() {
			return nil, fmt.Errorf("mpegts selector: requested PIDs %v are not all declared by an input PMT", s.missingPIDs())
		}
		return nil, nil
	}
	if !wasReady {
		out := s.outputPAT()
		for _, id := range s.selectedProgramIDs() {
			out = append(out, s.outputPMT(s.selectedPrograms[id])...)
		}
		return out, nil
	}
	if selected := s.selectedPrograms[programID]; selected != nil {
		return s.outputPMT(selected), nil
	}
	return nil, nil
}

func (s *Selector) recompute() {
	s.clearResolved()
	if len(s.cfg.ProgramIDs) > 0 {
		for _, id := range s.cfg.ProgramIDs {
			pmtPID, exists := s.programPMTPIDs[id]
			if !exists {
				return
			}
			p := s.programs[id]
			if p == nil || p.pmtPID != pmtPID {
				return
			}
			s.selectedPrograms[id] = p
			for _, es := range p.streams {
				s.selectedPIDs[es.pid] = true
				if psi.LookupPmtStreamType(es.streamType).IsVideoContent() {
					s.videoPIDs[es.pid] = true
				}
			}
			if p.pcrPID != 0x1fff {
				s.selectedPIDs[p.pcrPID] = true
			}
		}
		s.ready = true
		return
	}

	requested := make(map[uint16]bool, len(s.cfg.PIDs))
	found := make(map[uint16]bool, len(s.cfg.PIDs))
	for _, pid := range s.cfg.PIDs {
		requested[pid] = true
	}
	for id, p := range s.programs {
		ownsRequestedPID := requested[p.pcrPID]
		if ownsRequestedPID {
			found[p.pcrPID] = true
		}
		for _, es := range p.streams {
			if requested[es.pid] {
				found[es.pid] = true
				ownsRequestedPID = true
			}
		}
		if ownsRequestedPID {
			s.selectedPrograms[id] = p
		}
	}
	if len(found) != len(requested) {
		s.clearResolved()
		return
	}
	for _, p := range s.selectedPrograms {
		for _, es := range p.streams {
			if requested[es.pid] {
				s.selectedPIDs[es.pid] = true
				if psi.LookupPmtStreamType(es.streamType).IsVideoContent() {
					s.videoPIDs[es.pid] = true
				}
			}
		}
		// PCR is mandatory even when it was not explicitly listed. If it is also
		// an elementary PID, its PMT entry and packets are retained below.
		if p.pcrPID != 0x1fff {
			s.selectedPIDs[p.pcrPID] = true
		}
	}
	s.ready = true
}

func (s *Selector) clearResolved() {
	s.ready = false
	s.selectedPrograms = make(map[uint16]*programTable)
	s.selectedPIDs = make(map[uint16]bool)
	s.videoPIDs = make(map[uint16]bool)
}

func (s *Selector) outputPAT() []packet.Packet {
	if !s.ready {
		return nil
	}
	ids := s.selectedProgramIDs()
	sectionLen := 9 + 4*len(ids)
	if sectionLen > 1021 {
		return nil
	}
	section := make([]byte, 8, 12+4*len(ids))
	section[0] = 0x00
	section[1] = 0xb0 | byte(sectionLen>>8)
	section[2] = byte(sectionLen)
	binary.BigEndian.PutUint16(section[3:5], s.transportID)
	section[5] = 0xc1 | byte((s.patVersion&0x1f)<<1)
	section[6], section[7] = 0, 0
	for _, id := range ids {
		pid := s.selectedPrograms[id].pmtPID
		section = append(section, byte(id>>8), byte(id), 0xe0|byte(pid>>8), byte(pid))
	}
	section = append(section, gots.ComputeCRC(section)...)
	return s.packetizePSI(0, section)
}

func (s *Selector) outputPMT(p *programTable) []packet.Packet {
	if p == nil {
		return nil
	}
	requested := make(map[uint16]bool, len(s.cfg.PIDs))
	for _, pid := range s.cfg.PIDs {
		requested[pid] = true
	}
	keepAll := len(s.cfg.ProgramIDs) > 0
	section := make([]byte, 12, 64)
	section[0] = 0x02
	binary.BigEndian.PutUint16(section[3:5], p.id)
	section[5] = p.versionByte
	section[6], section[7] = 0, 0
	section[8] = 0xe0 | byte(p.pcrPID>>8)
	section[9] = byte(p.pcrPID)
	section[10] = 0xf0 | byte(len(p.programInfo)>>8)
	section[11] = byte(len(p.programInfo))
	section = append(section, p.programInfo...)
	for _, es := range p.streams {
		if keepAll || requested[es.pid] || es.pid == p.pcrPID {
			section = append(section, es.raw...)
		}
	}
	sectionLen := len(section) - 3 + 4
	if sectionLen > 1021 {
		return nil
	}
	section[1] = 0xb0 | byte(sectionLen>>8)
	section[2] = byte(sectionLen)
	section = append(section, gots.ComputeCRC(section)...)
	return s.packetizePSI(p.pmtPID, section)
}

func (s *Selector) packetizePSI(pid uint16, section []byte) []packet.Packet {
	data := make([]byte, 1+len(section))
	copy(data[1:], section) // pointer_field is zero
	var out []packet.Packet
	first := true
	for len(data) > 0 {
		var pkt packet.Packet
		for i := range pkt {
			pkt[i] = 0xff
		}
		pkt[0] = 0x47
		pkt[1] = byte(pid>>8) & 0x1f
		pkt[2] = byte(pid)
		pkt[3] = 0x10 | (s.psiCC[pid] & 0x0f)
		if first {
			pkt[1] |= 0x40
		}
		n := copy(pkt[4:], data)
		data = data[n:]
		s.psiCC[pid] = (s.psiCC[pid] + 1) & 0x0f
		out = append(out, pkt)
		first = false
	}
	return out
}

func (s *Selector) selectedProgramIDs() []uint16 {
	ids := make([]uint16, 0, len(s.selectedPrograms))
	for id := range s.selectedPrograms {
		ids = append(ids, id)
	}
	sortUint16s(ids)
	return ids
}

func (s *Selector) patComplete() bool {
	for section := uint8(0); section <= s.patLastSection; section++ {
		if !s.patSections[section] {
			return false
		}
		if section == 0xff {
			break
		}
	}
	return len(s.patSections) > 0
}

func (s *Selector) allPMTsParsed() bool {
	if !s.patComplete() || len(s.programPMTPIDs) == 0 {
		return false
	}
	for id := range s.programPMTPIDs {
		if s.programs[id] == nil {
			return false
		}
	}
	return true
}

func (s *Selector) missingPIDs() []uint16 {
	found := make(map[uint16]bool)
	for _, p := range s.programs {
		found[p.pcrPID] = true
		for _, es := range p.streams {
			found[es.pid] = true
		}
	}
	var missing []uint16
	for _, pid := range s.cfg.PIDs {
		if !found[pid] {
			missing = append(missing, pid)
		}
	}
	return missing
}

func validateSection(section []byte, tableID byte, minLen int) error {
	if len(section) < minLen || section[0] != tableID {
		return fmt.Errorf("invalid table 0x%02x section", tableID)
	}
	want := 3 + int(section[1]&0x0f)<<8 + int(section[2])
	if want != len(section) || want > 1024 {
		return fmt.Errorf("invalid table 0x%02x section length %d (got %d bytes)", tableID, want, len(section))
	}
	crc := gots.ComputeCRC(section[:len(section)-4])
	if !bytes.Equal(crc, section[len(section)-4:]) {
		return fmt.Errorf("table 0x%02x CRC mismatch", tableID)
	}
	return nil
}

func sortUint16s(v []uint16) {
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
}

func compactUint16s(v []uint16) []uint16 {
	if len(v) < 2 {
		return v
	}
	out := v[:1]
	for _, n := range v[1:] {
		if n != out[len(out)-1] {
			out = append(out, n)
		}
	}
	return out
}

// sectionAssembler reconstructs PSI sections across TS packets. It also handles
// multiple complete sections in a single payload and pointer-field continuations.
type sectionAssembler struct {
	buf  []byte
	want int
}

func (a *sectionAssembler) Push(pkt *packet.Packet) ([][]byte, error) {
	payload, err := pkt.Payload()
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, nil
	}
	var sections [][]byte
	if pkt.PayloadUnitStartIndicator() {
		pointer := int(payload[0])
		if 1+pointer > len(payload) {
			return nil, fmt.Errorf("pointer_field %d exceeds payload", pointer)
		}
		if len(a.buf) > 0 && pointer > 0 {
			completed, err := a.feed(payload[1 : 1+pointer])
			if err != nil {
				return nil, err
			}
			sections = append(sections, completed...)
		}
		// A new payload-unit start abandons any incomplete previous section.
		a.buf, a.want = nil, 0
		completed, err := a.feed(payload[1+pointer:])
		if err != nil {
			return nil, err
		}
		sections = append(sections, completed...)
		return sections, nil
	}
	if len(a.buf) == 0 {
		return nil, nil
	}
	return a.feed(payload)
}

func (a *sectionAssembler) feed(data []byte) ([][]byte, error) {
	var sections [][]byte
	for len(data) > 0 {
		if len(a.buf) == 0 && data[0] == 0xff {
			return sections, nil
		}
		if a.want == 0 {
			need := 3 - len(a.buf)
			if need > len(data) {
				need = len(data)
			}
			a.buf = append(a.buf, data[:need]...)
			data = data[need:]
			if len(a.buf) < 3 {
				return sections, nil
			}
			a.want = 3 + int(a.buf[1]&0x0f)<<8 + int(a.buf[2])
			if a.want < 3 || a.want > 1024 {
				invalidLength := a.want
				a.buf, a.want = nil, 0
				return nil, fmt.Errorf("invalid PSI section length %d", invalidLength)
			}
		}
		need := a.want - len(a.buf)
		if need > len(data) {
			need = len(data)
		}
		a.buf = append(a.buf, data[:need]...)
		data = data[need:]
		if len(a.buf) == a.want {
			sections = append(sections, append([]byte(nil), a.buf...))
			a.buf, a.want = nil, 0
		}
	}
	return sections, nil
}
