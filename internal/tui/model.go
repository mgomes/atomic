package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	defaultWidth  = 100
	defaultHeight = 30
	refreshEvery  = 2 * time.Second
)

var errDashboardTimeout = errors.New("dashboard refresh exceeded 10 seconds")

type dashboardLoadedMsg struct {
	dashboard Dashboard
	err       error
}

type runStartedMsg struct {
	err error
}

type tickMsg time.Time

type model struct {
	backend   Backend
	dashboard Dashboard
	selected  int
	width     int
	height    int
	loading   bool
	showHelp  bool
	err       error
}

// NewModel returns a Bubble Tea model for backend.
func NewModel(backend Backend) tea.Model {
	return model{
		backend: backend,
		width:   defaultWidth,
		height:  defaultHeight,
		loading: true,
	}
}

// Run starts the alternate-screen terminal dashboard.
func Run(ctx context.Context, backend Backend) error {
	defer backend.Close()
	program := tea.NewProgram(NewModel(backend), tea.WithContext(ctx))
	if _, err := program.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return fmt.Errorf("run terminal dashboard: %w", err)
	}
	return nil
}

func (m model) Init() tea.Cmd {
	return tea.Batch(loadDashboard(m.backend), tick())
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
	case tea.KeyPressMsg:
		switch message.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected+1 < len(m.dashboard.Plans) {
				m.selected++
			}
		case "g":
			m.selected = 0
		case "G":
			if len(m.dashboard.Plans) > 0 {
				m.selected = len(m.dashboard.Plans) - 1
			}
		case "?":
			m.showHelp = !m.showHelp
		case "R":
			if !m.loading {
				m.loading = true
				return m, loadDashboard(m.backend)
			}
		case "r":
			if len(m.dashboard.Plans) > 0 && !m.dashboard.Plans[m.selected].Running {
				return m, startPlan(m.backend, m.dashboard.Plans[m.selected].ID)
			}
		}
	case dashboardLoadedMsg:
		m.loading = false
		m.err = message.err
		if message.err == nil {
			selectedID := ""
			if m.selected < len(m.dashboard.Plans) {
				selectedID = m.dashboard.Plans[m.selected].ID
			}
			m.dashboard = message.dashboard
			m.selected = selectedIndex(message.dashboard.Plans, selectedID)
		}
	case runStartedMsg:
		m.err = message.err
		m.loading = true
		return m, loadDashboard(m.backend)
	case tickMsg:
		if !m.loading {
			m.loading = true
			return m, tea.Batch(loadDashboard(m.backend), tick())
		}
		return m, tick()
	}
	return m, nil
}

func (m model) View() tea.View {
	content := m.render()
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "Ressik"
	return view
}

func (m model) render() string {
	title := titleStyle.Render("RESSIK")
	subtitle := subtleStyle.Render("encrypted backup console")
	header := title + "  " + subtitle

	var body string
	switch {
	case m.err != nil && len(m.dashboard.Plans) == 0:
		body = errorStyle.Render("Unable to load Ressik") + "\n\n" + m.err.Error()
	case len(m.dashboard.Plans) == 0:
		body = emptyStyle.Render("No backup plans yet") + "\n\n" +
			subtleStyle.Render("Edit "+m.dashboard.ConfigPath+" and press R to reload.")
	default:
		plans := m.renderPlans()
		detail := m.renderDetail(m.dashboard.Plans[m.selected])
		if m.width >= 80 {
			leftWidth := max(26, m.width*34/100)
			plans = lipgloss.NewStyle().Width(leftWidth).Render(plans)
			detail = lipgloss.NewStyle().Width(max(40, m.width-leftWidth-5)).Render(detail)
			body = lipgloss.JoinHorizontal(lipgloss.Top, plans, "   ", detail)
		} else {
			body = plans + "\n\n" + divider(m.width) + "\n\n" + detail
		}
	}
	if m.err != nil && len(m.dashboard.Plans) > 0 {
		body += "\n\n" + errorStyle.Render("Refresh failed: "+m.err.Error())
	}
	if m.dashboard.Warning != "" {
		body += "\n\n" + errorStyle.Render(m.dashboard.Warning)
	}

	footer := m.renderFooter()
	return pageStyle.Width(max(40, m.width-4)).Render(
		header + "\n" + divider(max(30, m.width-4)) + "\n\n" + body + "\n\n" + footer,
	)
}

func (m model) renderPlans() string {
	lines := []string{sectionStyle.Render("BACKUP PLANS")}
	start, end := m.visiblePlans()
	if start > 0 {
		lines = append(lines, subtleStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
	}
	for offset := range end - start {
		index := start + offset
		plan := m.dashboard.Plans[index]
		cursor := "  "
		style := planStyle
		if index == m.selected {
			cursor = "› "
			style = selectedPlanStyle
		}
		status := "ready"
		switch {
		case plan.Running:
			status = "running"
		case plan.LastError != "":
			status = "error"
		case !plan.Enabled:
			status = "paused"
		}
		line := fmt.Sprintf("%s%-20s %s", cursor, truncate(plan.Name, 20), status)
		lines = append(lines, style.Render(line))
	}
	if end < len(m.dashboard.Plans) {
		lines = append(lines, subtleStyle.Render(fmt.Sprintf("  ↓ %d more", len(m.dashboard.Plans)-end)))
	}
	return strings.Join(lines, "\n")
}

func (m model) visiblePlans() (int, int) {
	limit := max(1, m.height-10)
	if m.width < 80 {
		limit = max(1, min(6, (m.height-12)/2))
	}
	if len(m.dashboard.Plans) <= limit {
		return 0, len(m.dashboard.Plans)
	}
	start := max(0, m.selected-limit/2)
	start = min(start, len(m.dashboard.Plans)-limit)
	return start, start + limit
}

func (m model) renderDetail(plan Plan) string {
	lines := []string{sectionStyle.Render(strings.ToUpper(plan.Name))}
	status := "Ready"
	if plan.Running {
		status = "Backup in progress"
	} else if plan.LastError != "" {
		status = "Needs attention"
	} else if !plan.Enabled {
		status = "Automatic backups paused"
	}
	lines = append(lines,
		labelValue("Status", status),
		labelValue("Next run", formatTime(plan.NextRun)),
		labelValue("Last run", formatTime(plan.LastRun)),
		labelValue("Retention", plan.Retention),
	)
	if plan.LastError != "" {
		lines = append(lines, "", errorStyle.Render("Last error: "+plan.LastError))
	}
	lines = append(lines, "", sectionStyle.Render("RECENT SNAPSHOTS"))
	if len(plan.RecentSnapshots) == 0 {
		lines = append(lines, subtleStyle.Render("No completed snapshots"))
	} else {
		for _, snapshot := range plan.RecentSnapshots {
			line := fmt.Sprintf("%s  %s  %d files",
				snapshot.CreatedAt.Local().Format("Jan 02 15:04"),
				snapshot.ID.String()[:10],
				snapshot.Statistics.Files,
			)
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) renderFooter() string {
	if m.showHelp {
		return subtleStyle.Render("↑/k ↓/j select  g/G first/last  r run  R refresh  ? hide help  q quit")
	}
	status := "↑/↓ select   r run   R refresh   ? help   q quit"
	if m.loading {
		status = "Refreshing…   " + status
	}
	return subtleStyle.Render(status)
}

func loadDashboard(backend Backend) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeoutCause(context.Background(), 10*time.Second, errDashboardTimeout)
		defer cancel()
		dashboard, err := backend.Dashboard(ctx)
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = context.Cause(ctx)
		}
		return dashboardLoadedMsg{dashboard: dashboard, err: err}
	}
}

func startPlan(backend Backend, planID string) tea.Cmd {
	return func() tea.Msg {
		return runStartedMsg{err: backend.StartPlan(planID)}
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(now time.Time) tea.Msg { return tickMsg(now) })
}

func selectedIndex(plans []Plan, id string) int {
	for index, plan := range plans {
		if plan.ID == id {
			return index
		}
	}
	return 0
}

func labelValue(label, value string) string {
	return labelStyle.Render(fmt.Sprintf("%-12s", label)) + " " + value
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("Mon Jan 02, 15:04")
}

func truncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func divider(width int) string {
	return subtleStyle.Render(strings.Repeat("─", max(1, width)))
}
