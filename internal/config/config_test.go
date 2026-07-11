package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mgomes/ressik/internal/config"
)

func TestLoadResolvesRelativePaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	data := []byte(`version: 1
repository: repo
plans:
  documents:
    name: Documents
    enabled: true
    sources:
      work:
        path: files
    retention:
      keep_last: 3
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(config.yaml) returned error: %v", err)
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root) returned error: %v", err)
	}
	if got, want := loaded.Config.Repository, filepath.Join(physicalRoot, "repo"); got != want {
		t.Errorf("Load().Repository = %q, want %q", got, want)
	}
	if got, want := loaded.Config.Plans["documents"].Sources["work"].Path, filepath.Join(physicalRoot, "files"); got != want {
		t.Errorf("Load().Plans[documents].Sources[work].Path = %q, want %q", got, want)
	}
	if got := len(loaded.ID); got != 32 {
		t.Errorf("Load().ID length = %d, want 32", got)
	}
}

func TestLoadTreatsGlobMetacharactersAsLiteralPathCharacters(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	data := []byte(`version: 1
repository: repo
plans:
  photos:
    name: Photos
    enabled: true
    sources:
      archive:
        path: Photos [Archive]
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(config.yaml) returned error: %v", err)
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root) returned error: %v", err)
	}
	if got, want := loaded.Config.Plans["photos"].Sources["archive"].Path, filepath.Join(physicalRoot, "Photos [Archive]"); got != want {
		t.Errorf("Load().source path = %q, want %q", got, want)
	}
}

func TestLoadPreservesGlobalIgnorePatterns(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`version: 1
repository: ./repository
ignore:
  - "*.tmp"
  - cache
  - "build/**/*.map"
plans: {}
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(config.yaml) returned error: %v", err)
	}
	want := []string{"*.tmp", "cache", "build/**/*.map"}
	if diff := cmp.Diff(want, loaded.Config.Ignore); diff != "" {
		t.Errorf("Load().Config.Ignore mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadRejectsInvalidIgnorePatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
	}{
		{name: "empty"},
		{name: "negation", pattern: "!keep.tmp"},
		{name: "leading_slash", pattern: "/cache"},
		{name: "native_separator", pattern: `cache\file`},
		{name: "traversal", pattern: "cache/../file"},
		{name: "brace_alternation", pattern: "*.{tmp,log}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			cfg := config.New()
			cfg.Repository = filepath.Join(filepath.Dir(path), "repository")
			cfg.Ignore = []string{test.pattern}
			if err := config.SaveNew(path, cfg); err != nil {
				t.Fatalf("SaveNew(config with ignore %q) returned error: %v", test.pattern, err)
			}

			_, err := config.Load(path)
			var validationErr *config.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Load(config with ignore %q) error = %v, want *config.ValidationError", test.pattern, err)
			}
			if got, want := validationErr.Field, "ignore[0]"; got != want {
				t.Errorf("Load(config with ignore %q) error field = %q, want %q", test.pattern, got, want)
			}
		})
	}
}

func TestLoadRejectsPerPlanIgnorePatterns(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`version: 1
repository: ./repository
plans:
  home:
    name: Home
    enabled: true
    ignore:
      - "*.tmp"
    sources:
      files:
        path: ./files
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}
	if _, err := config.Load(path); err == nil {
		t.Error("Load(config with per-plan ignore) error = nil, want unknown-field error")
	}
}

func TestLoadResolvesAliasesBeforeRelativePaths(t *testing.T) {
	root := t.TempDir()
	physicalDir := filepath.Join(root, "physical")
	aliasDir := filepath.Join(root, "aliases")
	if err := os.MkdirAll(physicalDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(physical) returned error: %v", err)
	}
	if err := os.MkdirAll(aliasDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(aliases) returned error: %v", err)
	}
	physical := filepath.Join(physicalDir, "config.yaml")
	data := []byte("version: 1\nrepository: repo\nplans: {}\n")
	if err := os.WriteFile(physical, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config) returned error: %v", err)
	}
	alias := filepath.Join(aliasDir, "config.yaml")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("config symlinks are unavailable: %v", err)
	}

	loaded, err := config.Load(alias)
	if err != nil {
		t.Fatalf("Load(alias) returned error: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatalf("EvalSymlinks(config) returned error: %v", err)
	}
	if loaded.Path != canonical {
		t.Errorf("Load(alias).Path = %q, want %q", loaded.Path, canonical)
	}
	if got, want := loaded.Config.Repository, filepath.Join(filepath.Dir(canonical), "repo"); got != want {
		t.Errorf("Load(alias).Repository = %q, want %q", got, want)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nunknown: true\nplans: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}
	if _, err := config.Load(path); err == nil {
		t.Error("Load(config with unknown field) error = nil, want decode error")
	}
}

func TestLoadUsesExplicitConfigurationID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	want := "0123456789abcdef0123456789abcdef"
	data := []byte("version: 1\nconfiguration_id: " + want + "\nrepository: ./repository\nplans: {}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(config.yaml) returned error: %v", err)
	}
	if loaded.ID != want {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, want)
	}
}

func TestLoadRejectsRepositoryRecursion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	data := []byte(`version: 1
repository: source/repository
plans:
  home:
    name: Home
    enabled: true
    sources:
      all:
        path: source
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}
	_, err := config.Load(path)
	var validationErr *config.ValidationError
	if !errors.As(err, &validationErr) {
		t.Errorf("Load(recursive repository) error = %v, want *config.ValidationError", err)
	}
}

func TestLoadRejectsReservedSourceID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`version: 1
repository: ./repository
plans:
  home:
    name: Home
    enabled: true
    sources:
      con:
        path: ./source
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}
	if _, err := config.Load(path); err == nil {
		t.Error("Load(config with reserved source ID) error = nil, want portability error")
	}
}

func TestDurationRejectsUnsupportedUnits(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`version: 1
plans:
  home:
    name: Home
    enabled: true
    sources:
      all:
        path: source
    retention:
      keep_for: 3months
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}
	if _, err := config.Load(path); err == nil {
		t.Error("Load(config with 3months) error = nil, want duration error")
	}
}

func TestDurationRejectsCollections(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{
		"sequence": "[90d]",
		"mapping":  "{days: 90}",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			data := []byte("version: 1\nplans:\n  home:\n    name: Home\n    enabled: true\n    sources:\n      all:\n        path: source\n    retention:\n      keep_for: " + value + "\n")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
			}
			if _, err := config.Load(path); err == nil {
				t.Errorf("Load(config with %s duration) error = nil, want duration error", name)
			}
		})
	}
}

func TestLoadUsesPlatformRepositoryWhenOmitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`version: 1
plans: {}
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(config.yaml) returned error: %v", err)
	}
	want, err := config.DefaultRepository()
	if err != nil {
		t.Fatalf("DefaultRepository() returned error: %v", err)
	}
	if loaded.Config.Repository != want {
		t.Errorf("Load().Repository = %q, want %q", loaded.Config.Repository, want)
	}
}

func TestLoadReportsHomeDependentPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`version: 1
repository: ./repository
plans:
  home:
    name: Home
    enabled: true
    sources:
      documents:
        path: ~/Documents
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(config.yaml) returned error: %v", err)
	}
	if !loaded.HomeDependent {
		t.Error("Load().HomeDependent = false, want true")
	}
}

func TestSaveNewDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.SaveNew(path, config.New()); err != nil {
		t.Fatalf("SaveNew(first) returned error: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(first config) returned error: %v", err)
	}
	if err := config.SaveNew(path, config.Config{Version: 99}); err == nil {
		t.Fatal("SaveNew(second) error = nil, want already-exists error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(second config) returned error: %v", err)
	}
	if string(after) != string(before) {
		t.Error("SaveNew(second) changed the existing config")
	}
}
