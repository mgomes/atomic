package ignore_test

import (
	"testing"

	"github.com/mgomes/atomic/internal/ignore"
)

func TestMatcherMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{name: "empty matcher", path: "file.tmp"},
		{name: "literal basename at root", patterns: []string{".DS_Store"}, path: ".DS_Store", want: true},
		{name: "literal basename nested", patterns: []string{"cache"}, path: "work/cache", want: true},
		{name: "wildcard basename at root", patterns: []string{"*.tmp"}, path: "file.tmp", want: true},
		{name: "wildcard basename nested", patterns: []string{"*.tmp"}, path: "work/file.tmp", want: true},
		{name: "wildcard includes dotfiles", patterns: []string{"*"}, path: "work/.env", want: true},
		{name: "slash pattern anchored", patterns: []string{"build/*.map"}, path: "build/app.map", want: true},
		{name: "slash pattern excludes nested roots", patterns: []string{"build/*.map"}, path: "work/build/app.map"},
		{name: "single star excludes nested segments", patterns: []string{"build/*.map"}, path: "build/assets/app.map"},
		{name: "doublestar matches zero directories", patterns: []string{"build/**/*.map"}, path: "build/app.map", want: true},
		{name: "doublestar matches nested directories", patterns: []string{"build/**/*.map"}, path: "build/assets/app.map", want: true},
		{name: "question and class", patterns: []string{"reports/202?-0[1-3].csv"}, path: "reports/2026-02.csv", want: true},
		{name: "case sensitive", patterns: []string{"*.TMP"}, path: "work/file.tmp"},
		{name: "unicode normalized", patterns: []string{"Caf\u00e9"}, path: "Cafe\u0301", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			matcher, err := ignore.Compile(test.patterns)
			if err != nil {
				t.Fatalf("Compile(%q) returned error: %v", test.patterns, err)
			}
			if got := matcher.Match(test.path); got != test.want {
				t.Errorf("Matcher.Match(%q) = %t, want %t", test.path, got, test.want)
			}
		})
	}
}

func TestMatcherOwnsPatterns(t *testing.T) {
	t.Parallel()

	patterns := []string{"*.tmp"}
	matcher, err := ignore.Compile(patterns)
	if err != nil {
		t.Fatalf("Compile(%q) returned error: %v", patterns, err)
	}
	patterns[0] = "*.keep"
	if !matcher.Match("file.tmp") {
		t.Error("Matcher.Match(file.tmp) = false after caller mutation, want true")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "literal", value: ".DS_Store", valid: true},
		{name: "wildcard", value: "*.tmp", valid: true},
		{name: "doublestar", value: "build/**/*.map", valid: true},
		{name: "class", value: "file.[ch]", valid: true},
		{name: "empty"},
		{name: "whitespace", value: "   "},
		{name: "negation", value: "!keep.tmp"},
		{name: "leading slash", value: "/cache"},
		{name: "trailing slash", value: "cache/"},
		{name: "native separator", value: `cache\*.tmp`},
		{name: "volume", value: "C:/cache"},
		{name: "brace alternation", value: "*.{tmp,log}"},
		{name: "empty component", value: "cache//file"},
		{name: "current component", value: "cache/./file"},
		{name: "parent component", value: "cache/../file"},
		{name: "embedded doublestar", value: "cache/**.tmp"},
		{name: "malformed class", value: "file.[abc"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ignore.Validate(test.value)
			if got := err == nil; got != test.valid {
				t.Errorf("Validate(%q) success = %t, want %t; error: %v", test.value, got, test.valid, err)
			}
		})
	}
}

func FuzzValidateAndMatch(f *testing.F) {
	f.Add("*.tmp", "nested/file.tmp")
	f.Add("build/**/*.map", "build/assets/app.map")
	f.Add("[", "file")
	f.Add("!keep", "keep")
	f.Fuzz(func(t *testing.T, pattern, candidate string) {
		if ignore.Validate(pattern) != nil {
			return
		}
		matcher, err := ignore.Compile([]string{pattern})
		if err != nil {
			t.Fatalf("Compile(%q) returned error after successful validation: %v", pattern, err)
		}
		_ = matcher.Match(candidate)
	})
}
