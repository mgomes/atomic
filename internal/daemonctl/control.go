package daemonctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const requestFilename = "daemon-stop.request"

// Request asks the daemon using stateDir to cancel its current work and exit.
func Request(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create daemon state directory: %w", err)
	}
	path := filepath.Join(stateDir, requestFilename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create daemon stop request: %w", err)
	}
	written, writeErr := file.Write([]byte("stop\n"))
	if writeErr == nil && written != len("stop\n") {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

// Clear removes a pending stop request.
func Clear(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, requestFilename))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Watcher cancels a context when a stop request appears.
type Watcher struct {
	ctx    context.Context
	cancel context.CancelFunc

	requested atomic.Bool
	wg        sync.WaitGroup
	mu        sync.Mutex
	err       error
}

// Watch starts monitoring stateDir for a stop request.
func Watch(parent context.Context, stateDir string) *Watcher {
	ctx, cancel := context.WithCancel(parent)
	watcher := &Watcher{ctx: ctx, cancel: cancel}
	watcher.wg.Go(func() { watcher.run(stateDir) })
	return watcher
}

// Context returns the context canceled by the parent or a stop request.
func (w *Watcher) Context() context.Context {
	return w.ctx
}

// Requested reports whether a stop request caused cancellation.
func (w *Watcher) Requested() bool {
	return w.requested.Load()
}

// Err reports a filesystem error encountered while watching.
func (w *Watcher) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Close stops the watcher and waits for it to exit.
func (w *Watcher) Close() {
	w.cancel()
	w.wg.Wait()
}

func (w *Watcher) run(stateDir string) {
	path := filepath.Join(stateDir, requestFilename)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		requested, err := requestExists(path)
		if err != nil {
			w.mu.Lock()
			w.err = err
			w.mu.Unlock()
			w.cancel()
			return
		}
		if requested {
			w.requested.Store(true)
			_ = os.Remove(path)
			w.cancel()
			return
		}
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func requestExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("daemon stop request is not a regular file")
	}
	return true, nil
}
