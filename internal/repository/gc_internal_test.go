package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSyncModifiedDirsHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dirs := make(map[string]bool, 256)
	for i := range 256 {
		dirs[fmt.Sprintf("missing-collect-dir-%d", i)] = true
	}

	err := syncModifiedDirs(ctx, dirs)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("syncModifiedDirs(canceled) error = %v, want context.Canceled", err)
	}
}

func TestSyncModifiedDirsSyncsLiveContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := syncModifiedDirs(context.Background(), map[string]bool{dir: true}); err != nil {
		t.Fatalf("syncModifiedDirs(live) error = %v", err)
	}
}

func TestCollectCanceledContextSkipsDirectorySync(t *testing.T) {
	t.Parallel()

	repo, err := Initialize(t.TempDir())
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = repo.Exclusive(context.Background(), func() error {
		_, err := repo.Collect(ctx)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect(canceled) error = %v, want context.Canceled", err)
	}
}
