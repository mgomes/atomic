package cancelerr_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mgomes/ressik/internal/cancelerr"
)

func TestOnly(t *testing.T) {
	t.Parallel()

	storageErr := errors.New("save state")
	tests := map[string]struct {
		err  error
		want bool
	}{
		"nil":                 {want: true},
		"canceled":            {err: context.Canceled, want: true},
		"wrapped canceled":    {err: fmt.Errorf("stop: %w", context.Canceled), want: true},
		"joined cancellation": {err: errors.Join(context.Canceled, context.Canceled), want: true},
		"deadline":            {err: context.DeadlineExceeded},
		"storage":             {err: storageErr},
		"mixed":               {err: errors.Join(context.Canceled, storageErr)},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := cancelerr.Only(test.err); got != test.want {
				t.Errorf("Only(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}
