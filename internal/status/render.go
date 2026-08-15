package status

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"

	"github.com/mgomes/atomic/internal/daemon"
)

const (
	_timeFormat    = "2006-01-02 15:04 MST"
	_maxErrorRunes = 160
)

var (
	_successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#73D7A2")).Bold(true)
	_failureStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8A7A")).Bold(true)
	_unknownStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#87948D"))
)

// Render returns a color-styled status report in the user's local timezone.
// Callers should pass it through a terminal-aware writer before display.
func Render(report Report) string {
	return render(report, time.Local)
}

func render(report Report, location *time.Location) string {
	var builder strings.Builder
	if report.Warning != "" {
		builder.WriteString("Warning  ")
		builder.WriteString(cleanText(report.Warning))
		builder.WriteString("\n\n")
	}
	if len(report.Plans) == 0 {
		builder.WriteString("No backup plans configured")
		return builder.String()
	}

	for index, plan := range report.Plans {
		if index > 0 {
			builder.WriteByte('\n')
		}
		writePlan(&builder, plan, location)
	}
	builder.WriteByte('\n')
	builder.WriteString(marker(daemon.RunSucceeded))
	builder.WriteString(" succeeded  ")
	builder.WriteString(marker(daemon.RunFailed))
	builder.WriteString(" failed  ")
	builder.WriteString(marker(daemon.RunUnknown))
	builder.WriteString(" missing/unknown")
	return builder.String()
}

func writePlan(builder *strings.Builder, plan Plan, location *time.Location) {
	latest := daemon.RunUnknown
	if len(plan.Runs) > 0 {
		latest = plan.Runs[len(plan.Runs)-1].Outcome
	}
	builder.WriteString(marker(latest))
	builder.WriteByte(' ')
	name := cleanText(plan.Name)
	if name == "" {
		name = plan.ID
	}
	builder.WriteString(name)
	builder.WriteString(" (")
	builder.WriteString(plan.ID)
	builder.WriteString(")\n")

	markers := make([]string, len(plan.Runs))
	for index, run := range plan.Runs {
		markers[index] = marker(run.Outcome)
	}
	writeDetail(builder, "Runs", strings.Join(markers, " ")+"   oldest → newest")

	last, hasLast := lastCompleted(plan.Runs)
	if hasLast {
		writeDetail(
			builder,
			"Last",
			last.CompletedAt.In(location).Format(_timeFormat)+"   "+string(last.Outcome),
		)
	} else {
		writeDetail(builder, "Last", "No completed scheduled runs")
	}

	switch {
	case !plan.Enabled:
		writeDetail(builder, "Schedule", "Automatic backups disabled")
	case !plan.HasSchedule:
		writeDetail(builder, "Schedule", "No schedule configured")
	case !plan.RetryAt.IsZero():
		writeDetail(builder, "Retry", plan.RetryAt.In(location).Format(_timeFormat))
	case !plan.PendingRun.IsZero():
		writeDetail(builder, "Pending", plan.PendingRun.In(location).Format(_timeFormat))
	case !plan.NextRun.IsZero():
		writeDetail(builder, "Next", plan.NextRun.In(location).Format(_timeFormat))
	}
	if hasLast && last.Outcome == daemon.RunFailed && last.Error != "" {
		writeDetail(builder, "Error", truncate(cleanText(last.Error), _maxErrorRunes))
	}
}

func writeDetail(builder *strings.Builder, label, value string) {
	builder.WriteString(fmt.Sprintf("  %-10s%s\n", label, value))
}

func lastCompleted(runs []daemon.ScheduledRun) (daemon.ScheduledRun, bool) {
	for offset := range len(runs) {
		index := len(runs) - 1 - offset
		if runs[index].Outcome != daemon.RunUnknown {
			return runs[index], true
		}
	}
	return daemon.ScheduledRun{}, false
}

func marker(outcome daemon.RunOutcome) string {
	switch outcome {
	case daemon.RunSucceeded:
		return _successStyle.Render("●")
	case daemon.RunFailed:
		return _failureStyle.Render("×")
	default:
		return _unknownStyle.Render("·")
	}
}

func cleanText(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func truncate(value string, limit int) string {
	characters := []rune(value)
	if len(characters) <= limit {
		return value
	}
	return string(characters[:limit-1]) + "…"
}
