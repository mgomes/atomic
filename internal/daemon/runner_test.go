package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/daemonctl"
	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/internal/repository"
)

type fakeService struct {
	loaded       *config.Loaded
	runs         []string
	collectCalls int
	summary      repository.Summary
	runErr       error
	run          func(context.Context, string) (repository.Summary, error)
	collect      func(context.Context) error
	collected    chan struct{}
}

func (f *fakeService) Collect(ctx context.Context) error {
	f.collectCalls++
	if f.collected != nil {
		close(f.collected)
	}
	if f.collect != nil {
		return f.collect(ctx)
	}
	return nil
}

func (f *fakeService) Config() (*config.Loaded, error) {
	return f.loaded, nil
}

func (f *fakeService) Run(ctx context.Context, planID string) (repository.Summary, error) {
	f.runs = append(f.runs, planID)
	if f.run != nil {
		return f.run(ctx, planID)
	}
	if f.summary.ID == (object.ID{}) && f.runErr == nil {
		return repository.Summary{ID: object.ID{1}}, nil
	}
	return f.summary, f.runErr
}

func TestRunCollectsOnceAtStartup(t *testing.T) {
	t.Parallel()

	service := &fakeService{
		loaded:    &config.Loaded{Config: config.Config{Plans: make(map[string]config.Plan)}},
		collected: make(chan struct{}),
	}
	runner, err := New(service, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Runner.Run() returned error: %v", err)
	}
	if got, want := service.collectCalls, 1; got != want {
		t.Errorf("Runner.Run() collection calls = %d, want %d", got, want)
	}
}

func TestRunStopsGracefullyOnControlRequest(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	service := &fakeService{
		loaded:    &config.Loaded{Config: config.Config{Plans: make(map[string]config.Plan)}},
		collected: make(chan struct{}),
	}
	runner, err := New(service, stateDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	done := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() { done <- runner.Run(context.Background()) })
	defer workers.Wait()
	<-service.collected
	if err := daemonctl.Request(stateDir); err != nil {
		t.Fatalf("daemonctl.Request() returned error: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Runner.Run() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Runner.Run() did not stop after control request")
	}
}

func TestRunContenderDoesNotConsumeControlRequest(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	ownerService := &fakeService{
		loaded:    &config.Loaded{Config: config.Config{Plans: make(map[string]config.Plan)}},
		collected: make(chan struct{}),
	}
	owner, err := New(ownerService, stateDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New(owner) returned error: %v", err)
	}
	ownerDone := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() { ownerDone <- owner.Run(context.Background()) })
	defer workers.Wait()
	<-ownerService.collected

	contenderService := &fakeService{
		loaded: &config.Loaded{Config: config.Config{Plans: make(map[string]config.Plan)}},
	}
	contender, err := New(contenderService, stateDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New(contender) returned error: %v", err)
	}
	if err := contender.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("contender.Run() error = %v, want ErrAlreadyRunning", err)
	}
	if got := contenderService.collectCalls; got != 0 {
		t.Errorf("contender.Run() collection calls = %d, want 0", got)
	}

	if err := daemonctl.Request(stateDir); err != nil {
		t.Fatalf("daemonctl.Request() returned error: %v", err)
	}
	select {
	case err := <-ownerDone:
		if err != nil {
			t.Errorf("owner.Run() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner.Run() did not stop after control request")
	}
}

func TestRunPreservesShutdownErrorsJoinedWithCancellation(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	storageErr := errors.New("persist shutdown state")
	service := &fakeService{
		loaded:    &config.Loaded{Config: config.Config{Plans: make(map[string]config.Plan)}},
		collected: make(chan struct{}),
		collect: func(ctx context.Context) error {
			<-ctx.Done()
			return errors.Join(ctx.Err(), storageErr)
		},
	}
	runner, err := New(service, stateDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	done := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() { done <- runner.Run(context.Background()) })
	defer workers.Wait()
	<-service.collected
	if err := daemonctl.Request(stateDir); err != nil {
		t.Fatalf("daemonctl.Request() returned error: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, storageErr) {
			t.Errorf("Runner.Run() error = %v, want storage error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Runner.Run() did not stop after control request")
	}
}

func TestRunQuarantinesUnreadableState(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "daemon-state.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile(daemon state) returned error: %v", err)
	}
	service := &fakeService{loaded: &config.Loaded{Config: config.Config{
		Plans: make(map[string]config.Plan),
	}}}
	runner, err := New(service, stateDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Runner.Run() returned error: %v", err)
	}
	quarantined, err := filepath.Glob(filepath.Join(stateDir, "daemon-state.corrupt-*.json"))
	if err != nil {
		t.Fatalf("Glob(quarantined state) returned error: %v", err)
	}
	if got, want := len(quarantined), 1; got != want {
		t.Errorf("quarantined state files = %d, want %d", got, want)
	}
}

func TestStepRunsLatestMissedOccurrenceOnce(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	service := &fakeService{loaded: &config.Loaded{Config: config.Config{
		Plans: map[string]config.Plan{
			"documents": {
				Name:    "Documents",
				Enabled: true,
				Schedule: &config.Schedule{
					Kind:     "daily",
					At:       "02:30",
					Timezone: "UTC",
				},
			},
		},
	}}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := New(service, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	state := newState()
	state.Plans["documents"] = PlanState{LastScheduled: now.Add(-48 * time.Hour)}

	if err := runner.step(context.Background(), service.loaded, &state, now); err != nil {
		t.Fatalf("Runner.step() returned error: %v", err)
	}
	if got, want := len(service.runs), 1; got != want {
		t.Errorf("Runner.step() ran %d backups, want %d", got, want)
	}
	wantID := (object.ID{1}).String()
	if got := state.Plans["documents"].LastSnapshot; got != wantID {
		t.Errorf("Runner.step() LastSnapshot = %q, want %q", got, wantID)
	}
}

func TestStepInitializesWithoutRunningImmediately(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	service := &fakeService{loaded: &config.Loaded{Config: config.Config{
		Plans: map[string]config.Plan{
			"documents": {
				Name:     "Documents",
				Enabled:  true,
				Schedule: &config.Schedule{Kind: "daily", At: "02:30", Timezone: "UTC"},
			},
		},
	}}}
	runner, err := New(service, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	state := newState()

	if err := runner.step(context.Background(), service.loaded, &state, now); err != nil {
		t.Fatalf("Runner.step() returned error: %v", err)
	}
	if got := len(service.runs); got != 0 {
		t.Errorf("Runner.step(initial) ran %d backups, want 0", got)
	}
	if got := state.Plans["documents"].LastScheduled; !got.Equal(now) {
		t.Errorf("Runner.step(initial) LastScheduled = %s, want %s", got, now)
	}
}

func TestStepPreservesFailedOccurrenceForRetry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	runErr := errors.New("storage unavailable")
	service := &fakeService{
		loaded: &config.Loaded{Config: config.Config{Plans: map[string]config.Plan{
			"documents": {
				Enabled:  true,
				Schedule: &config.Schedule{Kind: "daily", At: "02:30", Timezone: "UTC"},
			},
		}}},
		runErr: runErr,
	}
	runner, err := New(service, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	completedAt := now.Add(5 * time.Minute)
	runner.now = func() time.Time { return completedAt }
	lastScheduled := now.Add(-24 * time.Hour)
	state := newState()
	state.Plans["documents"] = PlanState{LastScheduled: lastScheduled}

	if err := runner.step(context.Background(), service.loaded, &state, now); err != nil {
		t.Fatalf("Runner.step(first failure) returned error: %v", err)
	}
	failed := state.Plans["documents"]
	if failed.PendingScheduled.IsZero() {
		t.Error("Runner.step(first failure) cleared PendingScheduled, want retry marker")
	}
	if !failed.LastScheduled.Equal(lastScheduled) {
		t.Errorf("Runner.step(first failure) LastScheduled = %s, want %s", failed.LastScheduled, lastScheduled)
	}
	if failed.LastError == "" {
		t.Error("Runner.step(first failure) LastError is empty, want failure")
	}
	if got, want := failed.ConsecutiveFailures, 1; got != want {
		t.Errorf("Runner.step(first failure) ConsecutiveFailures = %d, want %d", got, want)
	}
	if got, want := failed.RetryAt, completedAt.Add(time.Minute); !got.Equal(want) {
		t.Errorf("Runner.step(first failure) RetryAt = %s, want %s", got, want)
	}
	pending := failed.PendingScheduled

	if err := runner.step(context.Background(), service.loaded, &state, completedAt.Add(30*time.Second)); err != nil {
		t.Fatalf("Runner.step(before retry) returned error: %v", err)
	}
	if got, want := len(service.runs), 1; got != want {
		t.Errorf("Runner.step(before retry) ran %d backups, want %d", got, want)
	}

	completedAt = completedAt.Add(time.Minute)
	if err := runner.step(context.Background(), service.loaded, &state, completedAt); err != nil {
		t.Fatalf("Runner.step(retry) returned error: %v", err)
	}
	if got, want := len(service.runs), 2; got != want {
		t.Errorf("Runner.step(retry) ran %d backups, want %d", got, want)
	}
	if got := state.Plans["documents"].PendingScheduled; !got.Equal(pending) {
		t.Errorf("Runner.step(retry) PendingScheduled = %s, want %s", got, pending)
	}
	if got, want := state.Plans["documents"].ConsecutiveFailures, 2; got != want {
		t.Errorf("Runner.step(retry) ConsecutiveFailures = %d, want %d", got, want)
	}
	if got, want := state.Plans["documents"].RetryAt, completedAt.Add(2*time.Minute); !got.Equal(want) {
		t.Errorf("Runner.step(retry) RetryAt = %s, want %s", got, want)
	}
	persisted, err := LoadState(runner.stateDir)
	if err != nil {
		t.Fatalf("LoadState() returned error: %v", err)
	}
	if got, want := persisted.Plans["documents"].ConsecutiveFailures, 2; got != want {
		t.Errorf("LoadState().ConsecutiveFailures = %d, want %d", got, want)
	}
}

func TestStepRecordsCommittedSnapshotAndMaintenanceError(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	snapshotID := object.ID{9}
	service := &fakeService{
		loaded: &config.Loaded{Config: config.Config{Plans: map[string]config.Plan{
			"documents": {
				Enabled:  true,
				Schedule: &config.Schedule{Kind: "daily", At: "02:30", Timezone: "UTC"},
			},
		}}},
		summary: repository.Summary{ID: snapshotID},
		runErr:  errors.New("retention cleanup failed"),
	}
	runner, err := New(service, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	state := newState()
	state.Plans["documents"] = PlanState{LastScheduled: now.Add(-24 * time.Hour)}

	if err := runner.step(context.Background(), service.loaded, &state, now); err != nil {
		t.Fatalf("Runner.step() returned error: %v", err)
	}
	got := state.Plans["documents"]
	if got.LastSnapshot != snapshotID.String() {
		t.Errorf("Runner.step() LastSnapshot = %q, want %q", got.LastSnapshot, snapshotID.String())
	}
	if got.LastError == "" {
		t.Error("Runner.step() LastError is empty, want maintenance error")
	}
	if !got.PendingScheduled.IsZero() {
		t.Errorf("Runner.step() PendingScheduled = %s, want cleared committed occurrence", got.PendingScheduled)
	}
}

func TestStepConsumesCommittedOccurrenceWhenCanceledDuringMaintenance(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	snapshotID := object.ID{7}
	ctx, cancel := context.WithCancel(context.Background())
	service := &fakeService{
		loaded: &config.Loaded{Config: config.Config{Plans: map[string]config.Plan{
			"documents": {
				Enabled:  true,
				Schedule: &config.Schedule{Kind: "daily", At: "02:30", Timezone: "UTC"},
			},
		}}},
		run: func(context.Context, string) (repository.Summary, error) {
			cancel()
			return repository.Summary{ID: snapshotID}, context.Canceled
		},
	}
	runner, err := New(service, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	completedAt := now.Add(5 * time.Minute)
	runner.now = func() time.Time { return completedAt }
	state := newState()
	state.Plans["documents"] = PlanState{LastScheduled: now.Add(-24 * time.Hour)}

	if err := runner.step(ctx, service.loaded, &state, now); err != nil {
		t.Fatalf("Runner.step() returned error: %v", err)
	}
	got := state.Plans["documents"]
	if got.LastSnapshot != snapshotID.String() {
		t.Errorf("Runner.step() LastSnapshot = %q, want %q", got.LastSnapshot, snapshotID.String())
	}
	if !got.PendingScheduled.IsZero() {
		t.Errorf("Runner.step() PendingScheduled = %s, want cleared", got.PendingScheduled)
	}
	if !got.LastScheduled.Equal(completedAt) {
		t.Errorf("Runner.step() LastScheduled = %s, want %s", got.LastScheduled, completedAt)
	}
}
