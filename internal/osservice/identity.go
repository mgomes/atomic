package osservice

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

func backgroundName(stateDir string) string {
	identity := filepath.Clean(stateDir)
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("ressik-%x", digest[:8])
}
