//go:build windows

package winpath_test

import (
	"strings"
	"testing"

	"github.com/mgomes/ressik/internal/winpath"
)

func TestExtendedHandlesLongLocalAndUNCPaths(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path       string
		wantPrefix string
	}{
		"local": {path: `C:\` + strings.Repeat(`segment\`, 40), wantPrefix: `\\?\C:\`},
		"UNC":   {path: `\\server\share\` + strings.Repeat(`segment\`, 40), wantPrefix: `\\?\UNC\server\share\`},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := winpath.Extended(test.path); !strings.HasPrefix(got, test.wantPrefix) {
				t.Errorf("Extended() = %q, want prefix %q", got, test.wantPrefix)
			}
		})
	}
}
