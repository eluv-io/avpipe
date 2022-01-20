/*
 This module is designed to generate stress load on one/multiple avpipe server persistent cache.
 Generating load test on a server persistent cache has two phases:
 1) Generate data and force the server to store segments in persistent cache using following command:
    avcmd stress warmup -c stress.json
 2) Generate load against the segments that are saved in persistent cache using following command:
    avcmd stress run -c stress.json
*/
package cmd

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	elog "github.com/eluv-io/log-go"

	"github.com/spf13/cobra"
)

var log = elog.Get("/eluvio/avcmd")

const MaxIdleConnections = 100

type RequestType int

const (
	UnknownABRSegment RequestType = iota
	VideoABRSegment
	AudioABRSegment
	HLSPlaylistVideo
	HLSPlaylistAudio
)

type TestResource struct {
	ReqType     string  `json:"request_type"`
	SegDuration float32 `json:"duration"`
	URLBase     string  `json:"url_base"`
	URLId       string  `json:"url_id"`
	StartId     int     `json:"start_id"`
	EndId       int     `json:"end_id"`
	URLParam    string  `json:"url_param"`

	stats TestStats
	m     sync.Mutex
}

type TestStats struct {
	nErrors       int
	nSuccess      int
	minRespTime   time.Duration
	maxRespTime   time.Duration
	totalRespTime time.Duration
	minResp       int // Minimum response length
	maxResp       int // Maximum response length
	totalResp     int // Total response length
}

type TestDescriptor struct {
	TestResources []TestResource `json:"test_resources"`
	NumSessions   int            `json:"n_sessions"`
	NumRepeats    int            `json:"n_repeats"`
	//VideoSegDuration int            `json:"video_seg_duration"` // in sec
	//AudioSegDuration int            `json:"audio_seg_duration"` // in sec
}

// createHTTPClient for connection re-use
func createHTTPClient() *http.Client {
	client := &http.Client{
		Transport: &http.Transport{
			MaxConnsPerHost:     10000,
			MaxIdleConns:        10000,
			MaxIdleConnsPerHost: 10000,
			// other option field
		},
		Timeout: time.Duration(10) * time.Second,
	}

	return client
}

func WarmupStress(cmdRoot *cobra.Command) error {
	cmdStress := &cobra.Command{
		Use:   "stress",
		Short: "Stress test the persistent cache",
		Long:  "Stress test the persistent cache",
	}
	cmdRoot.AddCommand(cmdStress)

	cmdStressWarmup := &cobra.Command{
		Use:   "warmup",
		Short: "Initialize the persistent cache",
		Long: `Initialize the persistent cache as a warm up stage.
Running this command is necessary at least once to make persistent cache ready for stress test.`,

		RunE: doStressWarmup,
	}
	cmdStress.AddCommand(cmdStressWarmup)
	cmdStressWarmup.PersistentFlags().StringP("config", "c", "", "(mandatory) config file to do test initialization")

	cmdStressRun := &cobra.Command{
		Use:   "run",
		Short: "Run the persistent cache stress test",
		Long: `Run the persistent cache stress test.
Running this command should be done after initializing persistent cache (init command).`,

		RunE: doStressRun,
	}
	cmdStress.AddCommand(cmdStressRun)
	cmdStressRun.PersistentFlags().StringP("config", "c", "", "(mandatory) config file to run stress test")

	cmdLiveRun := &cobra.Command{
		Use:   "live",
		Short: "Run live stress test",
		Long: `Run the live stress test.
Running this command would create requests every N seconds (default is 2 sec) for ABR segments.`,

		RunE: doLive,
	}
	cmdStress.AddCommand(cmdLiveRun)
	cmdLiveRun.PersistentFlags().StringP("config", "c", "", "(mandatory) config file to run stress test")

	return nil
}

func newTestDescriptor(cmd *cobra.Command, args []string) (*TestDescriptor, error) {
	filename := cmd.Flag("config").Value.String()
	if len(filename) == 0 {
		return nil, fmt.Errorf("Config file is needed after -c")
	}

	jsonFile, err := os.Open(filename)
	defer jsonFile.Close()
	if err != nil {
		return nil, fmt.Errorf("Config file %s doesn't exist or doesn't have permission", filename)
	}

	var testDescriptor TestDescriptor

	byteValue, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		return nil, err
	}

	//fmt.Printf("XXX byteValue=%s\n", string(byteValue))

	err = json.Unmarshal(byteValue, &testDescriptor)
	if err != nil {
		return nil, fmt.Errorf("Config file %s is invalid or has invalid json format (err=%v).", filename, err)
	}

	return &testDescriptor, nil
}

func doStressWarmup(cmd *cobra.Command, args []string) error {
	td, err := newTestDescriptor(cmd, args)
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("Got %d test resource, n_sessions=%d, n_repeats=%d\n",
		len(td.TestResources), td.NumSessions, td.NumRepeats)
	log.Info(msg)
	for i := 0; i < len(td.TestResources); i++ {
		msg = fmt.Sprintf("%+v\n", td.TestResources[i])
		log.Info(msg)
	}

	initPersistentCache(td)

	return nil
}

/*
 * initPersistentCache() initializes the persistent cache by sending GET requests for all the resources defined in
 * config file.
 */
func initPersistentCache(td *TestDescriptor) error {
	if len(td.TestResources) == 0 {
		return fmt.Errorf("No test resource is defined in config file")
	}

	/*
	 * Sequaentially fill the persistent cache, since the server has to do transcoding and it might be overloaded.
	 * (Overloading the server can easily eat all the CPU on the server and make the system unresponsive)
	 */
	for i := 0; i < len(td.TestResources); i++ {
		tr := &td.TestResources[i]
		tr.sendGetRequestForAllResources()
		tr.reportStats()
	}

	td.reportStats()
	return nil
}

func (tr *TestResource) sendGetRequestForAllResources() {
	URLBase := tr.URLBase
	URLId := tr.URLId
	StartId := tr.StartId
	EndId := tr.EndId
	URLParam := tr.URLParam

	for i := StartId; i <= EndId; i++ {
		// Build the URL
		IdStr := strconv.Itoa(i)
		newID := strings.Replace(URLId, "ID", IdStr, 1)
		url := URLBase + newID + "?" + URLParam

		tr.sendGetRequestAndUpdateStats(nil, 1, i, url)
	}
}

func (tr *TestResource) sendGetRequestAndUpdateStats(client *http.Client, session int, iteration int, url string) {
	// Send the URL to the server
	var body []byte
	var err error
	var resp *http.Response

	start := time.Now()
	if client == nil {
		resp, err = http.Get(url)
	} else {
		resp, err = client.Get(url)
	}
	if err == nil {
		body, err = ioutil.ReadAll(resp.Body)
	}
	elapsed := time.Since(start)

	// Update stats
	if err != nil {
		log.Error("GET failed", "url", url, "err", err)
		tr.stats.nErrors++
	} else {
		resp.Body.Close()
		tr.m.Lock()
		tr.stats.nSuccess++

		if tr.stats.minRespTime == 0 || elapsed < tr.stats.minRespTime {
			tr.stats.minRespTime = elapsed
		}
		if tr.stats.maxRespTime == 0 || elapsed > tr.stats.maxRespTime {
			tr.stats.maxRespTime = elapsed
		}
		tr.stats.totalRespTime += elapsed

		if tr.stats.minResp == 0 || len(body) < tr.stats.minResp {
			tr.stats.minResp = len(body)
		}
		if tr.stats.maxResp == 0 || len(body) > tr.stats.maxResp {
			tr.stats.maxResp = len(body)
		}
		tr.stats.totalResp += len(body)
		tr.m.Unlock()
		msg := fmt.Sprintf("session=%d, i=%d, url=%s, len=%d, elapsed=%+v\n", session, iteration, url, len(body), elapsed)
		log.Debug(msg)
	}
}

func (tr *TestResource) reportStats() {
	url := tr.URLBase + tr.URLId + "?" + tr.URLParam
	msg := fmt.Sprintf("URL=%s, nErrors=%d, nSuccess=%d, minRespTime=%+v, maxRespTime=%+v, totalRespTime=%+v, minLen=%d, maxLen=%d, totalLen=%d",
		url, tr.stats.nErrors, tr.stats.nSuccess, tr.stats.minRespTime, tr.stats.maxRespTime, tr.stats.totalRespTime, tr.stats.minResp, tr.stats.maxResp, tr.stats.totalResp)
	log.Info(msg)
}

func doStressRun(cmd *cobra.Command, args []string) error {
	td, err := newTestDescriptor(cmd, args)
	if err != nil {
		return err
	}

	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = MaxIdleConnections

	msg := fmt.Sprintf("Got %d test resource, n_sessions=%d, n_repeats=%d\n",
		len(td.TestResources), td.NumSessions, td.NumRepeats)
	log.Info(msg)
	for i := 0; i < len(td.TestResources); i++ {
		msg = fmt.Sprintf("%+v\n", td.TestResources[i])
		log.Info(msg)
	}

	done := make(chan struct{})

	for session := 1; session <= td.NumSessions; session++ {
		go doStressOneSession(session, td, done)
	}

	for session := 0; session < td.NumSessions; session++ {
		<-done // Wait for background goroutines to finish
	}

	for i := 0; i < len(td.TestResources); i++ {
		tr := &td.TestResources[i]
		tr.reportStats()
	}
	td.reportStats()

	return nil
}

func doStressOneSession(session int, td *TestDescriptor, done chan struct{}) {
	msg := fmt.Sprintf("Starting session %d, iteration=%d", session, td.NumRepeats)
	log.Info(msg)

	client := createHTTPClient()

	nTestResource := len(td.TestResources)
	var url string
	for i := 0; i < td.NumRepeats; i++ {
		n := rand.Intn(nTestResource)
		tr := &td.TestResources[n]
		if getReqType(tr.ReqType) == AudioABRSegment || getReqType(tr.ReqType) == VideoABRSegment {
			segNum := rand.Intn(tr.EndId - tr.StartId + 1)

			IdStr := strconv.Itoa(segNum + tr.StartId)
			newID := strings.Replace(tr.URLId, "ID", IdStr, 1)
			url = tr.URLBase + newID + "?" + tr.URLParam
		} else {
			url = tr.URLBase + "?" + tr.URLParam
		}

		tr.sendGetRequestAndUpdateStats(client, session, i, url)
	}
	msg = fmt.Sprintf("Finished session %d", session)
	log.Info(msg)
	done <- struct{}{}
}

func doLive(cmd *cobra.Command, args []string) error {
	td, err := newTestDescriptor(cmd, args)
	if err != nil {
		return err
	}

	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = MaxIdleConnections

	msg := fmt.Sprintf("Got %d test resource, n_sessions=%d, n_repeats=%d\n",
		len(td.TestResources), td.NumSessions, td.NumRepeats)
	log.Info(msg)

	for i := 0; i < len(td.TestResources); i++ {
		msg = fmt.Sprintf("%+v\n", td.TestResources[i])
		log.Info(msg)
	}

	done := make(chan struct{})

	videoResource, _ := td.getTestResource(VideoABRSegment)
	if videoResource != nil {
		if videoResource.URLBase[len(videoResource.URLBase)-1] != '/' {
			videoResource.URLBase = videoResource.URLBase + "/"
		}
	}

	audioResource, _ := td.getTestResource(AudioABRSegment)
	if audioResource != nil {
		if audioResource.URLBase[len(audioResource.URLBase)-1] != '/' {
			audioResource.URLBase = audioResource.URLBase + "/"
		}
	}

	videoManifestResource, _ := td.getTestResource(HLSPlaylistVideo)
	audioManifestResource, _ := td.getTestResource(HLSPlaylistAudio)

	nGoRoutines := 0

	for session := 1; session <= td.NumSessions; session++ {
		// Each live session tries to mimic a player when playing live
		if videoResource != nil {
			go doStressOneLiveSession(session, videoResource, done)
			nGoRoutines++
		}

		if audioResource != nil {
			go doStressOneLiveSession(session, audioResource, done)
			nGoRoutines++
		}

		if videoManifestResource != nil {
			go doStressOneLiveSession(session, videoManifestResource, done)
			nGoRoutines++
		}

		if audioManifestResource != nil {
			go doStressOneLiveSession(session, audioManifestResource, done)
			nGoRoutines++
		}
	}

	for session := 0; session < nGoRoutines; session++ {
		<-done // Wait for background goroutines to finish
	}

	for i := 0; i < len(td.TestResources); i++ {
		tr := &td.TestResources[i]
		tr.reportStats()
	}
	td.reportStats()

	return nil
}

func doStressOneLiveSession(session int, tr *TestResource, done chan struct{}) {
	endId := tr.EndId
	startId := tr.StartId

	client := createHTTPClient()

	msg := fmt.Sprintf("Starting session %d, iteration=%d, SegDuration=%f",
		session, tr.EndId-tr.StartId, tr.SegDuration)
	log.Info(msg)

	for i := startId; i <= endId; i++ {
		var elapsed time.Duration
		IdStr := strconv.Itoa(i)

		// Every videoManifestReqPeriod ask for a video manifest
		if getReqType(tr.ReqType) == HLSPlaylistVideo || getReqType(tr.ReqType) == HLSPlaylistAudio {
			url := tr.URLBase + "?" + tr.URLParam

			start := time.Now()
			tr.sendGetRequestAndUpdateStats(client, session, i, url)
			elapsed = time.Since(start)
		} else {
			newID := strings.Replace(tr.URLId, "ID", IdStr, 1)
			url := tr.URLBase + newID + "?" + tr.URLParam

			start := time.Now()
			tr.sendGetRequestAndUpdateStats(client, session, i, url)
			elapsed = time.Since(start)
		}

		//log.Info("XXX", "elapsed", elapsed, "SegDuration", tr.SegDuration)
		if int(elapsed) < int(time.Duration(tr.SegDuration)*time.Second) {
			time.Sleep(time.Duration(tr.SegDuration)*time.Second - elapsed)
		}
	}
	msg = fmt.Sprintf("Finished session %d", session)
	log.Info(msg)
	done <- struct{}{}
}

func (td *TestDescriptor) getTestResource(reqType RequestType) (*TestResource, time.Duration) {
	for i := 0; i < len(td.TestResources); i++ {
		if getReqType(td.TestResources[i].ReqType) == reqType {
			return &td.TestResources[i], time.Duration(td.TestResources[i].SegDuration) * time.Second
		}
	}

	return nil, 0
}

func getReqType(reqType string) RequestType {
	switch reqType {
	case "video":
		return VideoABRSegment
	case "audio":
		return AudioABRSegment
	case "playlist_video":
		return HLSPlaylistVideo
	case "playlist_audio":
		return HLSPlaylistAudio
	default:
		return UnknownABRSegment
	}
}

func (td *TestDescriptor) reportStats() {
	var totalStats TestStats
	var totalReq int

	for _, tr := range td.TestResources {
		totalStats.nErrors += tr.stats.nErrors
		totalReq += tr.stats.nErrors
		totalStats.nSuccess += tr.stats.nSuccess
		totalReq += tr.stats.nSuccess
		if totalStats.minRespTime == 0 || totalStats.minRespTime < tr.stats.minRespTime {
			totalStats.minRespTime = tr.stats.minRespTime
		}
		if totalStats.maxRespTime == 0 || totalStats.maxRespTime < tr.stats.maxRespTime {
			totalStats.maxRespTime = tr.stats.maxRespTime
		}
		totalStats.totalRespTime += tr.stats.totalRespTime
		totalStats.totalResp += tr.stats.totalResp
	}

	msg := fmt.Sprintf("Finished ALL, TotalReq=%d, NumErrors=%d, NumSuccess=%d, TotalTime=%+v, TotalBytes=%d, MinRespTime=%+v, MaxRespTime=%+v",
		totalReq, totalStats.nErrors, totalStats.nSuccess, totalStats.totalRespTime, totalStats.totalResp, totalStats.minRespTime, totalStats.maxRespTime)
	log.Info(msg)
}
