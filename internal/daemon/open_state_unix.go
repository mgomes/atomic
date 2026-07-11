//go:build !windows

package daemon

import "os"

func openState(path string) (*os.File, error) {
	return os.Open(path)
}
