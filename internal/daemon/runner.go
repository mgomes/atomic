package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"

	"github.com/mgomes/ressik/internal/cancelerr"
	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/daemonctl"
	"github.com/mgomes/ressik/internal/repository"
	"github.com/mgomes/ressik/internal/schedule"
)

// ErrAlreadyRunning reports that another daemon owns the same state folder.
var ErrAlreadyRunning = errors.New("Ressik daemon is already running")

type backupService interface {
	Collect(context.Context) error
	Config() (*config.Loaded, error)
	Run(context.Context, string) (repository.Summary, error)
}

const (
	initialPlanRetry = time.Minute
	maximumPlanRetry = time.Hour
)

// Runner reloads configuration and serially runs due plans.
type Runner struct {
	service  backupService
	stateDir string
	interval time.Duration
	logger   atomic.Pointer[slog.Logger]
	now      func() time.Time
}

// New returns a foreground scheduler. StateDir must be absolute and durable.
func New(service backupService, stateDir string, logger *slog.Logger) (*Runner, error) {
	resolved, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve daemon state directory: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	runner := &Runner{
		service:  service,
		stateDir: resolved,
		interval: 30 * time.Second,
		now:      time.Now,
	}
	runner.logger.Store(logger)
	return runner, nil
}

// SetLogger replaces the logger used for subsequent daemon events.
func (r *Runner) SetLogger(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	r.logger.Store(logger)
}

// Run blocks until ctx is canceled or the scheduler itself fails.
func (r *Runner) Run(ctx context.Context) (runErr error) {
	if err := os.MkdirAll(r.stateDir, 0o700); err != nil {
		return fmt.Errorf("create daemon state directory: %w", err)
	}
	lock := flock.New(filepath.Join(r.stateDir, "daemon.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("lock daemon: %w", err)
	}
	if !locked {
		return ErrAlreadyRunning
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("unlock daemon: %w", err))
		}
	}()
	watcher := daemonctl.Watch(ctx, r.stateDir)
	ctx = watcher.Context()
	defer func() {
		watcher.Close()
		if watcher.Requested() && cancelerr.Only(runErr) {
			runErr = nil
		} else if err := watcher.Err(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("watch daemon stop request: %w", err))
		}
	}()

	state, err := LoadState(r.stateDir)
	if err != nil {
		r.log().WarnContext(ctx, "daemon state is unreadable; rebuilding it", "err", err)
		state, err = quarantineState(r.stateDir)
		if err != nil {
			return err
		}
	}
	if err := r.service.Collect(ctx); err != nil {
		return fmt.Errorf("collect unreachable backup data: %w", err)
	}
	loaded, err := r.service.Config()
	if err != nil {
		return err
	}
	if err := r.step(ctx, loaded, &state, r.now()); err != nil {
		return err
	}

	r.log().InfoContext(ctx, "daemon started", "config", loaded.Path, "state_dir", r.stateDir)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.log().InfoContext(context.Background(), "daemon stopped")
			return nil
		case now := <-ticker.C:
			reloaded, err := r.service.Config()
			if err != nil {
				r.log().WarnContext(ctx, "config reload failed; keeping last known good configuration", "err", err)
			} else {
				loaded = reloaded
			}
			if err := r.step(ctx, loaded, &state, now); err != nil {
				return err
			}
		}
	}
}

func (r *Runner) step(ctx context.Context, loaded *config.Loaded, state *State, now time.Time) error {
	planIDs := make([]string, 0, len(loaded.Config.Plans))
	for planID := range loaded.Config.Plans {
		planIDs = append(planIDs, planID)
	}
	sort.Strings(planIDs)
	changed := false
	for _, planID := range planIDs {
		plan := loaded.Config.Plans[planID]
		if !plan.Enabled || plan.Schedule == nil {
			continue
		}
		planState := state.Plans[planID]
		if planState.LastScheduled.IsZero() {
			planState.LastScheduled = now
			state.Plans[planID] = planState
			changed = true
			continue
		}
		due := planState.PendingScheduled
		if due.IsZero() {
			var err error
			due, err = schedule.Next(*plan.Schedule, planState.LastScheduled)
			if err != nil {
				return fmt.Errorf("compute plan %q schedule: %w", planID, err)
			}
		}
		if due.After(now) {
			continue
		}
		if planState.RetryAt.After(now) {
			continue
		}

		planState.PendingScheduled = due
		state.Plans[planID] = planState
		if err := saveState(r.stateDir, *state); err != nil {
			return err
		}
		r.log().InfoContext(ctx, "scheduled backup started", "plan_id", planID, "scheduled_at", due)
		snapshot, runErr := r.service.Run(ctx, planID)
		hasSnapshot := !snapshot.ID.IsZero()
		if ctx.Err() != nil {
			planState = state.Plans[planID]
			if hasSnapshot {
				completedAt := r.now()
				planState.LastRun = completedAt
				planState.LastSnapshot = snapshot.ID.String()
				planState.LastScheduled = completedAt
				planState.PendingScheduled = time.Time{}
				planState.RetryAt = time.Time{}
				planState.ConsecutiveFailures = 0
				if runErr != nil {
					planState.LastError = runErr.Error()
				} else {
					planState.LastError = ""
				}
				state.Plans[planID] = planState
				return saveState(r.stateDir, *state)
			}
			planState.LastError = "backup interrupted: " + ctx.Err().Error()
			state.Plans[planID] = planState
			return saveState(r.stateDir, *state)
		}
		planState = state.Plans[planID]
		completedAt := r.now()
		planState.LastRun = completedAt
		if runErr == nil && !hasSnapshot {
			runErr = errors.New("backup completed without a snapshot")
		}
		if hasSnapshot {
			planState.LastSnapshot = snapshot.ID.String()
		}
		switch {
		case runErr != nil && !hasSnapshot:
			planState.ConsecutiveFailures++
			planState.RetryAt = completedAt.Add(planRetryDelay(planState.ConsecutiveFailures))
			planState.LastError = runErr.Error()
			r.log().ErrorContext(
				ctx,
				"scheduled backup failed; retrying occurrence",
				"err", runErr,
				"plan_id", planID,
				"retry_at", planState.RetryAt,
			)
		case runErr != nil:
			planState.LastScheduled = completedAt
			planState.PendingScheduled = time.Time{}
			planState.RetryAt = time.Time{}
			planState.ConsecutiveFailures = 0
			planState.LastError = runErr.Error()
			r.log().ErrorContext(ctx, "scheduled backup committed with an error", "err", runErr, "plan_id", planID, "snapshot_id", snapshot.ID.String())
		default:
			planState.LastScheduled = completedAt
			planState.PendingScheduled = time.Time{}
			planState.RetryAt = time.Time{}
			planState.ConsecutiveFailures = 0
			planState.LastError = ""
			r.log().InfoContext(ctx, "scheduled backup completed", "plan_id", planID, "snapshot_id", snapshot.ID.String())
		}
		state.Plans[planID] = planState
		changed = true
	}
	if changed {
		return saveState(r.stateDir, *state)
	}
	return nil
}

func (r *Runner) log() *slog.Logger {
	return r.logger.Load()
}

func planRetryDelay(failures int) time.Duration {
	delay := initialPlanRetry
	for range max(0, min(failures-1, 6)) {
		delay *= 2
	}
	return min(delay, maximumPlanRetry)
}
