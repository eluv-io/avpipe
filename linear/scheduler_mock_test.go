package linear

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMakeMockScteFromJson(t *testing.T) {
	sr := SignalRecorder{signals: make(map[SignalKind][]*Signal)}
	src, err := MakeMockSourceFlagDayBaseFromJson(&sr, "hq__mocksource1")

	if err != nil {
		t.Errorf("MakeMockScteFromJson() error: %v", err)
	} else {
		assert.NotNil(t, src)
		assert.Equal(t, 19, len(sr.signals[Scte35]))
	}
}

func TestLoadScteFromJson(t *testing.T) {
	scte, err := LoadScteFromJson()
	if err != nil {
		t.Errorf("LoadScteJson() error: %v", err)
	} else {
		assert.Equal(t, 15, len(scte))
	}
}
