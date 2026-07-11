//go:build windows

// Package winpath prepares absolute paths for long-path Windows APIs.
package winpath

import (
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// Extended adds a Windows extended-length prefix when an absolute path is
// long enough to need one.
func Extended(path string) string {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\?\`) {
		return path
	}
	path = filepath.Clean(path)
	if len(utf16.Encode([]rune(path))) < 248 {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	return `\\?\` + path
}
