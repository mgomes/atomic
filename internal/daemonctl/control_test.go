package daemonctl_test

import (
	"context"
	"testing"
	"time"

	"github.com/mgomes/ressik/internal/daemonctl"
)

func TestWatcherCancelsOnStopRequest(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	watcher := daemonctl.Watch(context.Background(), stateDir)
	defer watcher.Close()
	if err := daemonctl.Request(stateDir); err != nil {
		t.Fatalf("Request() returned error: %v", err)
	}
	select {
	case <-watcher.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Watcher context was not canceled")
	}
	if !watcher.Requested() {
		t.Error("Watcher.Requested() = false, want true")
	}
	if err := watcher.Err(); err != nil {
		t.Errorf("Watcher.Err() = %v, want nil", err)
	}
}

func TestWatcherDistinguishesParentCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	watcher := daemonctl.Watch(ctx, t.TempDir())
	cancel()
	watcher.Close()
	if watcher.Requested() {
		t.Error("Watcher.Requested() = true after parent cancellation")
	}
}
