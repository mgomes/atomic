package osservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	service "github.com/kardianos/service"

	"github.com/mgomes/ressik/internal/cancelerr"
)

const shutdownTimeout = 25 * time.Second

const (
	initialRetryDelay = time.Second
	maximumRetryDelay = 30 * time.Second
)

// Scope controls the ownership boundary of an installed background job.
type Scope string

const (
	// UserScope installs a launch agent or systemd user service.
	UserScope Scope = "user"
	// SystemScope is reserved; Ressik currently rejects machine services.
	SystemScope Scope = "system"
)

// Action is a platform background-manager operation.
type Action string

const (
	// Install registers the daemon.
	Install Action = "install"
	// Uninstall removes the daemon without deleting Ressik data.
	Uninstall Action = "uninstall"
	// Start requests daemon startup.
	Start Action = "start"
	// Stop requests daemon shutdown.
	Stop Action = "stop"
	// Restart stops and starts the daemon.
	Restart Action = "restart"
)

// Status is a portable service state.
type Status string

const (
	// Unknown means the platform could not determine service state.
	Unknown Status = "unknown"
	// Running means the service manager reports the daemon active.
	Running Status = "running"
	// Stopped means the service is installed but inactive.
	Stopped Status = "stopped"
)

// Worker is the foreground daemon lifecycle managed by the operating system.
type Worker interface {
	Run(context.Context) error
}

type loggerSetter interface {
	SetLogger(*slog.Logger)
}

// Options describes an installed background job. Every path must be absolute
// because platform managers do not share the invoking shell's working directory.
type Options struct {
	Scope      Scope
	Executable string
	ConfigPath string
	StateDir   string
}

// Manager controls and, on POSIX systems, hosts one Ressik background job.
type Manager struct {
	service    service.Service
	controller platformController
	program    *program
}

type platformController interface {
	Control(context.Context, Action) error
	Status() (Status, error)
}

// DefaultScope returns the per-user scope used on every supported platform.
func DefaultScope() Scope {
	return UserScope
}

// New validates options and constructs a platform background manager.
func New(worker Worker, options Options) (*Manager, error) {
	options, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		controller, err := newTaskController(options)
		if err != nil {
			return nil, err
		}
		return &Manager{controller: controller}, nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, fmt.Errorf("background installation is unsupported on %s; run ressik daemon under a supervisor", runtime.GOOS)
	}
	definition, err := serviceDefinition(options)
	if err != nil {
		return nil, err
	}
	program := &program{worker: worker, logger: slog.Default()}
	native, err := service.New(program, definition)
	if err != nil {
		return nil, fmt.Errorf("create native service: %w", err)
	}
	if native.Platform() != "darwin-launchd" && native.Platform() != "linux-systemd" {
		return nil, fmt.Errorf("background installation requires launchd or systemd, found %s", native.Platform())
	}
	return &Manager{service: native, program: program}, nil
}

// Run hosts the worker under a POSIX service manager and blocks until it stops.
func (m *Manager) Run() error {
	if m.controller != nil {
		return errors.New("the Windows scheduled task runs the daemon directly")
	}
	logger, err := m.service.SystemLogger(nil)
	if err == nil {
		managedLogger := slog.New(&systemHandler{logger: logger})
		m.program.setLogger(managedLogger)
		if setter, ok := m.program.worker.(loggerSetter); ok {
			setter.SetLogger(managedLogger)
		}
	}
	if err := m.service.Run(); err != nil {
		return fmt.Errorf("run native service: %w", err)
	}
	return nil
}

// Control performs one install, start, stop, restart, or uninstall action.
func (m *Manager) Control(ctx context.Context, action Action) error {
	if m.controller != nil {
		return m.controller.Control(ctx, action)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	switch action {
	case Install:
		err = m.service.Install()
	case Uninstall:
		status, statusErr := m.service.Status()
		if statusErr != nil {
			if stopErr := m.service.Stop(); stopErr != nil {
				return errors.Join(
					fmt.Errorf("query service before uninstall: %w", statusErr),
					fmt.Errorf("stop service with unknown status: %w", stopErr),
				)
			}
		}
		if statusErr == nil && status == service.StatusRunning {
			if stopErr := m.service.Stop(); stopErr != nil {
				return fmt.Errorf("stop service before uninstall: %w", stopErr)
			}
		}
		err = m.service.Uninstall()
	case Start:
		err = m.service.Start()
	case Stop:
		err = m.service.Stop()
	case Restart:
		err = m.service.Restart()
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
	if err != nil {
		return fmt.Errorf("%s service: %w", action, err)
	}
	return ctx.Err()
}

// Status returns the background job state.
func (m *Manager) Status() (Status, error) {
	if m.controller != nil {
		return m.controller.Status()
	}
	status, err := m.service.Status()
	if err != nil && !errors.Is(err, service.ErrNotInstalled) {
		return Unknown, fmt.Errorf("query service status: %w", err)
	}
	switch status {
	case service.StatusRunning:
		return Running, nil
	case service.StatusStopped:
		return Stopped, nil
	default:
		return Unknown, err
	}
}

func serviceDefinition(options Options) (*service.Config, error) {
	var err error
	options, err = normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		return nil, errors.New("Windows uses a per-user scheduled task")
	}

	return posixServiceDefinition(options), nil
}

func normalizeOptions(options Options) (Options, error) {
	if options.Scope == "" {
		options.Scope = DefaultScope()
	}
	if options.Scope != UserScope {
		return Options{}, errors.New("Ressik background services support only per-user scope")
	}
	paths := map[string]string{
		"executable":  options.Executable,
		"config path": options.ConfigPath,
		"state dir":   options.StateDir,
	}
	for name, path := range paths {
		if !filepath.IsAbs(path) {
			return Options{}, fmt.Errorf("service %s must be absolute: %q", name, path)
		}
		if runtime.GOOS == "linux" && strings.Contains(path, "%") {
			return Options{}, fmt.Errorf("service %s cannot contain %% on Linux: %q", name, path)
		}
	}
	return options, nil
}

func posixServiceDefinition(options Options) *service.Config {
	return &service.Config{
		Name:        backgroundName(options.StateDir),
		DisplayName: "Ressik Backup Service",
		Description: "Runs scheduled Ressik backup plans.",
		Executable:  options.Executable,
		Arguments: []string{
			"--config", options.ConfigPath,
			"daemon", "--managed", "--service-scope", string(options.Scope),
			"--state-dir", options.StateDir,
		},
		Option: service.KeyValue{
			"UserService":            true,
			"KeepAlive":              true,
			"RunAtLoad":              true,
			"Restart":                "on-failure",
			"SystemdScript":          userSystemdScript,
			"DelayedAutoStart":       true,
			"StartType":              "automatic",
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "10s",
			"OnFailureResetPeriod":   60,
		},
	}
}

type program struct {
	worker Worker
	logger *slog.Logger

	mu        sync.Mutex
	current   *programRun
	wait      func(context.Context, time.Duration) bool
	stopAfter func(time.Duration) <-chan time.Time
}

type programRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	wg     sync.WaitGroup
}

func (p *program) Start(_ service.Service) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != nil {
		return errors.New("service worker is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &programRun{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	p.current = run
	logger := p.logger
	wait := p.wait
	if wait == nil {
		wait = waitForRetry
	}
	run.wg.Go(func() {
		defer close(run.done)
		run.err = p.run(ctx, logger, wait)
	})
	return nil
}

func (p *program) Stop(_ service.Service) error {
	p.mu.Lock()
	run := p.current
	p.mu.Unlock()
	if run == nil {
		return nil
	}
	run.cancel()
	var (
		timer   *time.Timer
		timeout <-chan time.Time
	)
	if p.stopAfter != nil {
		timeout = p.stopAfter(shutdownTimeout)
	} else {
		timer = time.NewTimer(shutdownTimeout)
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case <-run.done:
		run.wg.Wait()
		p.mu.Lock()
		if p.current == run {
			p.current = nil
		}
		p.mu.Unlock()
		return run.err
	case <-timeout:
		return fmt.Errorf("daemon did not stop within %s", shutdownTimeout)
	}
}

func (p *program) setLogger(logger *slog.Logger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logger = logger
}

func (p *program) run(
	ctx context.Context,
	logger *slog.Logger,
	wait func(context.Context, time.Duration) bool,
) error {
	delay := initialRetryDelay
	for {
		err := p.worker.Run(ctx)
		if ctx.Err() != nil {
			if cancelerr.Only(err) {
				return nil
			}
			return err
		}
		if err != nil {
			logger.Error("daemon exited unexpectedly; retrying", "err", err, "retry_in", delay)
		} else {
			logger.Error("daemon exited unexpectedly without an error; retrying", "retry_in", delay)
		}
		if !wait(ctx, delay) {
			return nil
		}
		delay = min(delay*2, maximumRetryDelay)
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
