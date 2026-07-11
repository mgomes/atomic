package schedule_test

import (
	"testing"
	"time"

	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/schedule"
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
