// Package app holds the TUI's state and actions, independent of the terminal UI.
//
// Threading model: every field is owned by the UI goroutine. Background work runs via
// Async.Go and hands results back with Async.UI; work in a named group is superseded
// by newer work in the same group, and stale results are dropped.
package app

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

const (
	listMetaMaxAge = time.Hour
	maxLogLines    = 300
)

// View is what the task panel shows: a list, or the tasks assigned to me.
type View struct {
	Kind string `json:"kind"` // "list" or "my"
	ID   string `json:"id,omitzero"`
	Name string `json:"name"`
}

var MyTasks = View{Kind: "my", Name: "Mine"}

type Level int

const (
	Info Level = iota
	Warn
	Error
)

type Panel int

const (
	PanelWorkspace Panel = iota + 1
	PanelLists
	PanelTasks
	PanelDetail
	PanelPinned
)

type MenuItem struct {
	Key   rune
	Label string // may contain ANSI styling
	Value any
}

type Option struct {
	ID    string
	Label string // may contain ANSI styling; filtering uses the plain text
}

type Prompt struct {
	Title, Value, Placeholder, Hint string
	AllowEmpty                      bool // submit "" instead of treating an empty answer as cancel
	// Check validates the answer before the prompt closes: on an error the prompt stays
	// open with the message, so a typo can be fixed instead of retyped.
	Check func(answer string) error
	// Complete, when set, is what tab does to the text typed so far.
	Complete func(text string) string
}

// UI is what the app needs from the terminal. Callbacks run on the UI goroutine and are
// not called when the user cancels.
type UI interface {
	Menu(title string, items []MenuItem, highlighted int, onPick func(MenuItem))
	Pick(title string, options []Option, current string, onPick func(id string))
	MultiPick(title string, options []Option, selected map[string]bool, onDone func(map[string]bool))
	Prompt(p Prompt, onSubmit func(string))
	Confirm(title, message string, onYes func())
	Form(f *Form)
	Edit(title, initial string, onDone func(string))
	Notify(level Level, msg string)
	ShowLatestComment() // scroll the task panel to its newest comment
	Focus(p Panel)
	Clipboard(text string) error
	OpenURL(url string) error
	Refresh()
}

// Async runs background work and schedules functions on the UI goroutine.
type Async interface {
	// Go runs fn in the background; a non-empty group cancels the group's previous work.
	Go(group string, fn func(ctx context.Context))
	UI(fn func())
}

type App struct {
	API      *clickup.Client
	Cache    *cache.Cache
	UI       UI
	Async    Async
	Now      func() time.Time
	Debounce time.Duration // delay before fetching the highlighted task's details
	TeamPref string

	Me            clickup.User
	Teams         []clickup.Team
	TeamID        string
	Hierarchy     []clickup.Space
	View          View
	LastList      *View
	Tasks         []*clickup.Task
	Pinned        []*clickup.Task // copies of their own, see pinned.go
	PinnedSel     int
	IncludeClosed bool
	Filter        string
	Group         render.GroupBy // how the task list is grouped
	Loading       bool
	SelectedID    string
	Detail        *clickup.Task
	Comments      []clickup.Comment // nil while loading
	SyncedAt      time.Time
	Log           []string
	Sheet         Timesheet
	Timer         *clickup.TimeEntry // the running timer, if any

	selIndex     int
	seq          map[string]uint64
	tasksKey     string                      // view the task list was loaded for
	touch        map[string]uint64           // per task: bumped when an edit starts or ends
	deleting     map[clickup.FlexString]bool // time entries being deleted
	busy         atomic.Int32
	timerRunning atomic.Bool
}

func New(api *clickup.Client, c *cache.Cache, ui UI, async Async) *App {
	a := &App{
		API: api, Cache: c, UI: ui, Async: async,
		Now:      time.Now,
		Debounce: 150 * time.Millisecond,
		View:     MyTasks,
		seq:      map[string]uint64{},
		touch:    map[string]uint64{},
		deleting: map[clickup.FlexString]bool{},
	}
	a.Group = cache.Value(c, "ui:group", render.ByStatus)
	api.OnRequest = func(r clickup.RequestLog) { async.UI(func() { a.logRequest(r) }) }
	return a
}

// Busy reports whether background work is in flight; safe from any goroutine.
func (a *App) Busy() bool { return a.busy.Load() > 0 }

// run executes fn in the background. apply schedules a UI update that is dropped if newer
// work in the same group started meanwhile.
func (a *App) run(group string, fn func(ctx context.Context, apply func(func()))) {
	var gen uint64
	if group != "" {
		a.seq[group]++
		gen = a.seq[group]
	}
	a.busy.Add(1)
	a.Async.Go(group, func(ctx context.Context) {
		apply := func(f func()) {
			a.Async.UI(func() {
				if group == "" || a.seq[group] == gen {
					f()
					a.UI.Refresh()
				}
			})
		}
		defer a.Async.UI(func() {
			if a.busy.Add(-1) == 0 {
				a.SyncedAt = a.Now()
			}
			a.UI.Refresh()
		})
		fn(ctx, apply)
	})
}

// --- log / errors -----------------------------------------------------------------------

func (a *App) log(line string) {
	a.Log = append(a.Log, style.Dim(a.Now().Format("15:04:05 "))+line)
	if over := len(a.Log) - maxLogLines; over > 0 {
		a.Log = slices.Delete(a.Log, 0, over)
	}
}

func (a *App) logRequest(r clickup.RequestLog) {
	if r.Canceled {
		a.log(style.Dim(fmt.Sprintf("%-6s %s canceled", r.Method, r.Path)))
		return
	}
	ok := r.Status > 0 && r.Status < 400
	method := style.Cell{Text: r.Method, Style: style.Green}
	status := fmt.Sprintf("%d", r.Status)
	if r.Status == 0 {
		status = "ERR"
	}
	if !ok {
		method.Style = style.Red
		status = style.Red(status)
	}
	a.log(fmt.Sprintf("%s %s %s · %dms", method.Fit(6), style.Dim(r.Path), status, r.Duration.Milliseconds()))
}

func (a *App) error(what string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	msg := fmt.Sprintf("%s: %v", what, err)
	if e, ok := errors.AsType[*clickup.APIError](err); ok {
		msg = fmt.Sprintf("%s: ClickUp said %q (HTTP %d)", what, e.Message, e.Status)
	}
	a.log(style.Red(msg))
	a.UI.Notify(Error, msg)
}

// --- boot / workspace ---------------------------------------------------------------------

// Boot shows cached identity and workspace immediately and refreshes them in the background.
func (a *App) Boot() {
	me, _, okMe := cache.Get[clickup.User](a.Cache, "me")
	teams, _, okTeams := cache.Get[[]clickup.Team](a.Cache, "teams")
	if okMe && okTeams {
		a.Me, a.Teams = me, teams
		a.enterDefaultWorkspace()
	}
	a.run("identity", func(ctx context.Context, apply func(func())) {
		var me clickup.User
		var teams []clickup.Team
		var errMe, errTeams error
		var wg sync.WaitGroup
		wg.Go(func() { me, errMe = a.API.User(ctx) })
		wg.Go(func() { teams, errTeams = a.API.Teams(ctx) })
		wg.Wait()
		apply(func() {
			if err := cmp.Or(errMe, errTeams); err != nil {
				if !okMe || !okTeams {
					a.error("Could not connect to ClickUp", err)
				}
				return
			}
			_ = cache.Put(a.Cache, "me", me)
			_ = cache.Put(a.Cache, "teams", teams)
			a.Me, a.Teams = me, teams
			if a.TeamID == "" {
				a.enterDefaultWorkspace()
			}
		})
	})
}

func (a *App) enterDefaultWorkspace() {
	if len(a.Teams) == 0 {
		a.UI.Notify(Error, "No workspaces found for this token")
		return
	}
	pref := cmp.Or(a.TeamPref, cache.Value(a.Cache, "ui:team", ""))
	team := a.Teams[0]
	if i := slices.IndexFunc(a.Teams, func(t clickup.Team) bool { return string(t.ID) == pref }); i >= 0 {
		team = a.Teams[i]
	}
	a.EnterWorkspace(string(team.ID))
}

func (a *App) EnterWorkspace(teamID string) {
	// Nothing from the previous workspace may linger: actions would reach its tasks.
	a.TeamID = teamID
	a.Tasks, a.Detail, a.Comments, a.SelectedID, a.Filter, a.tasksKey = nil, nil, nil, "", "", ""
	a.Sheet = Timesheet{}
	a.setTimer(nil)
	a.Hierarchy = cache.Value(a.Cache, "hier:"+teamID, []clickup.Space{})
	a.LastList = nil
	if l, _, ok := cache.Get[View](a.Cache, "ui:list:"+teamID); ok {
		a.LastList = &l
	}
	a.LoadHierarchy()
	a.loadPinned()
	a.LoadTimer()
	a.OpenView(cache.Value(a.Cache, "ui:view:"+teamID, MyTasks))
}

// Team is the current workspace.
func (a *App) Team() clickup.Team {
	for _, t := range a.Teams {
		if string(t.ID) == a.TeamID {
			return t
		}
	}
	return clickup.Team{}
}

// Members are the people in the current workspace.
func (a *App) Members() []clickup.User {
	var users []clickup.User
	for _, m := range a.Team().Members {
		users = append(users, m.User)
	}
	return users
}

func (a *App) LoadHierarchy() {
	teamID := a.TeamID
	a.run("hierarchy", func(ctx context.Context, apply func(func())) {
		fetch := func(ctx context.Context) ([]clickup.Space, error) { return a.API.Hierarchy(ctx, teamID) }
		for u, err := range cache.SWR(ctx, a.Cache, "hier:"+teamID, fetch) {
			if err != nil {
				apply(func() { a.error("Loading workspace", err) })
				return
			}
			apply(func() { a.Hierarchy = u.Value })
		}
	})
}

// Lists returns every list in the workspace with a "Space / Folder / List" path.
func (a *App) Lists() []Option {
	var out []Option
	for _, s := range a.Hierarchy {
		for _, f := range s.Folders {
			for _, l := range f.Lists {
				out = append(out, Option{ID: l.ID, Label: s.Name + " / " + f.Name + " / " + l.Name})
			}
		}
		for _, l := range s.Lists {
			out = append(out, Option{ID: l.ID, Label: s.Name + " / " + l.Name})
		}
	}
	return out
}

// --- task views ----------------------------------------------------------------------------

func (a *App) viewKey() string {
	return fmt.Sprintf("tasks:%s:%s:%s:%t", a.TeamID, a.View.Kind, a.View.ID, a.IncludeClosed)
}

func (a *App) OpenView(v View) {
	a.View = v
	if v.Kind == "list" {
		a.LastList = &v
		_ = cache.Put(a.Cache, "ui:list:"+a.TeamID, v)
	}
	_ = cache.Put(a.Cache, "ui:view:"+a.TeamID, v)
	a.LoadTasks(false)
}

// LoadTasks renders the cached tasks of the current view at once, then refreshes them.
// On a first visit pages stream in as they arrive.
func (a *App) LoadTasks(quiet bool) {
	view, closed, key, teamID, me := a.View, a.IncludeClosed, a.viewKey(), a.TeamID, a.Me.ID
	cached, _, hit := cache.Get[[]*clickup.Task](a.Cache, key)
	// Snapshot now: once handed to the UI, the cached tasks are mutated on this goroutine.
	cachedJSON, _ := json.Marshal(cached)
	sameView := key == a.tasksKey
	a.tasksKey = key
	a.setTasks(a.keepLocal(cached, sameView, nil))
	a.Loading = !hit
	touched := maps.Clone(a.touch) // tasks edited after this point keep their local state
	a.run("tasks", func(ctx context.Context, apply func(func())) {
		pages := a.API.ListTasks(ctx, view.ID, closed)
		if view.Kind == "my" {
			pages = a.API.AssignedTasks(ctx, teamID, me, closed)
		}
		// The worker keeps its own copies: pointers handed to the UI get mutated there.
		var values []clickup.Task
		for page, err := range pages {
			if err != nil {
				apply(func() {
					a.Loading = false
					if !quiet {
						a.error("Loading "+view.Name, err)
					}
				})
				return
			}
			values = append(values, page...)
			if !hit {
				partial := pointers(values)
				apply(func() { a.Loading = false; a.setTasks(a.keepLocal(partial, true, touched)) })
			}
		}
		freshJSON, _ := json.Marshal(values)
		changed := !hit || !bytes.Equal(freshJSON, cachedJSON)
		_ = cache.Put(a.Cache, key, values)
		all := pointers(values)
		apply(func() {
			a.Loading = false
			if changed {
				a.setTasks(a.keepLocal(all, true, touched))
			}
		})
	})
}

// pointers copies tasks into fresh values the UI goroutine can own.
func pointers(tasks []clickup.Task) []*clickup.Task {
	out := make([]*clickup.Task, len(tasks))
	for i, t := range tasks {
		out[i] = &t
	}
	return out
}

// keepLocal adjusts freshly loaded tasks: tasks being created stay (when it's the same view),
// and tasks edited since the load started keep their local state instead of older data.
func (a *App) keepLocal(tasks []*clickup.Task, sameView bool, touched map[string]uint64) []*clickup.Task {
	if touched != nil {
		for i, t := range tasks {
			if a.touch[t.ID] != touched[t.ID] {
				if local := a.find(t.ID); local != nil {
					tasks[i] = local
				}
			}
		}
	}
	if sameView {
		for _, t := range a.Tasks {
			if t.Pending && !slices.ContainsFunc(tasks, func(x *clickup.Task) bool { return x.ID == t.ID }) {
				tasks = append(tasks, t)
			}
		}
	}
	return tasks
}

func (a *App) setTasks(tasks []*clickup.Task) {
	a.Tasks = tasks
	if a.Detail != nil && !slices.Contains(a.Pinned, a.Detail) { // a pinned task stays shown as is
		if fresh := a.find(a.Detail.ID); fresh != nil && fresh != a.Detail {
			if fresh.Subtasks == nil {
				fresh.Subtasks = a.Detail.Subtasks
			}
			a.Detail = fresh
		}
	}
	a.fixSelection()
}

// live returns the task object currently shown for t: a reload may have replaced it.
func (a *App) live(t *clickup.Task) *clickup.Task {
	if cur := a.find(t.ID); cur != nil {
		return cur
	}
	return t
}

func (a *App) find(id string) *clickup.Task {
	if i := slices.IndexFunc(a.Tasks, func(t *clickup.Task) bool { return t.ID == id }); i >= 0 {
		return a.Tasks[i]
	}
	return nil
}

func (a *App) persistView() {
	_ = cache.Put(a.Cache, a.viewKey(), slices.DeleteFunc(slices.Clone(a.Tasks), func(t *clickup.Task) bool { return t.Pending }))
}

// Rows are the visible tasks in display order, after grouping and filtering.
func (a *App) Rows() []render.Row {
	return slices.DeleteFunc(render.Grouped(a.Tasks, a.Group, a.Now()), func(r render.Row) bool { return !render.Matches(r.Task, a.Filter) })
}

// SortMenu picks how the task list is sorted, under a heading per group; the choice is remembered.
func (a *App) SortMenu() {
	order := []render.GroupBy{render.ByStatus, render.ByAssignee, render.ByPriority, render.ByDue}
	items := make([]MenuItem, len(order))
	current := 0
	for i, g := range order {
		items[i] = MenuItem{Key: rune('1' + i), Label: render.GroupNames[g], Value: g}
		if g == a.Group {
			current = i
		}
	}
	a.UI.Menu("Sort tasks by", items, current, func(item MenuItem) {
		a.Group = item.Value.(render.GroupBy)
		_ = cache.Put(a.Cache, "ui:group", a.Group)
		a.fixSelection()
	})
}

// SelectedIndex is the highlighted row, or -1.
func (a *App) SelectedIndex() int {
	return slices.IndexFunc(a.Rows(), func(r render.Row) bool { return r.Task.ID == a.SelectedID })
}

// Select highlights the row at index i and shows it in the detail panel.
func (a *App) Select(i int) {
	rows := a.Rows()
	if len(rows) == 0 {
		return
	}
	i = min(max(i, 0), len(rows)-1)
	a.selIndex = i
	if t := rows[i].Task; t.ID != a.SelectedID || a.Detail != t {
		a.SelectedID = t.ID
		a.LoadDetail(t)
	}
}

// fixSelection keeps a valid highlight when the highlighted task disappears.
func (a *App) fixSelection() {
	rows := a.Rows()
	if len(rows) == 0 {
		// The filter hides every task: actions must not reach the hidden one still shown.
		if a.Filter != "" && a.Detail != nil && a.find(a.Detail.ID) == a.Detail {
			a.Detail, a.Comments, a.SelectedID = nil, nil, ""
		}
		return
	}
	if i := a.SelectedIndex(); i >= 0 {
		a.selIndex = i
		return
	}
	a.Select(min(a.selIndex, len(rows)-1))
}

func (a *App) SetFilter(text string) {
	a.Filter = text
	a.fixSelection()
}

func (a *App) ToggleClosed() {
	a.IncludeClosed = !a.IncludeClosed
	a.LoadTasks(false)
}

// SwitchTab flips between the last opened list and my tasks, like lazygit's [ and ].
func (a *App) SwitchTab() {
	switch {
	case a.View.Kind == "list":
		a.OpenView(MyTasks)
	case a.LastList != nil:
		a.OpenView(*a.LastList)
	default:
		a.UI.Notify(Info, "Pick a list first")
		a.UI.Focus(PanelLists)
	}
}

// --- detail ----------------------------------------------------------------------------------

// LoadDetail shows t (and cached comments) now, then fetches the full task and comments.
func (a *App) LoadDetail(t *clickup.Task) {
	if full, _, ok := cache.Get[clickup.Task](a.Cache, "task:"+t.ID); ok && t.Subtasks == nil {
		t.Subtasks = full.Subtasks
	}
	a.Detail = t
	a.Comments = cache.Value[[]clickup.Comment](a.Cache, "comments:"+t.ID, nil)
	if t.Pending {
		return
	}
	id, touched := t.ID, a.touch[t.ID]
	a.run("detail", func(ctx context.Context, apply func(func())) {
		if a.Debounce > 0 {
			select { // debounce while scrolling through the list
			case <-ctx.Done():
				return
			case <-time.After(a.Debounce):
			}
		}
		var full clickup.Task
		var comments []clickup.Comment
		var errTask, errComments error
		var wg sync.WaitGroup
		wg.Go(func() { full, errTask = a.API.GetTask(ctx, id, "") })
		wg.Go(func() { comments, errComments = a.API.Comments(ctx, id) })
		wg.Wait()
		if errTask == nil && errComments == nil {
			_ = cache.Put(a.Cache, "task:"+id, full)
			_ = cache.Put(a.Cache, "comments:"+id, comments)
		}
		apply(func() {
			if err := cmp.Or(errTask, errComments); err != nil {
				a.error("Loading task", err)
				return
			}
			target := t
			if !slices.Contains(a.Pinned, t) {
				target = a.live(t) // a list reload may have replaced the object meanwhile
			}
			changed := full.DateUpdated != target.DateUpdated
			if a.touch[id] == touched { // an edit since the fetch started is newer than this data
				*target = full
				a.propagate(target)
			} else {
				changed = false
			}
			if a.Detail != nil && a.Detail.ID == id {
				if !slices.Contains(a.Pinned, a.Detail) {
					a.Detail = target
				}
				a.Comments = comments
			}
			if changed && a.find(id) == target {
				a.persistView()
			}
		})
	})
}

// current is the task actions apply to: the one in the detail panel.
func (a *App) current() *clickup.Task {
	switch {
	case a.Detail == nil:
		a.UI.Notify(Warn, "No task selected")
		return nil
	case a.Detail.Pending:
		a.UI.Notify(Warn, "Task is still being created")
		return nil
	}
	return a.Detail
}

func (a *App) Refresh() {
	a.log("Refresh")
	a.LoadTasks(false)
	a.LoadHierarchy()
	a.RefreshPinned()
	a.LoadTimer()
	if !a.Sheet.Week.IsZero() {
		a.LoadWeek()
	}
	if a.Detail != nil && !a.Detail.Pending {
		a.LoadDetail(a.Detail)
	}
}

// AutoRefresh quietly reloads the current view when nothing else is happening.
func (a *App) AutoRefresh() {
	if a.TeamID != "" && !a.Busy() {
		a.LoadTasks(true)
		a.RefreshPinned()
	}
}
