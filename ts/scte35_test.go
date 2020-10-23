package ts

import (
	"encoding/json"
	"github.com/Comcast/gots/scte35"
	"github.com/stretchr/testify/assert"
	"io/ioutil"
	"testing"
)

// print byte array for use in declaring variables, e.g. var testVss = []byte{0x00, 0xfc, 0x30}
// for _, b := range msg.Data() {
//	 fmt.Printf("%#02x, ", b)
// }

// time_signal with 3 segmentation_descriptors
var scteMsg = []byte{0x00, 0xfc, 0x30, 0x57, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0xf0, 0x05, 0x06, 0xff, 0xff, 0x90, 0x5c, 0x30, 0x00, 0x41, 0x02, 0x0f, 0x43, 0x55, 0x45, 0x49, 0x09, 0x7b, 0x74, 0xaf, 0x7f, 0x9f, 0x03, 0x00, 0x31, 0x01, 0x01, 0x02, 0x0f, 0x43, 0x55, 0x45, 0x49, 0x09, 0x79, 0xfc, 0x8b, 0x7f, 0x9f, 0x00, 0x00, 0x35, 0x00, 0x01, 0x02, 0x1d, 0x43, 0x55, 0x45, 0x49, 0x09, 0x7b, 0xe8, 0x83, 0x7f, 0x9f, 0x01, 0x0e, 0x45, 0x50, 0x30, 0x33, 0x34, 0x31, 0x31, 0x35, 0x30, 0x36, 0x30, 0x30, 0x39, 0x39, 0x20, 0x06, 0x01, 0x28, 0x20, 0xce, 0x5c}

func TestConvert(t *testing.T) {
	gsi, err := scte35.NewSCTE35(scteMsg)
	if err != nil {
		t.Error(err)
		return
	}
	si, err := ConvertGots(0, gsi)
	if err != nil {
		t.Error(err)
		return
	}
	expected := "EP034115060099"
	found := string(si.SpliceDescriptors[2].SegmentationUpids[0].Upid)
	if found != expected {
		t.Errorf("Expected Upid %s, got %s", expected, found)
	}
	_, err = json.MarshalIndent(si, "", "  ")
	if err != nil {
		t.Error(err)
		return
	}
	//fmt.Println(string(bytes))
}

func TestMarshalJson(t *testing.T) {
	si := SpliceInfo{}
	bytes, err := json.Marshal(si)
	if err != nil {
		t.Error(err)
		return
	}
	//fmt.Println(string(bytes))
	expected := "{\"pid\":0,\"pts\":0,\"tier\":0,\"splice_command_type\":0}"
	if expected != string(bytes) {
		t.Errorf("Expected %s, got %s", expected, string(bytes))
	}
}

func TestUnmarshalJson(t *testing.T) {
	b, err := ioutil.ReadFile("./scte35_test_data.json")
	if err != nil {
		t.Error(err)
		return
	}
	var scte []SpliceInfo
	err = json.Unmarshal(b, &scte)
	if err != nil {
		t.Error(err)
		return
	}
	assert.Equal(t, 15, len(scte))
	upid := []byte{0x45, 0x50, 0x30, 0x33, 0x34, 0x31, 0x31, 0x35, 0x30, 0x36, 0x30, 0x30, 0x39, 0x39}
	assert.Equal(t, upid, (scte)[14].SpliceDescriptors[0].SegmentationUpids[0].Upid)
	assert.Equal(t, 3, len((scte)[9].SpliceDescriptors))
}
