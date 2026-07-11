package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	stateVersion = 1
	maxStateSize = 4 << 20
)

// State is the daemon's rebuildable schedule and outcome history.
type State struct {
	Version int                  `json:"version"`
	Plans   map[string]PlanState `json:"plans"`
}

// PlanState records the last handled occurrence and backup outcome.
type PlanState struct {
	LastScheduled       time.Time `json:"last_scheduled,omitempty"`
	PendingScheduled    time.Time `json:"pending_scheduled,omitempty"`
	RetryAt             time.Time `json:"retry_at,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	LastRun             time.Time `json:"last_run,omitempty"`
	LastSnapshot        string    `json:"last_snapshot,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
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
	if state.Version != stateVersion {
		return State{}, fmt.Errorf("daemon state version is %d, want %d", state.Version, stateVersion)
	}
	if state.Plans == nil {
		state.Plans = make(map[string]PlanState)
	}
	return state, nil
}

func saveState(stateDir string, state State) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create daemon state directory: %w", err)
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
