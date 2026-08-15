package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mgomes/atomic/internal/config"
	"github.com/mgomes/atomic/internal/daemon"
)

func TestLoadJoinsExpectedScheduleSlotsAndSortsPlans(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	writeConfig(t, configPath, `version: 1
repository: ./repository-does-not-exist
plans:
  documents:
    name: Documents
    enabled: true
    sources:
      files:
        path: ./documents
    schedule:
      kind: daily
      at: "02:30"
      timezone: UTC
  archive:
    name: Archive
    enabled: false
    sources:
      files:
        path: ./archive
`)
	stateDir := t.TempDir()
	start := time.Date(2026, time.July, 9, 2, 30, 0, 0, time.UTC)
	writeDaemonState(t, stateDir, daemon.State{
		Version: 2,
		Plans: map[string]daemon.PlanState{
			"documents": {
				PendingScheduled: start.Add(48 * time.Hour),
				Runs: []daemon.ScheduledRun{
					{
						ScheduledAt: start,
						CompletedAt: start.Add(5 * time.Minute),
						Outcome:     daemon.RunSucceeded,
						SnapshotID:  "snapshot-1",
					},
					{
						ScheduledAt: start.Add(24 * time.Hour),
						CompletedAt: start.Add(24*time.Hour + 5*time.Minute),
						Outcome:     daemon.RunFailed,
						Error:       "storage unavailable",
					},
					{ScheduledAt: start.Add(48 * time.Hour)},
				},
			},
		},
	})
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)

	report, err := Load(configPath, stateDir, now)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if got, want := len(report.Plans), 2; got != want {
		t.Fatalf("len(Load().Plans) = %d, want %d", got, want)
	}
	if got, want := report.Plans[0].ID, "archive"; got != want {
		t.Errorf("Load().Plans[0].ID = %q, want %q", got, want)
	}
	documents := report.Plans[1]
	if got, want := len(documents.Runs), daemon.RunHistoryLimit; got != want {
		t.Fatalf("len(documents.Runs) = %d, want %d", got, want)
	}
	wantOutcomes := []daemon.RunOutcome{
		daemon.RunUnknown,
		daemon.RunUnknown,
		daemon.RunUnknown,
		daemon.RunUnknown,
		daemon.RunUnknown,
		daemon.RunUnknown,
		daemon.RunUnknown,
		daemon.RunSucceeded,
		daemon.RunFailed,
		daemon.RunUnknown,
	}
	for index, want := range wantOutcomes {
		if got := documents.Runs[index].Outcome; got != want {
			t.Errorf("documents.Runs[%d].Outcome = %q, want %q", index, got, want)
		}
	}
	if got, want := documents.PendingRun, start.Add(48*time.Hour); !got.Equal(want) {
		t.Errorf("documents.PendingRun = %s, want %s", got, want)
	}
}

func TestLoadUsesRecordedHistoryForPlanWithoutSchedule(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	writeConfig(t, configPath, `version: 1
repository: ./repository-does-not-exist
plans:
  archive:
    name: Archive
    enabled: true
    sources:
      files:
        path: ./archive
`)
	stateDir := t.TempDir()
	scheduledAt := time.Date(2026, time.July, 10, 2, 30, 0, 0, time.UTC)
	writeDaemonState(t, stateDir, daemon.State{
		Version: 2,
		Plans: map[string]daemon.PlanState{
			"archive": {Runs: []daemon.ScheduledRun{
				{
					ScheduledAt: scheduledAt,
					CompletedAt: scheduledAt.Add(5 * time.Minute),
					Outcome:     daemon.RunFailed,
					Error:       "storage unavailable",
				},
			}},
		},
	})

	report, err := Load(configPath, stateDir, time.Now())
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	runs := report.Plans[0].Runs
	if got, want := len(runs), daemon.RunHistoryLimit; got != want {
		t.Fatalf("len(Load().Plans[0].Runs) = %d, want %d", got, want)
	}
	if got, want := runs[len(runs)-1].Outcome, daemon.RunFailed; got != want {
		t.Errorf("latest outcome = %q, want %q", got, want)
	}
}

func TestRunHistoryPreservesOutcomesAfterScheduleChange(t *testing.T) {
	t.Parallel()

	recordedAt := time.Date(2026, time.July, 11, 2, 30, 0, 0, time.UTC)
	recorded := daemon.ScheduledRun{
		ScheduledAt: recordedAt,
		CompletedAt: recordedAt.Add(5 * time.Minute),
		Outcome:     daemon.RunSucceeded,
		SnapshotID:  "snapshot-before-schedule-change",
	}
	current := &config.Schedule{Kind: "daily", At: "03:30", Timezone: "UTC"}
	runs, err := runHistory(
		current,
		[]daemon.ScheduledRun{recorded},
		time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("runHistory() returned error: %v", err)
	}
	if got, want := len(runs), daemon.RunHistoryLimit; got != want {
		t.Fatalf("len(runHistory()) = %d, want %d", got, want)
	}
	found := false
	for _, run := range runs {
		if run.ScheduledAt.Equal(recordedAt) && run.Outcome == daemon.RunSucceeded {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("runHistory() = %#v, want retained pre-change outcome", runs)
	}
	latest := runs[len(runs)-1]
	wantLatest := time.Date(2026, time.July, 11, 3, 30, 0, 0, time.UTC)
	if !latest.ScheduledAt.Equal(wantLatest) || latest.Outcome != daemon.RunUnknown {
		t.Errorf("runHistory() latest = %#v, want current missing schedule slot", latest)
	}
}

func TestLoadDegradesUnreadableStateToUnknownWithoutRepositoryAccess(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	writeConfig(t, configPath, `version: 1
repository: ./repository-does-not-exist
plans:
  documents:
    name: Documents
    enabled: true
    sources:
      files:
        path: ./documents-does-not-exist
    schedule:
      kind: daily
      at: "02:30"
      timezone: UTC
`)
	stateDir := t.TempDir()
	writeConfig(t, filepath.Join(stateDir, "daemon-state.json"), "not json")
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)

	report, err := Load(configPath, stateDir, now)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if !strings.Contains(report.Warning, "history is shown as unknown") {
		t.Errorf("Load().Warning = %q, want unknown-history warning", report.Warning)
	}
	for index, run := range report.Plans[0].Runs {
		if run.Outcome != daemon.RunUnknown {
			t.Errorf("Load().Plans[0].Runs[%d].Outcome = %q, want unknown", index, run.Outcome)
		}
	}
	wantNext := time.Date(2026, time.July, 12, 2, 30, 0, 0, time.UTC)
	if got := report.Plans[0].NextRun; !got.Equal(wantNext) {
		t.Errorf("Load().Plans[0].NextRun = %s, want %s", got, wantNext)
	}
}

func writeConfig(t testing.TB, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) returned error: %v", path, err)
	}
}

func writeDaemonState(t testing.TB, stateDir string, state daemon.State) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal(daemon state) returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "daemon-state.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile(daemon state) returned error: %v", err)
	}
}
