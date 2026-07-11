package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mgomes/ressik/internal/application"
	"github.com/mgomes/ressik/internal/daemon"
	"github.com/mgomes/ressik/internal/repository"
	"github.com/mgomes/ressik/internal/schedule"
)

// Dashboard is a point-in-time view of configured plans and backup history.
type Dashboard struct {
	ConfigPath string
	Plans      []Plan
	Warning    string
}

// Plan is one row and detail pane in the terminal dashboard.
type Plan struct {
	ID              string
	Name            string
	Enabled         bool
	Running         bool
	NextRun         time.Time
	LastRun         time.Time
	LastSnapshot    string
	LastError       string
	Retention       string
	RecentSnapshots []repository.Summary
}

// Backend supplies dashboard state and accepts non-blocking manual runs.
type Backend interface {
	Dashboard(context.Context) (Dashboard, error)
	StartPlan(string) error
	Close()
}

// Controller adapts application operations to a lifecycle-managed TUI
// backend.
type Controller struct {
	service  *application.Service
	stateDir string
	ctx      context.Context
	cancel   context.CancelFunc

	mu        sync.Mutex
	running   map[string]bool
	results   map[string]repository.Summary
	errors    map[string]string
	history   []repository.Summary
	historyAt time.Time
	closed    bool
	wg        sync.WaitGroup
}

const historyCacheDuration = 30 * time.Second

// NewController returns a backend whose manual jobs stop when Close is called.
func NewController(service *application.Service, stateDir string) *Controller {
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{
		service:  service,
		stateDir: stateDir,
		ctx:      ctx,
		cancel:   cancel,
		running:  make(map[string]bool),
		results:  make(map[string]repository.Summary),
		errors:   make(map[string]string),
	}
}

// Dashboard loads fresh configuration, schedule state, and snapshots.
func (c *Controller) Dashboard(ctx context.Context) (Dashboard, error) {
	loaded, err := c.service.Config()
	if err != nil {
		return Dashboard{}, err
	}
	state, stateErr := daemon.LoadState(c.stateDir)
	if stateErr != nil {
		state = daemon.State{Plans: make(map[string]daemon.PlanState)}
	}
	snapshots, err := c.snapshotHistory(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	byPlan := make(map[string][]repository.Summary)
	for _, snapshot := range snapshots {
		byPlan[snapshot.PlanID] = append(byPlan[snapshot.PlanID], snapshot)
	}

	planIDs := make([]string, 0, len(loaded.Config.Plans))
	for planID := range loaded.Config.Plans {
		planIDs = append(planIDs, planID)
	}
	sort.Strings(planIDs)
	dashboard := Dashboard{ConfigPath: loaded.Path, Plans: make([]Plan, 0, len(planIDs))}
	if stateErr != nil {
		dashboard.Warning = "Schedule state could not be read; the daemon will rebuild it: " + stateErr.Error()
	}
	now := time.Now()
	for _, planID := range planIDs {
		configured := loaded.Config.Plans[planID]
		planState := state.Plans[planID]
		plan := Plan{
			ID:           planID,
			Name:         configured.Name,
			Enabled:      configured.Enabled,
			LastRun:      planState.LastRun,
			LastSnapshot: planState.LastSnapshot,
			LastError:    planState.LastError,
			Retention:    retentionText(configured.Retention.KeepLast, configured.Retention.KeepFor.DurationValue()),
		}
		if configured.Enabled && configured.Schedule != nil {
			plan.NextRun = planState.RetryAt
			if plan.NextRun.IsZero() {
				plan.NextRun = planState.PendingScheduled
			}
			if plan.NextRun.IsZero() {
				after := now
				if !planState.LastScheduled.IsZero() {
					after = planState.LastScheduled
				}
				plan.NextRun, err = schedule.Next(*configured.Schedule, after)
				if err != nil {
					return Dashboard{}, fmt.Errorf("compute plan %q next run: %w", planID, err)
				}
			}
		}
		if recent := byPlan[planID]; len(recent) > 0 {
			if len(recent) > 5 {
				recent = recent[:5]
			}
			plan.RecentSnapshots = recent
			if plan.LastRun.IsZero() {
				plan.LastRun = recent[0].CreatedAt
			}
			if plan.LastSnapshot == "" {
				plan.LastSnapshot = recent[0].ID.String()
			}
		}

		c.mu.Lock()
		plan.Running = c.running[planID]
		if result, exists := c.results[planID]; exists && result.CreatedAt.After(plan.LastRun) {
			plan.LastRun = result.CreatedAt
			plan.LastSnapshot = result.ID.String()
		}
		if message := c.errors[planID]; message != "" {
			plan.LastError = message
		}
		c.mu.Unlock()
		dashboard.Plans = append(dashboard.Plans, plan)
	}
	return dashboard, nil
}

// StartPlan starts one manual backup and returns immediately.
func (c *Controller) StartPlan(planID string) error {
	ids, err := c.service.PlanIDs()
	if err != nil {
		return err
	}
	found := false
	for _, id := range ids {
		if id == planID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", application.ErrPlanNotFound, planID)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("dashboard is closed")
	}
	if c.running[planID] {
		c.mu.Unlock()
		return errors.New("backup is already running")
	}
	c.running[planID] = true
	c.errors[planID] = ""
	c.wg.Go(func() {
		result, err := c.service.Run(c.ctx, planID)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.running[planID] = false
		if !result.ID.IsZero() {
			c.results[planID] = result
			c.historyAt = time.Time{}
		}
		if err != nil {
			c.errors[planID] = err.Error()
			return
		}
		c.errors[planID] = ""
	})
	c.mu.Unlock()
	return nil
}

func (c *Controller) snapshotHistory(ctx context.Context) ([]repository.Summary, error) {
	c.mu.Lock()
	if !c.historyAt.IsZero() && time.Since(c.historyAt) < historyCacheDuration {
		history := append([]repository.Summary(nil), c.history...)
		c.mu.Unlock()
		return history, nil
	}
	c.mu.Unlock()

	history, err := c.service.Snapshots(ctx, "")
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.history = append(c.history[:0], history...)
	c.historyAt = time.Now()
	c.mu.Unlock()
	return history, nil
}

// Close cancels controller ownership and waits for manual jobs to finish.
func (c *Controller) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
}

func retentionText(keepLast int, keepFor time.Duration) string {
	switch {
	case keepLast > 0 && keepFor > 0:
		return fmt.Sprintf("last %d or %s", keepLast, keepFor)
	case keepLast > 0:
		return fmt.Sprintf("last %d", keepLast)
	case keepFor > 0:
		return keepFor.String()
	default:
		return "keep all"
	}
}
