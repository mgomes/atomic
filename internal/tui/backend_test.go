package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mgomes/ressik/internal/application"
	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/repository"
)

func TestControllerRejectsJobsAfterClose(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	cfg := config.Config{
		Version:    config.CurrentVersion,
		Repository: filepath.Join(root, "repository"),
		Plans: map[string]config.Plan{
			"documents": {
				Name:    "Documents",
				Enabled: true,
				Sources: map[string]config.Source{
					"files": {Path: filepath.Join(root, "source")},
				},
			},
		},
	}
	if err := config.SaveNew(configPath, cfg); err != nil {
		t.Fatalf("SaveNew() returned error: %v", err)
	}
	controller := NewController(application.New(configPath), filepath.Join(root, "state"))
	controller.Close()

	if err := controller.StartPlan("documents"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("StartPlan() error = %v, want closed error", err)
	}
}

func TestControllerKeepsDashboardUsableWithCorruptScheduleState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	if _, err := repository.Initialize(repositoryPath); err != nil {
		t.Fatalf("repository.Initialize() returned error: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	sourcePath := filepath.Join(root, "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatalf("Mkdir(source) returned error: %v", err)
	}
	cfg := config.Config{
		Version:    config.CurrentVersion,
		Repository: repositoryPath,
		Plans: map[string]config.Plan{
			"documents": {
				Name:    "Documents",
				Enabled: true,
				Sources: map[string]config.Source{"files": {Path: sourcePath}},
			},
		},
	}
	if err := config.SaveNew(configPath, cfg); err != nil {
		t.Fatalf("SaveNew() returned error: %v", err)
	}
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("Mkdir(state) returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "daemon-state.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile(state) returned error: %v", err)
	}
	controller := NewController(application.New(configPath), stateDir)
	defer controller.Close()

	dashboard, err := controller.Dashboard(context.Background())
	if err != nil {
		t.Fatalf("Dashboard() returned error: %v", err)
	}
	if got, want := len(dashboard.Plans), 1; got != want {
		t.Fatalf("Dashboard() plan count = %d, want %d", got, want)
	}
	if !strings.Contains(dashboard.Warning, "daemon will rebuild") {
		t.Errorf("Dashboard().Warning = %q, want rebuild warning", dashboard.Warning)
	}
}
