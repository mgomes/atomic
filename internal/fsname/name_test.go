package fsname_test

import (
	"strings"
	"testing"

	"github.com/mgomes/ressik/internal/fsname"
)

func TestComponent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		valid bool
	}{
		{name: "report.txt", valid: true},
		{name: "résumé", valid: true},
		{name: "CON"},
		{name: "con.txt"},
		{name: "LPT9.data"},
		{name: "COM¹"},
		{name: "trailing."},
		{name: "trailing "},
		{name: "colon:name"},
		{name: "question?"},
		{name: string([]byte{'b', 'a', 'd', 0xff})},
		{name: strings.Repeat("a", 256)},
		{name: strings.Repeat("é", 200)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := fsname.Component(test.name)
			if test.valid && err != nil {
				t.Errorf("Component(%q) returned error: %v", test.name, err)
			}
			if !test.valid && err == nil {
				t.Errorf("Component(%q) error = nil, want portability error", test.name)
			}
		})
	}
}

func TestFoldDetectsCommonCaseCollisions(t *testing.T) {
	t.Parallel()

	if fsname.Fold("Folder/Report.TXT") != fsname.Fold("folder/report.txt") {
		t.Error("Fold() did not normalize a case-only path difference")
	}
	if fsname.Fold("Σ") != fsname.Fold("ς") {
		t.Error("Fold() did not normalize a Unicode case-fold collision")
	}
	if fsname.Fold("é") != fsname.Fold("e\u0301") {
		t.Error("Fold() did not normalize canonically equivalent names")
	}
}
