package tui

import "charm.land/lipgloss/v2"

var (
	green    = lipgloss.Color("#73D7A2")
	ink      = lipgloss.Color("#E7ECE9")
	muted    = lipgloss.Color("#87948D")
	danger   = lipgloss.Color("#FF8A7A")
	selected = lipgloss.Color("#19392C")

	pageStyle = lipgloss.NewStyle().
			Foreground(ink).
			Padding(1, 2)
	titleStyle = lipgloss.NewStyle().
			Foreground(green).
			Bold(true)
	sectionStyle = lipgloss.NewStyle().
			Foreground(muted).
			Bold(true).
			MarginBottom(1)
	subtleStyle       = lipgloss.NewStyle().Foreground(muted)
	errorStyle        = lipgloss.NewStyle().Foreground(danger)
	emptyStyle        = lipgloss.NewStyle().Foreground(green).Bold(true)
	labelStyle        = lipgloss.NewStyle().Foreground(muted)
	planStyle         = lipgloss.NewStyle().Padding(0, 1)
	selectedPlanStyle = lipgloss.NewStyle().
				Foreground(green).
				Background(selected).
				Bold(true).
				Padding(0, 1)
)
