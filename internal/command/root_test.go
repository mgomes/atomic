package command_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mgomes/ressik/internal/command"
)

func TestCLIUsesRSName(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if got, want := command.New("test", &output, &output).Name, "rs"; got != want {
		t.Errorf("command.New().Name = %q, want %q", got, want)
	}
}

func TestCLIBackupAndRestoreWorkflow(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if output, err := run(t, "--config", configPath, "init"); err != nil {
		t.Fatalf("rs init returned error: %v\n%s", err, output)
	}
	initialized, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(initialized config) returned error: %v", err)
	}
	if !strings.Contains(string(initialized), "configuration_id:") {
		t.Error("rs init config has no persistent configuration_id")
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "hello.txt"), []byte("hello from Ressik"), 0o600); err != nil {
		t.Fatalf("WriteFile(hello.txt) returned error: %v", err)
	}
	yaml := `version: 1
repository: ./repository
plans:
  documents:
    name: Documents
    enabled: true
    sources:
      files:
        path: ./source
    retention:
      keep_last: 3
destinations: {}
`
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}

	if output, err := run(t, "--config", configPath, "check"); err != nil {
		t.Fatalf("rs check returned error: %v\n%s", err, output)
	}
	output, err := run(t, "--config", configPath, "run", "documents")
	if err != nil {
		t.Fatalf("rs run returned error: %v\n%s", err, output)
	}
	fields := strings.Fields(output)
	if len(fields) < 2 || fields[0] != "Snapshot" {
		t.Fatalf("rs run output = %q, want Snapshot <id>", output)
	}
	snapshotID := fields[1]
	fullOutput, err := run(t, "--config", configPath, "run", "--full", "documents")
	if err != nil {
		t.Fatalf("rs run --full returned error: %v\n%s", err, fullOutput)
	}
	fullFields := strings.Fields(fullOutput)
	if len(fullFields) < 2 || fullFields[0] != "Snapshot" {
		t.Fatalf("rs run --full output = %q, want Snapshot <id>", fullOutput)
	}
	fullSnapshotID := fullFields[1]

	if output, err := run(t, "--config", configPath, "snapshots", "documents"); err != nil {
		t.Fatalf("rs snapshots returned error: %v\n%s", err, output)
	} else if !strings.Contains(output, snapshotID) || !strings.Contains(output, fullSnapshotID) {
		t.Errorf("rs snapshots output = %q, want snapshots %s and %s", output, snapshotID, fullSnapshotID)
	}
	if output, err := run(t, "--config", configPath, "verify"); err != nil {
		t.Fatalf("rs verify returned error: %v\n%s", err, output)
	} else if !strings.Contains(output, "Verified 2 snapshots, 1 unique blocks") {
		t.Errorf("rs verify output = %q, want verification summary", output)
	}

	restore := filepath.Join(root, "restore")
	if output, err := run(t, "--config", configPath, "restore", fullSnapshotID, "--to", restore); err != nil {
		t.Fatalf("rs restore returned error: %v\n%s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(restore, "files", "hello.txt"))
	if err != nil {
		t.Fatalf("ReadFile(restored hello.txt) returned error: %v", err)
	}
	if got, want := string(data), "hello from Ressik"; got != want {
		t.Errorf("restored hello.txt = %q, want %q", got, want)
	}
	if output, err := run(t, "--config", configPath, "gc"); err != nil {
		t.Fatalf("rs gc returned error: %v\n%s", err, output)
	} else if !strings.Contains(output, "Removed 0 unreferenced blocks") {
		t.Errorf("rs gc output = %q, want zero-removal summary", output)
	}
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	app := command.New("test", &output, &output)
	err := app.Run(context.Background(), append([]string{"rs"}, args...))
	return output.String(), err
}
