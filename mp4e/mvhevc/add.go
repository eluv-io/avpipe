package mvhevc

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/eluv-io/avpipe/mp4e"
)

const (
	DefaultBaselineUM = uint32(63500)
	DefaultHFOV       = uint32(63500)
)

type AddOptions struct {
	FPS        float64
	Spatial    bool
	BaselineUM uint32
	HFOV       uint32
	HeroEye    string
	Reversed   bool
}

type input struct {
	baseVPS [][]byte
	baseSPS [][]byte
	basePPS [][]byte
	baseSEI [][]byte
	enhSPS  [][]byte
	enhPPS  [][]byte
	vps     *hevc.VPS

	width     uint16
	height    uint16
	timeScale uint32
	sampleDur uint32
	samples   []sample

	addSpatial  bool
	stereoFlags byte
	heroEye     byte
	baseline    uint32
	hfov        uint32
	projType    string
}

type sample struct {
	data   []byte
	size   uint32
	isSync bool
}

func Add(inputPath, outputPath string, opts AddOptions) error {
	log.Info("adding MV-HEVC MP4 metadata", "input", inputPath, "output", outputPath)

	inp, err := parseInput(inputPath, opts.FPS)
	if err != nil {
		return err
	}

	if opts.Spatial {
		if err := applySpatialOptions(inp, opts); err != nil {
			return err
		}
	}

	return buildAndWriteMP4(inp, outputPath)
}

func parseInput(inputPath string, fps float64) (*input, error) {
	if isMP4Input(inputPath) {
		return parseMP4Input(inputPath)
	}
	if fps <= 0 {
		return nil, fmt.Errorf("-fps is required for Annex B input")
	}
	return parseAnnexBInput(inputPath, fps)
}

func applySpatialOptions(inp *input, opts AddOptions) error {
	inp.addSpatial = true
	inp.baseline = opts.BaselineUM
	if inp.baseline == 0 {
		inp.baseline = DefaultBaselineUM
	}
	inp.hfov = opts.HFOV
	if inp.hfov == 0 {
		inp.hfov = DefaultHFOV
	}
	inp.projType = "rect"

	inp.stereoFlags = 0x03 // hasLeft | hasRight
	if opts.Reversed {
		inp.stereoFlags |= 0x08
	}

	switch opts.HeroEye {
	case "", "left":
		inp.heroEye = 1
	case "right":
		inp.heroEye = 2
	case "none":
		inp.heroEye = 0
	default:
		return fmt.Errorf("invalid hero eye: %s", opts.HeroEye)
	}
	return nil
}

func isMP4Input(path string) bool {
	if mp4e.IsMP4Extension(path) {
		return true
	}
	// Also check ISOBMFF/QuickTime ftyp box
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return mp4e.HasFtypHeader(f)
}

func parseAnnexBInput(inputPath string, fps float64) (*input, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, fmt.Errorf("could not read input: %w", err)
	}

	log.Info("parsing Annex B bitstream", "input", inputPath, "bytes", len(data))

	allNalus := avc.ExtractNalusFromByteStream(data)
	if len(allNalus) == 0 {
		return nil, fmt.Errorf("no NALUs found in bitstream")
	}
	log.Info("found Annex B NALUs", "count", len(allNalus))

	var baseVPS, baseSPS, basePPS, baseSEI [][]byte
	var enhSPS, enhPPS [][]byte

	type sampleNalu struct {
		nalu    []byte
		layerID byte
	}
	var videoNalus []sampleNalu
	var currentAU []sampleNalu
	lastIsVideo := false

	seen := make(map[string]bool)
	addUnique := func(dst *[][]byte, nalu []byte) {
		key := string(nalu)
		if !seen[key] {
			seen[key] = true
			*dst = append(*dst, nalu)
		}
	}

	for _, nalu := range allNalus {
		if len(nalu) < 2 {
			continue
		}
		info := hevc.ParseNaluHeader(nalu)
		naluType := info.Type
		layerID := info.LayerID

		switch {
		case naluType == hevc.NALU_VPS:
			if layerID == 0 {
				addUnique(&baseVPS, nalu)
			}
		case naluType == hevc.NALU_SPS:
			if layerID == 0 {
				addUnique(&baseSPS, nalu)
			} else {
				addUnique(&enhSPS, nalu)
			}
		case naluType == hevc.NALU_PPS:
			if layerID == 0 {
				addUnique(&basePPS, nalu)
			} else {
				addUnique(&enhPPS, nalu)
			}
		case naluType == hevc.NALU_SEI_PREFIX || naluType == hevc.NALU_SEI_SUFFIX:
			if layerID == 0 {
				addUnique(&baseSEI, nalu)
			}
		case naluType <= 31:
			sn := sampleNalu{nalu: nalu, layerID: layerID}
			if lastIsVideo && layerID == 0 && len(currentAU) > 0 {
				videoNalus = append(videoNalus, currentAU...)
				videoNalus = append(videoNalus, sampleNalu{})
				currentAU = nil
			}
			currentAU = append(currentAU, sn)
			lastIsVideo = true
		default:
			lastIsVideo = false
		}
	}
	if len(currentAU) > 0 {
		videoNalus = append(videoNalus, currentAU...)
	}

	var samples []sample
	var curData []byte
	var curSize uint32
	var curSync bool
	for _, sn := range videoNalus {
		if sn.nalu == nil {
			if curSize > 0 {
				samples = append(samples, sample{data: curData, size: curSize, isSync: curSync})
				curData = nil
				curSize = 0
				curSync = false
			}
			continue
		}

		naluType := hevc.GetNaluType(sn.nalu[0])
		lenBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBuf, uint32(len(sn.nalu)))
		curData = append(curData, lenBuf...)
		curData = append(curData, sn.nalu...)
		curSize += 4 + uint32(len(sn.nalu))
		if sn.layerID == 0 &&
			(naluType == hevc.NALU_IDR_W_RADL ||
				naluType == hevc.NALU_IDR_N_LP ||
				naluType == hevc.NALU_CRA) {
			curSync = true
		}
	}
	if curSize > 0 {
		samples = append(samples, sample{data: curData, size: curSize, isSync: curSync})
	}

	log.Info("parsed Annex B samples", "samples", len(samples))
	if len(samples) == 0 {
		return nil, fmt.Errorf("no video samples found")
	}
	log.Info("found base parameter sets",
		"vps", len(baseVPS),
		"sps", len(baseSPS),
		"pps", len(basePPS),
		"sei", len(baseSEI))
	log.Info("found enhancement parameter sets", "sps", len(enhSPS), "pps", len(enhPPS))

	if len(baseVPS) == 0 {
		return nil, fmt.Errorf("no VPS found in bitstream (input may be an MP4/MOV container with .hevc extension, not a raw Annex B stream)")
	}
	vps, err := hevc.ParseVPSNALUnit(baseVPS[0])
	if err != nil {
		return nil, fmt.Errorf("VPS parse error: %w", err)
	}
	log.Info("parsed VPS",
		"layers", vps.GetNumLayers(),
		"views", vps.GetNumViews(),
		"multiLayer", vps.IsMultiLayer())

	if len(baseSPS) == 0 {
		return nil, fmt.Errorf("no SPS found in bitstream")
	}
	parsedSPS, err := hevc.ParseSPSNALUnit(baseSPS[0])
	if err != nil {
		return nil, fmt.Errorf("SPS parse error: %w", err)
	}
	imgW, imgH := parsedSPS.ImageSize()

	timeScale, sampleDur := fpsToTimescale(fps)
	log.Info("computed Annex B timing", "timescale", timeScale, "sampleDur", sampleDur, "fps", fps)

	return &input{
		baseVPS:   baseVPS,
		baseSPS:   baseSPS,
		basePPS:   basePPS,
		baseSEI:   baseSEI,
		enhSPS:    enhSPS,
		enhPPS:    enhPPS,
		vps:       vps,
		width:     uint16(imgW),
		height:    uint16(imgH),
		timeScale: timeScale,
		sampleDur: sampleDur,
		samples:   samples,
	}, nil
}

func parseMP4Input(inputPath string) (*input, error) {
	ifd, err := os.Open(inputPath)
	if err != nil {
		return nil, fmt.Errorf("could not open input: %w", err)
	}
	defer func() { _ = ifd.Close() }()

	parsedMP4, err := mp4.DecodeFile(ifd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return nil, fmt.Errorf("could not decode MP4: %w", err)
	}
	if parsedMP4.Moov == nil {
		return nil, fmt.Errorf("no moov box found")
	}

	var trak *mp4.TrakBox
	for _, t := range parsedMP4.Moov.Traks {
		if t.Mdia != nil && t.Mdia.Hdlr != nil && t.Mdia.Hdlr.HandlerType == "vide" {
			trak = t
			break
		}
	}
	if trak == nil {
		return nil, fmt.Errorf("no video track found")
	}

	stbl := trak.Mdia.Minf.Stbl
	if stbl == nil || stbl.Stsd == nil {
		return nil, fmt.Errorf("incomplete sample table")
	}

	var vse *mp4.VisualSampleEntryBox
	for _, child := range stbl.Stsd.Children {
		v, ok := child.(*mp4.VisualSampleEntryBox)
		if ok && v.HvcC != nil {
			vse = v
			break
		}
	}
	if vse == nil {
		return nil, fmt.Errorf("no HEVC sample entry found")
	}

	log.Info("found HEVC sample entry",
		"input", inputPath,
		"type", vse.Type(),
		"width", vse.Width,
		"height", vse.Height)

	hdcr := vse.HvcC.DecConfRec
	baseVPS := dedupNalus(hdcr.GetNalusForType(hevc.NALU_VPS))
	baseSPS := dedupNalus(hdcr.GetNalusForType(hevc.NALU_SPS))
	basePPS := dedupNalus(hdcr.GetNalusForType(hevc.NALU_PPS))
	baseSEI := dedupNalus(hdcr.GetNalusForType(hevc.NALU_SEI_PREFIX))

	var enhSPS, enhPPS [][]byte
	if vse.LhvC != nil {
		enhSPS = dedupNalus(vse.LhvC.GetNalusForType(hevc.NALU_SPS))
		enhPPS = dedupNalus(vse.LhvC.GetNalusForType(hevc.NALU_PPS))
	}

	log.Info("found base parameter sets",
		"vps", len(baseVPS),
		"sps", len(baseSPS),
		"pps", len(basePPS),
		"sei", len(baseSEI))
	log.Info("found enhancement parameter sets", "sps", len(enhSPS), "pps", len(enhPPS))

	if len(baseVPS) == 0 {
		return nil, fmt.Errorf("no VPS found in hvcC")
	}
	vps, err := hevc.ParseVPSNALUnit(baseVPS[0])
	if err != nil {
		return nil, fmt.Errorf("VPS parse error: %w", err)
	}
	log.Info("parsed VPS",
		"layers", vps.GetNumLayers(),
		"views", vps.GetNumViews(),
		"multiLayer", vps.IsMultiLayer())

	timeScale := trak.Mdia.Mdhd.Timescale
	var sampleDur uint32
	if stbl.Stts != nil && len(stbl.Stts.SampleTimeDelta) > 0 {
		sampleDur = stbl.Stts.SampleTimeDelta[0]
	}
	if sampleDur == 0 {
		return nil, fmt.Errorf("could not determine sample duration from stts")
	}
	log.Info("read MP4 timing", "timescale", timeScale, "sampleDur", sampleDur)

	nrSamples := trak.GetNrSamples()
	log.Info("reading MP4 samples", "samples", nrSamples)

	syncMap := make(map[uint32]bool)
	if stbl.Stss != nil {
		for _, sn := range stbl.Stss.SampleNumber {
			syncMap[sn] = true
		}
	}

	samples := make([]sample, 0, nrSamples)
	mdat := parsedMP4.Mdat
	if mdat == nil {
		return nil, fmt.Errorf("no mdat box found")
	}

	dataRanges, err := trak.GetRangesForSampleInterval(1, nrSamples)
	if err != nil {
		return nil, fmt.Errorf("get data ranges: %w", err)
	}

	sampleNr := uint32(1)
	for _, dr := range dataRanges {
		chunkData, err := mdat.ReadData(int64(dr.Offset), int64(dr.Size), ifd)
		if err != nil {
			return nil, fmt.Errorf("read sample data at offset %d: %w", dr.Offset, err)
		}
		offset := uint32(0)
		for offset < uint32(len(chunkData)) && sampleNr <= nrSamples {
			sampleSize := stbl.Stsz.GetSampleSize(int(sampleNr))
			if offset+sampleSize > uint32(len(chunkData)) {
				break
			}
			sData := make([]byte, sampleSize)
			copy(sData, chunkData[offset:offset+sampleSize])
			samples = append(samples, sample{
				data:   sData,
				size:   sampleSize,
				isSync: syncMap[sampleNr],
			})
			offset += sampleSize
			sampleNr++
		}
	}

	log.Info("parsed MP4 samples", "samples", len(samples))

	inp := &input{
		baseVPS:   baseVPS,
		baseSPS:   baseSPS,
		basePPS:   basePPS,
		baseSEI:   baseSEI,
		enhSPS:    enhSPS,
		enhPPS:    enhPPS,
		vps:       vps,
		width:     vse.Width,
		height:    vse.Height,
		timeScale: timeScale,
		sampleDur: sampleDur,
		samples:   samples,
	}

	if vse.Vexu != nil {
		inp.addSpatial = true
		if vse.Vexu.Eyes != nil {
			if vse.Vexu.Eyes.Stri != nil {
				inp.stereoFlags = vse.Vexu.Eyes.Stri.StereoFlags
			}
			if vse.Vexu.Eyes.Hero != nil {
				inp.heroEye = vse.Vexu.Eyes.Hero.HeroEye
			}
			if vse.Vexu.Eyes.Cams != nil && vse.Vexu.Eyes.Cams.Blin != nil {
				inp.baseline = vse.Vexu.Eyes.Cams.Blin.Baseline
			}
		}
		if vse.Vexu.Proj != nil && vse.Vexu.Proj.Prji != nil {
			inp.projType = vse.Vexu.Proj.Prji.ProjectionType
		}
	}
	if vse.Hfov != nil {
		inp.hfov = vse.Hfov.FieldOfView
	}

	return inp, nil
}

func buildAndWriteMP4(inp *input, outputPath string) error {
	hvcC, err := mp4.CreateHvcC(inp.baseVPS, inp.baseSPS, inp.basePPS, true, true, true, true)
	if err != nil {
		return fmt.Errorf("CreateHvcC: %w", err)
	}

	if len(inp.baseSEI) > 0 {
		hvcC.AddNaluArrays([]hevc.NaluArray{
			hevc.NewNaluArray(true, hevc.NALU_SEI_PREFIX, inp.baseSEI),
		})
	}

	var lhvC *mp4.LhvCBox
	if len(inp.enhSPS) > 0 || len(inp.enhPPS) > 0 {
		lhvC = mp4.CreateLhvCFromNalus(inp.enhSPS, inp.enhPPS)
	}

	outFile := mp4.NewFile()
	outFile.AddChild(mp4.NewFtyp("isom", 0, []string{"isom", "iso2", "mp41"}), 0)

	moov := mp4.NewMoovBox()
	moov.AddChild(mp4.CreateMvhd())

	trak := &mp4.TrakBox{}
	tkhd := mp4.CreateTkhd()
	tkhd.TrackID = 1
	tkhd.Width = mp4.Fixed32(inp.width) << 16
	tkhd.Height = mp4.Fixed32(inp.height) << 16
	trak.AddChild(tkhd)

	mdia := &mp4.MdiaBox{}
	mdhd := &mp4.MdhdBox{Timescale: inp.timeScale}
	mdia.AddChild(mdhd)
	hdlr, _ := mp4.CreateHdlr("vide")
	mdia.AddChild(hdlr)

	minf := &mp4.MinfBox{}
	minf.AddChild(&mp4.VmhdBox{})
	dinf := &mp4.DinfBox{}
	dref := &mp4.DrefBox{}
	url := &mp4.URLBox{Flags: 1}
	dref.AddChild(url)
	dinf.AddChild(dref)
	minf.AddChild(dinf)

	stbl := &mp4.StblBox{}
	stsd := mp4.NewStsdBox()
	vse := mp4.CreateVisualSampleEntryBox("hvc1", inp.width, inp.height, hvcC)
	if lhvC != nil {
		vse.AddChild(lhvC)
	}
	if inp.addSpatial {
		vexu := mp4.CreateVexuBox(inp.stereoFlags, inp.heroEye, inp.baseline, inp.projType)
		vse.AddChild(vexu)
		vse.AddChild(&mp4.HfovBox{FieldOfView: inp.hfov})
	}
	stsd.AddChild(vse)
	stbl.AddChild(stsd)

	stts := &mp4.SttsBox{}
	stts.SampleCount = append(stts.SampleCount, uint32(len(inp.samples)))
	stts.SampleTimeDelta = append(stts.SampleTimeDelta, inp.sampleDur)
	stbl.AddChild(stts)

	stss := &mp4.StssBox{}
	for i, s := range inp.samples {
		if s.isSync {
			stss.SampleNumber = append(stss.SampleNumber, uint32(i+1))
		}
	}
	if len(stss.SampleNumber) > 0 {
		stbl.AddChild(stss)
	}

	stsz := &mp4.StszBox{SampleNumber: uint32(len(inp.samples))}
	for _, s := range inp.samples {
		stsz.SampleSize = append(stsz.SampleSize, s.size)
	}
	stbl.AddChild(stsz)

	stsc := &mp4.StscBox{}
	stsc.Entries = append(stsc.Entries, mp4.StscEntry{
		FirstChunk:      1,
		SamplesPerChunk: uint32(len(inp.samples)),
	})
	stsc.SetSingleSampleDescriptionID(1)
	stbl.AddChild(stsc)

	stco := &mp4.StcoBox{}
	stco.ChunkOffset = append(stco.ChunkOffset, 0)
	stbl.AddChild(stco)

	if inp.vps.IsMultiLayer() {
		oinf := mp4.BuildOinfFromVPS(inp.vps)
		sgpdOinf := &mp4.SgpdBox{
			Version:            2,
			GroupingType:       "oinf",
			DefaultLength:      uint32(oinf.Size()),
			SampleGroupEntries: []mp4.SampleGroupEntry{oinf},
		}
		stbl.AddChild(sgpdOinf)

		sbgpOinf := &mp4.SbgpBox{GroupingType: "oinf"}
		sbgpOinf.SampleCounts = append(sbgpOinf.SampleCounts, uint32(len(inp.samples)))
		sbgpOinf.GroupDescriptionIndices = append(sbgpOinf.GroupDescriptionIndices, 1)
		stbl.AddChild(sbgpOinf)

		maxTids := make([]byte, inp.vps.GetNumLayers())
		linf := mp4.BuildLinfFromVPS(inp.vps, maxTids)
		sgpdLinf := &mp4.SgpdBox{
			Version:            2,
			GroupingType:       "linf",
			DefaultLength:      uint32(linf.Size()),
			SampleGroupEntries: []mp4.SampleGroupEntry{linf},
		}
		stbl.AddChild(sgpdLinf)

		sbgpLinf := &mp4.SbgpBox{GroupingType: "linf"}
		sbgpLinf.SampleCounts = append(sbgpLinf.SampleCounts, uint32(len(inp.samples)))
		sbgpLinf.GroupDescriptionIndices = append(sbgpLinf.GroupDescriptionIndices, 1)
		stbl.AddChild(sbgpLinf)
	}

	minf.AddChild(stbl)
	mdia.AddChild(minf)
	trak.AddChild(mdia)

	if inp.vps.IsMultiLayer() {
		trgr := &mp4.TrgrBox{}
		cstg := mp4.CreateTrackGroupTypeBox("cstg", 1001)
		trgr.AddChild(cstg)
		trak.AddChild(trgr)
	}

	moov.AddChild(trak)

	totalDurMedia := uint64(len(inp.samples)) * uint64(inp.sampleDur)
	mdhd.Duration = totalDurMedia
	moov.Mvhd.Timescale = 600
	moov.Mvhd.Duration = totalDurMedia * uint64(moov.Mvhd.Timescale) / uint64(inp.timeScale)
	tkhd.Duration = moov.Mvhd.Duration

	outFile.AddChild(moov, 0)

	var mdatData []byte
	for _, s := range inp.samples {
		mdatData = append(mdatData, s.data...)
	}

	mdatBox := &mp4.MdatBox{}
	mdatBox.SetData(mdatData)
	outFile.AddChild(mdatBox, 0)

	// Size() resolves mdatBox.LargeSize and therefore HeaderSize() based on the payload length
	// Must run before computing the chunk offset (or else HeaderSize() returns 8 for large files)
	_ = mdatBox.Size()

	var sizeBeforeMdat uint64
	for _, box := range outFile.Children {
		if box.Type() != "mdat" {
			sizeBeforeMdat += box.Size()
		}
	}
	chunkOffset := sizeBeforeMdat + mdatBox.HeaderSize()
	if chunkOffset > uint64(^uint32(0)) {
		return fmt.Errorf("chunk offset %d exceeds uint32 range; co64 required", chunkOffset)
	}
	stco.ChunkOffset[0] = uint32(chunkOffset)

	ofd, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("could not create output file: %w", err)
	}
	defer func() { _ = ofd.Close() }()

	if err := outFile.Encode(ofd); err != nil {
		return fmt.Errorf("encode error: %w", err)
	}

	log.Info("wrote MV-HEVC MP4",
		"output", outputPath,
		"width", inp.width,
		"height", inp.height,
		"samples", len(inp.samples),
		"layers", inp.vps.GetNumLayers())
	return nil
}

func fpsToTimescale(fps float64) (timeScale uint32, sampleDur uint32) {
	switch {
	case fps > 23.975 && fps < 23.977:
		return 24000, 1001
	case fps > 29.969 && fps < 29.971:
		return 30000, 1001
	case fps > 59.939 && fps < 59.941:
		return 60000, 1001
	default:
		ts := uint32(fps * 1000)
		return ts, 1000
	}
}

func dedupNalus(nalus [][]byte) [][]byte {
	seen := make(map[string]bool, len(nalus))
	out := make([][]byte, 0, len(nalus))
	for _, nalu := range nalus {
		key := string(nalu)
		if !seen[key] {
			seen[key] = true
			out = append(out, nalu)
		}
	}
	return out
}
