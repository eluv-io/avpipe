package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type LogEventType int

const (
	Unknown = iota
	XcVideoStart
	XcVideoDone
	XcAudioStart
	XcAudioDone
)

type LogAnalyser struct {
	xcVideoMap map[string]*xcStat
	xcAudioMap map[string]*xcStat

	nBypass             int
	minVideoTranscode   time.Duration
	maxVideoTranscode   time.Duration
	totalVideoTranscode time.Duration
	nVideoTranscode     int

	minAudioTranscode   time.Duration
	maxAudioTranscode   time.Duration
	totalAudioTranscode time.Duration
	nAudioTranscode     int
}

type xcStat struct {
	key        string
	xcStart    time.Time
	xcComplete time.Time
	isBypass   bool
}

func newLogAnalyser() *LogAnalyser {
	return &LogAnalyser{
		xcVideoMap: map[string]*xcStat{},
		xcAudioMap: map[string]*xcStat{},
	}
}

func (la *LogAnalyser) analyse(line string) {

	switch la.eventType(line) {
	case XcVideoStart:
		la.xcVideoStart(line)
	case XcVideoDone:
		la.xcVideoDone(line)
	case XcAudioStart:
		la.xcAudioStart(line)
	case XcAudioDone:
		la.xcAudioDone(line)
	}
}

func (la *LogAnalyser) eventType(line string) LogEventType {
	if strings.Contains(line, "AVPSRV xcVideo start") {
		return XcVideoStart
	}

	if strings.Contains(line, "AVPSRV xcAudio start") {
		return XcAudioStart
	}

	if strings.Contains(line, "AVPSRV xcVideo done") {
		return XcVideoDone
	}

	if strings.Contains(line, "AVPSRV xcAudio done") {
		return XcAudioDone
	}

	return Unknown
}

func (la *LogAnalyser) xcVideoStart(line string) {
	tokens := strings.Split(line, " ")
	timeStr := tokens[0]
	var key string
	var bypass string
	for _, token := range tokens {
		if strings.Contains(token, "mustXc") {
			bp := strings.Split(token, "=")
			bypass = bp[1]
			if len(key) > 0 {
				break
			}
		}
		if strings.Contains(token, "key") {
			kv := strings.Split(token, ":")
			key = strings.TrimSpace(kv[1])
			if len(bypass) > 0 {
				break
			}
		}
	}

	layout := "2006-01-02T15:04:05.000Z"
	t, err := time.Parse(layout, timeStr)
	if err != nil {
		return
	}

	isBypass := false
	if bypass == "true" {
		isBypass = true
	}
	stat := &xcStat{
		key:      key,
		xcStart:  t,
		isBypass: isBypass,
	}

	la.xcVideoMap[key] = stat
}

func (la *LogAnalyser) xcVideoDone(line string) {
	tokens := strings.Split(line, " ")
	timeStr := tokens[0]
	var key string
	for _, token := range tokens {
		if strings.Contains(token, "url") {
			kv := strings.Split(token, "=")
			key = strings.TrimSpace(kv[1])
			break
		}
	}

	stat, ok := la.xcVideoMap[key]
	if ok {
		layout := "2006-01-02T15:04:05.000Z"
		t, err := time.Parse(layout, timeStr)
		if err != nil {
			return
		}
		stat.xcComplete = t
		la.xcVideoMap[key] = stat
	}
}

func (la *LogAnalyser) xcAudioStart(line string) {
	tokens := strings.Split(line, " ")
	timeStr := tokens[0]
	var key string
	var bypass string
	for _, token := range tokens {
		if strings.Contains(token, "url") {
			kv := strings.Split(token, "=")
			key = strings.TrimSpace(kv[1])
			if len(bypass) > 0 {
				break
			}
		}
	}

	layout := "2006-01-02T15:04:05.000Z"
	t, err := time.Parse(layout, timeStr)
	if err != nil {
		return
	}

	stat := &xcStat{
		key:     key,
		xcStart: t,
	}

	la.xcAudioMap[key] = stat
}

func (la *LogAnalyser) xcAudioDone(line string) {
	tokens := strings.Split(line, " ")
	timeStr := tokens[0]
	var key string
	for _, token := range tokens {
		if strings.Contains(token, "url") {
			kv := strings.Split(token, "=")
			key = strings.TrimSpace(kv[1])
			break
		}
	}

	stat, ok := la.xcAudioMap[key]
	if ok {
		layout := "2006-01-02T15:04:05.000Z"
		t, err := time.Parse(layout, timeStr)
		if err != nil {
			return
		}
		stat.xcComplete = t
		la.xcAudioMap[key] = stat
	}
}

func (la *LogAnalyser) report() {
	for _, stat := range la.xcVideoMap {
		d := stat.xcComplete.Sub(stat.xcStart)
		if d <= 0 {
			continue
		}
		if d < la.minVideoTranscode || la.minVideoTranscode == 0 {
			la.minVideoTranscode = d
		}

		if d > la.maxVideoTranscode {
			la.maxVideoTranscode = d
		}

		la.totalVideoTranscode += d
		la.nVideoTranscode++

		if stat.isBypass {
			la.nBypass++
		}
	}

	fmt.Printf("Total video transcode=%d, bypass=%d, minTranscode=%v, maxTranscode=%v, avgTranscode=%v\n",
		la.nVideoTranscode, la.nBypass, la.minVideoTranscode, la.maxVideoTranscode, la.totalVideoTranscode/time.Duration(la.nVideoTranscode))

	for _, stat := range la.xcAudioMap {
		d := stat.xcComplete.Sub(stat.xcStart)
		if d <= 0 {
			continue
		}
		if d < la.minAudioTranscode || la.minAudioTranscode == 0 {
			la.minAudioTranscode = d
		}

		if d > la.maxAudioTranscode {
			la.maxAudioTranscode = d
		}

		la.totalAudioTranscode += d
		la.nAudioTranscode++
	}

	fmt.Printf("Total audio transcode=%d, minTranscode=%v, maxTranscode=%v, avgTranscode=%v\n",
		la.nAudioTranscode, la.minAudioTranscode, la.maxAudioTranscode, la.totalAudioTranscode/time.Duration(la.nAudioTranscode))

}

func AnalyseLog(cmdRoot *cobra.Command) error {
	cmdAnalyse := &cobra.Command{
		Use:   "analyse",
		Short: "Analyse qfab log file",
		Long:  "Analyse qfab log file",

		RunE: doAnalyseLog,
	}
	cmdRoot.AddCommand(cmdAnalyse)
	cmdAnalyse.PersistentFlags().StringP("log", "l", "", "(mandatory) log file to be analysed")
	return nil
}

func doAnalyseLog(cmd *cobra.Command, args []string) error {
	filename := cmd.Flag("log").Value.String()
	if len(filename) == 0 {
		return fmt.Errorf("A valid log file is needed after -l")
	}

	file, err := os.Open(filename) // Open for read
	if err != nil {
		log.Fatal("Failed to open log file %s", filename)
	}
	defer file.Close()

	logAnalyser := newLogAnalyser()

	reader := bufio.NewReader(file)
	var line string
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			break
		}

		logAnalyser.analyse(line)
		//fmt.Printf(" > Read %d characters\n", len(line))
	}

	if err != nil && err != io.EOF {
		return err
	}

	logAnalyser.report()

	return nil
}
