package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
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

var priorities = []string{"urgent", "high", "normal", "low"} // ClickUp ids 1…4

// afterLocalChange re-renders and persists after an optimistic edit.
func (a *App) afterLocalChange() {
	a.persistView()
	a.fixSelection()
	a.UI.Refresh()
}

// mutate applies change immediately, then sends it; on failure the task is restored.
// change must replace slices rather than modify them in place, so the snapshot stays intact.
//
// Only the fields this change touched are reverted or taken from the server's answer, so
// overlapping edits of one task (status, then priority before the first returns) don't undo
// each other.
func (a *App) mutate(t *clickup.Task, what string, change func(*clickup.Task), send func(context.Context) (*clickup.Task, error)) {
	a.mutateThen(t, what, change, send, nil)
}

// mutateThen is mutate with a callback once the change was saved (err nil) or reverted.
func (a *App) mutateThen(t *clickup.Task, what string, change func(*clickup.Task), send func(context.Context) (*clickup.Task, error), after func(error)) {
	before := *t
	change(t)
	touched := changedFields(&before, t)
	a.touch[t.ID]++
	a.propagate(t)
	a.afterLocalChange()
	a.log(what + " · " + t.Label())
	a.run("", func(ctx context.Context, apply func(func())) {
		updated, err := send(ctx)
		apply(func() {
			a.touch[t.ID]++
			// propagate also reaches a copy that a list reload created meanwhile.
			if err != nil {
				copyFields(t, &before, touched)
				a.propagate(t)
				if after != nil {
					after(err)
				}
				a.afterLocalChange()
				a.error("Update failed, reverted", err)
				return
			}
			if updated != nil {
				fields := append(slices.Clone(touched), dateUpdatedField)
				if updated.MarkdownDescription == "" { // update responses don't carry it
					fields = slices.DeleteFunc(fields, func(i int) bool { return i == markdownField })
				}
				copyFields(t, updated, fields)
			}
			a.propagate(t)
			_ = cache.Put(a.Cache, "task:"+t.ID, t)
			if after != nil {
				after(nil)
			}
			a.afterLocalChange()
		})
	})
}

var (
	taskType         = reflect.TypeFor[clickup.Task]()
	dateUpdatedField = fieldIndex("DateUpdated")
	markdownField    = fieldIndex("MarkdownDescription")
)

func fieldIndex(name string) int {
	f, _ := taskType.FieldByName(name)
	return f.Index[0]
}

// changedFields lists the task fields that differ between a and b.
func changedFields(a, b *clickup.Task) []int {
	va, vb := reflect.ValueOf(a).Elem(), reflect.ValueOf(b).Elem()
	var out []int
	for i := range taskType.NumField() {
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			out = append(out, i)
		}
	}
	return out
}

// copyFields copies the given fields from src into dst.
func copyFields(dst, src *clickup.Task, fields []int) {
	vd, vs := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()
	for _, i := range fields {
		vd.Field(i).Set(vs.Field(i))
	}
}

// Send functions run in the background, so they must not touch t: capture what they need first.

func (a *App) update(t *clickup.Task, what string, change func(*clickup.Task), fields map[string]any) {
	id := t.ID
	a.mutate(t, what, change, func(ctx context.Context) (*clickup.Task, error) {
		updated, err := a.API.UpdateTask(ctx, id, fields)
		return &updated, err
	})
}

// withStatuses calls then with the statuses of a list (cached for an hour).
func (a *App) withStatuses(listID string, then func([]clickup.Status)) {
	cached, at, ok := cache.Get[clickup.List](a.Cache, "list:"+listID)
	if ok && a.Now().Sub(at) < listMetaMaxAge {
		then(cached.Statuses)
		return
	}
	a.run("", func(ctx context.Context, apply func(func())) {
		list, err := a.API.GetList(ctx, listID)
		apply(func() {
			switch {
			case err == nil:
				_ = cache.Put(a.Cache, "list:"+listID, list)
				then(list.Statuses)
			case ok:
				then(cached.Statuses)
			default:
				a.error("Loading statuses", err)
			}
		})
	})
}

func sortedStatuses(statuses []clickup.Status) []clickup.Status {
	return slices.SortedStableFunc(slices.Values(statuses), func(x, y clickup.Status) int {
		return cmp.Compare(x.OrderIndex, y.OrderIndex)
	})
}

func (a *App) setStatus(t *clickup.Task, s clickup.Status) {
	a.update(t, "Status → "+s.Status, func(t *clickup.Task) { t.Status = s }, map[string]any{"status": s.Status})
}

// NextStatus shows the list's statuses with the one after the current preselected, so
// space, enter moves a task along its workflow and the number keys jump anywhere.
func (a *App) NextStatus() {
	t := a.current()
	if t == nil {
		return
	}
	a.withStatuses(string(t.List.ID), func(statuses []clickup.Status) {
		statuses = sortedStatuses(statuses)
		if len(statuses) == 0 {
			a.UI.Notify(Warn, "This list has no statuses")
			return
		}
		current := slices.IndexFunc(statuses, func(s clickup.Status) bool { return strings.EqualFold(s.Status, t.Status.Status) })
		items := make([]MenuItem, 0, len(statuses))
		for i, s := range statuses[:min(len(statuses), len(optionKeys))] {
			label := style.Fg(s.Color, style.Plain)("● " + strings.ToUpper(s.Status))
			if i == current {
				label += style.Dim("  current")
			}
			items = append(items, MenuItem{Key: optionKeys[i], Label: label, Value: s})
		}
		// The next status, but no wrapping around: space, enter on a closed task keeps it closed.
		a.UI.Menu("Status", items, min(current+1, len(items)-1), func(item MenuItem) {
			if s := item.Value.(clickup.Status); !strings.EqualFold(s.Status, t.Status.Status) {
				a.setStatus(t, s)
			}
		})
	})
}

func (a *App) SetStatus() {
	t := a.current()
	if t == nil {
		return
	}
	a.withStatuses(string(t.List.ID), func(statuses []clickup.Status) {
		statuses = sortedStatuses(statuses)
		options := make([]Option, len(statuses))
		for i, s := range statuses {
			options[i] = Option{ID: s.Status, Label: style.Fg(s.Color, style.Plain)("● " + strings.ToUpper(s.Status))}
		}
		a.UI.Pick("Status", options, t.Status.Status, func(choice string) {
			if i := slices.IndexFunc(statuses, func(s clickup.Status) bool { return s.Status == choice }); i >= 0 && choice != t.Status.Status {
				a.setStatus(t, statuses[i])
			}
		})
	})
}

func (a *App) SetPriority() {
	t := a.current()
	if t == nil {
		return
	}
	current := 0
	if t.Priority != nil {
		current = int(t.Priority.ID.Int())
	}
	a.priorityMenu(current, func(n int) {
		if n == current {
			return
		}
		if n == 0 {
			a.update(t, "Priority → none", func(t *clickup.Task) { t.Priority = nil }, map[string]any{"priority": nil})
			return
		}
		choice, name := strconv.Itoa(n), priorities[n-1]
		p := &clickup.Priority{ID: clickup.FlexString(choice), Priority: name, Color: render.PriorityColors[name]}
		a.update(t, "Priority → "+name, func(t *clickup.Task) { t.Priority = p }, map[string]any{"priority": n})
	})
}

// priorityMenu asks for a priority: 1 urgent … 4 low, x none (0).
func (a *App) priorityMenu(current int, then func(int)) {
	items := make([]MenuItem, 0, len(priorities)+1)
	for i, name := range priorities {
		items = append(items, MenuItem{Key: rune('1' + i), Label: style.Fg(render.PriorityColors[name], style.Plain)("⚑ " + name), Value: i + 1})
	}
	items = append(items, MenuItem{Key: clearKey, Label: style.Dim("none"), Value: 0})
	highlighted := len(items) - 1
	if current > 0 {
		highlighted = current - 1
	}
	a.UI.Menu("Priority", items, highlighted, func(item MenuItem) { then(item.Value.(int)) })
}

// dateHint documents what parse.Due accepts.
const dateHint = "today · tomorrow · +3d · +2w · fri · 31-10 · 2026-10-31 · none"

// checkDate is the Check of date prompts.
func (a *App) checkDate(answer string) error {
	if _, kind := parse.Due(answer, a.Now()); kind == parse.DueInvalid {
		return fmt.Errorf("can't read %q as a date", answer)
	}
	return nil
}

func (a *App) SetDue() {
	t := a.current()
	if t == nil {
		return
	}
	value := ""
	if due, ok := render.Millis(t.DueDate); ok {
		value = due.Format(time.DateOnly)
	}
	a.UI.Prompt(Prompt{Title: "Due date", Value: value, Hint: dateHint, Check: a.checkDate}, func(answer string) {
		day, kind := parse.Due(answer, a.Now())
		switch kind {
		case parse.DueClear:
			a.update(t, "Due date cleared", func(t *clickup.Task) { t.DueDate = "" }, map[string]any{"due_date": nil})
		case parse.DueDate:
			ms := parse.Noon(day)
			a.update(t, "Due → "+day.Format(time.DateOnly),
				func(t *clickup.Task) { t.DueDate = clickup.FlexString(strconv.FormatInt(ms, 10)) },
				map[string]any{"due_date": ms, "due_date_time": false})
		}
	})
}

func (a *App) Rename() {
	t := a.current()
	if t == nil {
		return
	}
	a.UI.Prompt(Prompt{Title: "Rename task", Value: t.Name}, func(name string) {
		if name != t.Name {
			a.update(t, "Renamed", func(t *clickup.Task) { t.Name = name }, map[string]any{"name": name})
		}
	})
}

func (a *App) EditDescription() {
	t := a.current()
	if t == nil {
		return
	}
	current := t.Body()
	a.UI.Edit("Description · "+t.Label(), current, func(edited string) {
		if strings.TrimSpace(edited) == strings.TrimSpace(current) {
			return
		}
		a.update(t, "Description updated", func(t *clickup.Task) { t.MarkdownDescription = edited },
			map[string]any{"markdown_content": edited})
	})
}

func hasUser(users []clickup.User, id int64) bool {
	return slices.ContainsFunc(users, func(u clickup.User) bool { return u.ID == id })
}

// AssignMe toggles me on the task, and says which way it went: a toggle can surprise.
func (a *App) AssignMe() {
	t := a.current()
	if t == nil || a.Me.ID == 0 {
		return
	}
	chosen := map[string]bool{}
	for _, u := range t.Assignees {
		chosen[strconv.FormatInt(u.ID, 10)] = true
	}
	me := strconv.FormatInt(a.Me.ID, 10)
	chosen[me] = !chosen[me]
	label := t.Label()
	a.setAssignees(t, chosen)
	if chosen[me] {
		a.UI.Notify(Info, "Assigned you to "+label)
	} else {
		a.UI.Notify(Info, "Unassigned you from "+label)
	}
}

// setAssignees makes the chosen member ids the task's assignees, in one update.
func (a *App) setAssignees(t *clickup.Task, chosen map[string]bool) {
	var add, rem []int64
	var kept, added []clickup.User
	var names []string
	for _, u := range t.Assignees {
		if chosen[strconv.FormatInt(u.ID, 10)] {
			kept = append(kept, u)
		} else {
			rem = append(rem, u.ID)
			names = append(names, "-"+u.Username)
		}
	}
	for _, id := range slices.Sorted(keysOf(chosen)) {
		n, _ := strconv.ParseInt(id, 10, 64)
		if hasUser(t.Assignees, n) {
			continue
		}
		if u, ok := a.member(id); ok {
			added = append(added, u)
			add = append(add, n)
			names = append(names, "+"+u.Username)
		}
	}
	if len(add)+len(rem) == 0 {
		return
	}
	users := append(kept, added...)
	a.update(t, "Assignees "+strings.Join(names, " "),
		func(t *clickup.Task) { t.Assignees = nonNil(users) },
		map[string]any{"assignees": map[string][]int64{"add": nonNil(add), "rem": nonNil(rem)}})
}

// memberOptions lists workspace members, marking the ones in selected with ✓.
func (a *App) memberOptions(selected func(clickup.User) bool) []Option {
	members := slices.SortedFunc(slices.Values(a.Members()), func(x, y clickup.User) int {
		return cmp.Compare(strings.ToLower(x.Username), strings.ToLower(y.Username))
	})
	options := make([]Option, len(members))
	for i, u := range members {
		mark := ""
		switch {
		case selected == nil: // a multi-picker shows its own checkboxes
		case selected(u):
			mark = style.Green("✓ ")
		default:
			mark = "  "
		}
		options[i] = Option{ID: strconv.FormatInt(u.ID, 10), Label: mark + style.Fg(u.Color, style.Plain)(u.Username) + " " + style.Dim(u.Email)}
	}
	return options
}

func (a *App) member(id string) (clickup.User, bool) {
	members := a.Members()
	i := slices.IndexFunc(members, func(u clickup.User) bool { return strconv.FormatInt(u.ID, 10) == id })
	if i < 0 {
		return clickup.User{}, false
	}
	return members[i], true
}

// Assign picks the assignees: space toggles people, enter saves them all at once.
func (a *App) Assign() {
	t := a.current()
	if t == nil {
		return
	}
	current := map[string]bool{}
	for _, u := range t.Assignees {
		current[strconv.FormatInt(u.ID, 10)] = true
	}
	a.UI.MultiPick("Assignees · "+t.Label(), a.memberOptions(nil), current, func(chosen map[string]bool) {
		a.setAssignees(t, chosen)
	})
}

// MoveTask moves the task to another list: pick it by typing part of its name.
func (a *App) MoveTask() {
	t := a.current()
	if t == nil {
		return
	}
	options := slices.DeleteFunc(a.Lists(), func(o Option) bool { return o.ID == string(t.List.ID) })
	if len(options) == 0 {
		a.UI.Notify(Warn, "There is no other list to move it to.")
		return
	}
	a.UI.Pick("Move "+t.Label()+" to list", options, "", func(id string) {
		label := options[slices.IndexFunc(options, func(o Option) bool { return o.ID == id })].Label
		parts := strings.Split(label, " / ")
		a.moveTask(t, id, parts[len(parts)-1])
	})
}

// moveTask moves t at once: it leaves the list on screen (unless that's Mine) and comes
// back if ClickUp refuses.
func (a *App) moveTask(t *clickup.Task, listID, listName string) {
	id, teamID, key := t.ID, a.TeamID, a.viewKey()
	var listCopy *clickup.Task
	i := slices.IndexFunc(a.Tasks, func(x *clickup.Task) bool { return x.ID == id })
	if a.View.Kind == "list" && a.View.ID == string(t.List.ID) && i >= 0 {
		listCopy = a.Tasks[i]
		a.Tasks = slices.Delete(slices.Clone(a.Tasks), i, i+1)
	}
	var picked string // the task selected once t left the list
	a.mutateThen(t, "Moved to "+listName,
		func(t *clickup.Task) { t.List = clickup.Ref{ID: clickup.FlexString(listID), Name: listName} },
		func(ctx context.Context) (*clickup.Task, error) {
			return nil, a.API.MoveTask(ctx, teamID, id, listID)
		},
		func(err error) {
			if err == nil {
				a.UI.Notify(Info, "Moved "+t.Label()+" to "+listName)
				return
			}
			if listCopy != nil && a.viewKey() == key && a.find(id) == nil {
				a.Tasks = slices.Insert(slices.Clone(a.Tasks), min(i, len(a.Tasks)), listCopy)
				if a.SelectedID == picked { // nobody moved on meanwhile: select it again
					a.focusTask(listCopy)
				}
			}
		})
	picked = a.SelectedID
}

// focusTask highlights t in the table and shows it.
func (a *App) focusTask(t *clickup.Task) {
	a.SelectedID = t.ID
	if i := a.SelectedIndex(); i >= 0 {
		a.selIndex = i
	}
	a.LoadDetail(t)
}

func (a *App) Delete() {
	t := a.current()
	if t == nil {
		return
	}
	a.UI.Confirm("Delete task", fmt.Sprintf("Delete %s “%s”?", t.Label(), t.Name), func() {
		// Remove every copy (list row, pin, task panel) by id: t may be the pinned copy.
		id, key := t.ID, a.viewKey()
		byID := func(x *clickup.Task) bool { return x.ID == id }
		i, j := slices.IndexFunc(a.Tasks, byID), a.findPinned(id)
		var listCopy, pinCopy *clickup.Task
		if i >= 0 {
			listCopy = a.Tasks[i]
			a.Tasks = slices.Delete(slices.Clone(a.Tasks), i, i+1)
		}
		if j >= 0 {
			pinCopy = a.Pinned[j]
			a.removePinned(j)
		}
		if a.Detail != nil && a.Detail.ID == id {
			a.Detail, a.Comments, a.SelectedID = nil, nil, ""
		}
		a.fixSelection()
		a.log("Delete " + t.Label())
		a.run("", func(ctx context.Context, apply func(func())) {
			err := a.API.DeleteTask(ctx, id)
			apply(func() {
				if err != nil {
					// Restore only where it still makes sense: same view, not reloaded back in.
					if listCopy != nil && a.viewKey() == key && a.find(id) == nil {
						a.Tasks = slices.Insert(a.Tasks, min(i, len(a.Tasks)), listCopy)
					}
					if pinCopy != nil && a.findPinned(id) < 0 {
						a.Pinned = slices.Insert(a.Pinned, min(j, len(a.Pinned)), pinCopy)
						a.persistPinned()
					}
					a.fixSelection()
					a.error("Delete failed, restored", err)
					return
				}
				a.persistView()
				a.Cache.Delete("task:" + id)
			})
		})
	})
}

// Comment adds a one-line comment; CommentInEditor opens $EDITOR for longer ones.
func (a *App) Comment() {
	if t := a.current(); t != nil {
		complete := func(text string) string { return completeMention(text, a.Members()) }
		a.UI.Prompt(Prompt{Title: "Comment on " + t.Label(), Hint: "@name + tab mentions someone · C: multi-line in $EDITOR", Complete: complete},
			func(text string) { a.postComment(t, text) })
	}
}

func (a *App) CommentInEditor() {
	if t := a.current(); t != nil {
		a.UI.Edit("Comment · "+t.Label(), "", func(text string) { a.postComment(t, text) })
	}
}

func (a *App) postComment(t *clickup.Task, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	pending := clickup.Comment{
		ID: "pending", CommentText: text, User: a.Me, Pending: true,
		Date: clickup.FlexString(strconv.FormatInt(a.Now().UnixMilli(), 10)),
	}
	id := t.ID
	shown := func() bool { return a.Detail != nil && a.Detail.ID == id } // objects get replaced on reload
	if shown() {
		a.Comments = append(slices.Clone(a.Comments), pending)
		a.UI.ShowLatestComment()
	}
	label := t.Label()
	parts, mentioned := mentionParts(text, a.Members())
	names := make([]string, len(mentioned))
	for i, u := range mentioned {
		names[i] = "@" + u.Username
	}
	a.log(strings.TrimSpace("Comment on " + label + " " + strings.Join(names, " ")))
	a.run("", func(ctx context.Context, apply func(func())) {
		err := a.API.CreateComment(ctx, id, text, parts)
		var fresh []clickup.Comment
		if err == nil {
			fresh, err = a.API.Comments(ctx, id)
		}
		apply(func() {
			if err != nil {
				if shown() {
					a.Comments = slices.DeleteFunc(slices.Clone(a.Comments), func(c clickup.Comment) bool { return c.Pending })
				}
				a.error("Comment failed", err)
				return
			}
			_ = cache.Put(a.Cache, "comments:"+id, fresh)
			a.UI.Notify(Info, "Comment posted on "+label)
			if shown() {
				a.Comments = fresh
				a.UI.ShowLatestComment()
			}
		})
	})
}

// GoTo jumps to a task by id, custom id or URL, even outside the current view.
func (a *App) GoTo() {
	check := func(ref string) error {
		if parse.TaskRef(ref) == "" {
			return errors.New("that URL doesn't point at a task")
		}
		return nil
	}
	a.UI.Prompt(Prompt{Title: "Go to task", Placeholder: "task id, custom id (ABC-123) or URL", Check: check}, func(ref string) {
		id := parse.TaskRef(ref)
		if i := slices.IndexFunc(a.Tasks, func(t *clickup.Task) bool { return t.ID == id || t.CustomID == id }); i >= 0 {
			if !render.Matches(a.Tasks[i], a.Filter) {
				a.SetFilter("")
			}
			a.focusTask(a.Tasks[i])
			a.UI.Focus(PanelTasks)
			return
		}
		a.run("goto", func(ctx context.Context, apply func(func())) {
			t, err := a.API.GetTask(ctx, id, a.TeamID)
			apply(func() {
				if err != nil {
					a.error("Task "+id, err)
					return
				}
				_ = cache.Put(a.Cache, "task:"+t.ID, t)
				a.LoadDetail(&t)
				a.UI.Focus(PanelDetail)
			})
		})
	})
}

func (a *App) CopyMenu() {
	t := a.current()
	if t == nil {
		return
	}
	items := []MenuItem{{'u', "Task URL", t.URL}, {'i', "Task ID", t.ID}}
	if t.CustomID != "" {
		items = append(items, MenuItem{'c', "Custom ID", t.CustomID})
	}
	items = append(items,
		MenuItem{'n', "Task name", t.Name},
		MenuItem{'m', "Markdown link", fmt.Sprintf("[%s](%s)", t.Name, t.URL)})
	a.UI.Menu("Copy to clipboard", items, 0, func(item MenuItem) {
		text := item.Value.(string)
		if err := a.UI.Clipboard(text); err != nil {
			a.error("Copy", err)
			return
		}
		a.UI.Notify(Info, "Copied: "+text)
	})
}

func (a *App) OpenInBrowser() {
	if t := a.current(); t != nil && t.URL != "" {
		if err := a.UI.OpenURL(t.URL); err != nil {
			a.error("Open", err)
		}
	}
}

func (a *App) SwitchWorkspace() {
	if len(a.Teams) < 2 {
		a.UI.Notify(Info, "Only one workspace available")
		return
	}
	options := make([]Option, len(a.Teams))
	for i, t := range a.Teams {
		options[i] = Option{ID: string(t.ID), Label: t.Name}
	}
	a.UI.Pick("Workspace", options, a.TeamID, func(id string) {
		if id != a.TeamID {
			_ = cache.Put(a.Cache, "ui:team", id)
			a.EnterWorkspace(id)
		}
	})
}

// JumpToList fuzzy-finds any list in the workspace.
func (a *App) JumpToList() {
	options := append([]Option{{ID: "", Label: "★ My tasks"}}, a.Lists()...)
	a.UI.Pick("Jump to list", options, "", func(id string) {
		if id == "" {
			a.OpenView(MyTasks)
		} else {
			i := slices.IndexFunc(options, func(o Option) bool { return o.ID == id })
			parts := strings.Split(options[i].Label, " / ")
			a.OpenView(View{Kind: "list", ID: id, Name: parts[len(parts)-1]})
		}
		a.UI.Focus(PanelTasks)
	})
}
