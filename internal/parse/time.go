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
	colonDuration = regexp.MustCompile(`^(\d+):(\d{1,2})$`)
	unitDuration  = regexp.MustCompile(`^(?:(\d+(?:[.,]\d+)?)h)?\s*(?:(\d+)m)?$`)
	clock         = regexp.MustCompile(`^(\d{1,2})[:.](\d{2})$`)
)

// Duration parses the ways people write hours: 1:30, 1h30, 1h 30m, 1h30m, 90m, 1.5, 1.5h, 2h.
// A plain number means hours.
func Duration(text string) (time.Duration, bool) {
	text = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), " ", ""))
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
	if n, err := strconv.ParseFloat(strings.ReplaceAll(text, ",", "."), 64); err == nil {
		return time.Duration(n * float64(time.Hour)).Round(time.Minute), n >= 0
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
		return dayStart(day).Add(s.Clock)
	case !s.Day.IsZero() || !defaultDay.IsZero():
		return dayStart(day).Add(9 * time.Hour)
	}
	return now.Add(-s.Duration)
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
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
	d, ok := Duration(fields[0])
	// Allow "1h 30m" split over two words.
	if ok && len(fields) > 1 && strings.HasSuffix(strings.ToLower(fields[1]), "m") {
		if d2, ok2 := Duration(fields[0] + fields[1]); ok2 && strings.HasSuffix(strings.ToLower(fields[0]), "h") {
			d, fields = d2, slices.Delete(slices.Clone(fields), 1, 2)
		}
	}
	if !ok {
		return TimeSpec{}, ErrNoDuration
	}
	spec.Duration = d
	rest := fields[1:]
	for len(rest) > 0 {
		word := strings.ToLower(rest[0])
		if day, ok := pastDay(word, today); ok && spec.Day.IsZero() {
			spec.Day = day
		} else if m := clock.FindStringSubmatch(word); m != nil && !spec.HasClock {
			h, _ := strconv.Atoi(m[1])
			mins, _ := strconv.Atoi(m[2])
			if h > 23 || mins > 59 {
				return TimeSpec{}, fmt.Errorf("%q isn't a time of day", rest[0])
			}
			spec.Clock, spec.HasClock = time.Duration(h)*time.Hour+time.Duration(mins)*time.Minute, true
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
		if t, err := time.ParseInLocation(layout, word, today.Location()); err == nil {
			return t, true
		}
	}
	if m := monthDay.FindStringSubmatch(word); m != nil {
		month, _ := strconv.Atoi(m[1])
		day, _ := strconv.Atoi(m[2])
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

func fullWeekday(day int) string {
	return strings.ToLower(time.Weekday(day).String())
}
