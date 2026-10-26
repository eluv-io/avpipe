package goavpipe

import (
	"testing"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
)

func TestMPEGTSSelectionValidate(t *testing.T) {
	valid := []*MPEGTSSelection{
		{ProgramIDs: []uint16{101, 102}},
		{PIDs: []uint16{0x21, 0x22}},
	}
	for _, selection := range valid {
		if err := selection.Validate(); err != nil {
			t.Errorf("valid selection %+v: %v", selection, err)
		}
	}

	invalid := []*MPEGTSSelection{
		{},
		{ProgramIDs: []uint16{101}, PIDs: []uint16{0x21}},
		{ProgramIDs: []uint16{0}},
		{ProgramIDs: []uint16{101, 101}},
		{PIDs: []uint16{0}},
		{PIDs: []uint16{0x1fff}},
		{PIDs: []uint16{0x21, 0x21}},
	}
	for _, selection := range invalid {
		if err := selection.Validate(); err == nil {
			t.Errorf("expected invalid selection error for %+v", selection)
		}
	}
}

func TestInputConfigMPEGTSSelectionModes(t *testing.T) {
	selection := &MPEGTSSelection{ProgramIDs: []uint16{101}}
	for _, tc := range []struct {
		name string
		cfg  InputConfig
	}{
		{name: "none", cfg: InputConfig{CopyMode: CopyModeNone, MPEGTSSelection: selection}},
		{name: "raw", cfg: InputConfig{
			CopyMode:          CopyModeRaw,
			CopyPackaging:     transport.RawTs,
			BypassLibavReader: true,
			MPEGTSSelection:   selection,
		}},
		{name: "raw_only", cfg: InputConfig{
			CopyMode:          CopyModeRawOnly,
			CopyPackaging:     transport.RawTs,
			BypassLibavReader: true,
			MPEGTSSelection:   selection,
		}},
		{name: "remuxed", cfg: InputConfig{
			CopyMode:        CopyModeRemuxed,
			CopyPackaging:   transport.RawTs,
			MPEGTSSelection: selection,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate("udp://127.0.0.1:9000"); err == nil {
				t.Fatal("expected MPEG-TS selection mode validation error")
			}
		})
	}

	validRetranscode := InputConfig{
		CopyMode:        CopyModeRetranscode,
		CopyPackaging:   transport.RtpTs,
		StreamBitrate:   6_000_000,
		MPEGTSSelection: selection,
	}
	if err := validRetranscode.Validate("iq://source"); err != nil {
		t.Fatalf("retranscode selection: %v", err)
	}

	multiplePrograms := InputConfig{
		CopyMode:        CopyModeRetranscode,
		CopyPackaging:   transport.RtpTs,
		StreamBitrate:   6_000_000,
		MPEGTSSelection: &MPEGTSSelection{ProgramIDs: []uint16{101, 102}},
	}
	if err := multiplePrograms.Validate("iq://source"); err == nil {
		t.Fatal("expected multiple-program retranscode validation error")
	}
}
