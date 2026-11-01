package render

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/parse"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// EditableFieldTypes are the custom field types the TUI can edit.
var EditableFieldTypes = map[string]bool{
	"drop_down": true, "labels": true, "checkbox": true, "users": true, "date": true,
	"emoji": true, "manual_progress": true, "number": true, "currency": true,
	"text": true, "short_text": true, "url": true, "email": true, "phone": true,
}

// Options returns a field's dropdown/label options in display order.
func Options(f *clickup.CustomField) []clickup.Option {
	return slices.SortedStableFunc(slices.Values(f.TypeConfig.Options), func(a, b clickup.Option) int {
		return cmp.Compare(a.OrderIndex, b.OrderIndex)
	})
}

// scalar decodes a JSON scalar to its string form ("1", "abc", "true").
func scalar(raw json.RawMessage) string {
	var s clickup.FlexString
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return string(s)
}

// OptionFor finds an option by id, or by orderindex (what the API returns for dropdowns).
func OptionFor(f *clickup.CustomField, value string) (clickup.Option, bool) {
	if value == "" {
		return clickup.Option{}, false
	}
	for _, o := range Options(f) {
		if o.ID == value || strconv.FormatFloat(float64(o.OrderIndex), 'f', -1, 64) == value {
			return o, true
		}
	}
	return clickup.Option{}, false
}

// DropdownValue is the selected option of a dropdown field.
func DropdownValue(f *clickup.CustomField) (clickup.Option, bool) {
	if !f.IsSet() {
		return clickup.Option{}, false
	}
	return OptionFor(f, scalar(f.Value))
}

// LabelValues returns the selected option ids of a labels field.
func LabelValues(f *clickup.CustomField) []string {
	var ids []string
	_ = json.Unmarshal(f.Value, &ids)
	return ids
}

// UserValues returns the people in a users field.
func UserValues(f *clickup.CustomField) []clickup.User {
	var users []clickup.User
	_ = json.Unmarshal(f.Value, &users)
	return users
}

func Checked(raw json.RawMessage) bool {
	switch scalar(raw) {
	case "true", "1":
		return true
	}
	return false
}

type progress struct {
	Current          clickup.FlexFloat `json:"current"`
	PercentCompleted clickup.FlexFloat `json:"percent_completed"`
}

// FieldText renders a field value, coloured like ClickUp.
func FieldText(f *clickup.CustomField) string {
	if !f.IsSet() {
		return style.Dim("—")
	}
	switch f.Type {
	case "drop_down":
		if o, ok := DropdownValue(f); ok {
			return style.Chip(o.Title(), o.Color)
		}
		return style.Dim(scalar(f.Value))
	case "labels":
		var chips []string
		for _, id := range LabelValues(f) {
			if o, ok := OptionFor(f, id); ok {
				chips = append(chips, style.Chip(o.Title(), o.Color))
			} else {
				chips = append(chips, style.Chip(id, ""))
			}
		}
		return strings.Join(chips, " ")
	case "checkbox":
		if Checked(f.Value) {
			return style.Green("✓ yes")
		}
		return style.Dim("✗ no")
	case "users":
		names := make([]string, 0)
		for _, u := range UserValues(f) {
			names = append(names, u.Username)
		}
		return strings.Join(names, ", ")
	case "date":
		if t, ok := Millis(clickup.FlexString(scalar(f.Value))); ok {
			return t.Format("2006-01-02")
		}
	case "emoji":
		count := cmp.Or(f.TypeConfig.Count, 5)
		n, _ := strconv.Atoi(scalar(f.Value))
		n = min(max(n, 0), count)
		return style.Yellow(strings.Repeat("★", n) + strings.Repeat("☆", count-n))
	case "manual_progress", "automatic_progress":
		var p progress
		if json.Unmarshal(f.Value, &p) == nil {
			return fmt.Sprintf("%.0f%%", float64(p.PercentCompleted))
		}
	case "currency":
		return strings.TrimSpace(scalar(f.Value) + " " + f.TypeConfig.CurrencyType)
	case "tasks":
		var tasks []struct{ Name string }
		_ = json.Unmarshal(f.Value, &tasks)
		names := make([]string, len(tasks))
		for i, t := range tasks {
			names[i] = t.Name
		}
		return strings.Join(names, ", ")
	case "location":
		var loc struct {
			Address string `json:"formatted_address"`
		}
		_ = json.Unmarshal(f.Value, &loc)
		return loc.Address
	}
	if s := scalar(f.Value); s != "" {
		return s
	}
	return string(f.Value)
}

// FieldRow is one row of the task panel's fields block: a name and its (styled) value. A value
// may span lines; they line up under the first.
type FieldRow struct{ Name, Value string }

// StandardFields are ClickUp's own task fields that the panel's header doesn't show, in a
// fixed order, each only when set.
func StandardFields(t *clickup.Task) []FieldRow {
	var rows []FieldRow
	add := func(name, value string) {
		if value != "" {
			rows = append(rows, FieldRow{name, value})
		}
	}
	day := func(ms clickup.FlexString) string {
		if at, ok := Millis(ms); ok {
			return at.Format(time.DateOnly)
		}
		return ""
	}
	names := func(users []clickup.User) string {
		out := make([]string, len(users))
		for i, u := range users {
			out[i] = u.Username
		}
		return strings.Join(out, ", ")
	}
	add("Start date", day(t.StartDate))
	if created := day(t.DateCreated); created != "" && t.Creator != nil && t.Creator.Username != "" {
		add("Created", created+style.Dim(" by ")+t.Creator.Username)
	} else {
		add("Created", created)
	}
	add("Closed", day(cmp.Or(t.DateClosed, t.DateDone)))
	add("Estimate", EstimateText(t))
	if spent := t.TimeSpent.Int(); spent > 0 {
		add("Tracked", parse.Hours(time.Duration(spent)*time.Millisecond))
	}
	if t.Points != 0 {
		add("Points", strconv.FormatFloat(float64(t.Points), 'f', -1, 64))
	}
	add("Watchers", names(t.Watchers))
	var groups []string
	for _, g := range t.GroupAssignees {
		groups = append(groups, g.Name)
	}
	add("Groups", strings.Join(groups, ", "))
	add("Subtask of", string(t.Parent))
	var lists []string
	for _, l := range t.Locations {
		lists = append(lists, l.Name)
	}
	add("Also in", strings.Join(lists, ", "))
	if !t.Folder.Hidden {
		add("Folder", t.Folder.Name)
	}
	var linked []string
	for _, l := range t.LinkedTasks {
		other := l.TaskID // a link lists both tasks: show the other one
		if other == t.ID {
			other = l.LinkID
		}
		linked = append(linked, other)
	}
	add("Linked", strings.Join(linked, ", "))
	for _, c := range t.Checklists {
		done := 0
		lines := []string{""}
		for _, it := range c.Items {
			if it.Resolved {
				done++
				lines = append(lines, style.Green("[✓]")+" "+style.Dim(it.Name))
			} else {
				lines = append(lines, "[ ] "+it.Name)
			}
		}
		lines[0] = style.Dim(fmt.Sprintf("%d/%d done", done, len(c.Items)))
		add(cmp.Or(c.Name, "Checklist"), strings.Join(lines, "\n"))
	}
	var files []string
	for _, a := range t.Attachments {
		name := cmp.Or(a.Title, linkName(a.URL))
		files = append(files, style.Link(a.URL, style.LinkText(name)))
	}
	add("Attachments", strings.Join(files, "\n"))
	if t.Archived {
		add("Archived", style.Yellow("yes"))
	}
	if t.Sharing.Public {
		add("Public", "shared publicly")
	}
	return rows
}

// FieldsBlock lists the standard fields that are set, then every custom field (sorted by name)
// with its value, names aligned. Empty when there's nothing to list.
func FieldsBlock(t *clickup.Task) string {
	rows := StandardFields(t)
	for _, f := range SortedFields(t.CustomFields) {
		rows = append(rows, FieldRow{f.Name, FieldText(f)})
	}
	width := 0
	for _, r := range rows {
		width = max(width, min(24, style.Width(r.Name)))
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(style.Dim(style.Cell{Text: r.Name}.Fit(width)))
		b.WriteString("  ")
		b.WriteString(strings.ReplaceAll(r.Value, "\n", "\n"+strings.Repeat(" ", width+2)))
		b.WriteString("\n")
	}
	return b.String()
}

// SortedFields returns pointers to the fields sorted by name.
func SortedFields(fields []clickup.CustomField) []*clickup.CustomField {
	out := make([]*clickup.CustomField, len(fields))
	for i := range fields {
		out[i] = &fields[i]
	}
	slices.SortStableFunc(out, func(a, b *clickup.CustomField) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Hotkeys gives each label a unique single-letter key, preferring its earliest unused letter.
func Hotkeys(labels []string, reserved string) []rune {
	used := map[rune]bool{}
	for _, r := range reserved {
		used[r] = true
	}
	keys := make([]rune, len(labels))
	for i, label := range labels {
		var key rune
		for _, r := range strings.ToLower(label) {
			if unicode.IsLetter(r) && r < unicode.MaxASCII && !used[r] {
				key = r
				break
			}
		}
		if key == 0 {
			for _, r := range "abcdefghilmnoprstuvwxyz0123456789" {
				if !used[r] {
					key = r
					break
				}
			}
		}
		used[key] = true
		keys[i] = key
	}
	return keys
}

// DropdownColumn is a dropdown custom field shown as a column in the task table.
type DropdownColumn struct {
	FieldID string
	Name    string
	Width   int
}

// DropdownColumns lists dropdown fields used by these tasks, sorted by name.
func DropdownColumns(tasks []*clickup.Task) []DropdownColumn {
	seen := map[string]*clickup.CustomField{}
	for _, t := range tasks {
		for i := range t.CustomFields {
			f := &t.CustomFields[i]
			if f.Type == "drop_down" && seen[f.ID] == nil {
				seen[f.ID] = f
			}
		}
	}
	cols := make([]DropdownColumn, 0, len(seen))
	for id, f := range seen {
		longest := 4
		for _, o := range f.TypeConfig.Options {
			longest = max(longest, runewidth.StringWidth(o.Title()))
		}
		cols = append(cols, DropdownColumn{FieldID: id, Name: f.Name, Width: min(12, longest+2)})
	}
	slices.SortFunc(cols, func(a, b DropdownColumn) int { return cmp.Compare(a.Name, b.Name) })
	return cols
}

// DropdownCell renders a task's value for a dropdown column as a chip.
func DropdownCell(t *clickup.Task, col DropdownColumn) string {
	f, ok := t.Field(col.FieldID)
	if !ok {
		return ""
	}
	o, ok := DropdownValue(f)
	if !ok {
		return ""
	}
	// Truncate by display width: CJK and emoji take two columns.
	return style.Chip(runewidth.Truncate(o.Title(), col.Width-2, "…"), o.Color)
}
