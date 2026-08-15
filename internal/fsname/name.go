// Package fsname validates path components that must round-trip across
// Atomic's supported filesystems.
package fsname

import (
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const maxComponentUnits = 255

// Component validates one portable filename component.
func Component(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("component %q is empty or reserved", name)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("component %q is not valid UTF-8", name)
	}
	if len(name) > maxComponentUnits {
		return fmt.Errorf("component %q exceeds %d UTF-8 bytes", name, maxComponentUnits)
	}
	if len(utf16.Encode([]rune(name))) > maxComponentUnits {
		return fmt.Errorf("component %q exceeds %d UTF-16 code units", name, maxComponentUnits)
	}
	for _, character := range name {
		if character < 32 || strings.ContainsRune(`<>:"/\|?*`, character) {
			return fmt.Errorf("component %q contains a Windows-incompatible character", name)
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("component %q ends with a dot or space", name)
	}
	if reservedDeviceName(name) {
		return fmt.Errorf("component %q is a reserved Windows device name", name)
	}
	return nil
}

// Path validates a slash-separated relative path. A single dot is the source
// root sentinel.
func Path(path string) error {
	if path == "." {
		return nil
	}
	for _, component := range strings.Split(path, "/") {
		if err := Component(component); err != nil {
			return err
		}
	}
	return nil
}

// Fold returns the comparison key used to detect case-insensitive collisions.
func Fold(path string) string {
	return cases.Fold().String(norm.NFC.String(path))
}

func reservedDeviceName(name string) bool {
	base := name
	if separator := strings.IndexByte(base, '.'); separator >= 0 {
		base = base[:separator]
	}
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	runes := []rune(base)
	if len(runes) == 4 && (string(runes[:3]) == "COM" || string(runes[:3]) == "LPT") {
		return runes[3] == '¹' || runes[3] == '²' || runes[3] == '³'
	}
	return false
}
