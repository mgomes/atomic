package pathcheck_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mgomes/ressik/internal/pathcheck"
)

func TestOverlapFollowsDirectoryAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links requires elevated privileges on some Windows hosts")
	}

	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	child := filepath.Join(physical, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatalf("MkdirAll(child) returned error: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatalf("Symlink(alias) returned error: %v", err)
	}

	overlaps, err := pathcheck.Overlap(filepath.Join(alias, "child"), physical)
	if err != nil {
		t.Fatalf("Overlap() returned error: %v", err)
	}
	if !overlaps {
		t.Error("Overlap() = false, want true for aliased ancestor")
	}
}

func TestOverlapRejectsSiblings(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	if err := os.Mkdir(left, 0o700); err != nil {
		t.Fatalf("Mkdir(left) returned error: %v", err)
	}
	if err := os.Mkdir(right, 0o700); err != nil {
		t.Fatalf("Mkdir(right) returned error: %v", err)
	}
	overlaps, err := pathcheck.Overlap(left, right)
	if err != nil {
		t.Fatalf("Overlap() returned error: %v", err)
	}
	if overlaps {
		t.Error("Overlap() = true, want false for siblings")
	}
}

func TestIdentitySetContainsAliasedAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links requires elevated privileges on some Windows hosts")
	}

	root := t.TempDir()
	protected := filepath.Join(root, "protected")
	child := filepath.Join(protected, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatalf("MkdirAll(child) returned error: %v", err)
	}
	set, err := pathcheck.NewIdentitySet([]string{protected})
	if err != nil {
		t.Fatalf("NewIdentitySet() returned error: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(protected, alias); err != nil {
		t.Fatalf("Symlink(alias) returned error: %v", err)
	}

	contains, err := set.Contains(filepath.Join(alias, "child"))
	if err != nil {
		t.Fatalf("IdentitySet.Contains() returned error: %v", err)
	}
	if !contains {
		t.Error("IdentitySet.Contains() = false for aliased protected ancestor")
	}
	info, err := os.Stat(alias)
	if err != nil {
		t.Fatalf("Stat(alias) returned error: %v", err)
	}
	if !set.Matches(info) {
		t.Error("IdentitySet.Matches() = false for aliased protected directory")
	}
}
