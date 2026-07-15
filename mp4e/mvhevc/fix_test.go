package mvhevc

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

const hdr10SPSHex = "420101022000000300b0000003000003009ca001e020021c4d8815ee4595602d4244024020"

func TestEncodeFileWithLazyMdatStreamsPayload(t *testing.T) {
	payload := bytes.Repeat([]byte{0x01, 0x23, 0x45, 0x67}, 1024)

	var source bytes.Buffer
	ftyp := mp4.NewFtyp("isom", 0, []string{"isom"})
	if err := ftyp.Encode(&source); err != nil {
		t.Fatalf("encode ftyp: %v", err)
	}
	mdat := &mp4.MdatBox{}
	mdat.SetData(payload)
	if err := mdat.Encode(&source); err != nil {
		t.Fatalf("encode mdat: %v", err)
	}

	input := bytes.NewReader(source.Bytes())
	parsed, err := mp4.DecodeFile(input, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		t.Fatalf("decode lazily: %v", err)
	}
	if parsed.Mdat == nil || !parsed.Mdat.IsLazy() {
		t.Fatal("expected a lazy mdat")
	}
	if parsed.Mdat.Data != nil {
		t.Fatalf("lazy mdat loaded %d payload bytes into memory", len(parsed.Mdat.Data))
	}

	var output bytes.Buffer
	if err := encodeFileWithLazyMdat(parsed, input, &output); err != nil {
		t.Fatalf("stream encode: %v", err)
	}
	if !bytes.Equal(output.Bytes(), source.Bytes()) {
		t.Fatal("streamed output differs from source")
	}
}

func TestAdjustChunkOffsetsForMoovGrowth(t *testing.T) {
	stco := &mp4.StcoBox{ChunkOffset: make([]uint32, 2)}
	co64 := &mp4.Co64Box{ChunkOffset: make([]uint64, 2)}
	stbl := mp4.NewStblBox()
	stbl.AddChild(stco)
	stbl.AddChild(co64)
	minf := mp4.NewMinfBox()
	minf.AddChild(stbl)
	mdia := mp4.NewMdiaBox()
	mdia.AddChild(minf)
	trak := mp4.NewTrakBox()
	trak.AddChild(&mp4.TkhdBox{TrackID: 7})
	trak.AddChild(mdia)
	moov := mp4.NewMoovBox()
	moov.AddChild(trak)
	moov.StartPos = 24

	originalMoovSize := moov.Size()
	originalMoovEnd := moov.StartPos + originalMoovSize
	stco.ChunkOffset[0], stco.ChunkOffset[1] = uint32(originalMoovEnd-1), uint32(originalMoovEnd+100)
	co64.ChunkOffset[0], co64.ChunkOffset[1] = originalMoovEnd-1, originalMoovEnd+200

	mdat := &mp4.MdatBox{StartPos: originalMoovEnd}
	mdat.SetLazyDataSize(1024)
	file := mp4.NewFile()
	file.AddChild(moov, moov.StartPos)
	file.AddChild(mdat, mdat.StartPos)

	const growth = 24
	moov.AddChild(mp4.CreateUnknownBox("grow", growth, make([]byte, growth-8)))
	if err := adjustChunkOffsetsForMoovResize(file, originalMoovSize); err != nil {
		t.Fatalf("adjust offsets: %v", err)
	}

	if got, want := stco.ChunkOffset[0], uint32(originalMoovEnd-1); got != want {
		t.Fatalf("stco before moov end = %d, want %d", got, want)
	}
	if got, want := stco.ChunkOffset[1], uint32(originalMoovEnd+100+growth); got != want {
		t.Fatalf("stco after moov = %d, want %d", got, want)
	}
	if got, want := co64.ChunkOffset[0], originalMoovEnd-1; got != want {
		t.Fatalf("co64 before moov end = %d, want %d", got, want)
	}
	if got, want := co64.ChunkOffset[1], originalMoovEnd+200+growth; got != want {
		t.Fatalf("co64 after moov = %d, want %d", got, want)
	}
}

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
