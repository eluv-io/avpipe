package cmd

import (
	"fmt"
	"github.com/eluv-io/avpipe"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const ManifestRetrivalTime = time.Second * 2
const EqualityThreshold = 10

func AnalyseStream(cmdRoot *cobra.Command) error {
	cmdAnalyseStream := &cobra.Command{
		Use:   "analyse-stream",
		Short: "Analyse HLS stream",
		Long:  "Analyse manifest and ABR segments of an HLS stream",

		RunE: doAnalyseStream,
	}
	cmdRoot.AddCommand(cmdAnalyseStream)
	cmdAnalyseStream.PersistentFlags().StringP("url", "", "", "(mandatory) url of HLS stream master play list")
	cmdAnalyseStream.PersistentFlags().StringP("save-to", "", "", "directory to save the downloaded manifest / segments")
	cmdAnalyseStream.PersistentFlags().Int32("test-duration", 600, "test duration in sec.")
	cmdAnalyseStream.PersistentFlags().Int32("connection-timeout", 0, "connection timeout.")
	return nil
}

func doAnalyseStream(cmd *cobra.Command, args []string) error {
	url := cmd.Flag("url").Value.String()
	if len(url) == 0 {
		return fmt.Errorf("url is needed after --url")
	}

	outputDir := cmd.Flag("save-to").Value.String()
	if len(outputDir) > 0 {
		os.Mkdir(outputDir, os.ModePerm)
	}

	testDuration, err := cmd.Flags().GetInt32("test-duration")
	if err != nil {
		return fmt.Errorf("invalid test-duration")
	}

	resp, err := http.Get(url)
	masterManifestBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read master manifest, err=%v", err)
	}

	parsedMasterPlaylist, err := parseMasterPlaylist(string(masterManifestBytes))
	if err != nil {
		fmt.Printf("analyse-stream failed, err=%v\n", err)
		log.Error("analyse-stream failed", "err", err)
		return err
	}

	done := make(chan bool)
	for i := 0; i < len(parsedMasterPlaylist.variants); i++ {
		go func(url string, testDuration int32, outputDir string, variant *hlsVariant) {
			err := analyseVariant(url, testDuration, outputDir, variant)
			if err != nil {
				log.Error("analyse-stream", "err", err)
			} else {
				log.Info("analyse-stream finished", "duration", time.Second*time.Duration(testDuration))
			}
			done <- true
		}(url, testDuration, outputDir, &parsedMasterPlaylist.variants[i])
	}

	for i := 0; i < len(parsedMasterPlaylist.variants); i++ {
		<-done
	}

	return nil
}

func buildManifestUrl(url string, variant *hlsVariant) string {
	index := strings.LastIndex(url, "/")
	variantUrl := variant.attributes["URI"].(string)
	newUrl := url[:index+1] + variantUrl
	log.Debug("buildManifestUrl", "index", index, "url[:index+1]", url[:index+1], "newUrl", newUrl)
	return newUrl
}

func analyseVariant(url string, testDuration int32, outputDir string, variant *hlsVariant) error {

	manifestUrl := buildManifestUrl(url, variant)

	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 20,
		},
		Timeout: 10 * time.Second,
	}

	var variantOutputDir string
	if len(outputDir) > 0 {
		if variant.attributes["WIDTH"] != nil {
			variantOutputDir = filepath.Join(outputDir,
				fmt.Sprintf("video-%dx%d-%d", variant.attributes["WIDTH"].(int), variant.attributes["HEIGHT"].(int), variant.attributes["BANDWIDTH"].(int)))
		} else {
			variantOutputDir = filepath.Join(outputDir, "audio")
		}
		os.Mkdir(variantOutputDir, os.ModePerm)
	}

	now := time.Now()
	endTime := now.Add(time.Duration(testDuration) * time.Second)

	log.Debug("analyseVariant", "variantOutputDir", variantOutputDir, "now", now, "endTime", endTime, "manifestUrl", manifestUrl)

	var prevHlsManifest, curHlsManifest *parsedHlsManifest
	var manifestBytes []byte
	var prevManifestBytes []byte
	var nConsecutiveEqual int
	var curSequenceNumber int
	var prevSequenceNumber int

	mp4Analyser := newMp4Analyser(manifestUrl, variantOutputDir)
	mp4Analyser.Start()

	for now.Before(endTime) {
		log.Debug("NewRequest", "url", manifestUrl)
		req, err := http.NewRequest("GET", manifestUrl, nil)
		if err != nil {
			return fmt.Errorf("fatal error in preparing new request, err=%v", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to get media play list, err=%v", err)
		}

		prevManifestBytes = manifestBytes
		manifestBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Error("analyseVariant failed to read media play list\"", "variant", variantOutputDir, "err", err)
			return fmt.Errorf("failed to read media play list, err=%v", err)
		}

		prevHlsManifest = curHlsManifest
		curHlsManifest, err = parseManifest(string(manifestBytes))
		if err != nil {
			fmt.Printf("Error=%v\n%s\n\n", err, string(manifestBytes))
		}

		// Push the first segment in the manifest into the mp4 analyser queue to download
		mp4Analyser.Push(curHlsManifest)

		prevSequenceNumber = curSequenceNumber
		curSequenceNumber = curHlsManifest.headers["#EXT-X-MEDIA-SEQUENCE"].(int)

		discontinuitySequence := 0
		if _, ok := curHlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"]; ok {
			discontinuitySequence = curHlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"].(int)
		}
		saveManifest(variantOutputDir, curSequenceNumber, manifestBytes, discontinuitySequence)

		//log.Debug("Verifying", "manifest", string(manifestBytes))

		err = verifyHlsManifest(prevHlsManifest, curHlsManifest)
		if err != nil {
			fmt.Printf("Error %v\n", err)
			fmt.Printf("mediaSequenec=%d\n%s\n\n", prevSequenceNumber, string(prevManifestBytes))
			fmt.Printf("mediaSequence=%d\n%s\n\n", curSequenceNumber, string(manifestBytes))
		}

		if curSequenceNumber == prevSequenceNumber {
			nConsecutiveEqual++
		} else {
			nConsecutiveEqual = 0
		}

		if nConsecutiveEqual >= EqualityThreshold {
			log.Error("analyseVariant, the manifests have been the same", "duration", ManifestRetrivalTime*time.Duration(nConsecutiveEqual))
			return fmt.Errorf("The manifests have been the same for the last %v", ManifestRetrivalTime*time.Duration(nConsecutiveEqual))
		}

		log.Debug("Processed", "manifest mediaSequence", curSequenceNumber, "time", now)
		resp.Body.Close()
		time.Sleep(time.Second * 2)
		now = time.Now()
	}

	return nil

}

func saveManifest(variantOutputDir string, startIndex int, manifest []byte, discontinuitySequence int) {
	if len(variantOutputDir) == 0 {
		return
	}

	discontinuityDir := filepath.Join(variantOutputDir, fmt.Sprintf("disc-%d", discontinuitySequence))
	os.Mkdir(discontinuityDir, os.ModePerm)

	path := filepath.Join(discontinuityDir, fmt.Sprintf("manifest-%d.m3u8", startIndex))
	os.WriteFile(path, manifest, 0644)
}

type parsedHlsMasterPlaylist struct {
	variants []hlsVariant
}

type hlsVariant struct {
	mediaType  avpipe.AVMediaType
	attributes map[string]interface{}
	uri        string
}

func parseMasterPlaylist(master string) (*parsedHlsMasterPlaylist, error) {
	hlsMaster := &parsedHlsMasterPlaylist{}

	lines := strings.Split(master, "\n")
	i := 0
	for i < len(lines) {
		switch lines[i] {
		case "#EXTM3U":
			i++
		case "#EXT-X-INDEPENDENT-SEGMENTS":
			i++
		default:
			if strings.Contains(lines[i], "#EXT-X-VERSION") {
				i++
			} else if strings.Contains(lines[i], "#EXT-X-STREAM-INF") ||
				strings.Contains(lines[i], "#EXT-X-MEDIA") {
				variant := hlsVariant{
					attributes: map[string]interface{}{},
				}

				variantLine := strings.Split(lines[i], ":")
				j := strings.Index(variantLine[1], "BANDWIDTH=")
				if j >= 0 {
					k := strings.Index(variantLine[1][j+10:], ",")
					bandwidth, _ := strconv.Atoi(variantLine[1][j+10 : j+10+k])
					variant.attributes["BANDWIDTH"] = bandwidth
				}

				j = strings.Index(variantLine[1], "CODECS=")
				if j >= 0 {
					nQuote := 0
					for k := j + 1; k < len(variantLine[1]) && nQuote < 2; k++ {
						if variantLine[1][k] == '"' {
							nQuote++
						}
						if nQuote == 2 {
							codecs := variantLine[1][j+8 : k]
							variant.attributes["CODECS"] = codecs
						}
					}
				}

				j = strings.Index(variantLine[1], "RESOLUTION=")
				if j >= 0 {
					k := strings.Index(variantLine[1][j+11:], ",")
					resolution := variantLine[1][j+11 : j+11+k]
					resolutionLine := strings.Split(resolution, "x")
					width, _ := strconv.Atoi(resolutionLine[0])
					variant.attributes["WIDTH"] = width
					height, _ := strconv.Atoi(resolutionLine[1])
					variant.attributes["HEIGHT"] = height
				}

				j = strings.Index(variantLine[1], "URI=")
				if j >= 0 {
					k := strings.Index(variantLine[1][j+5:], ",")
					if k < 0 {
						k = len(variantLine[1]) - 1
					}
					uri := variantLine[1][j+5 : k]
					variant.attributes["URI"] = uri
				}

				if strings.Contains(lines[i], "#EXT-X-STREAM-INF") {
					i++
					variant.attributes["URI"] = lines[i]
					variant.uri = lines[i]
				}
				i++
				hlsMaster.variants = append(hlsMaster.variants, variant)
			}
		}
	}

	return hlsMaster, nil
}

type parsedHlsManifest struct {
	headers  map[string]interface{}
	segments []hlsSegment
}

type hlsSegment struct {
	isInit                bool
	isDiscontinuityTag    bool
	discontinuitySequence int
	uri                   string
	duration              float64
}

func parseManifest(manifest string) (*parsedHlsManifest, error) {
	hlsManifest := &parsedHlsManifest{
		headers: map[string]interface{}{},
	}

	hlsLines := strings.Split(manifest, "\n")
	var discontinuityIndex int    // line number of DISCONTINUITY tag in the hls manifest
	var discontinuitySequence int // discontinuity sequence of the manifest

	i := 0
	for i < len(hlsLines) {
		switch hlsLines[i] {
		case "#EXTM3U":
			i++
		case "#EXT-X-VERSION:6":
			i++
		case "#EXT-X-INDEPENDENT-SEGMENTS":
			i++
		default:
			if strings.Contains(hlsLines[i], "#EXT-X-TARGETDURATION") {
				i++
			} else if strings.Contains(hlsLines[i], "#EXT-X-MEDIA-SEQUENCE") {
				seqLine := strings.Split(hlsLines[i], ":")
				sequenceNumber, _ := strconv.Atoi(seqLine[1])
				hlsManifest.headers[seqLine[0]] = sequenceNumber
				i++
			} else if strings.Contains(hlsLines[i], "#EXT-X-DISCONTINUITY-SEQUENCE") {
				discontinuityLine := strings.Split(hlsLines[i], ":")
				discontinuitySequence, _ = strconv.Atoi(discontinuityLine[1])
				hlsManifest.headers[discontinuityLine[0]] = discontinuitySequence
				i++
			} else if strings.Contains(hlsLines[i], "#EXT-X-MAP:URI") {
				initSeg := hlsSegment{
					isInit:                true,
					uri:                   hlsLines[i][len("#EXT-X-MAP:URI=")+1 : len(hlsLines[i])-1],
					discontinuitySequence: discontinuitySequence,
				}
				hlsManifest.segments = append(hlsManifest.segments, initSeg)
				// If this is some init segment in the middle of manifest, check prev segment should be discontinuity segment
				if len(hlsManifest.segments) > 1 {
					if discontinuityIndex == 0 {
						return nil, fmt.Errorf("missing EXT-X-DISCONTINUITY before EXT-X-MAP:URI for INIT segment (discontinuityIndex = 0)")
					}
					if !hlsManifest.segments[discontinuityIndex].isDiscontinuityTag {
						return nil, fmt.Errorf("missing EXT-X-DISCONTINUITY before EXT-X-MAP:URI for INIT segment")
					}
				}
				i++
			} else if strings.Contains(hlsLines[i], "#EXTINF") {
				infLine := strings.Split(hlsLines[i], ":")
				duration, _ := strconv.ParseFloat(infLine[1][:len(infLine[1])-2], 64)
				i++
				seg := hlsSegment{
					uri:                   hlsLines[i],
					duration:              duration,
					discontinuitySequence: discontinuitySequence,
				}
				hlsManifest.segments = append(hlsManifest.segments, seg)
				i++
			} else if strings.Contains(hlsLines[i], "#EXT-X-DISCONTINUITY") {
				seg := hlsSegment{
					isDiscontinuityTag: true,
					uri:                "#EXT-X-DISCONTINUITY",
				}
				hlsManifest.segments = append(hlsManifest.segments, seg)
				discontinuityIndex = len(hlsManifest.segments) - 1
				i++
			} else {
				if len(hlsLines[i]) > 0 {
					fmt.Printf("Unrecognized line '%s'\n", hlsLines[i])
				}
				i++
			}
		}
	}

	if _, ok := hlsManifest.headers["#EXT-X-MEDIA-SEQUENCE"]; !ok {
		return nil, fmt.Errorf("no EXT-X-MEDIA-SEQUENCE found in the manifest")
	}

	if discontinuityIndex == len(hlsManifest.segments)-1 {
		return nil, fmt.Errorf("found EXT-X-DISCONTINUITY tag in the end of manifest")
	}

	return hlsManifest, nil
}

func verifyHlsManifest(prevHlsManifest, curHlsManifest *parsedHlsManifest) error {
	if prevHlsManifest == nil || curHlsManifest == nil {
		return nil
	}

	prevMediaSequence := prevHlsManifest.headers["#EXT-X-MEDIA-SEQUENCE"].(int)
	curMediaSequenece := curHlsManifest.headers["#EXT-X-MEDIA-SEQUENCE"].(int)

	diffSequence := curMediaSequenece - prevMediaSequence
	if diffSequence == 0 {
		// Check the two manifests are equal
		areEqual := equalManifests(prevHlsManifest, curHlsManifest)
		if curMediaSequenece != 0 && !areEqual {
			return fmt.Errorf("Expected equal manifests, media sequence = %d", prevMediaSequence)
		}

		if curMediaSequenece > 0 && areEqual {
			return nil
		}

		// If media sequence is 0, just return
		if curMediaSequenece == 0 {
			return nil
		}
	}

	if _, ok := prevHlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"]; ok {
		discontinuitySequence1 := prevHlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"].(int)
		discontinuitySequence2 := curHlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"].(int)
		if discontinuitySequence1 == discontinuitySequence2 {
			// Check init segments in the beginning of manifests, they should be equal if the discontinuity sequences are the same
			if prevHlsManifest.segments[0] != curHlsManifest.segments[0] {
				return fmt.Errorf("Incorrect init segments from media sequence %d to media sequence %d", prevMediaSequence, curMediaSequenece)
			}
		}
	}

	if diffSequence < 0 {
		return fmt.Errorf("Current manifest media sequence (%d) is smaller than prev manifest media sequence (%d)", curMediaSequenece, prevMediaSequence)
	}

	// Advance the index in prev manifest up to diffSequence (i.e. skip the segments that are out of boundry)
	j := 0
	startIndex := 0
	for i := 0; i < len(prevHlsManifest.segments); i++ {
		if prevHlsManifest.segments[i].isInit || prevHlsManifest.segments[i].isDiscontinuityTag {
			continue
		}
		j++
		if j == diffSequence+1 {
			startIndex = i
			break
		}
	}

	//fmt.Printf("XXX j=%d, diffSequence=%d, startIndex=%d\n", j, diffSequence, startIndex)

	for i := startIndex; i < len(prevHlsManifest.segments); i++ {
		if prevHlsManifest.segments[i].uri != curHlsManifest.segments[i-startIndex+1].uri {
			return fmt.Errorf("Incorrect transition from media sequence %d (seg[%d]=%v) to media sequence %d (seg[%d]=%v)",
				prevMediaSequence, i, prevHlsManifest.segments[i], curMediaSequenece, i-startIndex+1, curHlsManifest.segments[i-startIndex+1])
		}
	}

	return nil
}

func equalManifests(manifest1, manifest2 *parsedHlsManifest) bool {
	mediaSequence1 := manifest1.headers["#EXT-X-MEDIA-SEQUENCE"].(int)
	mediaSequenece2 := manifest2.headers["#EXT-X-MEDIA-SEQUENCE"].(int)

	if mediaSequence1 != mediaSequenece2 {
		return false
	}

	if _, ok := manifest1.headers["#EXT-X-DISCONTINUITY-SEQUENCE"]; ok {
		discontinuitySequence1 := manifest1.headers["#EXT-X-DISCONTINUITY-SEQUENCE"].(int)
		discontinuitySequence2 := manifest2.headers["#EXT-X-DISCONTINUITY-SEQUENCE"].(int)
		if discontinuitySequence1 != discontinuitySequence2 {
			return false
		}
	}

	if len(manifest1.segments) != len(manifest2.segments) {
		return false
	}

	for i := 0; i < len(manifest1.segments); i++ {
		if manifest1.segments[i] != manifest2.segments[i] {
			//log.Debug("NOOOT EQUAL", "seg1", manifest1.segments[i], "seg2", manifest2.segments[i])
			return false
		}
	}

	return true
}
