package parse

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	colonDuration = regexp.MustCompile(`^(\d+):(\d{2})$`)
	plainNumber   = regexp.MustCompile(`^\d+([.,]\d+)?$`)
	unitWord      = regexp.MustCompile(`^(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|u|uur)$`)
	unitDuration  = regexp.MustCompile(`^(?:(\d+(?:[.,]\d+)?)h)?\s*(?:(\d+)m)?$`)
	clock         = regexp.MustCompile(`^(\d{1,2})[:.](\d{2})$`)
)

// MaxDuration is the longest single entry accepted: typos like "45" (hours) or "1e3" stop here.
const MaxDuration = 24 * time.Hour

// Duration parses the ways people write hours: 1:30, 1h30, 1h 30m, 1h30m, 90m, 45 min,
// 1.5, 1.5h, 2h. A plain number means hours, up to 12; anything longer needs a unit.
func Duration(text string) (time.Duration, bool) {
	text = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), " ", ""))
	for long, short := range map[string]string{"minutes": "m", "minute": "m", "mins": "m", "min": "m", "hours": "h", "hour": "h", "hrs": "h", "hr": "h", "uur": "h", "u": "h"} {
		if strings.HasSuffix(text, long) {
			text = strings.TrimSuffix(text, long) + short
			break
		}
	}
	d, ok := duration(text)
	return d, ok && d <= MaxDuration
}

func duration(text string) (time.Duration, bool) {
	if text == "" {
		return 0, false
	}
	if m := colonDuration.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		mins, _ := strconv.Atoi(m[2])
		if mins >= 60 {
			return 0, false
		}
		return time.Duration(h)*time.Hour + time.Duration(mins)*time.Minute, true
	}
	if plainNumber.MatchString(text) {
		n, err := strconv.ParseFloat(strings.ReplaceAll(text, ",", "."), 64)
		return time.Duration(n * float64(time.Hour)).Round(time.Minute), err == nil && n <= 12
	}
	// "1h30" means 1h30m.
	if i := strings.Index(text, "h"); i >= 0 && i < len(text)-1 && !strings.HasSuffix(text, "m") {
		text += "m"
	}
	m := unitDuration.FindStringSubmatch(text)
	if m == nil || (m[1] == "" && m[2] == "") {
		return 0, false
	}
	var d time.Duration
	if m[1] != "" {
		h, _ := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
		d += time.Duration(h * float64(time.Hour))
	}
	if m[2] != "" {
		mins, _ := strconv.Atoi(m[2])
		if m[1] != "" && mins >= 60 { // 2h60 is a typo, not 3h
			return 0, false
		}
		d += time.Duration(mins) * time.Minute
	}
	return d.Round(time.Minute), true
}

// Hours formats a duration the way timesheets show it: 1:30, 0:05, 12:00.
func Hours(d time.Duration) string {
	d = d.Round(time.Minute)
	return fmt.Sprintf("%d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// TimeSpec is a one-line time log: "<duration> [day] [HH:MM] [note…]".
type TimeSpec struct {
	Duration time.Duration
	Day      time.Time // zero when no day was given
	Clock    time.Duration
	HasClock bool
	Note     string
}

// Start is when the entry begins: at the given day/clock, on defaultDay at 09:00 when only
// a day is known, or so that it ends now when neither was given.
func (s TimeSpec) Start(defaultDay, now time.Time) time.Time {
	day := s.Day
	if day.IsZero() {
		day = defaultDay
	}
	switch {
	case s.HasClock:
		return At(day, s.Clock)
	case !s.Day.IsZero() || !defaultDay.IsZero():
		return At(day, 9*time.Hour)
	}
	return now.Add(-s.Duration)
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// At is the wall-clock time clock (e.g. 9h30m) on day. It uses the calendar, not midnight
// plus a duration, so it's right on daylight saving days too.
func At(day time.Time, clock time.Duration) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), int(clock.Hours()), int(clock.Minutes())%60, 0, 0, day.Location())
}

// ClockOf is t's time of day as an offset from midnight, by the wall clock.
func ClockOf(t time.Time) time.Duration {
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

var ErrNoDuration = errors.New("start with a duration, like 1h30, 45m or 1:30")

// Time parses a one-line time log such as "1h30", "45m fixed login", "2h yesterday",
// "1:30 mon 09:00 review". Day words refer to the past: "mon" is the last Monday.
func Time(text string, today time.Time) (TimeSpec, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return TimeSpec{}, ErrNoDuration
	}
	var spec TimeSpec
	// A duration may be split over two words: "45 min", "90 m", "2 hours", "1h 30m", "1h 30".
	joined, text := 1, fields[0]
	if len(fields) > 1 {
		first, second := strings.ToLower(fields[0]), strings.ToLower(fields[1])
		switch {
		case unitWord.MatchString(second):
			joined, text = 2, first+second
		case strings.HasSuffix(first, "h") && plainNumber.MatchString(second):
			joined, text = 2, first+second+"m"
		case strings.HasSuffix(first, "h") && strings.HasSuffix(second, "m"):
			joined, text = 2, first+second
		}
		if _, ok := Duration(text); !ok {
			joined, text = 1, fields[0]
		}
	}
	d, ok := Duration(text)
	if !ok {
		if n, err := strconv.ParseFloat(strings.ReplaceAll(fields[0], ",", "."), 64); err == nil && n > 12 {
			return TimeSpec{}, fmt.Errorf("%s hours? Write %sm for minutes, or %sh to really mean hours", fields[0], fields[0], fields[0])
		}
		return TimeSpec{}, ErrNoDuration
	}
	if d == 0 {
		return TimeSpec{}, errors.New("that's no time at all: give a duration above zero")
	}
	fields = fields[joined-1:]
	spec.Duration = d
	rest := fields[1:]
	for len(rest) > 0 {
		word := strings.ToLower(rest[0])
		if day, ok := pastDay(word, today); ok && spec.Day.IsZero() {
			spec.Day = day
		} else if clock.MatchString(word) && !spec.HasClock {
			c, ok := Clock(word)
			if !ok {
				return TimeSpec{}, fmt.Errorf("%q isn't a time of day", rest[0])
			}
			spec.Clock, spec.HasClock = c, true
		} else if _, ok := futureDay(word, today); ok {
			return TimeSpec{}, fmt.Errorf("%s is in the future: time can only be logged on past days", rest[0])
		} else {
			break
		}
		rest = rest[1:]
	}
	spec.Note = strings.Join(rest, " ")
	return spec, nil
}

// pastDay parses today, yesterday, weekday names (the most recent, today included) and dates.
func pastDay(word string, today time.Time) (time.Time, bool) {
	today = dayStart(today)
	switch word {
	case "today", "tod":
		return today, true
	case "yesterday", "yest", "yd":
		return today.AddDate(0, 0, -1), true
	}
	if len(word) >= 3 {
		if day := slices.Index(weekdays, word[:3]); day >= 0 && strings.HasPrefix(fullWeekday(day), word) {
			back := (int(today.Weekday()) - day + 7) % 7
			return today.AddDate(0, 0, -back), true
		}
	}
	for _, layout := range dateLayout {
		if t, err := time.ParseInLocation(layout, word, today.Location()); err == nil && !t.After(today) {
			return t, true
		}
	}
	if m := dayMonth.FindStringSubmatch(word); m != nil {
		day, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		t := time.Date(today.Year(), time.Month(month), day, 0, 0, 0, 0, today.Location())
		if t.Month() == time.Month(month) {
			if t.After(today) {
				t = t.AddDate(-1, 0, 0)
			}
			return t, true
		}
	}
	return time.Time{}, false
}

// futureDay reports a full date after today: time can't be logged there.
func futureDay(word string, today time.Time) (time.Time, bool) {
	for _, layout := range dateLayout {
		if t, err := time.ParseInLocation(layout, word, today.Location()); err == nil && t.After(dayStart(today)) {
			return t, true
		}
	}
	return time.Time{}, false
}

func fullWeekday(day int) string {
	return strings.ToLower(time.Weekday(day).String())
}

// Day parses a day for a time entry: today, yesterday, a weekday (the most recent one,
// today included), 2026-09-21, 21-09-2026 or 21-09 (day first).
func Day(text string, today time.Time) (time.Time, bool) {
	return pastDay(strings.ToLower(strings.TrimSpace(text)), today)
}

// Clock parses a time of day, 9:00, 09:30 or 13.15, as the offset from midnight.
func Clock(text string) (time.Duration, bool) {
	m := clock.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mins, _ := strconv.Atoi(m[2])
	if h > 23 || mins > 59 {
		return 0, false
	}
	return time.Duration(h)*time.Hour + time.Duration(mins)*time.Minute, true
}
