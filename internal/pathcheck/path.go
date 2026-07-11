// Package pathcheck compares existing filesystem paths by identity.
package pathcheck

import (
	"fmt"
	"os"
	"path/filepath"
)

// IdentitySet matches filesystem objects even when they are reached through
// aliases, bind mounts, junctions, or case variants.
type IdentitySet struct {
	infos []os.FileInfo
}

// NewIdentitySet records the identities of existing paths.
func NewIdentitySet(paths []string) (*IdentitySet, error) {
	set := &IdentitySet{infos: make([]os.FileInfo, 0, len(paths))}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect protected path %q: %w", path, err)
		}
		set.infos = append(set.infos, info)
	}
	return set, nil
}

// Matches reports whether info is one of the recorded filesystem objects.
func (s *IdentitySet) Matches(info os.FileInfo) bool {
	for _, protected := range s.infos {
		if os.SameFile(protected, info) {
			return true
		}
	}
	return false
}

// Contains reports whether path or any existing ancestor has a recorded
// identity.
func (s *IdentitySet) Contains(path string) (bool, error) {
	current, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("resolve path %q: %w", path, err)
	}
	for {
		info, err := os.Stat(current)
		if err != nil {
			return false, fmt.Errorf("inspect path ancestor %q: %w", current, err)
		}
		if s.Matches(info) {
			return true, nil
		}
		next := filepath.Dir(current)
		if next == current {
			return false, nil
		}
		current = next
	}
}

// Overlap reports whether either existing path contains the other, following
// aliases and filesystem-specific case rules through os.SameFile.
func Overlap(left, right string) (bool, error) {
	leftContains, err := Contains(left, right)
	if err != nil || leftContains {
		return leftContains, err
	}
	return Contains(right, left)
}

// Contains reports whether parent is the same filesystem object as child or
// one of child's existing ancestors.
func Contains(parent, child string) (bool, error) {
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return false, fmt.Errorf("inspect parent path %q: %w", parent, err)
	}
	current, err := filepath.Abs(child)
	if err != nil {
		return false, fmt.Errorf("resolve child path %q: %w", child, err)
	}
	for {
		info, err := os.Stat(current)
		if err != nil {
			return false, fmt.Errorf("inspect child ancestor %q: %w", current, err)
		}
		if os.SameFile(parentInfo, info) {
			return true, nil
		}
		next := filepath.Dir(current)
		if next == current {
			return false, nil
		}
		current = next
	}
}
