package goavpipe

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

	for i := 1; i < 100; i++ {
		handle := int32(i)
		wg.Add(1)
		go func() {
			AssociateGIDWithHandle(handle)
			defer DissociateGIDFromHandle()
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

// TestErrorCapturing tests the error capturing mechanism
func TestErrorCapturing(t *testing.T) {
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

		Log.Error(fmt.Sprintf("Error %d", errUniq1))
		Log.Error(fmt.Sprintf("Error %d", errUniq2))
		Log.Warn(fmt.Sprintf("Warn %d", warnUniq1))
		Log.Warn(fmt.Sprintf("Warn %d", warnUniq2))
		///// EXIT C CODE /////

		XCEnded()

		// Check that all logs are captured
		require.Len(t, errChan, 4)
		require.Equal(t, fmt.Sprintf("ERROR Error %d", errUniq1), <-errChan)
		require.Equal(t, fmt.Sprintf("ERROR Error %d", errUniq2), <-errChan)
		require.Equal(t, fmt.Sprintf("WARN Warn %d", warnUniq1), <-errChan)
		require.Equal(t, fmt.Sprintf("WARN Warn %d", warnUniq2), <-errChan)
		require.Len(t, errChan, 0)
		_, ok := <-errChan
		require.False(t, ok)
	}

	// Test oneshot APIs
	wg := sync.WaitGroup{}
	for i := 1; i < 100; i++ {
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

	require.True(t, AllLogMapsEmpty())
}

func TestCorrectChanClosure(t *testing.T) {
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		errCh := make(chan string, 5)
		RegisterWarnErrChanForHandle(nil, errCh)

		// Pretend an error happened, and we never get the AssociateGIDWithHandle
		XCEnded()

		// Check that the channel is closed
		_, ok := <-errCh
		require.False(t, ok)

		wg.Done()
	}()

	wg.Wait()
	require.True(t, AllLogMapsEmpty())
}
