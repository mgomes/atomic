//go:build linux

package secretfile

import "os"

func validateExtendedProtection(*os.File, string) error {
	return nil
}
