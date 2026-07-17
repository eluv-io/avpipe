package avdesc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func iptr(i int) *int { return &i }

// TestAC4ChannelLayout exercises the ETSI Table 56 mapping and the immersive-mode
// refinement (BackChannelsPresent / TopChannelPairs) — including the many branches
// no real test asset reaches (mono, 3.0/5.0, the three 7.x variants, the .4
// containers, height collapse, 22.2, reserved, and object/nil).
func TestAC4ChannelLayout(t *testing.T) {
	cases := []struct {
		name string
		p    AC4Presentation
		want string
	}{
		{"object-nil-chmode", AC4Presentation{ChannelCoded: false}, ""},
		{"channelcoded-nil-chmode", AC4Presentation{ChannelCoded: true, ChMode: nil}, ""},
		{"mono", AC4Presentation{ChannelCoded: true, ChMode: iptr(0)}, "mono"},
		{"stereo", AC4Presentation{ChannelCoded: true, ChMode: iptr(1)}, "stereo"},
		{"3.0", AC4Presentation{ChannelCoded: true, ChMode: iptr(2)}, "3.0"},
		{"5.0", AC4Presentation{ChannelCoded: true, ChMode: iptr(3)}, "5.0"},
		{"5.1", AC4Presentation{ChannelCoded: true, ChMode: iptr(4)}, "5.1"},
		{"7.0-3/4/0", AC4Presentation{ChannelCoded: true, ChMode: iptr(5)}, "7.0"},
		{"7.1-3/4/0.1", AC4Presentation{ChannelCoded: true, ChMode: iptr(6)}, "7.1"},
		{"7.0-5/2/0", AC4Presentation{ChannelCoded: true, ChMode: iptr(7)}, "7.0"},
		{"7.1-5/2/0.1", AC4Presentation{ChannelCoded: true, ChMode: iptr(8)}, "7.1"},
		{"7.0-3/2/2", AC4Presentation{ChannelCoded: true, ChMode: iptr(9)}, "7.0"},
		{"7.1-3/2/2.1", AC4Presentation{ChannelCoded: true, ChMode: iptr(10)}, "7.1"},
		// 11 = 7.0.4 container; back=false demotes to 5.0.4.
		{"7.0.4", AC4Presentation{ChannelCoded: true, ChMode: iptr(11), BackChannelsPresent: true, TopChannelPairs: 2}, "7.0.4"},
		{"5.0.4", AC4Presentation{ChannelCoded: true, ChMode: iptr(11), BackChannelsPresent: false, TopChannelPairs: 2}, "5.0.4"},
		// 12 = 7.1.4 container; refined by back channels + top pairs.
		{"7.1.4", AC4Presentation{ChannelCoded: true, ChMode: iptr(12), BackChannelsPresent: true, TopChannelPairs: 2}, "7.1.4"},
		{"5.1.4", AC4Presentation{ChannelCoded: true, ChMode: iptr(12), BackChannelsPresent: false, TopChannelPairs: 2}, "5.1.4"},
		{"5.1.2", AC4Presentation{ChannelCoded: true, ChMode: iptr(12), BackChannelsPresent: false, TopChannelPairs: 1}, "5.1.2"},
		{"5.1-height0-collapse", AC4Presentation{ChannelCoded: true, ChMode: iptr(12), BackChannelsPresent: false, TopChannelPairs: 0}, "5.1"},
		{"7.1-height0-collapse", AC4Presentation{ChannelCoded: true, ChMode: iptr(12), BackChannelsPresent: true, TopChannelPairs: 0}, "7.1"},
		{"9.0.4", AC4Presentation{ChannelCoded: true, ChMode: iptr(13), TopChannelPairs: 2}, "9.0.4"},
		{"9.1.4", AC4Presentation{ChannelCoded: true, ChMode: iptr(14), TopChannelPairs: 2}, "9.1.4"},
		{"22.2", AC4Presentation{ChannelCoded: true, ChMode: iptr(15)}, "22.2"},
		{"reserved", AC4Presentation{ChannelCoded: true, ChMode: iptr(16)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.p.ChannelLayout())
		})
	}
}

// TestAC4Ajoc covers the object-detection loop over substream groups — which the
// single-group real assets do not exercise: mixed channel/object groups, b_ajoc on
// a non-first substream, and the "skip channel-coded groups" guard. Also confirms
// IsDolbyAtmos derives from Ajoc for a pv1 presentation.
func TestAC4Ajoc(t *testing.T) {
	t.Run("object group with ajoc on 2nd substream", func(t *testing.T) {
		p := AC4Presentation{
			Version:      1,
			ChannelCoded: false,
			SubstreamGroups: []AC4SubstreamGroup{
				{ChannelCoded: true, Substreams: []AC4Substream{{ChannelGroups: 0x47}}},
				{ChannelCoded: false, Substreams: []AC4Substream{{}, {Ajoc: true}}},
			},
		}
		assert.True(t, p.Ajoc())
		assert.True(t, p.IsDolbyAtmos(), "pv1 A-JOC is Dolby Atmos")
	})
	t.Run("all channel-coded groups", func(t *testing.T) {
		p := AC4Presentation{Version: 1, ChannelCoded: true, ChMode: iptr(4),
			SubstreamGroups: []AC4SubstreamGroup{{ChannelCoded: true, Substreams: []AC4Substream{{ChannelGroups: 0x47}}}}}
		assert.False(t, p.Ajoc())
		assert.False(t, p.IsDolbyAtmos())
	})
	t.Run("object group without ajoc", func(t *testing.T) {
		p := AC4Presentation{Version: 1, ChannelCoded: false,
			SubstreamGroups: []AC4SubstreamGroup{{ChannelCoded: false, Substreams: []AC4Substream{{DynamicObjects: true}}}}}
		assert.False(t, p.Ajoc())
	})
	t.Run("ajoc in a channel-coded group is ignored", func(t *testing.T) {
		// Guards the "if g.ChannelCoded { continue }" branch: b_ajoc lives only in
		// object-coded groups, so a channel-coded group is never A-JOC.
		p := AC4Presentation{Version: 1, ChannelCoded: true, ChMode: iptr(4),
			SubstreamGroups: []AC4SubstreamGroup{{ChannelCoded: true, Substreams: []AC4Substream{{Ajoc: true}}}}}
		assert.False(t, p.Ajoc())
	})
}

// TestAC4JSONOmitEmpty guards the pointer/omitempty shaping that replaced the
// custom marshalers: an unset pointer/zero optional is omitted, but a genuinely
// specified zero (mono ch_mode 0, bitrate_indicator 0) is preserved.
func TestAC4JSONOmitEmpty(t *testing.T) {
	t.Run("mono ch_mode 0 is preserved, not dropped", func(t *testing.T) {
		b, err := json.Marshal(AC4Presentation{ChannelCoded: true, ChMode: iptr(0)})
		require.NoError(t, err)
		assert.Contains(t, string(b), `"ch_mode":0`, "specified mono (0) must survive")
	})
	t.Run("nil ch_mode (object) is omitted", func(t *testing.T) {
		b, err := json.Marshal(AC4Presentation{ChannelCoded: false})
		require.NoError(t, err)
		assert.NotContains(t, string(b), `"ch_mode"`, "absent ch_mode must be omitted")
		// channel_coded is core and always emitted, even when false.
		assert.Contains(t, string(b), `"channel_coded":false`)
	})
	t.Run("bitrate_indicator: specified 0 kept, nil omitted", func(t *testing.T) {
		with, err := json.Marshal(AC4Substream{BitrateIndicator: iptr(0)})
		require.NoError(t, err)
		assert.Contains(t, string(with), `"bitrate_indicator":0`)

		without, err := json.Marshal(AC4Substream{})
		require.NoError(t, err)
		assert.NotContains(t, string(without), `"bitrate_indicator"`)
	})
	t.Run("false optional bools are omitted", func(t *testing.T) {
		b, err := json.Marshal(AC4Presentation{ChannelCoded: true, ChMode: iptr(4)})
		require.NoError(t, err)
		s := string(b)
		for _, k := range []string{"pre_virtualized", "de_indicator", "immersive_audio_indicator", "multi_pid", "alternative"} {
			assert.NotContains(t, s, `"`+k+`"`, "unset optional %q must be omitted", k)
		}
	})
	t.Run("AC4Info top-level mono ch_mode 0 preserved", func(t *testing.T) {
		b, err := json.Marshal(AC4Info{ChMode: iptr(0)})
		require.NoError(t, err)
		assert.Contains(t, string(b), `"ch_mode":0`)
	})
}
