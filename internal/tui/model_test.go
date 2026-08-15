package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type fakeBackend struct {
	dashboard Dashboard
	started   []string
}

func (f *fakeBackend) Dashboard(context.Context) (Dashboard, error) {
	return f.dashboard, nil
}

func (f *fakeBackend) StartPlan(planID string) error {
	f.started = append(f.started, planID)
	return nil
}

func (f *fakeBackend) Close() {}

func TestModelSelectsAndStartsPlan(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{dashboard: Dashboard{Plans: []Plan{
		{ID: "documents", Name: "Documents", Enabled: true},
		{ID: "photos", Name: "Photos", Enabled: true},
	}}}
	current := NewModel(backend).(model)
	updated, _ := current.Update(dashboardLoadedMsg{dashboard: backend.dashboard})
	current = updated.(model)
	updated, _ = current.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	current = updated.(model)
	if got, want := current.selected, 1; got != want {
		t.Errorf("Update(down).selected = %d, want %d", got, want)
	}
	updated, command := current.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	current = updated.(model)
	if command == nil {
		t.Fatal("Update(r) command = nil, want start command")
	}
	message := command()
	current.Update(message)
	if got, want := strings.Join(backend.started, ","), "photos"; got != want {
		t.Errorf("Update(r) started plans = %q, want %q", got, want)
	}
}

func TestModelRendersWideAndNarrowLayouts(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{dashboard: Dashboard{
		ConfigPath: "/tmp/atomic/config.yaml",
		Plans: []Plan{{
			ID:        "documents",
			Name:      "Documents",
			Enabled:   true,
			NextRun:   time.Date(2026, time.July, 11, 2, 30, 0, 0, time.Local),
			Retention: "last 14 or 2160h0m0s",
		}},
	}}
	current := NewModel(backend).(model)
	updated, _ := current.Update(dashboardLoadedMsg{dashboard: backend.dashboard})
	current = updated.(model)

	wide := current.View().Content
	if !strings.Contains(wide, "BACKUP PLANS") || !strings.Contains(wide, "RECENT SNAPSHOTS") {
		t.Errorf("wide View() = %q, want plans and snapshot sections", wide)
	}
	updated, _ = current.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	narrow := updated.(model).View().Content
	if !strings.Contains(narrow, "Documents") || !strings.Contains(narrow, "Retention") {
		t.Errorf("narrow View() = %q, want plan details", narrow)
	}
}

func TestModelKeepsPlansAndShowsRefreshError(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{dashboard: Dashboard{Plans: []Plan{{
		ID:      "documents",
		Name:    "Documents",
		Enabled: true,
	}}}}
	current := NewModel(backend).(model)
	updated, _ := current.Update(dashboardLoadedMsg{dashboard: backend.dashboard})
	current = updated.(model)
	updated, _ = current.Update(dashboardLoadedMsg{err: errors.New("config is temporarily unavailable")})

	view := updated.(model).View().Content
	if !strings.Contains(view, "Documents") || !strings.Contains(view, "Refresh failed") {
		t.Errorf("View() = %q, want existing plan and refresh error", view)
	}
}

func TestModelKeepsSelectedPlanVisibleInShortTerminal(t *testing.T) {
	t.Parallel()

	plans := make([]Plan, 20)
	for index := range plans {
		plans[index] = Plan{ID: fmt.Sprintf("plan-%02d", index), Name: fmt.Sprintf("Plan %02d", index)}
	}
	backend := &fakeBackend{dashboard: Dashboard{Plans: plans}}
	current := NewModel(backend).(model)
	updated, _ := current.Update(dashboardLoadedMsg{dashboard: backend.dashboard})
	current = updated.(model)
	updated, _ = current.Update(tea.WindowSizeMsg{Width: 100, Height: 14})
	current = updated.(model)
	updated, _ = current.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	view := updated.(model).View().Content
	if !strings.Contains(view, "Plan 19") {
		t.Errorf("short View() does not contain selected final plan: %q", view)
	}
	if strings.Contains(view, "Plan 00") {
		t.Errorf("short View() still renders first off-screen plan: %q", view)
	}
}
