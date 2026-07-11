package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestLoadStateMigratesVersionOneWithoutInventingHistory(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	writeStateFile(t, stateDir, `{
  "version": 1,
  "plans": {
    "documents": {
      "last_scheduled": "2026-07-10T02:30:00Z",
      "pending_scheduled": "2026-07-11T02:30:00Z",
      "retry_at": "2026-07-11T03:30:00Z",
      "consecutive_failures": 2,
      "last_run": "2026-07-11T02:35:00Z",
      "last_error": "storage unavailable"
    }
  }
}`)

	state, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState() returned error: %v", err)
	}
	if got, want := state.Version, stateVersion; got != want {
		t.Errorf("LoadState().Version = %d, want %d", got, want)
	}
	planState := state.Plans["documents"]
	if got, want := planState.ConsecutiveFailures, 2; got != want {
		t.Errorf("LoadState().ConsecutiveFailures = %d, want %d", got, want)
	}
	if got, want := planState.LastError, "storage unavailable"; got != want {
		t.Errorf("LoadState().LastError = %q, want %q", got, want)
	}
	if len(planState.Runs) != 0 {
		t.Errorf("LoadState().Runs = %#v, want no invented history", planState.Runs)
	}
}

func TestSaveStateRoundTripsRunHistory(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	start := time.Date(2026, time.July, 9, 2, 30, 0, 0, time.UTC)
	state := newState()
	state.Plans["documents"] = PlanState{Runs: []ScheduledRun{
		{ScheduledAt: start},
		{
			ScheduledAt: start.Add(24 * time.Hour),
			CompletedAt: start.Add(24*time.Hour + 5*time.Minute),
			Outcome:     RunFailed,
			Error:       "storage unavailable",
		},
		{
			ScheduledAt: start.Add(48 * time.Hour),
			CompletedAt: start.Add(48*time.Hour + 5*time.Minute),
			Outcome:     RunSucceeded,
			SnapshotID:  strings.Repeat("a", 64),
		},
	}}
	if err := saveState(stateDir, state); err != nil {
		t.Fatalf("saveState() returned error: %v", err)
	}

	got, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState() returned error: %v", err)
	}
	if diff := cmp.Diff(state, got); diff != "" {
		t.Errorf("LoadState() mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadStateRejectsInvalidRunHistory(t *testing.T) {
	t.Parallel()

	t.Run("outcome", func(t *testing.T) {
		t.Parallel()

		stateDir := t.TempDir()
		writeStateFile(t, stateDir, `{
  "version": 2,
  "plans": {
    "documents": {
      "runs": [{
        "scheduled_at": "2026-07-11T02:30:00Z",
        "completed_at": "2026-07-11T02:35:00Z",
        "outcome": "maybe"
      }]
    }
  }
}`)
		if _, err := LoadState(stateDir); err == nil || !strings.Contains(err.Error(), "unknown outcome") {
			t.Fatalf("LoadState() error = %v, want invalid outcome", err)
		}
	})

	t.Run("limit", func(t *testing.T) {
		t.Parallel()

		stateDir := t.TempDir()
		start := time.Date(2026, time.July, 1, 2, 30, 0, 0, time.UTC)
		runs := make([]ScheduledRun, RunHistoryLimit+1)
		for index := range runs {
			runs[index] = ScheduledRun{ScheduledAt: start.Add(time.Duration(index) * 24 * time.Hour)}
		}
		data, err := json.Marshal(State{
			Version: stateVersion,
			Plans:   map[string]PlanState{"documents": {Runs: runs}},
		})
		if err != nil {
			t.Fatalf("Marshal(state) returned error: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "daemon-state.json"), data, 0o600); err != nil {
			t.Fatalf("WriteFile(daemon state) returned error: %v", err)
		}
		if _, err := LoadState(stateDir); err == nil || !strings.Contains(err.Error(), "at most 10") {
			t.Fatalf("LoadState() error = %v, want history limit error", err)
		}
	})
}

func TestPlanStateRunHistoryUpdatesRetriesAndKeepsNewestTen(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.July, 1, 2, 30, 0, 0, time.UTC)
	var planState PlanState
	for index := range RunHistoryLimit + 2 {
		scheduledAt := start.Add(time.Duration(index) * 24 * time.Hour)
		planState.beginRun(scheduledAt)
		planState.completeRun(
			scheduledAt,
			scheduledAt.Add(5*time.Minute),
			RunSucceeded,
			fmt.Sprintf("snapshot-%d", index),
			"",
		)
	}
	if got, want := len(planState.Runs), RunHistoryLimit; got != want {
		t.Fatalf("len(PlanState.Runs) = %d, want %d", got, want)
	}
	if got, want := planState.Runs[0].ScheduledAt, start.Add(48*time.Hour); !got.Equal(want) {
		t.Errorf("PlanState.Runs[0].ScheduledAt = %s, want %s", got, want)
	}

	retryAt := start.Add(time.Duration(RunHistoryLimit+1) * 24 * time.Hour)
	planState.beginRun(retryAt)
	if got, want := len(planState.Runs), RunHistoryLimit; got != want {
		t.Fatalf("len(PlanState.Runs) after retry = %d, want %d", got, want)
	}
	latest := planState.Runs[len(planState.Runs)-1]
	if latest.Outcome != RunUnknown || !latest.CompletedAt.IsZero() || latest.SnapshotID != "" {
		t.Errorf("retry run = %#v, want unknown outcome", latest)
	}
}

func writeStateFile(t testing.TB, stateDir, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, "daemon-state.json"), []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile(daemon state) returned error: %v", err)
	}
}
