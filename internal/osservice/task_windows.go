//go:build windows

package osservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/sys/windows"

	"github.com/mgomes/atomic/internal/daemonctl"
)

const (
	taskStartupGrace   = 2 * time.Second
	taskStartupTimeout = 15 * time.Second
)

type taskController struct {
	name               string
	userSID            string
	executable         string
	arguments          string
	stateDir           string
	commandHook        func(context.Context, ...string) error
	waitStoppedHook    func(context.Context) (bool, error)
	waitTerminatedHook func(context.Context) error
	waitRunningHook    func(context.Context) (bool, error)
	waitQuiescentHook  func(context.Context) (bool, error)
}

func newTaskController(options Options) (platformController, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows user token: %w", err)
	}
	userSID := user.User.Sid.String()
	if userSID == "" {
		return nil, errors.New("current Windows user SID is empty")
	}
	arguments := windows.ComposeCommandLine([]string{
		"--config", options.ConfigPath,
		"daemon", "--state-dir", options.StateDir,
		"--log-file", filepath.Join(options.StateDir, "daemon.log"),
	})
	return &taskController{
		name:       backgroundName(options.StateDir),
		userSID:    userSID,
		executable: options.Executable,
		arguments:  arguments,
		stateDir:   options.StateDir,
	}, nil
}

func (t *taskController) Control(ctx context.Context, action Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch action {
	case Install:
		return t.install(ctx)
	case Start:
		if err := t.ensureInstalled(ctx); err != nil {
			return err
		}
		return t.start(ctx)
	case Stop:
		if err := t.ensureInstalled(ctx); err != nil {
			return err
		}
		return t.stopAndDisable(ctx, true)
	case Restart:
		if err := t.ensureInstalled(ctx); err != nil {
			return err
		}
		if err := t.stopAndDisable(ctx, false); err != nil {
			return err
		}
		return t.start(ctx)
	case Uninstall:
		if err := t.ensureInstalled(ctx); err != nil {
			return err
		}
		if err := t.stopAndDisable(ctx, false); err != nil {
			return err
		}
		if err := t.command(ctx, "/Delete", "/TN", t.name, "/F"); err != nil {
			return err
		}
		return daemonctl.Clear(t.stateDir)
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
}

func (t *taskController) ensureInstalled(ctx context.Context) error {
	return t.command(ctx, "/Query", "/TN", t.name)
}

func (t *taskController) disable(ctx context.Context) error {
	return t.command(ctx, "/Change", "/TN", t.name, "/Disable")
}

func (t *taskController) stopAndDisable(ctx context.Context, clearRequest bool) error {
	stopErr := t.stop(ctx, false)
	disableCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	disableErr := t.disable(disableCtx)
	var quiesceErr error
	if disableErr == nil {
		if err := daemonctl.Request(t.stateDir); err != nil {
			quiesceErr = err
		} else {
			quiesceCtx, quiesceCancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				shutdownTimeout+taskStartupGrace,
			)
			defer quiesceCancel()
			stopped, err := t.awaitQuiescent(quiesceCtx)
			if err != nil {
				quiesceErr = err
			} else if !stopped {
				quiesceErr = errors.New("daemon did not become quiescent after disabling task")
			}
		}
	}
	if stopErr != nil || disableErr != nil || quiesceErr != nil {
		return errors.Join(stopErr, disableErr, quiesceErr)
	}
	if clearRequest {
		return daemonctl.Clear(t.stateDir)
	}
	return nil
}

func (t *taskController) start(ctx context.Context) error {
	if err := daemonctl.Clear(t.stateDir); err != nil {
		return fmt.Errorf("clear stale daemon stop request: %w", err)
	}
	if err := t.command(ctx, "/Change", "/TN", t.name, "/Enable"); err != nil {
		return err
	}
	available, err := t.lockAvailable()
	if err != nil {
		return err
	}
	if !available {
		return nil
	}
	startupCtx, cancel := context.WithTimeout(ctx, taskStartupTimeout)
	defer cancel()
	for {
		if err := t.command(startupCtx, "/Run", "/TN", t.name); err != nil {
			return err
		}
		running, err := t.awaitRunning(startupCtx)
		if err != nil {
			return err
		}
		if running {
			return nil
		}
		if err := startupCtx.Err(); err != nil {
			return fmt.Errorf("daemon did not start within %s: %w", taskStartupTimeout, err)
		}
	}
}

func (t *taskController) stop(ctx context.Context, clearRequest bool) error {
	if err := daemonctl.Request(t.stateDir); err != nil {
		return err
	}
	stopped, waitErr := t.awaitStopped(ctx)
	if stopped {
		if clearRequest {
			return daemonctl.Clear(t.stateDir)
		}
		return nil
	}
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := t.command(endCtx, "/End", "/TN", t.name); err != nil {
		return errors.Join(waitErr, err)
	}
	if err := t.awaitTerminated(endCtx); err != nil {
		return err
	}
	if clearRequest {
		return daemonctl.Clear(t.stateDir)
	}
	return nil
}

func (t *taskController) awaitStopped(ctx context.Context) (bool, error) {
	if t.waitStoppedHook != nil {
		return t.waitStoppedHook(ctx)
	}
	return t.waitStopped(ctx)
}

func (t *taskController) awaitQuiescent(ctx context.Context) (bool, error) {
	if t.waitQuiescentHook != nil {
		return t.waitQuiescentHook(ctx)
	}
	return t.waitStopped(ctx)
}

func (t *taskController) awaitTerminated(ctx context.Context) error {
	if t.waitTerminatedHook != nil {
		return t.waitTerminatedHook(ctx)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		available, err := t.lockAvailable()
		if err != nil {
			return err
		}
		if available {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for terminated daemon: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (t *taskController) awaitRunning(ctx context.Context) (bool, error) {
	if t.waitRunningHook != nil {
		return t.waitRunningHook(ctx)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	startup := time.NewTimer(taskStartupGrace)
	defer startup.Stop()
	for {
		available, err := t.lockAvailable()
		if err != nil {
			return false, err
		}
		if !available {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-startup.C:
			return false, nil
		case <-ticker.C:
		}
	}
}

func (t *taskController) waitStopped(ctx context.Context) (bool, error) {
	deadline := time.NewTimer(shutdownTimeout)
	defer deadline.Stop()
	startup := time.NewTimer(taskStartupGrace)
	defer startup.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	startupElapsed := false
	seenRunning := false
	for {
		available, err := t.lockAvailable()
		if err != nil {
			return false, err
		}
		if !available {
			seenRunning = true
		} else if seenRunning || startupElapsed {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, fmt.Errorf("daemon did not stop within %s", shutdownTimeout)
		case <-startup.C:
			startupElapsed = true
		case <-ticker.C:
		}
	}
}

func (t *taskController) Status() (Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := t.command(ctx, "/Query", "/TN", t.name); err != nil {
		return Unknown, err
	}
	available, err := t.lockAvailable()
	if err != nil {
		return Unknown, err
	}
	if !available {
		return Running, nil
	}
	return Stopped, nil
}

func (t *taskController) lockAvailable() (bool, error) {
	lock := flock.New(filepath.Join(t.stateDir, "daemon.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return false, fmt.Errorf("inspect daemon lock: %w", err)
	}
	if !locked {
		return false, nil
	}
	if err := lock.Unlock(); err != nil {
		return false, fmt.Errorf("release daemon status lock: %w", err)
	}
	return true, nil
}

func (t *taskController) install(ctx context.Context) error {
	if err := os.MkdirAll(t.stateDir, 0o700); err != nil {
		return fmt.Errorf("create task state directory: %w", err)
	}
	if err := daemonctl.Clear(t.stateDir); err != nil {
		return fmt.Errorf("clear stale daemon stop request: %w", err)
	}
	definition, err := marshalTask(t.userSID, t.executable, t.arguments)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(t.stateDir, ".atomic-task-*.xml")
	if err != nil {
		return fmt.Errorf("create temporary task definition: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect temporary task definition: %w", err)
	}
	written, err := file.Write(definition)
	if err == nil && written != len(definition) {
		err = io.ErrShortWrite
	}
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary task definition: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temporary task definition: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary task definition: %w", err)
	}
	return t.command(ctx, "/Create", "/TN", t.name, "/XML", path)
}

func (t *taskController) command(ctx context.Context, arguments ...string) error {
	if t.commandHook != nil {
		return t.commandHook(ctx, arguments...)
	}
	output, err := exec.CommandContext(ctx, "schtasks.exe", arguments...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("run schtasks %s: %w", arguments[0], err)
		}
		return fmt.Errorf("run schtasks %s: %w: %s", arguments[0], err, message)
	}
	return nil
}
