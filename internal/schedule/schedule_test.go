package schedule_test

import (
	"testing"
	"time"

	"github.com/mgomes/atomic/internal/config"
	"github.com/mgomes/atomic/internal/schedule"
)

func TestNextDaily(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, time.July, 10, 3, 0, 0, 0, time.UTC)
	got, err := schedule.Next(config.Schedule{Kind: "daily", At: "02:30", Timezone: "UTC"}, after)
	if err != nil {
		t.Fatalf("Next(daily) returned error: %v", err)
	}
	want := time.Date(2026, time.July, 11, 2, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next(daily) = %s, want %s", got, want)
	}
}

func TestNextWeekly(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, time.July, 10, 3, 0, 0, 0, time.UTC)
	got, err := schedule.Next(config.Schedule{
		Kind:     "weekly",
		Days:     []string{"wed", "sun"},
		At:       "02:30",
		Timezone: "UTC",
	}, after)
	if err != nil {
		t.Fatalf("Next(weekly) returned error: %v", err)
	}
	want := time.Date(2026, time.July, 12, 2, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next(weekly) = %s, want %s", got, want)
	}
}

func TestNextUsesFirstValidInstantAfterDSTGap(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("America/Detroit")
	if err != nil {
		t.Fatalf("LoadLocation(America/Detroit) returned error: %v", err)
	}
	after := time.Date(2026, time.March, 7, 12, 0, 0, 0, location)
	got, err := schedule.Next(config.Schedule{
		Kind:     "daily",
		At:       "02:30",
		Timezone: "America/Detroit",
	}, after)
	if err != nil {
		t.Fatalf("Next(DST gap) returned error: %v", err)
	}
	local := got.In(location)
	if local.Day() != 8 || local.Hour() != 3 || local.Minute() != 0 {
		t.Errorf("Next(DST gap) = %s, want 2026-03-08 03:00 local", local)
	}
}

func TestRecentDailyReturnsOldestToNewest(t *testing.T) {
	t.Parallel()

	through := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	got, err := schedule.Recent(
		config.Schedule{Kind: "daily", At: "02:30", Timezone: "UTC"},
		through,
		3,
	)
	if err != nil {
		t.Fatalf("Recent(daily) returned error: %v", err)
	}
	want := []time.Time{
		time.Date(2026, time.July, 9, 2, 30, 0, 0, time.UTC),
		time.Date(2026, time.July, 10, 2, 30, 0, 0, time.UTC),
		time.Date(2026, time.July, 11, 2, 30, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("len(Recent(daily)) = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if !got[index].Equal(want[index]) {
			t.Errorf("Recent(daily)[%d] = %s, want %s", index, got[index], want[index])
		}
	}
}

func TestRecentWeeklyExcludesFutureOccurrence(t *testing.T) {
	t.Parallel()

	through := time.Date(2026, time.July, 12, 1, 0, 0, 0, time.UTC)
	got, err := schedule.Recent(config.Schedule{
		Kind:     "weekly",
		Days:     []string{"wed", "sun"},
		At:       "02:30",
		Timezone: "UTC",
	}, through, 3)
	if err != nil {
		t.Fatalf("Recent(weekly) returned error: %v", err)
	}
	want := []time.Time{
		time.Date(2026, time.July, 1, 2, 30, 0, 0, time.UTC),
		time.Date(2026, time.July, 5, 2, 30, 0, 0, time.UTC),
		time.Date(2026, time.July, 8, 2, 30, 0, 0, time.UTC),
	}
	for index := range want {
		if !got[index].Equal(want[index]) {
			t.Errorf("Recent(weekly)[%d] = %s, want %s", index, got[index], want[index])
		}
	}
}

func TestRecentUsesDSTScheduleSemantics(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("America/Detroit")
	if err != nil {
		t.Fatalf("LoadLocation(America/Detroit) returned error: %v", err)
	}
	daily := config.Schedule{
		Kind:     "daily",
		At:       "02:30",
		Timezone: "America/Detroit",
	}
	got, err := schedule.Recent(
		daily,
		time.Date(2026, time.March, 9, 12, 0, 0, 0, location),
		3,
	)
	if err != nil {
		t.Fatalf("Recent(DST gap) returned error: %v", err)
	}
	gap := got[1].In(location)
	if gap.Day() != 8 || gap.Hour() != 3 || gap.Minute() != 0 {
		t.Errorf("Recent(DST gap)[1] = %s, want 2026-03-08 03:00 local", gap)
	}

	repeated := config.Schedule{
		Kind:     "daily",
		At:       "01:30",
		Timezone: "America/Detroit",
	}
	got, err = schedule.Recent(
		repeated,
		time.Date(2026, time.November, 2, 12, 0, 0, 0, location),
		2,
	)
	if err != nil {
		t.Fatalf("Recent(repeated wall time) returned error: %v", err)
	}
	_, offset := got[0].In(location).Zone()
	if want := -4 * 60 * 60; offset != want {
		t.Errorf("Recent(repeated wall time) offset = %d, want %d", offset, want)
	}
}
