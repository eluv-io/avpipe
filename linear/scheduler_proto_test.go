package linear

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMock1(t *testing.T) {

	t.Skip("skipping test temporarily")

	fmt.Println("Test Schedule")

	s := MakeMockSchedule1()

	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)
		seq, err := s.MakeSequence(nil, 0)
		fmt.Println(seq, err)
	}
}

func TestMockIBC(t *testing.T) {

	t.Skip("skipping test temporarily")

	fmt.Println("Test Schedule IBC")

	s := MakeMockScheduleIBC()

	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)
		seq, err := s.MakeSequence(nil, 0)
		fmt.Println(seq, err)
	}
}

func TestMockFlagDay(t *testing.T) {
	fmt.Println("Test Schedule FlagDay")

	s, err := MakeMockScheduleFlagDay(false)
	require.NoError(t, err)

	//for i := 0; i < 10; i++ {
	//	time.Sleep(1 * time.Second)
	//	seq, err := s.MakeSequence()
	//  require.NoError(t, err)
	//	fmt.Println(seq)
	//}

	baseStartPts := int64(8553957321 + 331984)

	actx := AudienceContext{Blackout: false}
	seq, err := s.MakeSequence(&actx, baseStartPts)
	require.NoError(t, err)
	fmt.Println(seq)
}

func TestMockIBCSmall(t *testing.T) {
	//t.Skip("skipping test temporarily")
	fmt.Println("Test Schedule IBC")

	offerings := []string{ "female", "female192", "male1", "male2", "default" }
	for _, offering := range offerings {
		s, err := MakeMockScheduleIBCSmall(offering)
		require.NoError(t, err)
		actx := AudienceContext{}
		seq, err := s.MakeSequence(&actx, 0)
		require.NoError(t, err)
		fmt.Println(offering + ":")
		fmt.Println(seq)
		fmt.Println("================================================================================")
	}
}
