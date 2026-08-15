package chunk_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mgomes/atomic/internal/chunk"
)

func TestFixedSplit(t *testing.T) {
	t.Parallel()

	splitter, err := chunk.NewFixed(3)
	if err != nil {
		t.Fatalf("NewFixed(3) returned error: %v", err)
	}

	var got []string
	err = splitter.Split(context.Background(), bytes.NewBufferString("abcdefgh"), func(block []byte) error {
		got = append(got, string(block))
		return nil
	})
	if err != nil {
		t.Fatalf("Fixed.Split(abcdefgh) returned error: %v", err)
	}

	want := []string{"abc", "def", "gh"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Fixed.Split(abcdefgh) mismatch (-want +got):\n%s", diff)
	}
}

func TestFixedSplitHonorsCancellation(t *testing.T) {
	t.Parallel()

	splitter, err := chunk.NewFixed(3)
	if err != nil {
		t.Fatalf("NewFixed(3) returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := splitter.Split(ctx, bytes.NewBufferString("abc"), func([]byte) error { return nil }); err == nil {
		t.Error("Fixed.Split() error = nil, want cancellation error")
	}
}
