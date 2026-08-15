package retention

import (
	"time"

	"github.com/mgomes/atomic/internal/object"
	"github.com/mgomes/atomic/internal/repository"
)

// Policy combines a minimum copy count, a maximum snapshot age, and an
// explicitly protected snapshot. The rules have union semantics.
type Policy struct {
	KeepLast int
	KeepFor  time.Duration
	Protect  object.ID
}

// Select returns snapshots that may be removed, preserving input order. The
// newest and explicitly protected snapshots are retained, and a zero policy
// retains everything.
func Select(snapshots []repository.Summary, policy Policy, now time.Time) []repository.Summary {
	if len(snapshots) <= 1 || policy.KeepLast == 0 && policy.KeepFor == 0 {
		return nil
	}

	var remove []repository.Summary
	for i, snapshot := range snapshots {
		if snapshot.ID == policy.Protect || i == 0 || i < policy.KeepLast {
			continue
		}
		if policy.KeepFor > 0 && now.Sub(snapshot.CreatedAt) <= policy.KeepFor {
			continue
		}
		remove = append(remove, snapshot)
	}
	return remove
}
