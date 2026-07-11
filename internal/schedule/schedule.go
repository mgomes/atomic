package schedule

import (
	"fmt"
	"math"
	"slices"
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
	parsed, err := parse(schedule)
	if err != nil {
		return time.Time{}, err
	}

	localAfter := after.In(parsed.location)
	for dayOffset := range 9 {
		day := localAfter.AddDate(0, 0, dayOffset)
		candidate := parsed.occurrence(day)
		if !parsed.includes(candidate) {
			continue
		}
		if candidate.After(after) {
			return candidate, nil
		}
	}
	return time.Time{}, errorsNoOccurrence(schedule)
}

// Recent returns the configured occurrences at or before through, ordered from
// oldest to newest.
func Recent(schedule config.Schedule, through time.Time, count int) ([]time.Time, error) {
	if count < 0 {
		return nil, fmt.Errorf("occurrence count must not be negative: %d", count)
	}
	if count == 0 {
		return nil, nil
	}
	if count > (math.MaxInt-1)/7 {
		return nil, fmt.Errorf("occurrence count is too large: %d", count)
	}
	parsed, err := parse(schedule)
	if err != nil {
		return nil, err
	}
	if parsed.kind == "weekly" && len(parsed.wantedDays) == 0 {
		return nil, errorsNoOccurrence(schedule)
	}

	localThrough := through.In(parsed.location)
	recent := make([]time.Time, 0, count)
	for dayOffset := range count*7 + 1 {
		day := localThrough.AddDate(0, 0, -dayOffset)
		candidate := parsed.occurrence(day)
		if !parsed.includes(candidate) || candidate.After(through) {
			continue
		}
		recent = append(recent, candidate)
		if len(recent) == count {
			break
		}
	}
	if len(recent) != count {
		return nil, errorsNoOccurrence(schedule)
	}
	slices.Reverse(recent)
	return recent, nil
}

type parsedSchedule struct {
	kind       string
	location   *time.Location
	clock      time.Time
	wantedDays map[time.Weekday]bool
}

func parse(schedule config.Schedule) (parsedSchedule, error) {
	if schedule.Kind != "daily" && schedule.Kind != "weekly" {
		return parsedSchedule{}, fmt.Errorf("schedule kind must be daily or weekly, got %q", schedule.Kind)
	}
	location, err := location(schedule.Timezone)
	if err != nil {
		return parsedSchedule{}, err
	}
	clock, err := time.Parse("15:04", schedule.At)
	if err != nil {
		return parsedSchedule{}, fmt.Errorf("parse schedule time %q: %w", schedule.At, err)
	}
	wantedDays := make(map[time.Weekday]bool, len(schedule.Days))
	for _, day := range schedule.Days {
		weekday, ok := weekdays[day]
		if !ok {
			return parsedSchedule{}, fmt.Errorf("unknown weekday %q", day)
		}
		wantedDays[weekday] = true
	}
	return parsedSchedule{
		kind:       schedule.Kind,
		location:   location,
		clock:      clock,
		wantedDays: wantedDays,
	}, nil
}

func (s parsedSchedule) occurrence(day time.Time) time.Time {
	return firstWallTime(
		day.Year(),
		day.Month(),
		day.Day(),
		s.clock.Hour(),
		s.clock.Minute(),
		s.location,
	)
}

func (s parsedSchedule) includes(candidate time.Time) bool {
	return s.kind != "weekly" || s.wantedDays[candidate.In(s.location).Weekday()]
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
