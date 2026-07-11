//go:build windows

package daemon

import (
	"sync"
	"testing"
)

func TestLoadStateDoesNotBlockConcurrentReplacement(t *testing.T) {
	stateDir := t.TempDir()
	state := newState()
	if err := saveState(stateDir, state); err != nil {
		t.Fatalf("saveState(initial) returned error: %v", err)
	}

	errors := make(chan error, 8)
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 500 {
				if _, err := LoadState(stateDir); err != nil {
					errors <- err
					return
				}
			}
		})
	}
	for sequence := range 200 {
		state.Plans["test"] = PlanState{ConsecutiveFailures: sequence}
		if err := saveState(stateDir, state); err != nil {
			t.Fatalf("saveState(%d) returned error: %v", sequence, err)
		}
	}
	readers.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("LoadState() returned error during replacement: %v", err)
	}
}
