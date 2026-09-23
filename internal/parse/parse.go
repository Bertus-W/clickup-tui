// Package parse turns user input into task references and dates.
package parse

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TaskRef accepts a task id, custom id or app.clickup.com/t/... URL.
func TaskRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if _, after, ok := strings.Cut(ref, "/t/"); ok {
		after, _, _ = strings.Cut(after, "?")
		parts := strings.Split(strings.Trim(after, "/"), "/")
		return parts[len(parts)-1]
	}
	if strings.Contains(ref, "/") { // a URL or path without a task in it
		return ""
	}
	ref, _, _ = strings.Cut(ref, "?")
	return ref
}

type DueKind int

const (
	DueInvalid DueKind = iota
	DueClear
	DueDate
)

var (
	relative   = regexp.MustCompile(`^\+?(\d+)\s*([dw])$`)
	dayMonth   = regexp.MustCompile(`^(\d{1,2})[-/](\d{1,2})$`) // 31-10: day first, like 31-10-2026
	weekdays   = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	dateLayout = []string{"2006-01-02", "2006/01/02", "02-01-2006"}
)

// Due parses today, tomorrow, +3d, 2w, weekday names (the next one), YYYY-MM-DD,
// DD-MM-YYYY, DD-MM (next occurrence) and none/clear/-.
func Due(text string, today time.Time) (time.Time, DueKind) {
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	text = strings.ToLower(strings.TrimSpace(text))
	switch text {
	case "none", "clear", "-", "no":
		return time.Time{}, DueClear
	case "today", "tod":
		return today, DueDate
	case "tomorrow", "tom", "tmr":
		return today.AddDate(0, 0, 1), DueDate
	}
	if m := relative.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		if m[2] == "w" {
			n *= 7
		}
		return today.AddDate(0, 0, n), DueDate
	}
	if len(text) >= 3 {
		if day := slices.Index(weekdays, text[:3]); day >= 0 && strings.HasPrefix(fullWeekday(day), text) {
			ahead := (day - int(today.Weekday()) + 7) % 7
			if ahead == 0 {
				ahead = 7
			}
			return today.AddDate(0, 0, ahead), DueDate
		}
	}
	for _, layout := range dateLayout {
		if t, err := time.ParseInLocation(layout, text, today.Location()); err == nil {
			return t, DueDate
		}
	}
	if m := dayMonth.FindStringSubmatch(text); m != nil {
		day, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		if month < 1 || month > 12 || day < 1 || day > 31 {
			return time.Time{}, DueInvalid
		}
		t := time.Date(today.Year(), time.Month(month), day, 0, 0, 0, 0, today.Location())
		if t.Month() != time.Month(month) {
			return time.Time{}, DueInvalid // e.g. 02-31
		}
		if t.Before(today) {
			t = t.AddDate(1, 0, 0)
		}
		return t, DueDate
	}
	return time.Time{}, DueInvalid
}

// Noon returns the timestamp ClickUp should store for a date-only value.
// Noon keeps the date stable across time zones.
func Noon(day time.Time) int64 {
	return time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, day.Location()).UnixMilli()
}
