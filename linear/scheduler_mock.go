package linear

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/qluvio/avpipe/ts"
)

func MakeMockSource1(sr *SignalRecorder) *SignalSource {
	src := &SignalSource{qhash: "hq__mocksource1"}
	go func() {
		time.Sleep(1 * time.Second)

		sr.Record(&Signal{Kind: Scte35, Source: src, Id: 16})
		fmt.Println("signal 16")
		time.Sleep(4 * time.Second)

		sr.Record(&Signal{Kind: Scte35, Source: src, Id: 52})
		fmt.Println("signal 52")
		time.Sleep(2 * time.Second)

		sr.Record(&Signal{Kind: Scte35, Source: src, Id: 53})
		fmt.Println("signal 53")
	}()
	return src
}

func MakeMockSourceFlagDayBase(sr *SignalRecorder, qhash string) (src *SignalSource, err error) {

	// There is a discrepancy of pts 331984 between scte35 signal and actual stream
	// A simple way to compensate is to add this value to the start PTS of the source stream
	src = &SignalSource{qhash: qhash, startPts: 8553957321 + 331984, timescale: 90000}

	//go func() {
	//time.Sleep(1 * time.Second)

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 1, Pts: 8554888184})
	fmt.Println("NEW SCTE35", qhash, 1) // content id
	//time.Sleep(4 * time.Second)

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 1, Pts: 8559388284})
	fmt.Println("NEW SCTE35", qhash, 1) // content id
	//time.Sleep(2 * time.Second)

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 1, Pts: 8563888223})
	fmt.Println("NEW SCTE35", qhash, 1) // content id

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 1, Pts: 8568388326})
	fmt.Println("NEW SCTE35", qhash, 1) // content id

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 33, Pts: 8571278764})
	fmt.Println("NEW SCTE35", qhash, 33) // chapter end

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 52, Pts: 8571278764})
	fmt.Println("NEW SCTE35", qhash, 52) // provider placement opp start

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 54, Pts: 8574101509})
	fmt.Println("NEW SCTE35", qhash, 54) // distributor placement opp start

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 48, Pts: 8574511852})
	fmt.Println("NEW SCTE35", qhash, 48) // provider ad start

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 49, Pts: 8579917252})
	fmt.Println("NEW SCTE35", qhash, 49) // provider ad end

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 48, Pts: 8579917252})
	fmt.Println("NEW SCTE35", qhash, 48) // provider ad start

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 55, Pts: 8582289863})
	fmt.Println("NEW SCTE35", qhash, 55) // distributor placement opp end

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 49, Pts: 8582618160})
	fmt.Println("NEW SCTE35", qhash, 49) // provider ad end

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 53, Pts: 8582618160})
	fmt.Println("NEW SCTE35", qhash, 53) // provider placement opp end

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 32, Pts: 8582618160})
	fmt.Println("NEW SCTE35", qhash, 32) // chapter start

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 64, Pts: 8584618160})
	fmt.Println("NEW USERAPI EVENT", qhash, 64) // API unscheduled event

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 1, Pts: 8586631690})
	fmt.Println("NEW SCTE35", qhash, 1) // content id

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 1, Pts: 1197122})
	fmt.Println("NEW SCTE35", qhash, 1) // content id

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 1, Pts: 5697223})
	fmt.Println("NEW SCTE35", qhash, 1) // content id

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 1, Pts: 10197057})
	fmt.Println("NEW SCTE35", qhash, 1) // content id

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 1, Pts: 14697109})
	fmt.Println("NEW SCTE35", qhash, 1) // content id
	//}()
	return src, nil
}

func MakeMockSourceFlagDayBaseFromJson(sr *SignalRecorder, qhash string) (src *SignalSource, err error) {

	// There is a discrepancy of pts 331984 between scte35 signal and actual stream
	// A simple way to compensate is to add this value to the start PTS of the source stream
	src = &SignalSource{qhash: qhash, startPts: 8553957321 + 331984, timescale: 90000}

	scteArray, err := LoadScteFromJson()
	if err != nil {
		return
	}
	for _, scte := range scteArray {
		for _, desc := range scte.SpliceDescriptors {
			s := Signal{
				Kind:         Scte35,
				Source:       src,
				Id:           int(desc.SegmentationTypeId),
				Msg:          "",
				Pts:          int64(scte.PTS),
				EpochNanoGmt: 0,
			}
			sr.Record(&s)
		}
	}
	return
}

func LoadScteFromJson() (scte []ts.SpliceInfo, err error) {
	b, err := ioutil.ReadFile("../ts/scte35_test_data.json")
	if err != nil {
		return
	}
	err = json.Unmarshal(b, &scte)
	return
}

func MakeMockSourceQhash(sr *SignalRecorder, qhash string) *SignalSource {

	src := &SignalSource{qhash: qhash}
	go func() {
		time.Sleep(1 * time.Second)
	}()
	return src
}

func MakeMockSchedule1() *Schedule {

	s := Schedule{}
	sr := &SignalRecorder{signals: make(map[SignalKind][]*Signal)}

	p1 := PlayableSingleLiveStream{qhash: "hq__mockstream1"}
	p2 := PlayableSingleOnDemand{qhash: "hq__mockvod1"}

	src1 := MakeMockSource1(sr)
	e1 := Event{trigger: TriggerScte35{source: src1, id: 16 /* program start */, sr: sr}}
	e2 := Event{trigger: TriggerScte35{source: src1, id: 52 /* placement start */, sr: sr}}
	e3 := Event{trigger: TriggerScte35{source: src1, id: 53 /* placement end */, sr: sr}}

	s.rules = []*Rule{
		&Rule{event: e1, playable: p1},
		&Rule{event: e2, playable: p2},
		&Rule{event: e3, playable: p1},
	}

	return &s
}

func MakeMockScheduleIBC() *Schedule {

	s := Schedule{}
	sr := &SignalRecorder{signals: make(map[SignalKind][]*Signal)}

	p1 := PlayableSingleOnDemand{qhash: "hq__A3cUbY9NdrGx9jDs8VvPU8T5tAReAnjqA4uoPKLmzsT1cSbVHLDexYHoWZ95jA2q35bTa4FHNd"}
	p2 := PlayableSingleOnDemand{qhash: "hq__LNsfomSYF1TqgGGqkoVphJH1o73PJedJUiWMCUuq7JjpcdqdbUdu2BH8XDFFFydHEVqjoJsz1B"}
	p3 := PlayableSingleOnDemand{qhash: "hq__LNsfomSYF1TqgGGqkoVphJH1o73PJedJUiWMCUuq7JjpcdqdbUdu2BH8XDFFFydHEVqjoJsz1B"}
	p4 := PlayableSingleOnDemand{qhash: "hq__EWBnSidXjrFPqFg9UkY67do7kWNBQLBFD9ghRfVaP2pbNAFPK6ANDhoAqNRYMJbSvGsFmufu7W"}
	p5 := PlayableSingleOnDemand{qhash: "hq__HaN8m6TGyHDE1RhkSy7WNmHJQELDJPmMcagK7JcEMAnRrMi2GWotp4ccRwsZ58J1FNWrRdzjf4"}
	p6 := PlayableSingleOnDemand{qhash: "hq__MYBvG8T7C2tnTAPktmVm7SNqt2u74dS2H5jfp9hKBopUYUmXVqq5UzRhMdWJG1Z2vL7PS4v8cG"}
	p7 := PlayableSingleOnDemand{qhash: "hq__4ab5yCsryuSdYv61WzAofzEkPvuukvMZJzdwoBC46UWLYTmqMiEK3N6byLEn7QPmtQgi4YuFWn"}
	p8 := PlayableSingleOnDemand{qhash: "hq__LNsfomSYF1TqgGGqkoVphJH1o73PJedJUiWMCUuq7JjpcdqdbUdu2BH8XDFFFydHEVqjoJsz1B"}
	p9 := PlayableSingleOnDemand{qhash: "hq__BWkkEF1yo2hftz7T4tEz3AT5sCFtxu6ftB5qkyDQvQj9jq487KSHe2ft4dv86tTYXrDaFWeKxr"}
	p10 := PlayableSingleOnDemand{qhash: "hq__AMDXWxLSVFNETDJLC7U5G3En6XD1GueLPZs7ePjak5tj264gfQfi9owi1BHkNjrhzVUP78rzz3"}
	p11 := PlayableSingleOnDemand{qhash: "hq__2N1j6KavvhN1p2asnKxZxgNMcGbLJKGMkSjgmX3ShdvfNiC9ULYMa48t9rjSULcovvAFbmqV19"}
	p12 := PlayableSingleOnDemand{qhash: "hq__A3cUbY9NdrGx9jDs8VvPU8T5tAReAnjqA4uoPKLmzsT1cSbVHLDexYHoWZ95jA2q35bTa4FHNd"}
	p13 := PlayableSingleOnDemand{qhash: "hq__LNsfomSYF1TqgGGqkoVphJH1o73PJedJUiWMCUuq7JjpcdqdbUdu2BH8XDFFFydHEVqjoJsz1B"}
	p14 := PlayableSingleOnDemand{qhash: "hq__4ab5yCsryuSdYv61WzAofzEkPvuukvMZJzdwoBC46UWLYTmqMiEK3N6byLEn7QPmtQgi4YuFWn"}
	p15 := PlayableSingleOnDemand{qhash: "hq__A6YPtg17F3FHWMqb2X6pWigXJMwUwxmzX9U6sX3KtXVd2L64HtweoT92DmSis3TfdJ1uFMUVZQ"}
	p16 := PlayableSingleOnDemand{qhash: "hq__L1JzDoxxHQi12iR36Sfk848BE1jjPkg9LAR8v2R83tXTR23wspEapaDDB3qgGcmFbcmzGVuA6f"}
	p17 := PlayableSingleOnDemand{qhash: "hq__H4TDGuUKf3pS5RoyotFA2j6xeRJtf1Dx2CvnRvGpjE3bkHK1Jbbwij357ByzxKEasyvj4Wej2N"}
	p18 := PlayableSingleOnDemand{qhash: "hq__4ab5yCsryuSdYv61WzAofzEkPvuukvMZJzdwoBC46UWLYTmqMiEK3N6byLEn7QPmtQgi4YuFWn"}
	p19 := PlayableSingleOnDemand{qhash: "hq__LNsfomSYF1TqgGGqkoVphJH1o73PJedJUiWMCUuq7JjpcdqdbUdu2BH8XDFFFydHEVqjoJsz1B"}
	p20 := PlayableSingleOnDemand{qhash: "hq__BWkkEF1yo2hftz7T4tEz3AT5sCFtxu6ftB5qkyDQvQj9jq487KSHe2ft4dv86tTYXrDaFWeKxr"}
	p21 := PlayableSingleOnDemand{qhash: "hq__83fcvfmXcBESJr4kkH6Apz6vkEkoReFJikGE2PH6vDVNrvf7TSsbbfU6mHYvn3RVwoctGTMfd5"}
	p22 := PlayableSingleOnDemand{qhash: "hq__A3cUbY9NdrGx9jDs8VvPU8T5tAReAnjqA4uoPKLmzsT1cSbVHLDexYHoWZ95jA2q35bTa4FHNd"}
	p23 := PlayableSingleOnDemand{qhash: "hq__LNsfomSYF1TqgGGqkoVphJH1o73PJedJUiWMCUuq7JjpcdqdbUdu2BH8XDFFFydHEVqjoJsz1B"}
	p24 := PlayableSingleOnDemand{qhash: "hq__4ab5yCsryuSdYv61WzAofzEkPvuukvMZJzdwoBC46UWLYTmqMiEK3N6byLEn7QPmtQgi4YuFWn"}

	src1 := MakeMockSourceQhash(sr, "hq__A3cUbY9NdrGx9jDs8VvPU8T5tAReAnjqA4uoPKLmzsT1cSbVHLDexYHoWZ95jA2q35bTa4FHNd")
	src2 := MakeMockSourceQhash(sr, "hq__LNsfomSYF1TqgGGqkoVphJH1o73PJedJUiWMCUuq7JjpcdqdbUdu2BH8XDFFFydHEVqjoJsz1B")
	src3 := MakeMockSourceQhash(sr, "hq__4ab5yCsryuSdYv61WzAofzEkPvuukvMZJzdwoBC46UWLYTmqMiEK3N6byLEn7QPmtQgi4YuFWn")
	src4 := MakeMockSourceQhash(sr, "hq__EWBnSidXjrFPqFg9UkY67do7kWNBQLBFD9ghRfVaP2pbNAFPK6ANDhoAqNRYMJbSvGsFmufu7W")
	src5 := MakeMockSourceQhash(sr, "hq__HaN8m6TGyHDE1RhkSy7WNmHJQELDJPmMcagK7JcEMAnRrMi2GWotp4ccRwsZ58J1FNWrRdzjf4")
	src6 := MakeMockSourceQhash(sr, "hq__MYBvG8T7C2tnTAPktmVm7SNqt2u74dS2H5jfp9hKBopUYUmXVqq5UzRhMdWJG1Z2vL7PS4v8cG")
	src7 := MakeMockSourceQhash(sr, "hq__4ab5yCsryuSdYv61WzAofzEkPvuukvMZJzdwoBC46UWLYTmqMiEK3N6byLEn7QPmtQgi4YuFWn")

	src8 := MakeMockSourceQhash(sr, "hq__BWkkEF1yo2hftz7T4tEz3AT5sCFtxu6ftB5qkyDQvQj9jq487KSHe2ft4dv86tTYXrDaFWeKxr")
	src9 := MakeMockSourceQhash(sr, "hq__AMDXWxLSVFNETDJLC7U5G3En6XD1GueLPZs7ePjak5tj264gfQfi9owi1BHkNjrhzVUP78rzz3")
	src10 := MakeMockSourceQhash(sr, "hq__2N1j6KavvhN1p2asnKxZxgNMcGbLJKGMkSjgmX3ShdvfNiC9ULYMa48t9rjSULcovvAFbmqV19")
	src11 := MakeMockSourceQhash(sr, "hq__A6YPtg17F3FHWMqb2X6pWigXJMwUwxmzX9U6sX3KtXVd2L64HtweoT92DmSis3TfdJ1uFMUVZQ")
	src12 := MakeMockSourceQhash(sr, "hq__L1JzDoxxHQi12iR36Sfk848BE1jjPkg9LAR8v2R83tXTR23wspEapaDDB3qgGcmFbcmzGVuA6f")
	src13 := MakeMockSourceQhash(sr, "hq__H4TDGuUKf3pS5RoyotFA2j6xeRJtf1Dx2CvnRvGpjE3bkHK1Jbbwij357ByzxKEasyvj4Wej2N")

	// Every content is a playable and every content but the last is an Event (unless we loop)
	_ = p1 // Todo - how to start with playable p1!
	e1 := Event{trigger: TriggerPts{source: src1, pts: 30 * 25000 /* program start */}}
	e2 := Event{trigger: TriggerPts{source: src2, pts: 30 * 25000 /* program start */}}
	e3 := Event{trigger: TriggerPts{source: src3, pts: 120 * 25000 /* program start */}}
	e4 := Event{trigger: TriggerPts{source: src4, pts: 20 * 12800 /* program start */}}
	e5 := Event{trigger: TriggerPts{source: src5, pts: 30 * 12800 /* program start */}}
	e6 := Event{trigger: TriggerPts{source: src6, pts: 20 * 12800 /* program start */}}
	e7 := Event{trigger: TriggerPts{source: src7, pts: 120 * 25000 /* program start */}}
	e8 := Event{trigger: TriggerPts{source: src8, pts: 30 * 25000 /* program start */}}
	e9 := Event{trigger: TriggerPts{source: src9, pts: 30 * 25000 /* program start */}}
	e10 := Event{trigger: TriggerPts{source: src10, pts: 120 * 250000 /* program start */}}
	e11 := Event{trigger: TriggerPts{source: src11, pts: 120 * 25000 /* program start */}}
	e12 := Event{trigger: TriggerPts{source: src12, pts: 15 * 25000 /* program start */}}
	e13 := Event{trigger: TriggerPts{source: src13, pts: 30 * 25000 /* program start */}}
	e14 := Event{trigger: TriggerPts{source: src1, pts: 120 * 12800 /* program start */}}
	e15 := Event{trigger: TriggerPts{source: src2, pts: 120 * 12800 /* program start */}}
	e16 := Event{trigger: TriggerPts{source: src3, pts: 30 * 12800 /* program start */}}
	e17 := Event{trigger: TriggerPts{source: src4, pts: 30 * 25000 /* program start */}}
	e18 := Event{trigger: TriggerPts{source: src5, pts: 120 * 25000 /* program start */}}
	e19 := Event{trigger: TriggerPts{source: src6, pts: 30 * 25000 /* program start */}}
	e20 := Event{trigger: TriggerPts{source: src7, pts: 30 * 12800 /* program start */}}
	e21 := Event{trigger: TriggerPts{source: src8, pts: 300 * 25000 /* program start */}}
	e22 := Event{trigger: TriggerPts{source: src9, pts: 15 * 25000 /* program start */}}
	e23 := Event{trigger: TriggerPts{source: src10, pts: 30 * 25000 /* program start */}}

	// Add event for end of each of the above

	s.rules = []*Rule{
		&Rule{event: e1, playable: p2},
		&Rule{event: e2, playable: p3},
		&Rule{event: e3, playable: p4},
		&Rule{event: e4, playable: p5},
		&Rule{event: e5, playable: p6},
		&Rule{event: e6, playable: p7},
		&Rule{event: e7, playable: p8},
		&Rule{event: e8, playable: p9},
		&Rule{event: e9, playable: p10},
		&Rule{event: e10, playable: p11},
		&Rule{event: e11, playable: p12},
		&Rule{event: e12, playable: p13},
		&Rule{event: e13, playable: p14},
		&Rule{event: e14, playable: p15},
		&Rule{event: e15, playable: p16},
		&Rule{event: e16, playable: p17},
		&Rule{event: e17, playable: p18},
		&Rule{event: e18, playable: p19},
		&Rule{event: e19, playable: p20},
		&Rule{event: e20, playable: p21},
		&Rule{event: e21, playable: p22},
		&Rule{event: e22, playable: p23},
		&Rule{event: e23, playable: p24},
	}

	return &s

}

func MakeMockSourceIBCShortBase(sr *SignalRecorder, qhash string) (src *SignalSource, err error) {

	// There is a discrepancy of pts 331984 between scte35 signal and actual stream
	// A simple way to compensate is to add this value to the start PTS of the source stream
	src = &SignalSource{qhash: qhash, startPts: 0, timescale: 1600000} // Ad1 (insert)

	//go func() {
	//time.Sleep(1 * time.Second)

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 48, Pts: 32000000}) // IRI
	fmt.Println("NEW SCTE35", qhash, 48)                                 // provider ad sart
	//time.Sleep(4 * time.Second)

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 48, Pts: 80000000}) // SSC
	fmt.Println("NEW SCTE35", qhash, 48)                                 // provider ad start
	//time.Sleep(2 * time.Second)

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 54, Pts: 128000000}) // Menuboard
	fmt.Println("NEW SCTE35", qhash, 54)                                  // distributor placement opportunity start

	sr.Record(&Signal{Kind: Scte35, Source: src, Id: 32, Pts: 176000000}) // BTCC
	fmt.Println("NEW SCTE35", qhash, 32)                                  // chapter start

	return src, nil
}

func MakeMockScheduleIBCSmall(offering string) (s *Schedule, err error) {
	s = &Schedule{}
	sr := &SignalRecorder{signals: make(map[SignalKind][]*Signal)}

	p1 := PlayableSingleOnDemand{qhash: "hq__28axGfeVvUZMCk3ETYAWFk7AxtjnQxi7K1dYEmGFrN62xmpht9QbCMPwfpWv6KyoDwyKchRFSW", timescale: 12800} // AMV_ASJ278
	p2 := PlayableSingleOnDemand{qhash: "hq__AT3Vzv1LtHgTGrEXENbk7cZmAze8sWkgrdBC7LyiVw7hmveGqkoayYmWCqJ1U3J6eUfZ18kJBW", timescale: 12800} // LorealLashes
	p3 := PlayableSingleOnDemand{qhash: "hq__6ct8hVeonWahgfLb7XvqtcszNYeYNVoPJGjMQ5NBVgpWNB6efjyC5b3ziDsfXBsQLZiSgQtQA5", timescale: 12800} // PepsiMax
	p4 := PlayableSingleOnDemand{qhash: "hq__8sxu5xy2M9QYRKMLLUxWpTq9cuf8YjjsYdU8YdouRP6DXc6LS7WnLxUBNs3w8cSGnQhcWZMRts", timescale: 12800} // VolvoXC40
	p5 := PlayableSingleOnDemand{qhash: "hq__N9PMBCZWg4ZFy2BNvfN8AuEgdkS51roktnCecuXL8qpFJ3ziwFc7E7kHcKvGLwPFf6qcejFQh", timescale: 12800}  // IRI_H
	p6 := PlayableSingleOnDemand{qhash: "hq__8nDj8VuHgoiaAWommyuPmpcsaMxVGCKoMMup8hTrrZjM1KXi9xWXvqksm44UpDzeRjpspZqkwk", timescale: 25000} // BTCC_Test
	p7 := PlayableSingleOnDemand{qhash: "hq__473ijWE5HqeA7TwPMc84CLghExkFjF3USFvbjEEJ2ofmyPkhTEQPcCRbfRFkS6ztcxx4Kpin9q", timescale: 12800} // SSC
	p8 := PlayableSingleOnDemand{qhash: "hq__7VZLz9BPMEmooWJ83VGWpC9z3tTjBsiDaWfyH1bAvDH58y9tEqW2tCyh2XGY47Xck2ap48z7MB", timescale: 12800} // Menuboard
	p9 := PlayableSingleOnDemand{qhash: "hq__28aGc28rkvZVZxCSFQHxB5zgsEdrL28farcQnYVqMduboAtoMCapNPjDaXvireULGPvLuAw8bv", timescale: 12800} // OurStory

	if offering == "female192" {
		p2 = PlayableSingleOnDemand{qhash: "hq__FpLP9GUompQ7SHs6CeZqfJdShSqrFzEKHRXgJLMcMhDa3EV6Xg9M94oadAQ2m4crsa81L62cPn", timescale: 12800} // LorealLashes
		p5 = PlayableSingleOnDemand{qhash: "hq__N9PMBCZWg4ZFy2BNvfN8AuEgdkS51roktnCecuXL8qpFJ3ziwFc7E7kHcKvGLwPFf6qcejFQh", timescale: 12800}  // IRI_H
		p6 = PlayableSingleOnDemand{qhash: "hq__KAkjBRTGL6gFFSSGdJB5EZmLFSYmncWfWPK2eQTdDGoAHDn4cucwu6WaUBXbKvvZv7y9GZCG91", timescale: 12800} // BTCC_Test
		p7 = PlayableSingleOnDemand{qhash: "hq__473ijWE5HqeA7TwPMc84CLghExkFjF3USFvbjEEJ2ofmyPkhTEQPcCRbfRFkS6ztcxx4Kpin9q", timescale: 12800} // SSC
		p8 = PlayableSingleOnDemand{qhash: "hq__7nX8imF4fzcwV3qRTbVzah4viyer6vazWbW7XGMPrcC8fwQEPQSkx3uRG3K8vdwjJSZ2b4xq4m", timescale: 12800} // Menuboard

	}

	//	src1 := MakeMockSourceQhash(sr, "hq__EWBnSidXjrFPqFg9UkY67do7kWNBQLBFD9ghRfVaP2pbNAFPK6ANDhoAqNRYMJbSvGsFmufu7W") // AMV_ASJ278
	//	src2 := MakeMockSourceQhash(sr, "hq__LiKfWfTwEMzzwnyuxhoNvRPxtdyHWufZDvJ7yd446bZq4119GosSGYtH8eGdGbDaQmHfEdz98")  // LorealLashes
	//	src3 := MakeMockSourceQhash(sr, "hq__95BXny9HgNv6GeUAAP3JXd3dC6k7cP2LY62vYcSsA7EstXbagaNLgdZfyyr7bqGz8sG6WY3Tp")  // PepsiMax
	//	src4 := MakeMockSourceQhash(sr, "hq__7eXk7pmYrWSKBbLUU2vAwAZMQ1fYrTf6jKXBKwHsrGhfoBDuF6Y1UYwshuPSjfmqHVRRccqZq")  // VolvoXC40
	//	src5 := MakeMockSourceQhash(sr, "hq__HaN8m6TGyHDE1RhkSy7WNmHJQELDJPmMcagK7JcEMAnRrMi2GWotp4ccRwsZ58J1FNWrRdzjf4") // IRI_H
	/// src6 := MakeMockSourceQhash(sr, "hq__83fcvfmXcBESJr4kkH6Apz6vkEkoReFJikGE2PH6vDVNrvf7TSsbbfU6mHYvn3RVwoctGTMfd5") // BTCC_Test
	//	src7 := MakeMockSourceQhash(sr, "hq__H4TDGuUKf3pS5RoyotFA2j6xeRJtf1Dx2CvnRvGpjE3bkHK1Jbbwij357ByzxKEasyvj4Wej2N") // SSC
	//	src8 := MakeMockSourceQhash(sr, "hq__BWkkEF1yo2hftz7T4tEz3AT5sCFtxu6ftB5qkyDQvQj9jq487KSHe2ft4dv86tTYXrDaFWeKxr") // Menuboard

	// Every content is a playable and every content but the last is an Event (unless we loop)
	_ = p1 // Todo - how to start with playable p1!
	//	e1a := Event{trigger: TriggerPts{source: src1, pts: 20 * 12800 /* program start */}} // AMV or custom
	//	e1b := Event{trigger: TriggerPts{source: src2, pts: 20 * 12800 /* program start */}} // Loreal
	//	e1c := Event{trigger: TriggerPts{source: src3, pts: 20 * 25000 /* program start */}} // Pepsi
	//	e1d := Event{trigger: TriggerPts{source: src4, pts: 20 * 25000 /* program start */}} // Volvo
	//e2 := Event{trigger: TriggerPts{source: src5, pts: 30 * 12800 /* program start */}}  // IRI
	//e3 := Event{trigger: TriggerPts{source: src7, pts: 30 * 12800 /* program start */}}  // SSC
	//e4 := Event{trigger: TriggerPts{source: src8, pts: 30 * 25000 /* program start */}}  // Menuboard
	//e5 := Event{trigger: TriggerPts{source: src6, pts: 300 * 12800 /* program start */}} // BTCC

	qhash := "hq__4BKNw8XMejsabzixBo8C3eThtXPSyzZavNtKuv5pTkEeG5qkQq2dRgHU3hb3gQqx6zKGyzeJJk" // placeholder
	var src6 *SignalSource
	src6, err = MakeMockSourceIBCShortBase(sr, qhash)

	if err != nil {
		return nil, err
	}

	e1 := Event{trigger: TriggerWallClockTime{epochNanoGmt: 0 /* Start of sequence */}} //AMV or Loreal|Pepsi|Volvo

	e2 := Event{trigger: TriggerScte35{source: src6, id: 48 /* ProviderAdvertisementStart */, sr: sr}}          //IRI
	e3 := Event{trigger: TriggerScte35{source: src6, id: 48 /* ProviderAdvertisementStart */, sr: sr}}          //SSC
	e4 := Event{trigger: TriggerScte35{source: src6, id: 54 /* DistributorPlacmentOpportunityStart */, sr: sr}} //Menuboard
	e5 := Event{trigger: TriggerScte35{source: src6, id: 32 /* Chapter Start */, sr: sr}}                       //BTCC

	// Add event for end of each of the above

	switch offering {
	case "female":
		offering = "gender=female,age=*"
	case "female192":
		offering = "gender=female,age=*"
	case "male1":
		offering = "gender=male,age=under25"
	case "male2":
		offering = "gender=male,age=over25"
	default:
		offering = "generic"
	}

	if offering == "gender=female,age=*" {
		s.rules = []*Rule{
			&Rule{event: e1, playable: p2},
			&Rule{event: e2, playable: p5},
			&Rule{event: e3, playable: p7},
			&Rule{event: e4, playable: p8},
			&Rule{event: e5, playable: p9},
		}
	} else if offering == "gender=male,age=under25" {
		s.rules = []*Rule{
			&Rule{event: e1, playable: p3},
			&Rule{event: e2, playable: p5},
			&Rule{event: e3, playable: p7},
			&Rule{event: e4, playable: p8},
			&Rule{event: e5, playable: p6},
		}
	} else if offering == "gender=male,age=over25" {
		s.rules = []*Rule{
			&Rule{event: e1, playable: p4},
			&Rule{event: e2, playable: p5},
			&Rule{event: e3, playable: p7},
			&Rule{event: e4, playable: p8},
			&Rule{event: e5, playable: p6},
		}
	} else {
		s.rules = []*Rule{
			&Rule{event: e1, playable: p1},
			&Rule{event: e2, playable: p5},
			&Rule{event: e3, playable: p7},
			&Rule{event: e4, playable: p8},
			&Rule{event: e5, playable: p6},
		}
	}

	return s, nil
}

type BlackoutCondition struct {
}

func (blkc BlackoutCondition) Eval(actx *AudienceContext) bool {
	if actx == nil {
		return false
	}
	return actx.Blackout
}

type NoBlackoutCondition struct {
}

func (blkc NoBlackoutCondition) Eval(actx *AudienceContext) bool {
	if actx == nil {
		return true
	}
	return !actx.Blackout
}

func MakeMockScheduleFlagDay(loadSignalsFromJson bool) (s *Schedule, err error) {
	s = &Schedule{}
	sr := &SignalRecorder{signals: make(map[SignalKind][]*Signal)}

	// Signal source (base stream)
	qhash := "hq__4BKNw8XMejsabzixBo8C3eThtXPSyzZavNtKuv5pTkEeG5qkQq2dRgHU3hb3gQqx6zKGyzeJJk" //Phoenix FOX Channel Capture REPLAY

	p1 := PlayableSingleLiveStream{
		qhash:     qhash,
		timescale: 90000} //Phoenix FOX Channel CAPTURE REPLAY
	p2 := PlayableSingleOnDemand{
		qhash:     "hq__BwoAY5m75QXKzqANZkjTVmHuFpXKqy3peDPXuWhRqL7pxR3c7HqxEwungFLazshxD7iPumuESj",
		timescale: 60000} // Fry's Promo Mez
	p3 := PlayableSingleOnDemand{
		qhash:     "hq__AtWEsTuz83gUjgGgio75eJfjEYYcEEVRV4N2ob1JLenx6HHXJ48iBXXStRSGqpMuxHsJHJS4it",
		timescale: 60000} // News10 Bumper Mez
	p4 := PlayableSingleOnDemand{
		qhash:     "hq__3RRdauF2qVZRBZzG79UfCpSZbdnSf9MU84bJkCznHDueS1vi8mK9Bem3SAcnLVXsbCZJQDJBGb",
		timescale: 60000} // WWE Promo Mezz
	p5 := PlayableSingleOnDemand{
		qhash:     "hq__7ok1vfNrzaZqjUE6AmXHdiYriAjV3VmGeS8gL7F9esmZxN5WcZ9NSxB9wj4SaQYmqWTNw1Rmip",
		timescale: 60000} // Wynn Promo Mezz
	p6 := PlayableSingleOnDemand{
		qhash:      "hq__7ei6FPs78hNARpfunBHFkWJ5jcoxBdQjrswRAsMVkkrDRxd2p5Ju7emsQUKeTNttqgEV11CaqC",
		timescale:  24000,
		durationTs: 571571 - 216000 /* subtract 9 sec of black */} // car chase
	p7 := PlayableSingleOnDemand{
		qhash:     "hq__LuuHovGUHaxG7vBSxKD3YCuiSk2jfxkLxdfFX2YndSmU7jVBG2AD5eCauU24ArtscikLjFQJDi",
		timescale: 60000}

	var src1 *SignalSource
	loadSignalsFromJson = false
	if loadSignalsFromJson {
		src1, err = MakeMockSourceFlagDayBaseFromJson(sr, qhash)
	} else {
		src1, err = MakeMockSourceFlagDayBase(sr, qhash)
	}
	if err != nil {
		return nil, err
	}

	blk := BlackoutCondition{}
	noblk := NoBlackoutCondition{}

	e0a := Event{trigger: TriggerScte35{source: src1, id: 64 /* Unscheduled Event Start */, sr: sr}}
	//e0b := Event{trigger: TriggerScte35{source: src1, id: 61 /* Unscheduled Event End */, sr: sr}}

	e1 := Event{trigger: TriggerWallClockTime{epochNanoGmt: 0 /* Start of sequence */},
		conditions: []Condition{noblk}} // prime
	e1b := Event{trigger: TriggerWallClockTime{epochNanoGmt: 0 /* Start of sequence */},
		conditions: []Condition{blk}} // prime

	e2 := Event{trigger: TriggerScte35{source: src1, id: 52 /* ProviderPlacementOpportunityStart */, sr: sr}}             // WWE
	e3 := Event{trigger: TriggerScte35{source: src1, id: 54 /* DistributorPlacementOpportunityStart */, sr: sr}}          // Bumper
	e4 := Event{trigger: TriggerScte35{source: src1, id: 48 /* ProviderAdvertisementStart */, sr: sr, adjust: 112564}}    // Wynn
	e5 := Event{trigger: TriggerScte35{source: src1, id: 48 /* ProviderAdvert571isementStart */, sr: sr, adjust: 112564}} // Frys

	e6 := Event{trigger: TriggerScte35{source: src1, id: 32 /* ChapterStart */, sr: sr, adjust: 112564},
		conditions: []Condition{noblk}} // prime

	e6b := Event{trigger: TriggerScte35{source: src1, id: 32 /* ChapterStart */, sr: sr, adjust: 112564},
		conditions: []Condition{blk}} // prime

	e7 := Event{trigger: TriggerScte35{source: src1, id: 1 /*  */, sr: sr}} // prime resume

	s.rules = []*Rule{ // Todo: Should be a RuleSequence in this particular case
		&Rule{event: e1, playable: p1},  // prime
		&Rule{event: e1b, playable: p7}, // prime blackout

		&Rule{event: e2, playable: p4}, // WWE
		&Rule{event: e3, playable: p3}, // bumper
		&Rule{event: e4, playable: p5}, // Wynn
		&Rule{event: e5, playable: p2}, // Frys

		&Rule{event: e6, playable: p1},  // prime
		&Rule{event: e6b, playable: p7}, // prime blackout

		&Rule{event: e7, playable: p1}, // prime resume
	}

	//	p7 := PlayableSingleOnDemandResume{
	//		qhash:      "",
	//		timescale:  -1,
	//		resume_pts: -1}

	s.rules_unscheduled = []*Rule{
		&Rule{event: e0a, playable: p6},
		// MM - For now we let the unscheduled event play through entirely
		// and resume by default but we can also trigger the resume off an unscheduled event end
		//&Rule{event: e0b, playable: p7},
	}

	return s, nil
}

func MakeMockSequence() MediaSequence {
	ms := MediaSequence{[]*MediaSlice{
		{
			QHash:      "hq__4BKNw8XMejsabzixBo8C3eThtXPSyzZavNtKuv5pTkEeG5qkQq2dRgHU3hb3gQqx6zKGyzeJJk",
			Offering:   "default",
			StartPts:   0, // 8553957321,
			DurationTs: 8571278765 - 8553957321,
			Type:       LivePlayable,
		},
		{
			QHash:      "hq__ByCHpMgESioemrs9PfQpgM27Q1wttVrfxyCKyZkKaRrd2CgwosRujzdrnNqzhWxwDpXape9gQP",
			Offering:   "default",
			StartPts:   0,
			DurationTs: 3233088,
			Type:       VodPlayable,
		},
		{
			QHash:      "hq__8jSRGUGd6h829CZFSXR2dfZWqDzeVw2eeWRgmsEPTjHZnKMn6Cnq62DzH2ZpTcZkDiHaFQA8CQ",
			Offering:   "default",
			StartPts:   0,
			DurationTs: 5405400,
			Type:       VodPlayable,
		},
		{
			QHash:      "hq__LnRZuN8r8CVLc7emMDHNTDT5sxDxhuRKhyBdtkKn7zBZzoNHp7Tmk4TvmKtmSDSRMpuXsBQii3",
			Offering:   "default",
			StartPts:   0,
			DurationTs: 2372611,
			Type:       VodPlayable,
		},
		{
			QHash:      "hq__LcY7TKewPc4feWfJGHeX5yvV2vbPxRZMNDREk3QSxtYgTcCFdvrc7T8UxnqyJNfsWteJGLiPnY",
			Offering:   "default",
			StartPts:   0,
			DurationTs: 328297,
			Type:       VodPlayable,
		},
		{
			QHash:      "hq__4BKNw8XMejsabzixBo8C3eThtXPSyzZavNtKuv5pTkEeG5qkQq2dRgHU3hb3gQqx6zKGyzeJJk",
			Offering:   "default",
			StartPts:   28660839, // 8586631690,
			DurationTs: -1,
			Type:       LivePlayable,
		},
	}}
	return ms
}
