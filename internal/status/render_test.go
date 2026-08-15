package status

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/mgomes/atomic/internal/daemon"
)

func TestRenderShowsThreeStateHistoryAndPlanDetails(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	start := time.Date(2026, time.July, 11, 2, 30, 0, 0, time.UTC)
	report := Report{Plans: []Plan{
		{
			ID:          "documents",
			Name:        "Documents",
			Enabled:     true,
			HasSchedule: true,
			Runs: history(
				daemon.ScheduledRun{
					ScheduledAt: start,
					CompletedAt: start.Add(4 * time.Minute),
					Outcome:     daemon.RunSucceeded,
					SnapshotID:  "snapshot-1",
				},
			),
			NextRun: start.Add(24 * time.Hour),
		},
		{
			ID:          "photos",
			Name:        "Photos",
			Enabled:     true,
			HasSchedule: true,
			Runs: history(
				daemon.ScheduledRun{
					ScheduledAt: start.Add(-24 * time.Hour),
					CompletedAt: start.Add(-24*time.Hour + 5*time.Minute),
					Outcome:     daemon.RunSucceeded,
					SnapshotID:  "snapshot-2",
				},
				daemon.ScheduledRun{
					ScheduledAt: start,
					CompletedAt: start.Add(6 * time.Minute),
					Outcome:     daemon.RunFailed,
					Error:       "source pictures:\naccess denied",
				},
			),
			RetryAt: start.Add(time.Hour),
		},
		{
			ID:      "archive",
			Name:    "Archive",
			Enabled: false,
			Runs:    history(),
		},
	}}

	var output bytes.Buffer
	if _, err := lipgloss.Fprint(&output, render(report, time.UTC)); err != nil {
		t.Fatalf("lipgloss.Fprint() returned error: %v", err)
	}
	want := `● Documents (documents)
  Runs      · · · · · · · · · ●   oldest → newest
  Last      2026-07-11 02:34 UTC   succeeded
  Next      2026-07-12 02:30 UTC

× Photos (photos)
  Runs      · · · · · · · · ● ×   oldest → newest
  Last      2026-07-11 02:36 UTC   failed
  Retry     2026-07-11 03:30 UTC
  Error     source pictures: access denied

· Archive (archive)
  Runs      · · · · · · · · · ·   oldest → newest
  Last      No completed scheduled runs
  Schedule  Automatic backups disabled

● succeeded  × failed  · missing/unknown`
	if got := output.String(); got != want {
		t.Errorf("render() =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderEmitsStylesForTerminalWriter(t *testing.T) {
	t.Parallel()

	report := Report{Plans: []Plan{{
		ID:      "documents",
		Name:    "Documents",
		Enabled: false,
		Runs: history(
			daemon.ScheduledRun{ScheduledAt: time.Now()},
			daemon.ScheduledRun{
				ScheduledAt: time.Now().Add(time.Second),
				CompletedAt: time.Now().Add(2 * time.Second),
				Outcome:     daemon.RunFailed,
			},
			daemon.ScheduledRun{
				ScheduledAt: time.Now().Add(3 * time.Second),
				CompletedAt: time.Now().Add(4 * time.Second),
				Outcome:     daemon.RunSucceeded,
				SnapshotID:  "snapshot",
			},
		),
	}}}

	got := render(report, time.UTC)
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("render() = %q, want ANSI styles", got)
	}
	for _, glyph := range []string{"●", "×", "·"} {
		if !strings.Contains(got, glyph) {
			t.Errorf("render() does not contain %q", glyph)
		}
	}
}

func history(recorded ...daemon.ScheduledRun) []daemon.ScheduledRun {
	runs := make([]daemon.ScheduledRun, daemon.RunHistoryLimit)
	copy(runs[len(runs)-len(recorded):], recorded)
	return runs
}
