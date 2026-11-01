package app

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/parse"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// ClickUp's API can't search tasks by name: its task endpoints ignore every search parameter.
// So the task pickers (go to task, add a timesheet row) search local copies of the tasks the
// app has come across: lists you opened, my tasks, pinned tasks, tasks you opened, timesheet
// rows and lookups. While you type something that looks like an id, a custom id (or just its
// number) or a URL, the picker also asks ClickUp for that task, and keeps a copy of it too.

// maxSeen caps the local copies; the ones seen longest ago go first.
const maxSeen = 10000

// ByRef is the picker option that asks for a task id or URL instead. The picker keeps it
// visible whatever is typed (see Option.Always).
const ByRef = "\x00ref"

// seenTask is a local copy of a task: enough to find it again and show it in a picker.
type seenTask struct {
	render.SheetTask
	Seen int64 // unix seconds
}

func (a *App) seenKey() string { return "seen:" + a.TeamID }

// seenTasks are the local copies, the most recently seen first.
func (a *App) seenTasks() []seenTask {
	if a.seen == nil || a.seenTeam != a.TeamID {
		a.seen, a.seenTeam = map[string]seenTask{}, a.TeamID
		for _, t := range cache.Value[[]seenTask](a.Cache, a.seenKey(), nil) {
			a.seen[t.ID] = t
		}
	}
	return slices.SortedFunc(maps.Values(a.seen), func(x, y seenTask) int {
		return cmp.Or(cmp.Compare(y.Seen, x.Seen), cmp.Compare(x.ID, y.ID))
	})
}

// remember keeps local copies of tasks the app came across, for the task pickers.
func (a *App) remember(tasks ...render.SheetTask) {
	a.seenTasks() // loads them
	now, changed := a.Now().Unix(), false
	for _, t := range tasks {
		if t.ID == "" || strings.HasPrefix(t.ID, "\x00") {
			continue
		}
		old, ok := a.seen[t.ID]
		if ok { // some sources know less (time entries have no list): keep what's known
			t.Label, t.Name, t.List = cmp.Or(t.Label, old.Label), cmp.Or(t.Name, old.Name), cmp.Or(t.List, old.List)
		}
		if ok && old.SheetTask == t && now-old.Seen < 3600 {
			continue // nothing new: spare the cache a write
		}
		a.seen[t.ID], changed = seenTask{t, now}, true
	}
	if !changed {
		return
	}
	all := a.seenTasks()
	if len(all) > maxSeen {
		for _, t := range all[maxSeen:] {
			delete(a.seen, t.ID)
		}
		all = all[:maxSeen]
	}
	_ = cache.Put(a.Cache, a.seenKey(), all)
}

// rememberTasks keeps local copies of loaded tasks; pending ones aren't real yet.
func (a *App) rememberTasks(tasks ...*clickup.Task) {
	var out []render.SheetTask
	for _, t := range tasks {
		if t != nil && !t.Pending {
			out = append(out, sheetTask(t))
		}
	}
	a.remember(out...)
}

var (
	taskIDPattern   = regexp.MustCompile(`^[0-9a-z]{6,12}$`) // ClickUp's own ids: 869f68ygu
	customIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-\d+$`)
	customIDPrefix  = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)-\d+$`)
	numberPattern   = regexp.MustCompile(`^\d{1,7}$`)
)

// lookups are the task refs worth asking ClickUp about for what's typed: an id, custom id or
// URL, or a bare number with each custom id prefix seen so far (1042 → DEV-1042). Names
// can't be looked up.
func (a *App) lookups(query string) []string {
	q := strings.TrimSpace(query)
	if q == "" || strings.ContainsAny(q, " \t") {
		return nil
	}
	var refs []string
	switch ref := parse.TaskRef(q); {
	case strings.Contains(q, "/t/") && ref != "":
		refs = []string{ref}
	case customIDPattern.MatchString(q):
		refs = []string{strings.ToUpper(q)}
	case numberPattern.MatchString(q):
		prefixes := map[string]bool{}
		for _, t := range a.seenTasks() {
			if m := customIDPrefix.FindStringSubmatch(t.Label); m != nil && len(prefixes) < 3 && !prefixes[m[1]] {
				prefixes[m[1]] = true
				refs = append(refs, m[1]+"-"+q)
			}
		}
	case taskIDPattern.MatchString(q) && strings.ContainsAny(q, "0123456789"):
		refs = []string{q}
	}
	return slices.DeleteFunc(refs, func(ref string) bool { // known already: no request
		_, ok := a.seen[ref]
		return ok || slices.ContainsFunc(a.seenTasks(), func(t seenTask) bool { return strings.EqualFold(t.Label, ref) })
	})
}

// pickTask lets you pick a task by part of its name or id: first the given tasks (what's on
// screen), then the local copies of every task seen before. Typing an id, custom id or URL
// also looks it up in ClickUp. The last option asks for an id or URL, and says why when that
// fails. onPick gets the chosen task.
func (a *App) pickTask(title string, first []render.SheetTask, onPick func(render.SheetTask)) {
	a.rememberTasks(slices.Concat(a.Pinned, []*clickup.Task{a.Detail}, a.Tasks)...)
	a.remember(first...)
	tasks := map[string]render.SheetTask{}
	var found []render.SheetTask // looked up while typing: listed first
	options := func() []Option {
		var out []Option
		clear(tasks)
		add := func(r render.SheetTask) {
			if r.ID == "" || tasks[r.ID].ID != "" {
				return
			}
			tasks[r.ID] = r
			label := style.Dim(r.Label) + " " + r.Name + style.Dim("  "+r.List)
			if r.Label != r.ID { // the internal id too, so either finds it
				label += style.Dim("  " + r.ID)
			}
			out = append(out, Option{ID: r.ID, Label: label})
		}
		for _, r := range slices.Concat(found, first) {
			add(r)
		}
		for _, t := range a.seenTasks() {
			add(t.SheetTask)
		}
		return append(out, Option{ID: ByRef, Label: style.Green("+ another task by id or URL…"), Always: true})
	}
	title += " · type a name or id"
	search := func(query string) {
		refs := a.lookups(query)
		if len(refs) == 0 {
			return
		}
		teamID, debounce := a.TeamID, a.Debounce
		a.run("search", func(ctx context.Context, apply func(func())) {
			select { // wait for a pause in the typing; the next key cancels this
			case <-ctx.Done():
				return
			case <-time.After(debounce):
			}
			var hits []clickup.Task
			for _, ref := range refs {
				if t, err := a.API.GetTask(ctx, ref, teamID); err == nil {
					hits = append(hits, t)
				} // a guess that misses (404) isn't worth a message
			}
			if len(hits) == 0 {
				return
			}
			apply(func() {
				for i := range hits {
					_ = cache.Put(a.Cache, "task:"+hits[i].ID, hits[i])
					a.rememberTasks(&hits[i])
					found = append(found, sheetTask(&hits[i]))
				}
				a.UI.UpdatePick(title, title, options())
			})
		})
	}
	a.UI.SearchPick(title, options(), search, func(id string) {
		if id != ByRef {
			onPick(tasks[id])
			return
		}
		check := func(ref string) error {
			if parse.TaskRef(ref) == "" {
				return errors.New("that URL doesn't point at a task")
			}
			return nil
		}
		a.UI.Prompt(Prompt{Title: strings.TrimSuffix(title, " · type a name or id"), Placeholder: "task id, custom id (ABC-123) or URL", Check: check}, func(ref string) {
			id, teamID := parse.TaskRef(ref), a.TeamID
			a.run("", func(ctx context.Context, apply func(func())) {
				t, err := a.API.GetTask(ctx, id, teamID)
				apply(func() {
					if err != nil {
						a.error("Task "+id, err)
						return
					}
					_ = cache.Put(a.Cache, "task:"+t.ID, t)
					a.rememberTasks(&t)
					onPick(sheetTask(&t))
				})
			})
		})
	})
}

// shownTasks are the tasks on screen, for the top of a task picker: pinned, the open task,
// the list and my tasks.
func (a *App) shownTasks() []render.SheetTask {
	mine := cache.Value[[]*clickup.Task](a.Cache, "tasks:"+a.TeamID+":my::false", nil)
	var out []render.SheetTask
	for _, t := range slices.Concat(a.Pinned, []*clickup.Task{a.Detail}, a.Tasks, mine) {
		if t != nil && !t.Pending {
			out = append(out, sheetTask(t))
		}
	}
	return out
}
