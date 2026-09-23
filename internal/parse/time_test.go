package parse

import (
	"testing"
	"time"
)

func TestDuration(t *testing.T) {
	for text, want := range map[string]time.Duration{
		"1:30": 90 * time.Minute, "0:05": 5 * time.Minute, "1h30": 90 * time.Minute, "1h 30m": 90 * time.Minute,
		"1h30m": 90 * time.Minute, "90m": 90 * time.Minute, "1.5": 90 * time.Minute, "1,5h": 90 * time.Minute,
		"2h": 2 * time.Hour, "45m": 45 * time.Minute, "0": 0,
	} {
		if got, ok := Duration(text); !ok || got != want {
			t.Errorf("Duration(%q) = %v, %v; want %v", text, got, ok, want)
		}
	}
	for _, text := range []string{"", "abc", "1:75", "h", "-1"} {
		if got, ok := Duration(text); ok {
			t.Errorf("Duration(%q) = %v, want invalid", text, got)
		}
	}
	if Hours(90*time.Minute) != "1:30" || Hours(5*time.Minute) != "0:05" || Hours(12*time.Hour) != "12:00" {
		t.Error("Hours formats wrong")
	}
}

func TestTimeSpec(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local) // Wednesday
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.Local) }

	s, err := Time("45m fixed the login bug", now)
	if err != nil || s.Duration != 45*time.Minute || s.Note != "fixed the login bug" || !s.Day.IsZero() {
		t.Fatalf("spec = %+v, %v", s, err)
	}
	if got := s.Start(time.Time{}, now); !got.Equal(now.Add(-45 * time.Minute)) {
		t.Errorf("no day: starts %v, want to end now", got)
	}

	s, _ = Time("1h 30m yesterday review", now)
	if s.Duration != 90*time.Minute || !s.Day.Equal(day(22)) || s.Note != "review" {
		t.Fatalf("spec = %+v", s)
	}
	if got := s.Start(time.Time{}, now); !got.Equal(day(22).Add(9 * time.Hour)) {
		t.Errorf("day only: starts %v, want 09:00", got)
	}

	s, _ = Time("1:30 mon 13:15", now)
	if !s.Day.Equal(day(21)) || !s.HasClock || s.Start(time.Time{}, now) != day(21).Add(13*time.Hour+15*time.Minute) {
		t.Fatalf("spec = %+v", s)
	}
	s, _ = Time("2h wed", now) // today is Wednesday: today, not next week
	if !s.Day.Equal(day(23)) {
		t.Fatalf("wed = %v", s.Day)
	}
	s, _ = Time("2h 10:00", now) // a clock on the timesheet's day
	if s.Start(day(20), now) != day(20).Add(10*time.Hour) {
		t.Fatalf("clock on default day = %v", s.Start(day(20), now))
	}
	if _, err := Time("fixed stuff", now); err == nil {
		t.Error("a log without a duration should fail")
	}
}
