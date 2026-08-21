package backup

import (
	"context"
	"testing"
	"time"
)

func TestFailedBackupCollectContextIgnoresParentCancellation(t *testing.T) {
	t.Parallel()

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, stop := failedBackupCollectContext(parent)
	defer stop()

	if err := ctx.Err(); err != nil {
		t.Fatalf("failedBackupCollectContext(canceled parent).Err() = %v, want nil", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("failedBackupCollectContext(canceled parent) has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		t.Fatalf("failedBackupCollectContext(canceled parent) remaining = %s, want > 0", remaining)
	}
	if remaining > failedBackupCollectTimeout {
		t.Fatalf(
			"failedBackupCollectContext(canceled parent) remaining = %s, want <= %s",
			remaining,
			failedBackupCollectTimeout,
		)
	}
}
