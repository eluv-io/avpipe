// inject_spatial - Injects Apple MV-HEVC spatial video metadata into an MP4 file.
//
// Adds the vexu (Video Extended Usage) box hierarchy and hfov box to the
// video sample entry (hvc1/hev1), which is required for Apple Vision Pro
// and other spatial video players to recognize the file as stereoscopic.
//
// Box hierarchy injected:
//
//	VisualSampleEntry (hvc1)
//	  ├── vexu
//	  │     ├── eyes
//	  │     │     ├── stri  (stereo indication: both eyes, normal order)
//	  │     │     ├── hero  (hero eye: left)
//	  │     │     └── cams
//	  │     │           └── blin (baseline distance)
//	  │     └── proj
//	  │           └── prji (rectilinear projection)
//	  └── hfov (horizontal field of view)
//
// Usage:
//
//	inject_spatial [-baseline 63500] [-hfov 63500] [-hero left] <input.mp4> <output.mp4>
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/mp4"
)

const boxHdrSize = 8

// --- Box builders: construct raw byte payloads for each spatial metadata box ---

func buildStriBox(stereoFlags byte) []byte {
	// FullBox: size(4) + "stri"(4) + version(1) + flags(3) + stereo_flags(1) = 13
	buf := make([]byte, 13)
	binary.BigEndian.PutUint32(buf[0:4], 13)
	copy(buf[4:8], "stri")
	// version=0, flags=0 at [8:12]
	buf[12] = stereoFlags
	return buf
}

func buildHeroBox(heroEye byte) []byte {
	// FullBox: 13 bytes, same layout as stri
	buf := make([]byte, 13)
	binary.BigEndian.PutUint32(buf[0:4], 13)
	copy(buf[4:8], "hero")
	buf[12] = heroEye
	return buf
}

func buildBlinBox(baselineUM uint32) []byte {
	// FullBox: size(4) + "blin"(4) + version(1) + flags(3) + baseline(4) = 16
	buf := make([]byte, 16)
	binary.BigEndian.PutUint32(buf[0:4], 16)
	copy(buf[4:8], "blin")
	binary.BigEndian.PutUint32(buf[12:16], baselineUM)
	return buf
}

func buildCamsBox(baselineUM uint32) []byte {
	blin := buildBlinBox(baselineUM)
	buf := make([]byte, boxHdrSize, boxHdrSize+len(blin))
	binary.BigEndian.PutUint32(buf[0:4], uint32(boxHdrSize+len(blin)))
	copy(buf[4:8], "cams")
	return append(buf, blin...)
}

func buildPrjiBox(projType string) []byte {
	// FullBox: size(4) + "prji"(4) + version(1) + flags(3) + projection_type(4) = 16
	buf := make([]byte, 16)
	binary.BigEndian.PutUint32(buf[0:4], 16)
	copy(buf[4:8], "prji")
	copy(buf[12:16], projType)
	return buf
}

func buildProjBox(projType string) []byte {
	prji := buildPrjiBox(projType)
	buf := make([]byte, boxHdrSize, boxHdrSize+len(prji))
	binary.BigEndian.PutUint32(buf[0:4], uint32(boxHdrSize+len(prji)))
	copy(buf[4:8], "proj")
	return append(buf, prji...)
}

func buildEyesPayload(stereoFlags byte, heroEye byte, baselineUM uint32) []byte {
	var payload []byte
	payload = append(payload, buildStriBox(stereoFlags)...)
	payload = append(payload, buildHeroBox(heroEye)...)
	payload = append(payload, buildCamsBox(baselineUM)...)
	return payload
}

func buildVexuPayload(stereoFlags byte, heroEye byte, baselineUM uint32) []byte {
	// eyes container
	eyesChildren := buildEyesPayload(stereoFlags, heroEye, baselineUM)
	eyes := make([]byte, boxHdrSize, boxHdrSize+len(eyesChildren))
	binary.BigEndian.PutUint32(eyes[0:4], uint32(boxHdrSize+len(eyesChildren)))
	copy(eyes[4:8], "eyes")
	eyes = append(eyes, eyesChildren...)

	// proj container
	proj := buildProjBox("rect")

	var payload []byte
	payload = append(payload, eyes...)
	payload = append(payload, proj...)
	return payload
}

func buildHfovPayload(hfovThousandths uint32) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf[0:4], hfovThousandths)
	return buf
}

// adjustChunkOffsets adds delta to all chunk offsets in an stco or co64 box.
func adjustChunkOffsets(trak *mp4.TrakBox, delta int64) {
	stbl := trak.Mdia.Minf.Stbl
	if stbl.Stco != nil {
		for i := range stbl.Stco.ChunkOffset {
			stbl.Stco.ChunkOffset[i] = uint32(int64(stbl.Stco.ChunkOffset[i]) + delta)
		}
	}
	if stbl.Co64 != nil {
		for i := range stbl.Co64.ChunkOffset {
			stbl.Co64.ChunkOffset[i] = uint64(int64(stbl.Co64.ChunkOffset[i]) + delta)
		}
	}
}

// findMoovMdatOrder determines if moov is before mdat in the file's box order.
func moovBeforeMdat(f *mp4.File) bool {
	moovSeen := false
	for _, child := range f.Children {
		switch child.Type() {
		case "moov":
			moovSeen = true
		case "mdat":
			return moovSeen
		}
	}
	return false
}

func main() {
	baselineFlag := flag.Uint("baseline", 63500, "Camera baseline in micrometers (default: 63500 = 63.5mm)")
	hfovFlag := flag.Uint("hfov", 63500, "Horizontal field of view in thousandths of a degree (default: 63500 = 63.5°)")
	heroFlag := flag.String("hero", "left", "Hero eye: left, right, or none")
	reversedFlag := flag.Bool("reversed", false, "Eye views are reversed (right eye is base layer)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <input.mp4> <output.mp4>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Injects Apple MV-HEVC spatial video metadata (vexu, hfov boxes)\n")
		fmt.Fprintf(os.Stderr, "into an MP4 file's video sample entry.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) != 2 {
		flag.Usage()
		os.Exit(1)
	}

	inputPath := args[0]
	outputPath := args[1]

	var heroEye byte
	switch *heroFlag {
	case "left":
		heroEye = 1
	case "right":
		heroEye = 2
	case "none":
		heroEye = 0
	default:
		fmt.Fprintf(os.Stderr, "Invalid hero eye: %s (must be left, right, or none)\n", *heroFlag)
		os.Exit(1)
	}

	var stereoFlags byte = 0x03 // hasLeft | hasRight
	if *reversedFlag {
		stereoFlags |= 0x08
	}

	// Open and decode (DecModeNormal loads mdat into memory)
	inFile, err := os.Open(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open input: %v\n", err)
		os.Exit(1)
	}
	defer inFile.Close()

	parsedFile, err := mp4.DecodeFile(inFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot decode MP4: %v\n", err)
		os.Exit(1)
	}

	if parsedFile.Moov == nil {
		fmt.Fprintf(os.Stderr, "No moov box found\n")
		os.Exit(1)
	}

	// Find video track
	var videoTrak *mp4.TrakBox
	for _, trak := range parsedFile.Moov.Traks {
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil && trak.Mdia.Hdlr.HandlerType == "vide" {
			videoTrak = trak
			break
		}
	}
	if videoTrak == nil {
		fmt.Fprintf(os.Stderr, "No video track found\n")
		os.Exit(1)
	}

	stsd := videoTrak.Mdia.Minf.Stbl.Stsd
	if stsd.HvcX == nil {
		fmt.Fprintf(os.Stderr, "No HEVC sample entry (hvc1/hev1) found\n")
		os.Exit(1)
	}

	hvcEntry := stsd.HvcX
	fmt.Fprintf(os.Stderr, "Found HEVC sample entry: %s (%dx%d)\n",
		hvcEntry.Type(), hvcEntry.Width, hvcEntry.Height)

	// Record moov size before modification
	oldMoovSize := parsedFile.Moov.Size()

	// Build and inject vexu box
	vexuPayload := buildVexuPayload(stereoFlags, heroEye, uint32(*baselineFlag))
	vexuSize := uint64(boxHdrSize + len(vexuPayload))
	hvcEntry.AddChild(mp4.CreateUnknownBox("vexu", vexuSize, vexuPayload))

	// Build and inject hfov box (sibling of vexu in sample entry)
	hfovPayload := buildHfovPayload(uint32(*hfovFlag))
	hfovSize := uint64(boxHdrSize + len(hfovPayload))
	hvcEntry.AddChild(mp4.CreateUnknownBox("hfov", hfovSize, hfovPayload))

	fmt.Fprintf(os.Stderr, "Injected vexu box (%d bytes):\n", vexuSize)
	fmt.Fprintf(os.Stderr, "  eyes/stri: stereo_flags=0x%02x (left+right%s)\n",
		stereoFlags, map[bool]string{true: ", reversed", false: ""}[*reversedFlag])
	fmt.Fprintf(os.Stderr, "  eyes/hero: hero_eye=%d (%s)\n", heroEye, *heroFlag)
	fmt.Fprintf(os.Stderr, "  eyes/cams/blin: baseline=%d µm\n", *baselineFlag)
	fmt.Fprintf(os.Stderr, "  proj/prji: rect\n")
	fmt.Fprintf(os.Stderr, "Injected hfov box: %d/1000 degrees\n", *hfovFlag)

	// Fix chunk offsets if moov is before mdat (moov size increased, shifting mdat)
	newMoovSize := parsedFile.Moov.Size()
	delta := int64(newMoovSize) - int64(oldMoovSize)

	if delta != 0 && moovBeforeMdat(parsedFile) {
		fmt.Fprintf(os.Stderr, "Adjusting chunk offsets by %+d bytes (moov before mdat)\n", delta)
		for _, trak := range parsedFile.Moov.Traks {
			adjustChunkOffsets(trak, delta)
		}
	}

	// Write output
	outFile, err := os.Create(outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot create output: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	err = parsedFile.Encode(outFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\nSpatial metadata injected: %s -> %s\n", inputPath, outputPath)
}
