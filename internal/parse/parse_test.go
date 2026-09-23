package parse

import (
	"testing"
	"time"
)

func TestTaskRef(t *testing.T) {
	for ref, want := range map[string]string{
		"abc123":                           "abc123",
		"https://app.clickup.com/t/abc123": "abc123",
		"https://app.clickup.com/t/9000/DEV-12?x=1": "DEV-12",
		"  https://app.clickup.com/t/86abc/  ":      "86abc",
		"https://app.clickup.com/t/":                "",
		"https://app.clickup.com/9000/home":         "",
	} {
		if got := TaskRef(ref); got != want {
			t.Errorf("TaskRef(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestDue(t *testing.T) {
	today := time.Date(2026, 9, 23, 15, 4, 0, 0, time.Local) // a Wednesday
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	for text, want := range map[string]time.Time{
		"today":      day(2026, 9, 23),
		"tomorrow":   day(2026, 9, 24),
		"+3d":        day(2026, 9, 26),
		"2w":         day(2026, 10, 7),
		"fri":        day(2026, 9, 25),
		"wed":        day(2026, 9, 30), // today is Wednesday: next week's
		"2026-12-01": day(2026, 12, 1),
		"15-01":      day(2027, 1, 15), // day first, like 15-01-2027
	} {
		got, kind := Due(text, today)
		if kind != DueDate || !got.Equal(want) {
			t.Errorf("Due(%q) = %v %v, want %v", text, got, kind, want)
		}
	}
	for text, want := range map[string]DueKind{"none": DueClear, "-": DueClear, "gibberish": DueInvalid, "02-31": DueInvalid} {
		if _, kind := Due(text, today); kind != want {
			t.Errorf("Due(%q) kind = %v, want %v", text, kind, want)
		}
	}
}
