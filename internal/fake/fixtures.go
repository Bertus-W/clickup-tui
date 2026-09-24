package fake

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/seed"
)

// BasicFields covers every editable custom field type the tests exercise.
func BasicFields() []clickup.CustomField {
	return []clickup.CustomField{
		{ID: "f-sev", Name: "Severity", Type: "drop_down", Required: true, TypeConfig: clickup.TypeConfig{Options: []clickup.Option{
			{ID: "o1", Name: "S1", Color: "#f00", OrderIndex: 0},
			{ID: "o2", Name: "S2", Color: "#fa0", OrderIndex: 1},
		}}},
		{ID: "f-area", Name: "Areas", Type: "labels", TypeConfig: clickup.TypeConfig{Options: []clickup.Option{
			{ID: "l1", Label: "frontend", Color: "#0af", OrderIndex: 0},
			{ID: "l2", Label: "backend", Color: "#a0f", OrderIndex: 1},
		}}},
		{ID: "f-cust", Name: "Customer facing", Type: "checkbox"},
		{ID: "f-est", Name: "Estimate", Type: "number"},
		{ID: "f-rev", Name: "Reviewer", Type: "users"},
		{ID: "f-score", Name: "Score", Type: "formula", Value: json.RawMessage(`"42"`)},
	}
}

// Basic starts a server with n simple tasks (odd ones assigned to Me) and a comment on t1.
func Basic(n int) *Server {
	tasks := make([]clickup.Task, n)
	for i := range n {
		id := i + 1
		tasks[i] = clickup.Task{
			ID: fmt.Sprintf("t%d", id), CustomID: fmt.Sprintf("DEV-%d", id), Name: fmt.Sprintf("Task number %d", id),
			Status: Statuses[0], OrderIndex: clickup.FlexFloat(id),
			MarkdownDescription: fmt.Sprintf("Description of **task %d**", id),
			CustomFields:        BasicFields(),
		}
		if id%2 == 1 {
			tasks[i].Assignees = []clickup.User{Me}
		}
	}
	s := New(tasks...)
	s.Fields = BasicFields()
	s.Comments["t1"] = []clickup.Comment{{ID: "c1", CommentText: "first!", User: Me, Date: "1700000000000"}}
	return s
}

// Demo starts a server with the demo project from package seed.
func Demo() *Server {
	fields := make([]clickup.CustomField, len(seed.Fields))
	for i, f := range seed.Fields {
		// Severity is required, like in many real workspaces: the create form asks for it.
		fields[i] = clickup.CustomField{ID: "f-" + f.Name, Name: f.Name, Type: "drop_down", Required: f.Name == "Severity"}
		for j, o := range f.Options {
			fields[i].TypeConfig.Options = append(fields[i].TypeConfig.Options,
				clickup.Option{ID: fmt.Sprintf("%s-%d", f.Name, j), Name: o.Name, Color: o.Color, OrderIndex: clickup.FlexFloat(j)})
		}
	}
	valued := func(scope, severity string) []clickup.CustomField {
		out := slices.Clone(fields)
		for i, f := range out {
			want := map[string]string{"Scope": scope, "Severity": severity}[f.Name]
			for _, o := range f.TypeConfig.Options {
				if o.Name == want {
					out[i].Value = json.RawMessage(strconv.Itoa(int(o.OrderIndex)))
				}
			}
		}
		return out
	}
	today := time.Now()
	var tasks []clickup.Task
	for i, d := range seed.Tasks {
		t := clickup.Task{
			ID: fmt.Sprintf("d%d", i+1), CustomID: fmt.Sprintf("DEMO-%d", i+1), Name: d.Name,
			OrderIndex: clickup.FlexFloat(i + 1), MarkdownDescription: d.Description,
			List: clickup.Ref{ID: ListID, Name: "clickup-tui demo"}, CustomFields: valued(d.Scope, d.Severity),
		}
		for _, st := range Statuses {
			if st.Status == d.Status {
				t.Status = st
			}
		}
		if i == 1 { // a code block, and a pasted image the way ClickUp writes it: no name, just the URL
			t.MarkdownDescription += "\n\n```go\nif seen[event.ID] {\n\treturn nil // already charged\n}\n```\n\n" +
				"![](https://codeberg.org/b-wisman/clickup-tui/raw/branch/main/docs/screenshot.png)"
		}
		if d.Priority > 0 {
			name := []string{"", "urgent", "high", "normal", "low"}[d.Priority]
			t.Priority = &clickup.Priority{ID: clickup.FlexString(strconv.Itoa(d.Priority)), Priority: name}
		}
		if d.DueInDays != nil {
			due := today.AddDate(0, 0, *d.DueInDays)
			t.DueDate = clickup.FlexString(strconv.FormatInt(time.Date(due.Year(), due.Month(), due.Day(), 12, 0, 0, 0, time.Local).UnixMilli(), 10))
		}
		if d.Mine {
			t.Assignees = []clickup.User{Me}
		}
		tasks = append(tasks, t)
		for j, sub := range d.Subtasks {
			tasks = append(tasks, clickup.Task{
				ID: fmt.Sprintf("d%d-%d", i+1, j+1), Name: sub, Status: Statuses[0], Parent: clickup.FlexString(t.ID),
				OrderIndex: clickup.FlexFloat(j + 1), List: t.List, CustomFields: slices.Clone(fields),
			})
		}
	}
	s := New(tasks...)
	s.Fields = fields
	// Some time logged this week, so the timesheet (T) has something to show.
	monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	day := func(d, hour int) time.Time {
		return time.Date(monday.Year(), monday.Month(), monday.Day()+d, hour, 0, 0, 0, time.Local)
	}
	for _, e := range []struct {
		task      string
		day, hour int
		d         time.Duration
		note      string
	}{
		{"d1", 0, 9, 2*time.Hour + 30*time.Minute, "reproduce on Safari"}, {"d1", 1, 13, 90 * time.Minute, "cookie fix"},
		{"d2", 0, 14, time.Hour, "idempotency key"}, {"d2", 2, 9, 3 * time.Hour, "handler rewrite"},
		{"d5", 1, 9, 2 * time.Hour, "replication dry run"}, {"d9", 2, 14, 45 * time.Minute, "docs draft"},
	} {
		if start := day(e.day, e.hour); !start.After(today) {
			s.Entries = append(s.Entries, s.Entry(e.task, start, e.d, e.note))
		}
	}
	s.Comments["d1"] = []clickup.Comment{{ID: "c1", CommentText: "Reproduced on Safari 18.1 as well.", User: Other, Date: clickup.FlexString(strconv.FormatInt(today.Add(-2*time.Hour).UnixMilli(), 10))}}
	return s
}
