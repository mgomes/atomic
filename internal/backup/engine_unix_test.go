//go:build !windows

package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mgomes/ressik/internal/backup"
	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/repository"
)

func TestBackupCollectsNewBlocksAfterPrecommitFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	validSource := filepath.Join(root, "valid")
	writeTestFile(t, validSource, "unreachable content")
	invalidSource := filepath.Join(root, "invalid")
	if err := os.Mkdir(invalidSource, 0o700); err != nil {
		t.Fatalf("Mkdir(invalid source) returned error: %v", err)
	}
	writeTestFile(t, filepath.Join(invalidSource, `not\portable`), "rejected")
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	plan := config.Plan{
		Name: "Precommit failure",
		Sources: map[string]config.Source{
			"a-valid":   {Path: validSource},
			"z-invalid": {Path: invalidSource},
		},
		Retention: config.Retention{KeepLast: 1},
	}

	_, err := engine.Backup(context.Background(), "test", plan)
	if err == nil || !strings.Contains(err.Error(), "not portable") {
		t.Fatalf("Backup() error = %v, want portability error", err)
	}
	if got := countBlockObjects(t, repo.Root()); got != 0 {
		t.Errorf("failed Backup() left %d block objects, want 0", got)
	}
}

func TestBackupPersistsAndRestoresSymlinkTargetKinds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "target-dir"), 0o700); err != nil {
		t.Fatalf("MkdirAll(source) returned error: %v", err)
	}
	writeTestFile(t, filepath.Join(source, "target-file"), "target")
	links := map[string]string{
		"file-link":   "target-file",
		"dir-link":    "target-dir",
		"broken-link": "missing-target",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(source, name)); err != nil {
			t.Fatalf("Symlink(%q) returned error: %v", name, err)
		}
	}
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))

	summary, err := engine.Backup(context.Background(), "test", testPlan(source, 1))
	if err != nil {
		t.Fatalf("Backup() returned error: %v", err)
	}
	manifest, err := repo.Load(context.Background(), summary.ID)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	entries := make(map[string]repository.Entry, len(manifest.Sources[0].Entries))
	for _, entry := range manifest.Sources[0].Entries {
		entries[entry.Path] = entry
	}
	wantKinds := map[string]repository.SymlinkTargetKind{
		"file-link":   repository.FileSymlinkTarget,
		"dir-link":    repository.DirectorySymlinkTarget,
		"broken-link": repository.UnknownSymlinkTarget,
	}
	for path, want := range wantKinds {
		if got := entries[path].LinkKind; got != want {
			t.Errorf("manifest entry %q target kind = %q, want %q", path, got, want)
		}
	}

	destination := filepath.Join(root, "restore")
	if err := engine.Restore(context.Background(), summary.ID, backup.RestoreOptions{Destination: destination}); err != nil {
		t.Fatalf("Restore() returned error: %v", err)
	}
	for name, want := range links {
		got, err := os.Readlink(filepath.Join(destination, "files", name))
		if err != nil {
			t.Errorf("Readlink(%q) returned error: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("Readlink(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBackupRejectsInvalidUTF8SymlinkTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	invalidTarget := string([]byte{'b', 'a', 'd', 0xff})
	if err := os.Symlink(invalidTarget, source); err != nil {
		t.Fatalf("Symlink(invalid target) returned error: %v", err)
	}
	_, engine := newTestEngine(t, filepath.Join(root, "repository"))

	_, err := engine.Backup(context.Background(), "test", testPlan(source, 1))
	if err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Errorf("Backup() error = %v, want invalid UTF-8 error", err)
	}
}

func TestBackupRejectsPlatformSpecificSymlinkTargets(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"absolute":  "/srv/archive",
		"backslash": `dir\file`,
		"drive":     "C:/archive",
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.Symlink(target, source); err != nil {
				t.Fatalf("Symlink(%q) returned error: %v", target, err)
			}
			_, engine := newTestEngine(t, filepath.Join(root, "repository"))
			_, err := engine.Backup(context.Background(), "test", testPlan(source, 1))
			if err == nil || !strings.Contains(err.Error(), "not portable") {
				t.Errorf("Backup() error = %v, want portable symlink-target error", err)
			}
		})
	}
}

func TestBackupRejectsPhysicalRepositoryOverlap(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	source := filepath.Join(physical, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) returned error: %v", err)
	}
	repoPath := filepath.Join(source, "repository")
	_, engine := newTestEngine(t, repoPath)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatalf("Symlink(alias) returned error: %v", err)
	}
	logicalSource := filepath.Join(alias, "source")

	_, err := engine.Backup(context.Background(), "test", testPlan(logicalSource, 1))
	if err == nil || !strings.Contains(err.Error(), "physically overlaps") {
		t.Errorf("Backup() error = %v, want physical-overlap error", err)
	}
}

func TestBackupRejectsPortableNameCollisions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatalf("Mkdir(source) returned error: %v", err)
	}
	writeTestFile(t, filepath.Join(source, "Report.txt"), "first")
	writeTestFile(t, filepath.Join(source, "report.txt"), "second")
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatalf("ReadDir(source) returned error: %v", err)
	}
	if len(entries) < 2 {
		t.Skip("test filesystem is case-insensitive")
	}
	_, engine := newTestEngine(t, filepath.Join(root, "repository"))

	_, err = engine.Backup(context.Background(), "test", testPlan(source, 1))
	if err == nil || !strings.Contains(err.Error(), "collide") {
		t.Errorf("Backup(case-colliding names) error = %v, want collision error", err)
	}
}

func TestBackupRootIncludesSourceMetadata(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatalf("Mkdir(source) returned error: %v", err)
	}
	writeTestFile(t, filepath.Join(source, "child"), "unchanged")
	_, engine := newTestEngine(t, filepath.Join(root, "repository"))
	plan := testPlan(source, 2)

	first, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(first) returned error: %v", err)
	}
	if err := os.Chmod(source, 0o750); err != nil {
		t.Fatalf("Chmod(source) returned error: %v", err)
	}
	second, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(second) returned error: %v", err)
	}
	if second.Root == first.Root {
		t.Errorf("snapshot roots are both %s after source mode changed, want distinct roots", second.Root)
	}
	if got, want := second.Statistics.ReusedBlocks, 1; got != want {
		t.Errorf("Backup(second).ReusedBlocks = %d, want %d", got, want)
	}
}

func countBlockObjects(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(filepath.Join(root, "blocks"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".block") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(blocks) returned error: %v", err)
	}
	return count
}
