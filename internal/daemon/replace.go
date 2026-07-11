package daemon

import (
	"os"
	"path/filepath"
)

func replaceFile(source, destination string) error {
	directory, err := os.OpenRoot(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Rename(filepath.Base(source), filepath.Base(destination))
}
