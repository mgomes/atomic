// Package ignore validates and matches portable source-relative ignore globs.
package ignore

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/text/unicode/norm"
)

// Matcher applies a validated set of ignore patterns.
type Matcher struct {
	patterns []pattern
}

type pattern struct {
	value    string
	basename bool
}

// Compile validates patterns and returns an immutable matcher.
func Compile(patterns []string) (Matcher, error) {
	compiled := make([]pattern, 0, len(patterns))
	for index, value := range patterns {
		if err := Validate(value); err != nil {
			return Matcher{}, fmt.Errorf("pattern %d %q: %w", index, value, err)
		}
		value = norm.NFC.String(value)
		compiled = append(compiled, pattern{
			value:    value,
			basename: !strings.Contains(value, "/"),
		})
	}
	return Matcher{patterns: compiled}, nil
}

// Validate checks one portable ignore pattern.
func Validate(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("must not be empty")
	}
	if !utf8.ValidString(value) {
		return errors.New("must be valid UTF-8")
	}
	if strings.HasPrefix(value, "!") {
		return errors.New("must not use unsupported negation")
	}
	if strings.HasPrefix(value, "/") {
		return errors.New("must be relative to each source")
	}
	if strings.HasSuffix(value, "/") {
		return errors.New("must name a path, not end with a slash")
	}
	if strings.Contains(value, `\`) {
		return errors.New("must use forward slashes")
	}
	if strings.Contains(value, ":") {
		return errors.New("must not contain a volume designator")
	}
	if strings.ContainsAny(value, "{}") {
		return errors.New("must not use unsupported brace alternation")
	}

	value = norm.NFC.String(value)
	for _, component := range strings.Split(value, "/") {
		if component == "" {
			return errors.New("must not contain empty path components")
		}
		if component == "." || component == ".." {
			return errors.New("must not contain traversal components")
		}
		if strings.Contains(component, "**") && component != "**" {
			return errors.New("must use ** as a complete path component")
		}
	}
	if !doublestar.ValidatePattern(value) {
		return errors.New("has invalid glob syntax")
	}
	return nil
}

// Match reports whether a portable source-relative path should be ignored.
func (m Matcher) Match(relative string) bool {
	relative = norm.NFC.String(relative)
	for _, pattern := range m.patterns {
		candidate := relative
		if pattern.basename {
			candidate = path.Base(relative)
		}
		if doublestar.MatchUnvalidated(pattern.value, candidate) {
			return true
		}
	}
	return false
}
