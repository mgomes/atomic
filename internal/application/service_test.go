package application_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mgomes/ressik/internal/application"
	"github.com/mgomes/ressik/internal/backup"
	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/repository"
)

func TestRunAppliesGlobalIgnorePatterns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatalf("MkdirAll(source/nested) returned error: %v", err)
	}
	if err := os.Mkdir(filepath.Join(source, "cache"), 0o700); err != nil {
		t.Fatalf("Mkdir(source/cache) returned error: %v", err)
	}
	writeApplicationTestFile(t, filepath.Join(source, "keep.txt"), "kept")
	writeApplicationTestFile(t, filepath.Join(source, "root.tmp"), "ignored root file")
	writeApplicationTestFile(t, filepath.Join(source, "nested", "keep.md"), "nested kept")
	writeApplicationTestFile(t, filepath.Join(source, "nested", "skip.tmp"), "ignored nested file")
	writeApplicationTestFile(t, filepath.Join(source, "cache", "value"), "ignored directory")
	explicit := filepath.Join(root, "cache")
	writeApplicationTestFile(t, explicit, "explicit source")

	repo, err := repository.Initialize(filepath.Join(root, "repository"))
	if err != nil {
		t.Fatalf("repository.Initialize() returned error: %v", err)
	}
	cfg := config.New()
	cfg.Repository = repo.Root()
	cfg.Ignore = []string{"*.tmp", "cache"}
	cfg.Plans["documents"] = config.Plan{
		Name:    "Documents",
		Enabled: true,
		Sources: map[string]config.Source{
			"explicit": {Path: explicit},
			"files":    {Path: source},
		},
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := config.SaveNew(configPath, cfg); err != nil {
		t.Fatalf("config.SaveNew() returned error: %v", err)
	}

	service := application.New(configPath)
	first, err := service.Run(context.Background(), "documents")
	if err != nil {
		t.Fatalf("Service.Run() returned error: %v", err)
	}
	full, err := service.RunFull(context.Background(), "documents")
	if err != nil {
		t.Fatalf("Service.RunFull() returned error: %v", err)
	}
	if full.Root != first.Root {
		t.Errorf("RunFull().Root = %s, want incremental root %s", full.Root, first.Root)
	}

	manifest, err := repo.Load(context.Background(), full.ID)
	if err != nil {
		t.Fatalf("Repository.Load() returned error: %v", err)
	}
	gotPaths := make(map[string][]string, len(manifest.Sources))
	for _, captured := range manifest.Sources {
		paths := make([]string, 0, len(captured.Entries))
		for _, entry := range captured.Entries {
			paths = append(paths, entry.Path)
		}
		sort.Strings(paths)
		gotPaths[captured.ID] = paths
	}
	wantPaths := map[string][]string{
		"explicit": {"."},
		"files":    {".", "keep.txt", "nested", "nested/keep.md"},
	}
	if diff := cmp.Diff(wantPaths, gotPaths); diff != "" {
		t.Errorf("captured paths mismatch (-want +got):\n%s", diff)
	}
	if got, want := full.Statistics.Files, 3; got != want {
		t.Errorf("RunFull().Statistics.Files = %d, want %d", got, want)
	}
	if got, want := full.Statistics.Directories, 2; got != want {
		t.Errorf("RunFull().Statistics.Directories = %d, want %d", got, want)
	}
	if _, err := service.Verify(context.Background()); err != nil {
		t.Fatalf("Service.Verify() returned error: %v", err)
	}

	restore := filepath.Join(root, "restore")
	if err := service.Restore(context.Background(), full.ID.String(), backup.RestoreOptions{Destination: restore}); err != nil {
		t.Fatalf("Service.Restore() returned error: %v", err)
	}
	if got, want := applicationTestFileContents(t, filepath.Join(restore, "files", "keep.txt")), "kept"; got != want {
		t.Errorf("restored keep.txt = %q, want %q", got, want)
	}
	if got, want := applicationTestFileContents(t, filepath.Join(restore, "files", "nested", "keep.md")), "nested kept"; got != want {
		t.Errorf("restored nested/keep.md = %q, want %q", got, want)
	}
	if got, want := applicationTestFileContents(t, filepath.Join(restore, "explicit")), "explicit source"; got != want {
		t.Errorf("restored explicit source = %q, want %q", got, want)
	}
	ignored := []string{
		filepath.Join(restore, "files", "root.tmp"),
		filepath.Join(restore, "files", "nested", "skip.tmp"),
		filepath.Join(restore, "files", "cache"),
	}
	for _, path := range ignored {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Lstat(ignored path %q) error = %v, want os.ErrNotExist", path, err)
		}
	}
}

func TestCheckRejectsPhysicalRepositoryOverlap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links requires elevated privileges on some Windows hosts")
	}

	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	source := filepath.Join(physical, "source")
	repositoryPath := filepath.Join(source, "repository")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) returned error: %v", err)
	}
	if _, err := repository.Initialize(repositoryPath); err != nil {
		t.Fatalf("repository.Initialize() returned error: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatalf("Symlink(alias) returned error: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	configData := "version: 1\n" +
		"repository: " + filepath.ToSlash(repositoryPath) + "\n" +
		"plans:\n" +
		"  documents:\n" +
		"    name: Documents\n" +
		"    enabled: true\n" +
		"    sources:\n" +
		"      files:\n" +
		"        path: " + filepath.ToSlash(filepath.Join(alias, "source")) + "\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}

	_, err := application.New(configPath).Check(true)
	if err == nil || !strings.Contains(err.Error(), "physically overlaps") {
		t.Fatalf("Check(true) error = %v, want physical-overlap error", err)
	}
}

func TestCheckRepositoryAllowsOfflineSources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	if _, err := repository.Initialize(repositoryPath); err != nil {
		t.Fatalf("repository.Initialize() returned error: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	configData := "version: 1\n" +
		"repository: ./repository\n" +
		"plans:\n" +
		"  archived:\n" +
		"    name: Archived disk\n" +
		"    enabled: false\n" +
		"    sources:\n" +
		"      files:\n" +
		"        path: ./offline\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}

	if _, err := application.New(configPath).CheckRepository(); err != nil {
		t.Fatalf("CheckRepository() returned error for offline source: %v", err)
	}
	if _, err := application.New(configPath).Check(true); err == nil {
		t.Error("Check(true) error = nil for offline source")
	}
}

func writeApplicationTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", path, err)
	}
}

func applicationTestFileContents(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", path, err)
	}
	return string(data)
}
