package mpegtsxc

import (
	"testing"

	"github.com/eluv-io/avpipe/broadcastproto/mpegts"
)

func TestApplyResolvedSelection(t *testing.T) {
	valid := mpegts.SelectionSnapshot{
		Ready:      true,
		ProgramIDs: []uint16{101},
		PMTPIDs:    []uint16{0x100},
		PCRPIDs:    []uint16{0x21},
		VideoPIDs:  []uint16{0x21},
	}
	classifier := NewClassifier()
	if err := applyResolvedSelection(classifier, valid); err != nil {
		t.Fatal(err)
	}
	if classifier.VideoPID() != 0x21 || classifier.PcrPID() != 0x21 {
		t.Fatalf("classifier video=0x%x PCR=0x%x", classifier.VideoPID(), classifier.PcrPID())
	}

	for name, mutate := range map[string]func(*mpegts.SelectionSnapshot){
		"multiple programs": func(s *mpegts.SelectionSnapshot) { s.ProgramIDs = append(s.ProgramIDs, 102) },
		"multiple videos":   func(s *mpegts.SelectionSnapshot) { s.VideoPIDs = append(s.VideoPIDs, 0x71) },
		"missing PCR":       func(s *mpegts.SelectionSnapshot) { s.PCRPIDs = nil },
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := valid
			mutate(&snapshot)
			if err := applyResolvedSelection(NewClassifier(), snapshot); err == nil {
				t.Fatal("expected selection error")
			}
		})
	}
}
