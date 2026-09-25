package app

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/parse"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// optionKeys numbers dropdown options: 1-9, then letters (skipping j/k, used to move, and x, clear).
var optionKeys = []rune("123456789abcdefghilmnopqrstuvwyz")

const clearKey = 'x'

// EditField opens the custom field menu: every field has a hotkey, so `f e 2` sets a field in three keys.
func (a *App) EditField() {
	t := a.current()
	if t == nil {
		return
	}
	if len(t.CustomFields) == 0 {
		a.UI.Notify(Warn, "“"+t.List.Name+"” has no custom fields, so this task has none to edit.")
		return
	}
	fields := render.SortedFields(t.CustomFields)
	names := make([]string, len(fields))
	width := 0
	for i, f := range fields {
		names[i] = f.Name
		width = max(width, len(f.Name))
	}
	keys := render.Hotkeys(names, "jkq")
	items := make([]MenuItem, len(fields))
	for i, f := range fields {
		label := style.Bold(style.Cell{Text: f.Name}.Fit(width)) + "  " + render.FieldText(f)
		if !render.EditableFieldTypes[f.Type] {
			label += style.Dim("  (read-only)")
		}
		items[i] = MenuItem{Key: keys[i], Label: label, Value: f.ID}
	}
	a.UI.Menu("Custom fields", items, 0, func(item MenuItem) {
		if f, ok := t.Field(item.Value.(string)); ok {
			a.editField(t, *f)
		}
	})
}

// fieldEdit is a new value: local is shown immediately (in the API's read format),
// remote is sent. A nil local clears the field.
type fieldEdit struct {
	local        json.RawMessage
	remote       any
	valueOptions map[string]any
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (a *App) editField(t *clickup.Task, f clickup.CustomField) {
	if !render.EditableFieldTypes[f.Type] {
		a.UI.Notify(Warn, f.Name+" ("+f.Type+") can't be edited here")
		return
	}
	a.askField(f, func(e fieldEdit) { a.setField(t, f, e) })
}

// askField asks for a new value with an editor suited to the field type. save is not
// called when the user cancels or keeps the current value.
func (a *App) askField(f clickup.CustomField, save func(fieldEdit)) {
	clear := fieldEdit{}

	switch f.Type {
	case "drop_down":
		options := render.Options(&f)
		current, hasCurrent := render.DropdownValue(&f)
		items := make([]MenuItem, 0, len(options)+1)
		highlighted := 0
		for i, o := range options[:min(len(options), len(optionKeys))] {
			items = append(items, MenuItem{Key: optionKeys[i], Label: style.Chip(o.Title(), o.Color), Value: o.ID})
			if hasCurrent && o.ID == current.ID {
				highlighted = i
			}
		}
		items = append(items, MenuItem{Key: clearKey, Label: style.Dim("clear"), Value: ""})
		a.UI.Menu(f.Name, items, highlighted, func(item MenuItem) {
			id := item.Value.(string)
			switch {
			case hasCurrent && id == current.ID:
			case id == "":
				save(clear)
			default:
				save(fieldEdit{local: mustJSON(id), remote: id})
			}
		})

	case "labels":
		options := make([]Option, 0)
		for _, o := range render.Options(&f) {
			options = append(options, Option{ID: o.ID, Label: style.Chip(o.Title(), o.Color)})
		}
		current := setOf(render.LabelValues(&f))
		a.UI.MultiPick(f.Name, options, current, func(chosen map[string]bool) {
			if sameSet(chosen, current) {
				return
			}
			ids := slices.Sorted(keysOf(chosen))
			if len(ids) == 0 {
				save(clear)
				return
			}
			save(fieldEdit{local: mustJSON(ids), remote: ids})
		})

	case "users":
		users := render.UserValues(&f)
		current := map[string]bool{}
		for _, u := range users {
			current[strconv.FormatInt(u.ID, 10)] = true
		}
		a.UI.MultiPick(f.Name, a.memberOptions(nil), current, func(chosen map[string]bool) {
			if sameSet(chosen, current) {
				return
			}
			var add, rem []int64
			var local []clickup.User
			for id := range chosen {
				n, _ := strconv.ParseInt(id, 10, 64)
				if !current[id] {
					add = append(add, n)
				}
				if u, ok := a.member(id); ok {
					local = append(local, u)
				}
			}
			for id := range current {
				if !chosen[id] {
					n, _ := strconv.ParseInt(id, 10, 64)
					rem = append(rem, n)
				}
			}
			e := fieldEdit{remote: map[string][]int64{"add": nonNil(add), "rem": nonNil(rem)}}
			if len(local) > 0 {
				e.local = mustJSON(local)
			}
			save(e)
		})

	case "checkbox":
		checked := !render.Checked(f.Value)
		save(fieldEdit{local: mustJSON(checked), remote: checked})

	case "date":
		value := ""
		if d, ok := render.Millis(clickup.FlexString(strings.Trim(string(f.Value), `"`))); ok {
			value = d.Format(time.DateOnly)
		}
		a.UI.Prompt(Prompt{Title: f.Name, Value: value, Hint: dateHint, Check: a.checkDate}, func(answer string) {
			day, kind := parse.Due(answer, a.Now())
			switch kind {
			case parse.DueClear:
				save(clear)
			case parse.DueDate:
				ms := parse.Noon(day)
				save(fieldEdit{local: mustJSON(strconv.FormatInt(ms, 10)), remote: ms, valueOptions: map[string]any{"time": false}})
			}
		})

	case "text":
		var current string
		_ = json.Unmarshal(f.Value, &current)
		a.UI.Edit(f.Name, current, func(edited string) {
			edited = strings.TrimSpace(editorText(edited))
			switch {
			case edited == strings.TrimSpace(current):
			case edited == "":
				save(clear)
			default:
				save(fieldEdit{local: mustJSON(edited), remote: edited})
			}
		})

	default: // single line: short_text, url, email, phone, number, currency, emoji, manual_progress
		a.promptField(f, save)
	}
}

func (a *App) promptField(f clickup.CustomField, save func(fieldEdit)) {
	var shown, hint string
	numeric := f.Type == "number" || f.Type == "currency" || f.Type == "emoji" || f.Type == "manual_progress"
	switch f.Type {
	case "manual_progress":
		var p struct{ Current clickup.FlexFloat }
		if json.Unmarshal(f.Value, &p) == nil && f.IsSet() {
			shown = strconv.FormatFloat(float64(p.Current), 'f', -1, 64)
		}
		hint = "number from " + ftoa(f.TypeConfig.Start) + " to " + ftoa(cmp.Or(f.TypeConfig.End, 100))
	case "emoji":
		shown, hint = scalarString(f.Value), "rating 0–"+strconv.Itoa(cmp.Or(f.TypeConfig.Count, 5))
	default:
		shown, hint = scalarString(f.Value), "empty to clear"
	}
	check := func(answer string) error {
		if _, err := strconv.ParseFloat(answer, 64); numeric && answer != "" && err != nil {
			return fmt.Errorf("%q isn't a number", answer)
		}
		return nil
	}
	a.UI.Prompt(Prompt{Title: f.Name, Value: shown, Hint: hint, AllowEmpty: true, Check: check}, func(answer string) {
		answer = strings.TrimSpace(answer)
		if answer == shown {
			return
		}
		if answer == "" {
			save(fieldEdit{})
			return
		}
		if !numeric {
			save(fieldEdit{local: mustJSON(answer), remote: answer})
			return
		}
		n, err := strconv.ParseFloat(answer, 64)
		if err != nil {
			return // Check refused it
		}
		if f.Type == "manual_progress" {
			start, end := float64(f.TypeConfig.Start), float64(cmp.Or(f.TypeConfig.End, 100))
			pct := 100 * (n - start) / max(end-start, 1)
			save(fieldEdit{local: mustJSON(map[string]float64{"current": n, "percent_completed": pct}), remote: map[string]float64{"current": n}})
			return
		}
		save(fieldEdit{local: mustJSON(n), remote: n})
	})
}

func (a *App) setField(t *clickup.Task, f clickup.CustomField, e fieldEdit) {
	updated := f
	updated.Value = e.local
	what := f.Name + " cleared"
	if e.local != nil {
		what = f.Name + " → " + strings.TrimSpace(style.Strip(render.FieldText(&updated)))
	}
	taskID := t.ID
	a.mutate(t, what,
		func(t *clickup.Task) {
			fields := slices.Clone(t.CustomFields)
			if i := slices.IndexFunc(fields, func(x clickup.CustomField) bool { return x.ID == f.ID }); i >= 0 {
				fields[i] = updated
			}
			t.CustomFields = fields
		},
		func(ctx context.Context) (*clickup.Task, error) {
			if e.local == nil && e.remote == nil {
				return nil, a.API.ClearField(ctx, taskID, f.ID)
			}
			return nil, a.API.SetField(ctx, taskID, f.ID, e.remote, e.valueOptions)
		})
}

func scalarString(raw json.RawMessage) string {
	var s clickup.FlexString
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return string(s)
}

func ftoa(f clickup.FlexFloat) string { return strconv.FormatFloat(float64(f), 'f', -1, 64) }

func setOf(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// keysOf iterates over the ids that are set to true.
func keysOf(m map[string]bool) iter.Seq[string] {
	return func(yield func(string) bool) {
		for k, v := range m {
			if v && !yield(k) {
				return
			}
		}
	}
}

func sameSet(x, y map[string]bool) bool {
	return slices.Equal(slices.Sorted(keysOf(x)), slices.Sorted(keysOf(y)))
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
