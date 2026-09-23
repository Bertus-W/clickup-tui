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

var priorities = []string{"urgent", "high", "normal", "low"} // ClickUp ids 1…4

// afterLocalChange re-renders and persists after an optimistic edit.
func (a *App) afterLocalChange() {
	a.persistView()
	a.fixSelection()
	a.UI.Refresh()
}

// mutate applies change immediately, then sends it; on failure the task is restored.
// change must replace slices rather than modify them in place, so the snapshot stays intact.
func (a *App) mutate(t *clickup.Task, what string, change func(*clickup.Task), send func(context.Context) (*clickup.Task, error)) {
	before := *t
	change(t)
	a.propagate(t)
	a.afterLocalChange()
	a.log(what + " · " + t.Label())
	a.run("", func(ctx context.Context, apply func(func())) {
		updated, err := send(ctx)
		apply(func() {
			// propagate also reaches a copy that a list reload created meanwhile.
			if err != nil {
				*t = before
				a.propagate(t)
				a.afterLocalChange()
				a.error("Update failed, reverted", err)
				return
			}
			if updated != nil {
				merge(t, updated)
			}
			a.propagate(t)
			_ = cache.Put(a.Cache, "task:"+t.ID, t)
			a.afterLocalChange()
		})
	})
}

// merge copies a server response into t, keeping what update responses leave out.
func merge(t, updated *clickup.Task) {
	keep := *t
	*t = *updated
	t.MarkdownDescription = cmp.Or(t.MarkdownDescription, keep.MarkdownDescription)
	if t.Subtasks == nil {
		t.Subtasks = keep.Subtasks
	}
	if t.CustomFields == nil {
		t.CustomFields = keep.CustomFields
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
		a.UI.Menu("Status", items, (current+1)%len(items), func(item MenuItem) {
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
	var options []Option
	for i, name := range priorities {
		options = append(options, Option{ID: strconv.Itoa(i + 1), Label: style.Fg(render.PriorityColors[name], style.Plain)("⚑ " + name)})
	}
	options = append(options, Option{ID: "none", Label: style.Dim("  none")})
	current := "none"
	if t.Priority != nil {
		current = string(t.Priority.ID)
	}
	a.UI.Pick("Priority", options, current, func(choice string) {
		if choice == current {
			return
		}
		if choice == "none" {
			a.update(t, "Priority → none", func(t *clickup.Task) { t.Priority = nil }, map[string]any{"priority": nil})
			return
		}
		n, _ := strconv.Atoi(choice)
		name := priorities[n-1]
		p := &clickup.Priority{ID: clickup.FlexString(choice), Priority: name, Color: render.PriorityColors[name]}
		a.update(t, "Priority → "+name, func(t *clickup.Task) { t.Priority = p }, map[string]any{"priority": n})
	})
}

// dateHint documents what parse.Due accepts.
const dateHint = "today · tomorrow · +3d · +2w · fri · 10-31 · 2026-10-31 · none"

func (a *App) SetDue() {
	t := a.current()
	if t == nil {
		return
	}
	value := ""
	if due, ok := render.Millis(t.DueDate); ok {
		value = due.Format(time.DateOnly)
	}
	a.UI.Prompt(Prompt{Title: "Due date", Value: value, Hint: dateHint}, func(answer string) {
		day, kind := parse.Due(answer, a.Now())
		switch kind {
		case parse.DueInvalid:
			a.UI.Notify(Error, "Can't parse date: "+answer)
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

func (a *App) toggleAssignee(t *clickup.Task, u clickup.User) {
	if hasUser(t.Assignees, u.ID) {
		a.update(t, "Unassigned "+u.Username,
			func(t *clickup.Task) {
				t.Assignees = slices.DeleteFunc(slices.Clone(t.Assignees), func(x clickup.User) bool { return x.ID == u.ID })
			},
			map[string]any{"assignees": map[string][]int64{"add": {}, "rem": {u.ID}}})
		return
	}
	a.update(t, "Assigned "+u.Username,
		func(t *clickup.Task) { t.Assignees = append(slices.Clone(t.Assignees), u) },
		map[string]any{"assignees": map[string][]int64{"add": {u.ID}, "rem": {}}})
}

func (a *App) AssignMe() {
	if t := a.current(); t != nil && a.Me.ID != 0 {
		a.toggleAssignee(t, a.Me)
	}
}

// memberOptions lists workspace members, marking the ones in selected with ✓.
func (a *App) memberOptions(selected func(clickup.User) bool) []Option {
	members := slices.SortedFunc(slices.Values(a.Members()), func(x, y clickup.User) int {
		return cmp.Compare(strings.ToLower(x.Username), strings.ToLower(y.Username))
	})
	options := make([]Option, len(members))
	for i, u := range members {
		mark := "  "
		if selected != nil && selected(u) {
			mark = style.Green("✓ ")
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

// Assign toggles anyone: type part of a name, enter.
func (a *App) Assign() {
	t := a.current()
	if t == nil {
		return
	}
	options := a.memberOptions(func(u clickup.User) bool { return hasUser(t.Assignees, u.ID) })
	a.UI.Pick("Assign (type a name, enter toggles)", options, "", func(id string) {
		if u, ok := a.member(id); ok {
			a.toggleAssignee(t, u)
		}
	})
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
		i := slices.Index(a.Tasks, t)
		if i >= 0 {
			a.Tasks = slices.Delete(slices.Clone(a.Tasks), i, i+1)
			a.fixSelection()
		}
		a.log("Delete " + t.Label())
		id := t.ID
		a.run("", func(ctx context.Context, apply func(func())) {
			err := a.API.DeleteTask(ctx, id)
			apply(func() {
				if err != nil {
					if i >= 0 {
						a.Tasks = slices.Insert(a.Tasks, min(i, len(a.Tasks)), t)
						a.fixSelection()
					}
					a.error("Delete failed, restored", err)
					return
				}
				a.persistView()
				a.Cache.Delete("task:" + id)
				if j := a.findPinned(id); j >= 0 {
					a.Pinned = slices.Delete(a.Pinned, j, j+1)
					a.PinnedSel = min(a.PinnedSel, max(len(a.Pinned)-1, 0))
					a.persistPinned()
				}
			})
		})
	})
}

// Comment adds a one-line comment; CommentInEditor opens $EDITOR for longer ones.
func (a *App) Comment() {
	if t := a.current(); t != nil {
		a.UI.Prompt(Prompt{Title: "Comment on " + t.Label(), Placeholder: "C for a multi-line comment in $EDITOR"},
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
	if a.Detail == t {
		a.Comments = append(slices.Clone(a.Comments), pending)
	}
	a.log("Comment on " + t.Label())
	id := t.ID
	a.run("", func(ctx context.Context, apply func(func())) {
		err := a.API.CreateComment(ctx, id, text)
		var fresh []clickup.Comment
		if err == nil {
			fresh, err = a.API.Comments(ctx, id)
		}
		apply(func() {
			if err != nil {
				if a.Detail == t {
					a.Comments = slices.DeleteFunc(a.Comments, func(c clickup.Comment) bool { return c.Pending })
				}
				a.error("Comment failed", err)
				return
			}
			_ = cache.Put(a.Cache, "comments:"+id, fresh)
			if a.Detail == t {
				a.Comments = fresh
			}
		})
	})
}

// GoTo jumps to a task by id, custom id or URL, even outside the current view.
func (a *App) GoTo() {
	a.UI.Prompt(Prompt{Title: "Go to task", Placeholder: "task id, custom id (ABC-123) or URL"}, func(ref string) {
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
