package schedule

import (
	"fmt"
	"time"

	"github.com/mgomes/ressik/internal/config"
)

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday,
	"mon": time.Monday,
	"tue": time.Tuesday,
	"wed": time.Wednesday,
	"thu": time.Thursday,
	"fri": time.Friday,
	"sat": time.Saturday,
}

// Next returns the first configured occurrence strictly after the given time.
// Nonexistent wall times run at the first valid instant after the gap, and a
// repeated wall time uses its first occurrence.
func Next(schedule config.Schedule, after time.Time) (time.Time, error) {
	if schedule.Kind != "daily" && schedule.Kind != "weekly" {
		return time.Time{}, fmt.Errorf("schedule kind must be daily or weekly, got %q", schedule.Kind)
	}
	location, err := location(schedule.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	clock, err := time.Parse("15:04", schedule.At)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse schedule time %q: %w", schedule.At, err)
	}
	wantedDays := make(map[time.Weekday]bool, len(schedule.Days))
	for _, day := range schedule.Days {
		weekday, ok := weekdays[day]
		if !ok {
			return time.Time{}, fmt.Errorf("unknown weekday %q", day)
		}
		wantedDays[weekday] = true
	}

	localAfter := after.In(location)
	for dayOffset := range 9 {
		day := localAfter.AddDate(0, 0, dayOffset)
		candidate := firstWallTime(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), location)
		if schedule.Kind == "weekly" && !wantedDays[candidate.In(location).Weekday()] {
			continue
		}
		if candidate.After(after) {
			return candidate, nil
		}
	}
	return time.Time{}, errorsNoOccurrence(schedule)
}

func location(name string) (*time.Location, error) {
	if name == "" || name == "Local" || name == "local" {
		return time.Local, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", name, err)
	}
	return location, nil
}

func firstWallTime(year int, month time.Month, day, hour, minute int, location *time.Location) time.Time {
	wantMinutes := hour*60 + minute
	start := time.Date(year, month, day, 0, 0, 0, 0, location)
	for offset := range 26 * 60 {
		candidate := start.Add(time.Duration(offset) * time.Minute)
		local := candidate.In(location)
		if local.Year() != year || local.Month() != month || local.Day() != day {
			break
		}
		minutes := local.Hour()*60 + local.Minute()
		if minutes >= wantMinutes {
			return candidate
		}
	}
	return time.Date(year, month, day, hour, minute, 0, 0, location)
}

func errorsNoOccurrence(schedule config.Schedule) error {
	return fmt.Errorf("schedule %q has no occurrence in the next eight days", schedule.Kind)
}
