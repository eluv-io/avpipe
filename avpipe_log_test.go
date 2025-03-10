package avpipe

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGIDAssociation(t *testing.T) {
	wg := sync.WaitGroup{}

	for i := 0; i < 100; i++ {
		handle := int32(i)
		wg.Add(1)
		go func() {
			AssociateGIDWithHandle(handle)
			// Randomize scheduling a bit
			time.Sleep(time.Millisecond*20 + time.Millisecond*time.Duration(rand.IntN(10)))
			rHandle, ok := GIDHandle()
			require.True(t, ok)
			require.Equal(t, handle, rHandle)
			wg.Done()
		}()
	}

	wg.Wait()
}

// TestErrorCapturingTwoStep tests the error capturing mechanism when handle is known via XcInit
func TestErrorCapturingTwoStep(t *testing.T) {
	doOperation := func(handle int32, oneShot bool) {
		errChan := make(chan string, 5)
		// Oneshot API does not know the handle at this point
		var handlePtr *int32
		if !oneShot {
			handlePtr = &handle
		}
		RegisterWarnErrChanForHandle(handlePtr, errChan)

		errUniq1 := rand.IntN(100)
		errUniq2 := rand.IntN(100)
		warnUniq1 := rand.IntN(100)
		warnUniq2 := rand.IntN(100)
		// Randomize scheduling a bit
		time.Sleep(time.Millisecond*20 + time.Millisecond*time.Duration(rand.IntN(10)))

		///// ENTER C CODE /////
		if oneShot {
			AssociateGIDWithHandle(handle)
		}

		log.Error(fmt.Sprintf("Error %d", errUniq1))
		log.Error(fmt.Sprintf("Error %d", errUniq2))
		log.Warn(fmt.Sprintf("Warn %d", warnUniq1))
		log.Warn(fmt.Sprintf("Warn %d", warnUniq2))
		///// EXIT C CODE /////

		XCEnded()

		// Check that all logs are captured
		require.Len(t, errChan, 4)
		require.Equal(t, fmt.Sprintf("ERROR Error %d", errUniq1), <-errChan)
		require.Equal(t, fmt.Sprintf("ERROR Error %d", errUniq2), <-errChan)
		require.Equal(t, fmt.Sprintf("WARN Warn %d", warnUniq1), <-errChan)
		require.Equal(t, fmt.Sprintf("WARN Warn %d", warnUniq2), <-errChan)
		require.Len(t, errChan, 0)
	}

	// Test oneshot APIs
	wg := sync.WaitGroup{}
	for i := 0; i < 100; i++ {
		handle := int32(i)
		wg.Add(1)
		go func() {
			doOperation(handle, true)
			wg.Done()
		}()
	}
	wg.Wait()

	// Test two-step APIs
	wg2 := sync.WaitGroup{}
	for i := 0; i < 100; i++ {
		handle := int32(i)
		wg2.Add(1)
		go func() {
			doOperation(handle, false)
			wg2.Done()
		}()
	}
	wg2.Wait()
}
