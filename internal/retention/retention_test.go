package retention_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mgomes/atomic/internal/object"
	"github.com/mgomes/atomic/internal/repository"
	"github.com/mgomes/atomic/internal/retention"
)

func TestSelectUsesUnionSemantics(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	snapshots := []repository.Summary{
		{ID: object.ID{1}, CreatedAt: now.Add(-time.Hour)},
		{ID: object.ID{2}, CreatedAt: now.Add(-48 * time.Hour)},
		{ID: object.ID{3}, CreatedAt: now.Add(-20 * 24 * time.Hour)},
		{ID: object.ID{4}, CreatedAt: now.Add(-40 * 24 * time.Hour)},
	}
	policy := retention.Policy{KeepLast: 2, KeepFor: 30 * 24 * time.Hour}

	got := retention.Select(snapshots, policy, now)
	want := []repository.Summary{snapshots[3]}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Select() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectKeepsEverythingForZeroPolicy(t *testing.T) {
	t.Parallel()

	snapshots := []repository.Summary{{ID: object.ID{1}}, {ID: object.ID{2}}}
	if got := retention.Select(snapshots, retention.Policy{}, time.Now()); len(got) != 0 {
		t.Errorf("Select(zero policy) returned %d removals, want 0", len(got))
	}
}

func TestSelectProtectsCurrentSnapshotWhenClockMovesBackward(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	current := repository.Summary{ID: object.ID{2}, CreatedAt: now}
	snapshots := []repository.Summary{
		{ID: object.ID{1}, CreatedAt: now.Add(time.Hour)},
		current,
	}
	policy := retention.Policy{KeepLast: 1, Protect: current.ID}

	if got := retention.Select(snapshots, policy, now); len(got) != 0 {
		t.Errorf("Select(clock rollback) returned %d removals, want 0", len(got))
	}
}
