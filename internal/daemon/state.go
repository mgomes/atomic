package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	stateVersion = 2
	maxStateSize = 4 << 20

	// RunHistoryLimit is the number of scheduled occurrences retained per plan.
	RunHistoryLimit = 10
)

// RunOutcome is the durable result of one scheduled backup occurrence.
type RunOutcome string

const (
	// RunUnknown marks an occurrence without a durable result.
	RunUnknown RunOutcome = ""
	// RunSucceeded marks an occurrence that committed without an error.
	RunSucceeded RunOutcome = "succeeded"
	// RunFailed marks an occurrence that returned an error.
	RunFailed RunOutcome = "failed"
)

// State is the daemon's rebuildable schedule and outcome history.
type State struct {
	Version int                  `json:"version"`
	Plans   map[string]PlanState `json:"plans"`
}

// PlanState records handled occurrences and the latest backup outcome.
type PlanState struct {
	LastScheduled       time.Time      `json:"last_scheduled,omitempty"`
	PendingScheduled    time.Time      `json:"pending_scheduled,omitempty"`
	RetryAt             time.Time      `json:"retry_at,omitempty"`
	ConsecutiveFailures int            `json:"consecutive_failures,omitempty"`
	LastRun             time.Time      `json:"last_run,omitempty"`
	LastSnapshot        string         `json:"last_snapshot,omitempty"`
	LastError           string         `json:"last_error,omitempty"`
	Runs                []ScheduledRun `json:"runs,omitempty"`
}

// ScheduledRun records the outcome of one configured schedule occurrence.
type ScheduledRun struct {
	ScheduledAt time.Time  `json:"scheduled_at"`
	CompletedAt time.Time  `json:"completed_at,omitempty"`
	Outcome     RunOutcome `json:"outcome,omitempty"`
	SnapshotID  string     `json:"snapshot_id,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// LoadState loads daemon state or returns an empty state when it does not yet
// exist.
func LoadState(stateDir string) (State, error) {
	path := filepath.Join(stateDir, "daemon-state.json")
	file, err := openState(path)
	if errors.Is(err, os.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return State{}, fmt.Errorf("open daemon state: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateSize+1))
	if err != nil {
		return State{}, fmt.Errorf("read daemon state: %w", err)
	}
	if len(data) > maxStateSize {
		return State{}, fmt.Errorf("daemon state exceeds %d bytes", maxStateSize)
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("decode daemon state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return State{}, errors.New("daemon state contains trailing data")
	}
	if err := migrateState(&state); err != nil {
		return State{}, err
	}
	if state.Plans == nil {
		state.Plans = make(map[string]PlanState)
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func saveState(stateDir string, state State) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create daemon state directory: %w", err)
	}
	state.Version = stateVersion
	if err := validateState(state); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode daemon state: %w", err)
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(stateDir, ".daemon-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary daemon state: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("protect temporary daemon state: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary daemon state: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary daemon state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary daemon state: %w", err)
	}
	if err := replaceFile(tempName, filepath.Join(stateDir, "daemon-state.json")); err != nil {
		return fmt.Errorf("commit daemon state: %w", err)
	}
	if err := syncStateDir(stateDir); err != nil {
		return fmt.Errorf("sync daemon state directory: %w", err)
	}
	return nil
}

func newState() State {
	return State{Version: stateVersion, Plans: make(map[string]PlanState)}
}

func migrateState(state *State) error {
	switch state.Version {
	case 1:
		for planID, planState := range state.Plans {
			planState.Runs = nil
			state.Plans[planID] = planState
		}
		state.Version = stateVersion
	case stateVersion:
	default:
		return fmt.Errorf("daemon state version is %d, want 1 or %d", state.Version, stateVersion)
	}
	return nil
}

func validateState(state State) error {
	for planID, planState := range state.Plans {
		if len(planState.Runs) > RunHistoryLimit {
			return fmt.Errorf(
				"daemon state plan %q has %d scheduled runs, want at most %d",
				planID,
				len(planState.Runs),
				RunHistoryLimit,
			)
		}
		for index, run := range planState.Runs {
			if err := validateRun(run); err != nil {
				return fmt.Errorf("daemon state plan %q run %d: %w", planID, index, err)
			}
			if index > 0 && !planState.Runs[index-1].ScheduledAt.Before(run.ScheduledAt) {
				return fmt.Errorf("daemon state plan %q runs are not chronological", planID)
			}
		}
	}
	return nil
}

func validateRun(run ScheduledRun) error {
	if run.ScheduledAt.IsZero() {
		return errors.New("scheduled time is missing")
	}
	switch run.Outcome {
	case RunUnknown:
		if !run.CompletedAt.IsZero() || run.SnapshotID != "" || run.Error != "" {
			return errors.New("unknown outcome has completion data")
		}
	case RunSucceeded:
		if run.CompletedAt.IsZero() {
			return errors.New("successful outcome has no completion time")
		}
		if run.SnapshotID == "" {
			return errors.New("successful outcome has no snapshot")
		}
		if run.Error != "" {
			return errors.New("successful outcome has an error")
		}
	case RunFailed:
		if run.CompletedAt.IsZero() {
			return errors.New("failed outcome has no completion time")
		}
	default:
		return fmt.Errorf("unknown outcome %q", run.Outcome)
	}
	return nil
}

func (p *PlanState) beginRun(scheduledAt time.Time) {
	index := p.runIndex(scheduledAt)
	run := ScheduledRun{ScheduledAt: scheduledAt}
	if index >= 0 {
		p.Runs[index] = run
		return
	}
	p.Runs = append(p.Runs, run)
	sort.Slice(p.Runs, func(i, j int) bool {
		return p.Runs[i].ScheduledAt.Before(p.Runs[j].ScheduledAt)
	})
	if len(p.Runs) > RunHistoryLimit {
		p.Runs = append([]ScheduledRun(nil), p.Runs[len(p.Runs)-RunHistoryLimit:]...)
	}
}

func (p *PlanState) completeRun(
	scheduledAt time.Time,
	completedAt time.Time,
	outcome RunOutcome,
	snapshotID string,
	errorMessage string,
) {
	index := p.runIndex(scheduledAt)
	if index < 0 {
		p.beginRun(scheduledAt)
		index = p.runIndex(scheduledAt)
	}
	p.Runs[index] = ScheduledRun{
		ScheduledAt: scheduledAt,
		CompletedAt: completedAt,
		Outcome:     outcome,
		SnapshotID:  snapshotID,
		Error:       errorMessage,
	}
}

func (p PlanState) runIndex(scheduledAt time.Time) int {
	for index, run := range p.Runs {
		if run.ScheduledAt.Equal(scheduledAt) {
			return index
		}
	}
	return -1
}

func quarantineState(stateDir string) (State, error) {
	path := filepath.Join(stateDir, "daemon-state.json")
	quarantine := filepath.Join(
		stateDir,
		"daemon-state.corrupt-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json",
	)
	if err := os.Rename(path, quarantine); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newState(), nil
		}
		return State{}, fmt.Errorf("quarantine unreadable daemon state: %w", err)
	}
	if err := syncStateDir(stateDir); err != nil {
		return State{}, fmt.Errorf("sync quarantined daemon state: %w", err)
	}
	return newState(), nil
}
