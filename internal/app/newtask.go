package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/parse"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// FormRow is one property of the task being created.
type FormRow struct {
	Label    string
	Value    string // may contain ANSI styling
	Required bool
	Missing  bool // required and still empty
	// Edit opens an editor for the row; done runs once a value was chosen (not on cancel).
	Edit func(done func())
}

// Form is the "new task" editor: a name plus properties, some of them required.
type Form struct {
	Title string
	Name  string
	Rows  func() []FormRow
	// Submit creates the task. When a required row is still empty it returns that row's
	// index and false instead, so the UI can walk the user through it.
	Submit func(name string) (missing int, ok bool)
}

// draft holds the values chosen in the form until the task is created.
type draft struct {
	list      View
	parent    *clickup.Task
	statuses  []clickup.Status
	status    clickup.Status
	assignees []clickup.User
	priority  int
	due       int64                 // ms, 0 for none
	fields    []clickup.CustomField // the list's editable fields; Value holds the chosen value
	edits     map[string]fieldEdit
}

// withFields calls then with the custom field definitions of a list (cached for an hour).
func (a *App) withFields(listID string, then func([]clickup.CustomField)) {
	cached, at, ok := cache.Get[[]clickup.CustomField](a.Cache, "fields:"+listID)
	if ok && a.Now().Sub(at) < listMetaMaxAge {
		then(cached)
		return
	}
	a.run("", func(ctx context.Context, apply func(func())) {
		fields, err := a.API.ListFields(ctx, listID)
		apply(func() {
			switch {
			case err == nil:
				_ = cache.Put(a.Cache, "fields:"+listID, fields)
				then(fields)
			case ok:
				then(cached)
			default:
				a.error("Loading custom fields", err)
			}
		})
	})
}

// NewTask opens the create form for the current list, or for a subtask of the current task.
func (a *App) NewTask(subtask bool) {
	var parent *clickup.Task
	list := a.View
	switch {
	case subtask:
		if parent = a.current(); parent == nil {
			return
		}
		list = View{Kind: "list", ID: string(parent.List.ID), Name: parent.List.Name}
	case a.View.Kind != "list":
		a.UI.Notify(Warn, "Open a list to create tasks in (2 or ctrl+p). In Mine, N creates a subtask of the selected task.")
		return
	}
	a.withStatuses(list.ID, func(statuses []clickup.Status) {
		a.withFields(list.ID, func(fields []clickup.CustomField) {
			d := &draft{list: list, parent: parent, statuses: sortedStatuses(statuses), edits: map[string]fieldEdit{}}
			if len(d.statuses) > 0 {
				d.status = d.statuses[0]
			}
			for _, f := range fields {
				if render.EditableFieldTypes[f.Type] {
					f.Value = nil
					d.fields = append(d.fields, f)
				}
			}
			// Required fields first, then alphabetical.
			slices.SortStableFunc(d.fields, func(x, y clickup.CustomField) int {
				if x.Required != y.Required {
					if x.Required {
						return -1
					}
					return 1
				}
				return cmp.Compare(x.Name, y.Name)
			})
			title := "New task in " + list.Name
			if parent != nil {
				title = "New subtask of " + parent.Label()
			}
			a.UI.Form(&Form{Title: title, Rows: func() []FormRow { return a.formRows(d) }, Submit: func(name string) (int, bool) {
				return a.submitDraft(d, name)
			}})
		})
	})
}

func (a *App) formRows(d *draft) []FormRow {
	status := style.Dim("—")
	if d.status.Status != "" {
		status = style.Fg(d.status.Color, style.Plain)("● " + strings.ToUpper(d.status.Status))
	}
	people := make([]string, len(d.assignees))
	for i, u := range d.assignees {
		people[i] = u.Username
	}
	priority := style.Dim("—")
	if d.priority > 0 {
		name := priorities[d.priority-1]
		priority = style.Fg(render.PriorityColors[name], style.Plain)("⚑ " + name)
	}
	due := style.Dim("—")
	if d.due > 0 {
		due = time.UnixMilli(d.due).Format(time.DateOnly)
	}
	rows := []FormRow{
		{Label: "Status", Value: status, Edit: func(done func()) { a.draftStatus(d, done) }},
		{Label: "Assignees", Value: cmp.Or(strings.Join(people, ", "), style.Dim("—")), Edit: func(done func()) { a.draftAssignees(d, done) }},
		{Label: "Priority", Value: priority, Edit: func(done func()) { a.draftPriority(d, done) }},
		{Label: "Due date", Value: due, Edit: func(done func()) { a.draftDue(d, done) }},
	}
	for i := range d.fields {
		f := &d.fields[i]
		value := render.FieldText(f)
		missing := f.Required && !f.IsSet()
		if missing {
			value = style.Red("required")
		}
		rows = append(rows, FormRow{Label: f.Name, Value: value, Required: f.Required, Missing: missing,
			Edit: func(done func()) {
				a.askField(*f, func(e fieldEdit) {
					f.Value = e.local
					if e.local == nil && e.remote == nil {
						delete(d.edits, f.ID)
					} else {
						d.edits[f.ID] = e
					}
					done()
				})
			}})
	}
	return rows
}

func (a *App) draftStatus(d *draft, done func()) {
	items := make([]MenuItem, 0, len(d.statuses))
	current := 0
	for i, s := range d.statuses[:min(len(d.statuses), len(optionKeys))] {
		items = append(items, MenuItem{Key: optionKeys[i], Label: style.Fg(s.Color, style.Plain)("● " + strings.ToUpper(s.Status)), Value: s})
		if s.Status == d.status.Status {
			current = i
		}
	}
	a.UI.Menu("Status", items, current, func(item MenuItem) {
		d.status = item.Value.(clickup.Status)
		done()
	})
}

func (a *App) draftAssignees(d *draft, done func()) {
	current := map[string]bool{}
	for _, u := range d.assignees {
		current[strconv.FormatInt(u.ID, 10)] = true
	}
	a.UI.MultiPick("Assignees", a.memberOptions(nil), current, func(chosen map[string]bool) {
		d.assignees = nil
		for _, id := range slices.Sorted(keysOf(chosen)) {
			if u, ok := a.member(id); ok {
				d.assignees = append(d.assignees, u)
			}
		}
		done()
	})
}

func (a *App) draftPriority(d *draft, done func()) {
	items := make([]MenuItem, 0, len(priorities)+1)
	for i, name := range priorities {
		items = append(items, MenuItem{Key: rune('1' + i), Label: style.Fg(render.PriorityColors[name], style.Plain)("⚑ " + name), Value: i + 1})
	}
	items = append(items, MenuItem{Key: clearKey, Label: style.Dim("none"), Value: 0})
	a.UI.Menu("Priority", items, max(d.priority-1, 0), func(item MenuItem) {
		d.priority = item.Value.(int)
		done()
	})
}

func (a *App) draftDue(d *draft, done func()) {
	value := ""
	if d.due > 0 {
		value = time.UnixMilli(d.due).Format(time.DateOnly)
	}
	a.UI.Prompt(Prompt{Title: "Due date", Value: value, Hint: dateHint}, func(answer string) {
		switch day, kind := parse.Due(answer, a.Now()); kind {
		case parse.DueInvalid:
			a.UI.Notify(Error, "Can't parse date: "+answer)
		case parse.DueClear:
			d.due = 0
			done()
		case parse.DueDate:
			d.due = parse.Noon(day)
			done()
		}
	})
}

// submitDraft creates the task optimistically, or reports the first missing required row.
func (a *App) submitDraft(d *draft, name string) (int, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		a.UI.Notify(Error, "Give the task a name first")
		return -1, false
	}
	for i, row := range a.formRows(d) {
		if row.Missing {
			return i, false
		}
	}

	body := map[string]any{"name": name}
	if d.status.Status != "" {
		body["status"] = d.status.Status
	}
	if len(d.assignees) > 0 {
		ids := make([]int64, len(d.assignees))
		for i, u := range d.assignees {
			ids[i] = u.ID
		}
		body["assignees"] = ids
	}
	if d.priority > 0 {
		body["priority"] = d.priority
	}
	if d.due > 0 {
		body["due_date"], body["due_date_time"] = d.due, false
	}
	if d.parent != nil {
		body["parent"] = d.parent.ID
	}
	// Field values go into the create request; people fields use add/rem and are set after.
	var inline []map[string]any
	later := map[string]fieldEdit{}
	for _, f := range d.fields {
		e, ok := d.edits[f.ID]
		switch {
		case !ok:
		case f.Type == "users":
			later[f.ID] = e
		default:
			inline = append(inline, map[string]any{"id": f.ID, "value": e.remote})
		}
	}
	if len(inline) > 0 {
		body["custom_fields"] = inline
	}

	temp := &clickup.Task{
		ID: fmt.Sprintf("tmp-%d", a.Now().UnixNano()), Name: name, Status: d.status, Assignees: d.assignees,
		DueDate: clickup.FlexString(strconv.FormatInt(d.due, 10)), OrderIndex: -1, Pending: true,
		List: clickup.Ref{ID: clickup.FlexString(d.list.ID), Name: d.list.Name}, CustomFields: slices.Clone(d.fields),
	}
	if d.due == 0 {
		temp.DueDate = ""
	}
	if d.priority > 0 {
		temp.Priority = &clickup.Priority{ID: clickup.FlexString(strconv.Itoa(d.priority)), Priority: priorities[d.priority-1]}
	}
	if d.parent != nil {
		temp.Parent = clickup.FlexString(d.parent.ID)
	}
	view := a.View
	a.Tasks = append(a.Tasks, temp)
	a.focusTask(temp)
	a.log("Create task “" + name + "” in " + d.list.Name)
	listID, fields := d.list.ID, slices.Clone(d.fields)
	a.run("", func(ctx context.Context, apply func(func())) {
		created, err := a.API.CreateTask(ctx, listID, body)
		for fieldID, e := range later {
			if err == nil {
				err = a.API.SetField(ctx, created.ID, fieldID, e.remote, e.valueOptions)
			}
		}
		apply(func() {
			i := slices.Index(a.Tasks, temp)
			if err != nil {
				if i >= 0 {
					a.Tasks = slices.Delete(a.Tasks, i, i+1)
					a.fixSelection()
				}
				if created.ID != "" {
					a.error("Created "+created.Label()+", but setting its fields failed", err)
				} else {
					a.error("Create failed", err)
				}
				return
			}
			if len(created.CustomFields) == 0 {
				created.CustomFields = fields
			}
			if a.View == view && i >= 0 {
				a.Tasks[i] = &created
				a.persistView()
				a.focusTask(&created)
			}
			a.UI.Notify(Info, "Created "+created.Label())
		})
	})
	return 0, true
}
