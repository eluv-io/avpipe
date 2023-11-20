package ts

import (
	"encoding/hex"
	"encoding/json"
	"io/ioutil"
	"testing"

	"github.com/Comcast/gots/scte35"
	"github.com/stretchr/testify/assert"
)

// print byte array for use in declaring variables, e.g. var testVss = []byte{0x00, 0xfc, 0x30}
// for _, b := range msg.Data() {
//	 fmt.Printf("%#02x, ", b)
// }

// time_signal with 3 segmentation_descriptors
var scteMsg = []byte{0x00, 0xfc, 0x30, 0x57, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0xf0, 0x05, 0x06, 0xff, 0xff, 0x90, 0x5c, 0x30, 0x00, 0x41, 0x02, 0x0f, 0x43, 0x55, 0x45, 0x49, 0x09, 0x7b, 0x74, 0xaf, 0x7f, 0x9f, 0x03, 0x00, 0x31, 0x01, 0x01, 0x02, 0x0f, 0x43, 0x55, 0x45, 0x49, 0x09, 0x79, 0xfc, 0x8b, 0x7f, 0x9f, 0x00, 0x00, 0x35, 0x00, 0x01, 0x02, 0x1d, 0x43, 0x55, 0x45, 0x49, 0x09, 0x7b, 0xe8, 0x83, 0x7f, 0x9f, 0x01, 0x0e, 0x45, 0x50, 0x30, 0x33, 0x34, 0x31, 0x31, 0x35, 0x30, 0x36, 0x30, 0x30, 0x39, 0x39, 0x20, 0x06, 0x01, 0x28, 0x20, 0xce, 0x5c}

var scteMsgsHex = []string{
	"00fc30a00000000182f100fff00506ff9e52e89a008a021d43554549073d06b67fbf010e5348303430363333393330303030210101021b435545490713d3a97f830a0c147714edc71d0000000000000100000214435545490748ed757fff0000a4cb8001002200010216435545490748ed757fff0000a4cb80010034000100000214435545490748ed757fff0000a4cb800100300001000843554549000000009d86c037",
	"00fc305f0000000182f100fff00506ff9e3d94120049021b4355454907485a237fbf030c5a4d45523030303130303248310101022043554549074894bc7fff0000149970030c4e444545303133373030304830010100084355454900000000daf3a711",
	"00fc30250000000182f100fff0140564de518f7fefff9ea54ed1fe005265c00000000000008ac36b04",
}

func TestScteHex(t *testing.T) {

	for _, bufHex := range scteMsgsHex {
		buf, err := hex.DecodeString(bufHex)
		if err != nil {
			t.Error(err)
			return
		}
		gsi, err := scte35.NewSCTE35(buf)
		if err != nil {
			t.Error(err)
			return
		}
		assert.True(t, gsi.CommandInfo().CommandType() == 5 || gsi.CommandInfo().CommandType() == 6)

		if gsi.CommandInfo().CommandType() == 6 {
			si, err := ConvertGots(0, gsi)
			if err != nil {
				t.Error(err)
				return
			}
			assert.EqualValues(t, si.SpliceCommandType, 6)
		}

		if gsi.CommandInfo().CommandType() == 5 {
			assert.True(t, gsi.PTS().GreaterOrEqual(1))
		}
	}
}

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
