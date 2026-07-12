//go:build !darwin && !linux && !windows

package secretfile

import (
	"fmt"
	"os"
)

func ensureProtectedDirectory(string) error {
	return ErrUnsupported
}

func validateProtectedDirectory(string) error {
	return ErrUnsupported
}

func openProtectedFile(string) (*os.File, error) {
	return nil, ErrUnsupported
}

func createProtectedFile(string) (*os.File, error) {
	return nil, ErrUnsupported
}

func validateProtectedFile(*os.File, string) error {
	return ErrUnsupported
}

func syncProtectedDirectory(string) error {
	return ErrUnsupported
}

func publishNoReplace(string, string) error {
	return ErrUnsupported
}

func publishReplace(string, string) error {
	return ErrUnsupported
}

func isAlreadyExists(error) bool {
	return false
}

func insecure(path, reason string) error {
	return fmt.Errorf("%w: %q %s", ErrInsecure, path, reason)
}
