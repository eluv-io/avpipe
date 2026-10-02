package mvhevc

import (
	"encoding/hex"
	"testing"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

const hdr10SPSHex = "420101022000000300b0000003000003009ca001e020021c4d8815ee4595602d4244024020"

func TestAddMissingColrFromSPSVUI(t *testing.T) {
	vse := visualSampleEntryWithSPS(t, hdr10SPSHex)

	if !ensureColrFromSPSVUI(vse, 1) {
		t.Fatal("expected colr to be added from SPS VUI")
	}

	colr := findColr(vse)
	if colr == nil {
		t.Fatal("expected visual sample entry to have colr child")
	}
	if colr.ColorType != mp4.ColorTypeOnScreenColors {
		t.Fatalf("color type = %q, want %q", colr.ColorType, mp4.ColorTypeOnScreenColors)
	}
	if colr.ColorPrimaries != 9 || colr.TransferCharacteristics != 16 || colr.MatrixCoefficients != 9 {
		t.Fatalf("color triplet = %d/%d/%d, want 9/16/9",
			colr.ColorPrimaries, colr.TransferCharacteristics, colr.MatrixCoefficients)
	}
	if colr.FullRangeFlag {
		t.Fatal("full range flag = true, want false")
	}
}

func TestAddMissingColrFromSPSVUIDoesNotDuplicate(t *testing.T) {
	vse := visualSampleEntryWithSPS(t, hdr10SPSHex)

	if !ensureColrFromSPSVUI(vse, 1) {
		t.Fatal("expected first call to add colr")
	}
	if ensureColrFromSPSVUI(vse, 1) {
		t.Fatal("expected second call to skip existing colr")
	}

	count := 0
	for _, c := range vse.Children {
		if _, ok := c.(*mp4.ColrBox); ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("colr child count = %d, want 1", count)
	}
}

func TestEnsureColrFromSPSVUIUpdatesQuickTimeColr(t *testing.T) {
	vse := visualSampleEntryWithSPS(t, hdr10SPSHex)
	vse.AddChild(&mp4.ColrBox{
		ColorType:               mp4.QuickTimeColorParameters,
		ColorPrimaries:          1,
		TransferCharacteristics: 1,
		MatrixCoefficients:      1,
	})

	if !ensureColrFromSPSVUI(vse, 1) {
		t.Fatal("expected nclc colr to be updated")
	}

	colr := findColr(vse)
	if colr == nil {
		t.Fatal("expected visual sample entry to have colr child")
	}
	if colr.ColorType != mp4.ColorTypeOnScreenColors {
		t.Fatalf("color type = %q, want %q", colr.ColorType, mp4.ColorTypeOnScreenColors)
	}
	if colr.ColorPrimaries != 9 || colr.TransferCharacteristics != 16 || colr.MatrixCoefficients != 9 {
		t.Fatalf("color triplet = %d/%d/%d, want 9/16/9",
			colr.ColorPrimaries, colr.TransferCharacteristics, colr.MatrixCoefficients)
	}
}

func visualSampleEntryWithSPS(t *testing.T, spsHex string) *mp4.VisualSampleEntryBox {
	t.Helper()

	sps, err := hex.DecodeString(spsHex)
	if err != nil {
		t.Fatalf("decode SPS hex: %v", err)
	}

	hvcC := &mp4.HvcCBox{
		DecConfRec: hevc.DecConfRec{
			NaluArrays: []hevc.NaluArray{
				hevc.NewNaluArray(true, hevc.NALU_SPS, [][]byte{sps}),
			},
		},
	}
	return mp4.CreateVisualSampleEntryBox("hvc1", 3840, 2160, hvcC)
}
