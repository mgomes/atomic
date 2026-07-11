//go:build !darwin && !linux && !windows

package backup

import "fmt"

func renameNoReplace(_, _ string) error {
	return fmt.Errorf("atomic no-replace restore publication is unsupported on this operating system")
}
