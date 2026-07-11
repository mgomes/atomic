//go:build windows

package osservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gofrs/flock"

	"github.com/mgomes/ressik/internal/daemonctl"
)

func TestNewTaskControllerNamespacesConfigAndRunsForegroundDaemon(t *testing.T) {
	t.Parallel()

	controller, err := newTaskController(Options{
		Scope:      UserScope,
		Executable: `C:\Program Files\Ressik\rs.exe`,
		ConfigPath: `C:\Users\me\AppData\Roaming\ressik\config.yaml`,
		StateDir:   `C:\Users\me\AppData\Local\ressik\instances\abc`,
	})
	if err != nil {
		t.Fatalf("newTaskController() returned error: %v", err)
	}
	task := controller.(*taskController)
	if !strings.HasPrefix(task.name, "ressik-") {
		t.Errorf("task name = %q, want Ressik namespace", task.name)
	}
	if !strings.Contains(task.arguments, "daemon") || strings.Contains(task.arguments, "managed") {
		t.Errorf("task arguments = %q, want direct foreground daemon", task.arguments)
	}
	if !strings.Contains(task.arguments, "--log-file") {
		t.Errorf("task arguments = %q, want durable daemon log", task.arguments)
	}
}

func TestTaskControllerForcedRestartCompletesLifecycle(t *testing.T) {
	t.Parallel()

	waitErr := errors.New("cooperative stop timed out")
	var calls []string
	terminated := false
	task := &taskController{
		name:     "ressik-test",
		stateDir: t.TempDir(),
		commandHook: func(_ context.Context, arguments ...string) error {
			calls = append(calls, arguments[0])
			return nil
		},
		waitStoppedHook: func(context.Context) (bool, error) {
			return false, waitErr
		},
		waitTerminatedHook: func(context.Context) error {
			terminated = true
			return nil
		},
		waitRunningHook: func(context.Context) (bool, error) {
			return true, nil
		},
		waitQuiescentHook: func(context.Context) (bool, error) {
			return true, nil
		},
	}
	if err := task.Control(context.Background(), Restart); err != nil {
		t.Fatalf("Control(Restart) returned error: %v", err)
	}
	if got, want := strings.Join(calls, " "), "/Query /End /Change /Change /Run"; got != want {
		t.Errorf("Control(Restart) commands = %q, want %q", got, want)
	}
	if !terminated {
		t.Error("Control(Restart) did not wait after forced termination")
	}
	entries, err := os.ReadDir(task.stateDir)
	if err != nil {
		t.Fatalf("ReadDir(state) returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Control(Restart) left state control files: %v", entries)
	}
}

func TestTaskControllerUninstallKeepsRequestUntilDeletion(t *testing.T) {
	t.Parallel()

	requestPresentAtDelete := false
	var task *taskController
	task = &taskController{
		name:     "ressik-test",
		stateDir: t.TempDir(),
		commandHook: func(_ context.Context, arguments ...string) error {
			if arguments[0] == "/Delete" {
				entries, err := os.ReadDir(task.stateDir)
				if err != nil {
					return err
				}
				requestPresentAtDelete = len(entries) == 1
			}
			return nil
		},
		waitStoppedHook: func(context.Context) (bool, error) {
			if err := daemonctl.Clear(task.stateDir); err != nil {
				return false, err
			}
			return true, nil
		},
		waitQuiescentHook: func(context.Context) (bool, error) {
			return true, nil
		},
	}
	if err := task.Control(context.Background(), Uninstall); err != nil {
		t.Fatalf("Control(Uninstall) returned error: %v", err)
	}
	if !requestPresentAtDelete {
		t.Error("Control(Uninstall) cleared stop request before deleting task")
	}
	entries, err := os.ReadDir(task.stateDir)
	if err != nil {
		t.Fatalf("ReadDir(state) returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Control(Uninstall) left state control files: %v", entries)
	}
}

func TestTaskControllerRejectsMissingTaskBeforeRequest(t *testing.T) {
	t.Parallel()

	queryErr := errors.New("task not found")
	task := &taskController{
		name:     "ressik-test",
		stateDir: t.TempDir(),
		commandHook: func(_ context.Context, arguments ...string) error {
			if arguments[0] == "/Query" {
				return queryErr
			}
			return nil
		},
	}
	if err := task.Control(context.Background(), Stop); !errors.Is(err, queryErr) {
		t.Fatalf("Control(Stop) error = %v, want query error", err)
	}
	entries, err := os.ReadDir(task.stateDir)
	if err != nil {
		t.Fatalf("ReadDir(state) returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Control(Stop) for missing task created state files: %v", entries)
	}
}

func TestTaskControllerInstallDoesNotOverwriteExistingTask(t *testing.T) {
	t.Parallel()

	var createArguments []string
	task := &taskController{
		name:       "ressik-test",
		userSID:    "S-1-5-21-1-2-3-1001",
		executable: `C:\rs.exe`,
		arguments:  `daemon`,
		stateDir:   t.TempDir(),
		commandHook: func(_ context.Context, arguments ...string) error {
			createArguments = append([]string(nil), arguments...)
			return nil
		},
	}
	if err := task.Control(context.Background(), Install); err != nil {
		t.Fatalf("Control(Install) returned error: %v", err)
	}
	if slices.Contains(createArguments, "/F") {
		t.Errorf("Control(Install) arguments = %v, want existing task rejection", createArguments)
	}
}

func TestTaskControllerStartIsIdempotentWhenDaemonRuns(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	lock := flock.New(filepath.Join(stateDir, "daemon.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatalf("Lock() returned error: %v", err)
	}
	defer lock.Unlock()
	var calls []string
	task := &taskController{
		name:     "ressik-test",
		stateDir: stateDir,
		commandHook: func(_ context.Context, arguments ...string) error {
			calls = append(calls, arguments[0])
			return nil
		},
	}
	if err := task.Control(context.Background(), Start); err != nil {
		t.Fatalf("Control(Start) returned error: %v", err)
	}
	if got, want := strings.Join(calls, " "), "/Query /Change"; got != want {
		t.Errorf("Control(Start) commands = %q, want %q", got, want)
	}
}

func TestTaskControllerStartRetriesIgnoredRunUntilDaemonOwnsLock(t *testing.T) {
	t.Parallel()

	var calls []string
	waits := 0
	task := &taskController{
		name:     "ressik-test",
		stateDir: t.TempDir(),
		commandHook: func(_ context.Context, arguments ...string) error {
			calls = append(calls, arguments[0])
			return nil
		},
		waitRunningHook: func(context.Context) (bool, error) {
			waits++
			return waits == 2, nil
		},
	}
	if err := task.Control(context.Background(), Start); err != nil {
		t.Fatalf("Control(Start) returned error: %v", err)
	}
	if got, want := strings.Join(calls, " "), "/Query /Change /Run /Run"; got != want {
		t.Errorf("Control(Start) commands = %q, want %q", got, want)
	}
}

func TestTaskControllerStartClearsRequestBeforeEnablingTask(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := daemonctl.Request(stateDir); err != nil {
		t.Fatalf("daemonctl.Request() returned error: %v", err)
	}
	requestClearedAtEnable := false
	task := &taskController{
		name:     "ressik-test",
		stateDir: stateDir,
		commandHook: func(_ context.Context, arguments ...string) error {
			if arguments[0] == "/Change" && slices.Contains(arguments, "/Enable") {
				entries, err := os.ReadDir(stateDir)
				if err != nil {
					return err
				}
				requestClearedAtEnable = len(entries) == 0
			}
			return nil
		},
		waitRunningHook: func(context.Context) (bool, error) {
			return true, nil
		},
	}
	if err := task.Control(context.Background(), Start); err != nil {
		t.Fatalf("Control(Start) returned error: %v", err)
	}
	if !requestClearedAtEnable {
		t.Error("Control(Start) enabled task before clearing stop request")
	}
}
