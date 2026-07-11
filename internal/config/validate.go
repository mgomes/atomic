package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mgomes/ressik/internal/fsname"
	"github.com/mgomes/ressik/internal/ignore"
)

var validDays = map[string]bool{
	"mon": true,
	"tue": true,
	"wed": true,
	"thu": true,
	"fri": true,
	"sat": true,
	"sun": true,
}

// ValidationError reports one semantically invalid configuration field.
type ValidationError struct {
	Field   string
	Message string
}

// Error implements error.
func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

// Validate checks the complete resolved configuration.
func (l *Loaded) Validate() error {
	if l.Config.Version != CurrentVersion {
		return invalid("version", "must be %d", CurrentVersion)
	}
	if l.Config.ConfigurationID != "" && !configurationIDPattern.MatchString(l.Config.ConfigurationID) {
		return invalid("configuration_id", "must contain exactly 32 lowercase hexadecimal characters")
	}
	if !filepath.IsAbs(l.Config.Repository) {
		return invalid("repository", "must resolve to an absolute path")
	}
	for index, pattern := range l.Config.Ignore {
		if err := ignore.Validate(pattern); err != nil {
			return invalid(fmt.Sprintf("ignore[%d]", index), "%v", err)
		}
	}
	if len(l.Config.Destinations) > 0 {
		return invalid("destinations", "destination providers are not supported by this build")
	}

	planIDs := make([]string, 0, len(l.Config.Plans))
	for id := range l.Config.Plans {
		planIDs = append(planIDs, id)
	}
	sort.Strings(planIDs)
	for _, planID := range planIDs {
		if err := l.validatePlan(planID, l.Config.Plans[planID]); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loaded) validatePlan(planID string, plan Plan) error {
	prefix := fmt.Sprintf("plans.%s", planID)
	if !idPattern.MatchString(planID) {
		return invalid(prefix, "identifier must match %s", idPattern)
	}
	if strings.TrimSpace(plan.Name) == "" {
		return invalid(prefix+".name", "must not be empty")
	}
	if len(plan.Name) > 256 {
		return invalid(prefix+".name", "must not exceed 256 bytes")
	}
	if len(plan.Sources) == 0 {
		return invalid(prefix+".sources", "must contain at least one source")
	}
	if len(plan.Destinations) > 0 {
		return invalid(prefix+".destinations", "destinations are not supported by this build")
	}
	if plan.Retention.KeepLast < 0 || plan.Retention.KeepFor < 0 {
		return invalid(prefix+".retention", "values must not be negative")
	}
	if plan.Schedule != nil {
		if err := validateSchedule(prefix+".schedule", *plan.Schedule); err != nil {
			return err
		}
	}

	paths := make(map[string]string, len(plan.Sources))
	for sourceID, source := range plan.Sources {
		field := fmt.Sprintf("%s.sources.%s", prefix, sourceID)
		if !idPattern.MatchString(sourceID) {
			return invalid(field, "identifier must match %s", idPattern)
		}
		if err := fsname.Component(sourceID); err != nil {
			return invalid(field, "identifier is not portable: %v", err)
		}
		if strings.TrimSpace(source.Path) == "" {
			return invalid(field+".path", "must not be empty")
		}
		canonical := filepath.Clean(source.Path)
		if other, exists := paths[canonical]; exists {
			return invalid(field+".path", "duplicates source %q", other)
		}
		paths[canonical] = sourceID
		if containsPath(canonical, l.Config.Repository) || containsPath(l.Config.Repository, canonical) {
			return invalid(field+".path", "overlaps the Ressik repository and would recurse")
		}
	}
	return nil
}

func validateSchedule(field string, schedule Schedule) error {
	if schedule.Timezone == "" {
		schedule.Timezone = "Local"
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return invalid(field+".timezone", "load %q: %v", schedule.Timezone, err)
	}
	if _, err := time.Parse("15:04", schedule.At); err != nil {
		return invalid(field+".at", "must be a 24-hour time in HH:MM format")
	}

	switch schedule.Kind {
	case "daily":
		if len(schedule.Days) != 0 {
			return invalid(field+".days", "must be omitted for a daily schedule")
		}
	case "weekly":
		if len(schedule.Days) == 0 {
			return invalid(field+".days", "must contain at least one weekday")
		}
		seen := make(map[string]bool, len(schedule.Days))
		for _, day := range schedule.Days {
			if !validDays[day] {
				return invalid(field+".days", "contains invalid weekday %q", day)
			}
			if seen[day] {
				return invalid(field+".days", "contains duplicate weekday %q", day)
			}
			seen[day] = true
		}
	default:
		return invalid(field+".kind", "must be daily or weekly")
	}
	return nil
}

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)}
}

func containsPath(parent, child string) bool {
	if runtime.GOOS == "windows" {
		parent = strings.ToLower(parent)
		child = strings.ToLower(child)
	}
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
