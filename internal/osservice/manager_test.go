package osservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	service "github.com/kardianos/service"
)

func TestDefaultScopeIsPerUser(t *testing.T) {
	t.Parallel()

	if got := DefaultScope(); got != UserScope {
		t.Errorf("DefaultScope() = %q, want %q", got, UserScope)
	}
}

func TestServiceDefinitionUsesAbsoluteManagedArguments(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses a scheduled task instead of a service definition")
	}

	options := Options{
		Scope:      UserScope,
		Executable: "/opt/atomic/bin/atomic",
		ConfigPath: "/home/user/.config/atomic/config.yaml",
		StateDir:   "/home/user/.local/state/atomic",
	}
	definition, err := serviceDefinition(options)
	if err != nil {
		t.Fatalf("serviceDefinition() returned error: %v", err)
	}
	got := strings.Join(definition.Arguments, " ")
	if !strings.Contains(got, "daemon --managed") {
		t.Errorf("serviceDefinition().Arguments = %q, want managed daemon", got)
	}
	if !strings.Contains(got, options.ConfigPath) || !strings.Contains(got, options.StateDir) {
		t.Errorf("serviceDefinition().Arguments = %q, want absolute config and state paths", got)
	}
	if definition.WorkingDirectory != "" {
		t.Errorf("serviceDefinition().WorkingDirectory = %q, want service-manager default", definition.WorkingDirectory)
	}
	if !strings.HasPrefix(definition.Name, "atomic-") {
		t.Errorf("serviceDefinition().Name = %q, want Atomic namespace", definition.Name)
	}
	other := options
	other.StateDir += "-other"
	otherDefinition, err := serviceDefinition(other)
	if err != nil {
		t.Fatalf("serviceDefinition(other) returned error: %v", err)
	}
	if definition.Name == otherDefinition.Name {
		t.Errorf("service names = %q for distinct state directories", definition.Name)
	}
}

func TestServiceDefinitionRejectsRelativePaths(t *testing.T) {
	t.Parallel()

	options := Options{
		Scope:      DefaultScope(),
		Executable: "atomic",
		ConfigPath: "/config.yaml",
		StateDir:   "/state",
	}
	if runtime.GOOS == "windows" {
		options.ConfigPath = `C:\config.yaml`
		options.StateDir = `C:\state`
	}
	_, err := serviceDefinition(options)
	if err == nil {
		t.Error("serviceDefinition(relative executable) error = nil, want validation error")
	}
}

func TestServiceDefinitionRejectsSystemdSpecifierPaths(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("systemd specifiers apply only on Linux")
	}

	options := Options{
		Scope:      UserScope,
		Executable: "/opt/atomic/bin/atomic",
		ConfigPath: "/home/user/config-%i.yaml",
		StateDir:   "/home/user/state",
	}
	if _, err := serviceDefinition(options); err == nil || !strings.Contains(err.Error(), "%") {
		t.Errorf("serviceDefinition(systemd specifier) error = %v, want percent rejection", err)
	}
}

func TestServiceDefinitionRejectsUnsupportedScope(t *testing.T) {
	t.Parallel()

	options := Options{
		Scope:      SystemScope,
		Executable: "/opt/atomic",
		ConfigPath: "/config.yaml",
		StateDir:   "/state",
	}
	if runtime.GOOS == "windows" {
		options.Scope = UserScope
		options.Executable = `C:\atomic.exe`
		options.ConfigPath = `C:\config.yaml`
		options.StateDir = `C:\state`
	}
	if _, err := serviceDefinition(options); err == nil {
		t.Error("serviceDefinition(unsupported scope) error = nil, want validation error")
	}
}

type cancelWorker struct {
	started chan struct{}
}

func (w *cancelWorker) Run(ctx context.Context) error {
	close(w.started)
	<-ctx.Done()
	return ctx.Err()
}

type cancelErrorWorker struct {
	started chan struct{}
	err     error
}

func (w *cancelErrorWorker) Run(ctx context.Context) error {
	close(w.started)
	<-ctx.Done()
	return errors.Join(ctx.Err(), w.err)
}

func TestProgramStopSuppressesWorkerCancellation(t *testing.T) {
	t.Parallel()

	worker := &cancelWorker{started: make(chan struct{})}
	program := &program{
		worker: worker,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := program.Start(nil); err != nil {
		t.Fatalf("program.Start() returned error: %v", err)
	}
	<-worker.started
	if err := program.Stop(nil); err != nil {
		t.Fatalf("program.Stop() returned error: %v, want canceled worker suppressed", err)
	}
}

func TestProgramStopPreservesErrorsJoinedWithCancellation(t *testing.T) {
	t.Parallel()

	storageErr := errors.New("persist final state")
	worker := &cancelErrorWorker{started: make(chan struct{}), err: storageErr}
	program := &program{
		worker: worker,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := program.Start(nil); err != nil {
		t.Fatalf("program.Start() returned error: %v", err)
	}
	<-worker.started
	if err := program.Stop(nil); !errors.Is(err, storageErr) {
		t.Fatalf("program.Stop() error = %v, want storage error", err)
	}
}

func TestProgramRetriesUnexpectedWorkerExitUntilStopped(t *testing.T) {
	t.Parallel()

	runErr := errors.New("transient daemon failure")
	worker := &exitWorker{
		calls: make(chan struct{}, 2),
		err:   runErr,
	}
	delays := make(chan time.Duration, 2)
	retry := make(chan struct{})
	program := &program{
		worker: worker,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		wait: func(ctx context.Context, delay time.Duration) bool {
			delays <- delay
			select {
			case <-ctx.Done():
				return false
			case <-retry:
				return true
			}
		},
	}
	if err := program.Start(nil); err != nil {
		t.Fatalf("program.Start() returned error: %v", err)
	}

	<-worker.calls
	if got, want := <-delays, initialRetryDelay; got != want {
		t.Errorf("first retry delay = %s, want %s", got, want)
	}
	retry <- struct{}{}
	<-worker.calls
	if got, want := <-delays, 2*initialRetryDelay; got != want {
		t.Errorf("second retry delay = %s, want %s", got, want)
	}
	if err := program.Stop(nil); err != nil {
		t.Fatalf("program.Stop() returned error: %v", err)
	}
	if got, want := worker.callCount(), 2; got != want {
		t.Errorf("worker.Run() calls = %d, want %d", got, want)
	}
}

func TestProgramStopTimesOutWithoutLosingRunningWorker(t *testing.T) {
	t.Parallel()

	worker := &stubbornWorker{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	timeout := make(chan time.Time)
	program := &program{
		worker: worker,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stopAfter: func(time.Duration) <-chan time.Time {
			return timeout
		},
	}
	if err := program.Start(nil); err != nil {
		t.Fatalf("program.Start() returned error: %v", err)
	}
	<-worker.started

	stopped := make(chan error, 1)
	var stops sync.WaitGroup
	stops.Go(func() {
		stopped <- program.Stop(nil)
	})
	timeout <- time.Now()
	if err := <-stopped; err == nil {
		t.Error("program.Stop() error = nil, want timeout")
	}
	stops.Wait()
	if err := program.Start(nil); err == nil {
		t.Error("program.Start() while timed-out worker remains error = nil, want already running")
	}

	close(worker.release)
	if err := program.Stop(nil); err != nil {
		t.Fatalf("program.Stop() after worker release returned error: %v", err)
	}
}

func TestManagerRunInjectsSystemLogger(t *testing.T) {
	t.Parallel()

	worker := &loggerWorker{}
	native := &testNativeService{logger: discardServiceLogger{}}
	native.run = func() error {
		if worker.logger == nil {
			return errors.New("worker did not receive system logger")
		}
		return nil
	}
	manager := &Manager{
		service: native,
		program: &program{worker: worker, logger: slog.Default()},
	}
	if err := manager.Run(); err != nil {
		t.Fatalf("Manager.Run() returned error: %v", err)
	}
}

func TestManagerUninstallStopsServiceWhenStatusIsUnknown(t *testing.T) {
	t.Parallel()

	statusErr := errors.New("user service bus unavailable")
	stopped := false
	uninstalled := false
	native := &testNativeService{
		status: func() (service.Status, error) { return service.StatusUnknown, statusErr },
		stop: func() error {
			stopped = true
			return nil
		},
		uninstall: func() error {
			uninstalled = true
			return nil
		},
	}
	manager := &Manager{service: native}
	if err := manager.Control(context.Background(), Uninstall); err != nil {
		t.Fatalf("Control(Uninstall) returned error: %v", err)
	}
	if !stopped || !uninstalled {
		t.Errorf("Control(Uninstall) stopped = %t, uninstalled = %t, want both true", stopped, uninstalled)
	}
}

func TestManagerUninstallPreservesServiceWhenUnknownStopFails(t *testing.T) {
	t.Parallel()

	statusErr := errors.New("user service bus unavailable")
	stopErr := errors.New("stop failed")
	uninstalled := false
	native := &testNativeService{
		status: func() (service.Status, error) { return service.StatusUnknown, statusErr },
		stop:   func() error { return stopErr },
		uninstall: func() error {
			uninstalled = true
			return nil
		},
	}
	manager := &Manager{service: native}
	err := manager.Control(context.Background(), Uninstall)
	if !errors.Is(err, statusErr) || !errors.Is(err, stopErr) {
		t.Fatalf("Control(Uninstall) error = %v, want status and stop errors", err)
	}
	if uninstalled {
		t.Error("Control(Uninstall) removed service after stop failure")
	}
}

type exitWorker struct {
	calls chan struct{}
	err   error

	mu    sync.Mutex
	count int
}

func (w *exitWorker) Run(context.Context) error {
	w.mu.Lock()
	w.count++
	w.mu.Unlock()
	w.calls <- struct{}{}
	return w.err
}

func (w *exitWorker) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count
}

type stubbornWorker struct {
	started chan struct{}
	release chan struct{}
}

func (w *stubbornWorker) Run(context.Context) error {
	close(w.started)
	<-w.release
	return nil
}

type loggerWorker struct {
	logger *slog.Logger
}

func (w *loggerWorker) Run(context.Context) error {
	return nil
}

func (w *loggerWorker) SetLogger(logger *slog.Logger) {
	w.logger = logger
}

type testNativeService struct {
	logger    service.Logger
	run       func() error
	status    func() (service.Status, error)
	stop      func() error
	uninstall func() error
}

func (s *testNativeService) Run() error   { return s.run() }
func (s *testNativeService) Start() error { return nil }
func (s *testNativeService) Stop() error {
	if s.stop != nil {
		return s.stop()
	}
	return nil
}
func (s *testNativeService) Restart() error { return nil }
func (s *testNativeService) Install() error { return nil }
func (s *testNativeService) Uninstall() error {
	if s.uninstall != nil {
		return s.uninstall()
	}
	return nil
}
func (s *testNativeService) Logger(chan<- error) (service.Logger, error) {
	return s.logger, nil
}
func (s *testNativeService) SystemLogger(chan<- error) (service.Logger, error) {
	return s.logger, nil
}
func (s *testNativeService) String() string   { return "test" }
func (s *testNativeService) Platform() string { return "test" }
func (s *testNativeService) Status() (service.Status, error) {
	if s.status != nil {
		return s.status()
	}
	return service.StatusRunning, nil
}

type discardServiceLogger struct{}

func (discardServiceLogger) Error(...any) error            { return nil }
func (discardServiceLogger) Warning(...any) error          { return nil }
func (discardServiceLogger) Info(...any) error             { return nil }
func (discardServiceLogger) Errorf(string, ...any) error   { return nil }
func (discardServiceLogger) Warningf(string, ...any) error { return nil }
func (discardServiceLogger) Infof(string, ...any) error    { return nil }
