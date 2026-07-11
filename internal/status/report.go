package status

import (
	"fmt"
	"sort"
	"time"

	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/daemon"
	"github.com/mgomes/ressik/internal/schedule"
)

// Report is a point-in-time view of configured plans and scheduled outcomes.
type Report struct {
	ConfigPath string
	Plans      []Plan
	Warning    string
}

// Plan is one configured plan and its ten most recent schedule slots.
type Plan struct {
	ID          string
	Name        string
	Enabled     bool
	HasSchedule bool
	Runs        []daemon.ScheduledRun
	NextRun     time.Time
	PendingRun  time.Time
	RetryAt     time.Time
}

// Load reads configuration and daemon state without opening the backup
// repository or inspecting source paths.
func Load(configPath, stateDir string, now time.Time) (Report, error) {
	loaded, err := config.Load(configPath)
	if err != nil {
		return Report{}, err
	}
	state, stateErr := daemon.LoadState(stateDir)
	if stateErr != nil {
		state = daemon.State{Plans: make(map[string]daemon.PlanState)}
	}

	planIDs := make([]string, 0, len(loaded.Config.Plans))
	for planID := range loaded.Config.Plans {
		planIDs = append(planIDs, planID)
	}
	sort.Strings(planIDs)

	report := Report{
		ConfigPath: loaded.Path,
		Plans:      make([]Plan, 0, len(planIDs)),
	}
	if stateErr != nil {
		report.Warning = "Schedule state could not be read; history is shown as unknown: " + stateErr.Error()
	}
	for _, planID := range planIDs {
		configured := loaded.Config.Plans[planID]
		planState := state.Plans[planID]
		runs, err := runHistory(configured.Schedule, planState.Runs, now)
		if err != nil {
			return Report{}, fmt.Errorf("compute plan %q run history: %w", planID, err)
		}
		plan := Plan{
			ID:          planID,
			Name:        configured.Name,
			Enabled:     configured.Enabled,
			HasSchedule: configured.Schedule != nil,
			Runs:        runs,
			PendingRun:  planState.PendingScheduled,
			RetryAt:     planState.RetryAt,
		}
		if configured.Enabled && configured.Schedule != nil &&
			plan.PendingRun.IsZero() && plan.RetryAt.IsZero() {
			after := now
			if !planState.LastScheduled.IsZero() {
				after = planState.LastScheduled
			}
			plan.NextRun, err = schedule.Next(*configured.Schedule, after)
			if err != nil {
				return Report{}, fmt.Errorf("compute plan %q next run: %w", planID, err)
			}
		}
		report.Plans = append(report.Plans, plan)
	}
	return report, nil
}

func runHistory(
	configured *config.Schedule,
	recorded []daemon.ScheduledRun,
	now time.Time,
) ([]daemon.ScheduledRun, error) {
	if configured == nil {
		runs := make([]daemon.ScheduledRun, daemon.RunHistoryLimit)
		start := max(0, len(recorded)-daemon.RunHistoryLimit)
		copy(runs[daemon.RunHistoryLimit-(len(recorded)-start):], recorded[start:])
		return runs, nil
	}

	expected, err := schedule.Recent(*configured, now, daemon.RunHistoryLimit)
	if err != nil {
		return nil, err
	}
	runs := make([]daemon.ScheduledRun, len(expected))
	for index, scheduledAt := range expected {
		runs[index].ScheduledAt = scheduledAt
	}
	for _, recordedRun := range recorded {
		matched := false
		for index, run := range runs {
			if run.ScheduledAt.Equal(recordedRun.ScheduledAt) {
				runs[index] = recordedRun
				matched = true
				break
			}
		}
		if !matched {
			runs = append(runs, recordedRun)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].ScheduledAt.Before(runs[j].ScheduledAt)
	})
	if len(runs) > daemon.RunHistoryLimit {
		runs = append([]daemon.ScheduledRun(nil), runs[len(runs)-daemon.RunHistoryLimit:]...)
	}
	return runs, nil
}
