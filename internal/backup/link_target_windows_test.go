//go:build windows

package backup

import "testing"

func TestPortableLinkTargetNormalizesWindowsSeparators(t *testing.T) {
	t.Parallel()

	if got, want := portableLinkTarget(`..\target-dir\file`), "../target-dir/file"; got != want {
		t.Errorf("portableLinkTarget() = %q, want %q", got, want)
	}
	if err := validateSymlinkTarget(portableLinkTarget(`C:\archive\file`)); err == nil {
		t.Error("validateSymlinkTarget(absolute Windows target) error = nil, want portability error")
	}
}
