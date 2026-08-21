package repository

import (
	"context"
	"errors"
	"testing"
)

func TestCollectCanceledContextReturnsCanceled(t *testing.T) {
	t.Parallel()

	repo, err := Initialize(t.TempDir())
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	err = repo.Exclusive(context.Background(), func() error {
		_, _, err := repo.PutBlock(context.Background(), []byte("orphan"))
		return err
	})
	if err != nil {
		t.Fatalf("PutBlock() returned error: %v", err)
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
