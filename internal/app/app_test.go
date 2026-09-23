package app_test

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/fake"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// testAsync runs work in goroutines and serialises UI callbacks with a mutex.
type testAsync struct {
	ui      sync.Mutex
	wg      sync.WaitGroup
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (t *testAsync) Go(group string, fn func(context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	if group != "" {
		t.mu.Lock()
		if prev := t.cancels[group]; prev != nil {
			prev()
		}
		t.cancels[group] = cancel
		t.mu.Unlock()
	}
	t.wg.Go(func() {
		defer cancel()
		fn(ctx)
	})
}

func (t *testAsync) UI(fn func()) {
	t.ui.Lock()
	defer t.ui.Unlock()
	fn()
}

// scriptUI answers prompts from a queue: a rune picks a menu item, a string picks the
// option whose id or label contains it (or is typed into a prompt), a []string toggles
// options in a multi-picker, a bool answers a confirmation and nil cancels.
type scriptUI struct {
	t           *testing.T
	highlighted int
	answers     []any
	titles      []string
	notes       []string
	focus       app.Panel
	form        *app.Form
	clipboard   string
}

func (s *scriptUI) next(title string) any {
	s.titles = append(s.titles, title)
	if len(s.answers) == 0 {
		s.t.Fatalf("unexpected popup %q", title)
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a
}

func matches(o app.Option, want string) bool {
	return o.ID == want || strings.Contains(strings.ToLower(style.Strip(o.Label)), strings.ToLower(want))
}

func (s *scriptUI) Menu(title string, items []app.MenuItem, highlighted int, onPick func(app.MenuItem)) {
	s.highlighted = highlighted
	switch answer := s.next(title).(type) {
	case string: // "enter": take the preselected item
		onPick(items[highlighted])
	case rune:
		key := answer
		i := slices.IndexFunc(items, func(it app.MenuItem) bool { return it.Key == key })
		if i < 0 {
			s.t.Fatalf("menu %q has no key %q", title, key)
		}
		onPick(items[i])
	}
}

func (s *scriptUI) Pick(title string, options []app.Option, _ string, onPick func(string)) {
	if want, ok := s.next(title).(string); ok {
		i := slices.IndexFunc(options, func(o app.Option) bool { return matches(o, want) })
		if i < 0 {
			s.t.Fatalf("picker %q has no option %q", title, want)
		}
		onPick(options[i].ID)
	}
}

func (s *scriptUI) MultiPick(title string, options []app.Option, selected map[string]bool, onDone func(map[string]bool)) {
	if toggles, ok := s.next(title).([]string); ok {
		out := map[string]bool{}
		maps.Copy(out, selected)
		for _, want := range toggles {
			i := slices.IndexFunc(options, func(o app.Option) bool { return matches(o, want) })
			out[options[i].ID] = !out[options[i].ID]
		}
		onDone(out)
	}
}

func (s *scriptUI) Prompt(p app.Prompt, onSubmit func(string)) {
	if text, ok := s.next(p.Title).(string); ok && (text != "" || p.AllowEmpty) {
		onSubmit(text)
	}
}

func (s *scriptUI) Confirm(title, _ string, onYes func()) {
	if yes, _ := s.next(title).(bool); yes {
		onYes()
	}
}

func (s *scriptUI) Edit(title, _ string, onDone func(string)) {
	if text, ok := s.next(title).(string); ok {
		onDone(text)
	}
}

// Form types the answered name and presses enter, walking through required fields the way
// the real form does: each missing field's editor consumes the next answers.
func (s *scriptUI) Form(f *app.Form) {
	s.form = f
	name, ok := s.next(f.Title).(string)
	if !ok {
		return
	}
	for {
		missing, ok := f.Submit(name)
		if ok || missing < 0 {
			return
		}
		finished := false
		f.Rows()[missing].Edit(func() { finished = true })
		if !finished {
			return // cancelled
		}
	}
}

func (s *scriptUI) Notify(_ app.Level, msg string) { s.notes = append(s.notes, msg) }
func (s *scriptUI) Focus(p app.Panel)              { s.focus = p }
func (s *scriptUI) Clipboard(text string) error    { s.clipboard = text; return nil }
func (s *scriptUI) OpenURL(string) error           { return nil }
func (s *scriptUI) Refresh()                       {}

type harness struct {
	t     *testing.T
	fake  *fake.Server
	app   *app.App
	ui    *scriptUI
	async *testAsync
}

func newHarness(t *testing.T, srv *fake.Server, c *cache.Cache) *harness {
	t.Helper()
	if c == nil {
		var err error
		if c, err = cache.Open(":memory:"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
	}
	ui := &scriptUI{t: t}
	async := &testAsync{cancels: map[string]context.CancelFunc{}}
	a := app.New(srv.Client(), c, ui, async)
	a.Debounce = 0
	return &harness{t: t, fake: srv, app: a, ui: ui, async: async}
}

// do runs fn as a UI event, answering popups with answers, and waits for background work.
func (h *harness) do(fn func(*app.App), answers ...any) {
	h.t.Helper()
	h.ui.answers = append(h.ui.answers, answers...)
	h.async.UI(func() { fn(h.app) })
	h.async.wg.Wait()
	if len(h.ui.answers) > 0 {
		h.t.Fatalf("unused answers: %v", h.ui.answers)
	}
}

func (h *harness) boot() *harness {
	h.do((*app.App).Boot)
	return h
}

func (h *harness) rows() []string {
	var names []string
	for _, r := range h.app.Rows() {
		names = append(names, strings.Repeat("↳", r.Depth)+r.Task.Name)
	}
	return names
}

var backlog = app.View{Kind: "list", ID: fake.ListID, Name: "Backlog"}

func TestBootShowsMyTasksWithDetail(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	if got, want := h.rows(), []string{"Task number 1", "Task number 3", "Task number 5"}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if h.app.Detail == nil || h.app.Detail.ID != "t1" || len(h.app.Comments) != 1 {
		t.Fatalf("detail = %+v, comments = %v", h.app.Detail, h.app.Comments)
	}
	if len(h.app.Hierarchy) != 1 || h.app.Hierarchy[0].Folders[0].Lists[0].ID != fake.ListID {
		t.Fatalf("hierarchy = %+v", h.app.Hierarchy)
	}
	if len(h.app.Log) == 0 {
		t.Fatal("command log is empty")
	}
}

func TestSecondStartRendersFromCacheBeforeNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := cache.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, fake.Basic(5), c).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	c.Close()

	c2, _ := cache.Open(path)
	defer c2.Close()
	h2 := newHarness(t, fake.Basic(5), c2)
	h2.async.UI(func() {
		h2.app.Boot()
		// Still inside the same UI event: nothing has come back from the network yet.
		if h2.app.View.ID != fake.ListID || len(h2.app.Rows()) != 5 || h2.app.Detail == nil {
			t.Errorf("cached start: view %+v, %d rows", h2.app.View, len(h2.app.Rows()))
		}
	})
	h2.async.wg.Wait()
}

func TestFilter(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.SetFilter("number 3") })
	if got := h.rows(); !slices.Equal(got, []string{"Task number 3"}) {
		t.Fatalf("rows = %v", got)
	}
	if h.app.SelectedID != "t3" {
		t.Fatalf("selection didn't follow the filter: %q", h.app.SelectedID)
	}
}

func TestSetStatusIsOptimisticAndSelectionFollowsTheTask(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).SetStatus, "progress")
	if got := h.fake.Task("t1").Status.Status; got != "in progress" {
		t.Fatalf("server status = %q", got)
	}
	if rows := h.rows(); rows[len(rows)-1] != "Task number 1" {
		t.Fatalf("in-progress task should sort last: %v", rows)
	}
	if h.app.SelectedID != "t1" || h.app.Detail.ID != "t1" {
		t.Fatalf("selection moved to %q", h.app.SelectedID)
	}
}

func TestNextStatusPreselectsTheFollowingStatus(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).NextStatus, "enter") // to do → in progress
	if h.ui.highlighted != 1 || h.fake.Task("t1").Status.Status != "in progress" {
		t.Fatalf("highlighted %d, status %q", h.ui.highlighted, h.fake.Task("t1").Status.Status)
	}
	h.do((*app.App).NextStatus, "enter") // in progress → complete
	if got := h.fake.Task("t1").Status.Status; got != "complete" {
		t.Fatalf("status %q", got)
	}
	h.do((*app.App).NextStatus, '1') // jump back with a number key
	if got := h.fake.Task("t1").Status.Status; got != "to do" {
		t.Fatalf("status %q", got)
	}
}

func TestFailedUpdateReverts(t *testing.T) {
	srv := fake.Basic(5)
	srv.FailUpdates = true
	h := newHarness(t, srv, nil).boot()
	h.do((*app.App).Rename, "Renamed")
	if h.app.Detail.Name != "Task number 1" || h.rows()[0] != "Task number 1" {
		t.Fatalf("not reverted: %q", h.app.Detail.Name)
	}
	if len(h.ui.notes) == 0 || !strings.Contains(h.ui.notes[0], "reverted") {
		t.Fatalf("notes = %v", h.ui.notes)
	}
}

func TestPriorityDueAndDescription(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).SetPriority, "urgent")
	if p := h.fake.Task("t1").Priority; p == nil || p.Priority != "urgent" {
		t.Fatalf("priority = %+v", p)
	}
	h.do((*app.App).SetDue, "2030-01-15")
	if got := render.DueCell(new(h.fake.Task("t1")), time.Now()).Text; got != "Jan 2030" {
		t.Fatalf("due = %q", got)
	}
	h.do((*app.App).EditDescription, "New *body*")
	if got := h.fake.Task("t1").MarkdownDescription; got != "New *body*" {
		t.Fatalf("server description = %q", got)
	}
	if got := h.app.Detail.MarkdownDescription; got != "New *body*" {
		t.Fatalf("local description = %q (update responses carry no markdown)", got)
	}
}

func TestCreateSubtaskCommentDelete(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.do(func(a *app.App) { a.NewTask(false) }, "Brand new", '2') // Severity is required: pick S2
	if h.app.Detail.Name != "Brand new" || h.app.Detail.Pending {
		t.Fatalf("detail = %+v", h.app.Detail)
	}
	parent := h.app.Detail.ID

	h.do(func(a *app.App) { a.NewTask(true) }, "Child", '1')
	if got := h.fake.Task(h.app.Detail.ID).Parent; string(got) != parent {
		t.Fatalf("parent = %q, want %q", got, parent)
	}
	if !slices.Contains(h.rows(), "↳Child") {
		t.Fatalf("subtask not nested: %v", h.rows())
	}

	child := h.app.Detail.ID
	h.do((*app.App).Comment, "hello")
	if len(h.fake.Comments[child]) != 1 || len(h.app.Comments) != 1 || h.app.Comments[0].Pending {
		t.Fatalf("comments = %+v", h.app.Comments)
	}

	h.do((*app.App).Delete, true)
	if _, ok := h.fake.Tasks[child]; ok || slices.Contains(h.rows(), "↳Child") {
		t.Fatal("task not deleted")
	}
}

func TestGoToCustomIDOutsideView(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).GoTo, "https://app.clickup.com/t/T1/DEV-2")
	if h.app.Detail.ID != "t2" || h.ui.focus != app.PanelDetail {
		t.Fatalf("detail = %v, focus = %v", h.app.Detail.ID, h.ui.focus)
	}
}

func TestToggleClosedAndAssignMe(t *testing.T) {
	srv := fake.Basic(5)
	srv.Tasks["t3"].Status = fake.Statuses[2]
	h := newHarness(t, srv, nil).boot()
	if n := len(h.rows()); n != 2 {
		t.Fatalf("%d rows", n)
	}
	h.do((*app.App).ToggleClosed)
	if n := len(h.rows()); n != 3 {
		t.Fatalf("%d rows with closed", n)
	}
	h.do((*app.App).AssignMe) // t1 is assigned to me: unassign
	if a := h.fake.Task("t1").Assignees; len(a) != 0 {
		t.Fatalf("assignees = %v", a)
	}
}

func TestAssignByName(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).Assign, "ali")
	if got := usernames(h.fake.Task("t1").Assignees); !slices.Equal(got, []string{"tester", "alice"}) {
		t.Fatalf("assignees = %v", got)
	}
	h.do((*app.App).Assign, "test")
	if got := usernames(h.fake.Task("t1").Assignees); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("assignees = %v", got)
	}
}

func usernames(users []clickup.User) []string {
	var out []string
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out
}

func TestCustomFields(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	field := func(name string) clickup.CustomField { return h.fake.Field("t1", name) }

	// Menu hotkeys come from the field names: Areas a, Customer facing c, Estimate e,
	// Reviewer r, Score s, Severity v (s is taken).
	h.do((*app.App).EditField, 'v', '2')
	if got := string(field("Severity").Value); got != "1" {
		t.Fatalf("severity = %s, want orderindex 1", got)
	}
	if f, _ := h.app.Detail.Field("f-sev"); strings.TrimSpace(style.Strip(render.FieldText(f))) != "S2" {
		t.Fatalf("local severity = %s", render.FieldText(f))
	}

	h.do((*app.App).EditField, 'a', []string{"frontend", "backend"})
	if got := string(field("Areas").Value); got != `["l1","l2"]` {
		t.Fatalf("areas = %s", got)
	}

	h.do((*app.App).EditField, 'c')
	if got := string(field("Customer facing").Value); got != "true" {
		t.Fatalf("checkbox = %s", got)
	}

	h.do((*app.App).EditField, 'e', "3.5")
	h.do((*app.App).EditField, 'e', nil) // cancelling keeps the value
	if got := string(field("Estimate").Value); got != "3.5" {
		t.Fatalf("estimate = %s", got)
	}
	h.do((*app.App).EditField, 'e', "") // an empty answer clears it
	if f := field("Estimate"); f.IsSet() {
		t.Fatalf("estimate not cleared: %s", f.Value)
	}

	h.do((*app.App).EditField, 's') // formula: read-only
	if !strings.Contains(h.ui.notes[len(h.ui.notes)-1], "can't be edited") {
		t.Fatalf("notes = %v", h.ui.notes)
	}
}

func TestUsersFieldAndFailedFieldUpdateReverts(t *testing.T) {
	srv := fake.Basic(5)
	h := newHarness(t, srv, nil).boot()
	h.do((*app.App).EditField, 'r', []string{"alice"})
	if got := usernames(render.UserValues(new(srv.Field("t1", "Reviewer")))); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("reviewer = %v", got)
	}

	srv.FailUpdates = true
	h.do((*app.App).EditField, 'c')
	if f, _ := h.app.Detail.Field("f-cust"); f.IsSet() {
		t.Fatalf("checkbox not reverted: %s", f.Value)
	}
}

func TestTabsAndJumpToList(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).SwitchTab)
	if h.ui.focus != app.PanelLists {
		t.Fatal("with no list opened yet, switching tabs should point at the lists panel")
	}
	h.do((*app.App).JumpToList, "Product / Backlog")
	if h.app.View.ID != fake.ListID || len(h.rows()) != 5 {
		t.Fatalf("view = %+v", h.app.View)
	}
	h.do((*app.App).SwitchTab)
	if h.app.View.Kind != "my" {
		t.Fatalf("view = %+v", h.app.View)
	}
	h.do((*app.App).SwitchTab)
	if h.app.View.ID != fake.ListID {
		t.Fatalf("view = %+v", h.app.View)
	}
}

func TestCopyMenu(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).CopyMenu, 'm')
	if want := "[Task number 1](https://app.clickup.com/t/t1)"; h.ui.clipboard != want {
		t.Fatalf("clipboard = %q", h.ui.clipboard)
	}
}

// Scrolling through tasks only fetches details for the one you stop on.
func TestDetailFetchIsDebounced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := fake.Basic(5)
		h := newHarness(t, srv, nil)
		h.app.Debounce = 150 * time.Millisecond
		h.boot()
		h.do(func(a *app.App) {
			a.Select(1)
			a.Select(2)
		})
		if n := srv.Count("GET /api/v2/task/t3"); n != 0 {
			t.Fatalf("fetched a task that was only scrolled past (%d times)", n)
		}
		if n := srv.Count("GET /api/v2/task/t5"); n != 1 {
			t.Fatalf("fetched the final task %d times", n)
		}
	})
}

// The create form lists the list's fields with required ones first, and won't create the
// task until they are set; optional properties go into the same request.
func TestNewTaskFormRequiresRequiredFields(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.do(func(a *app.App) { a.NewTask(false) }, "Needs severity", nil) // cancel the Severity menu
	if h.ui.form == nil || len(h.fake.Tasks) != 5 {
		t.Fatalf("task created without its required field (%d tasks)", len(h.fake.Tasks))
	}
	var labels []string
	for _, r := range h.ui.form.Rows() {
		labels = append(labels, r.Label)
	}
	if want := "Status Assignees Priority Due date Severity Areas Customer facing Estimate Reviewer"; strings.Join(labels, " ") != want {
		t.Fatalf("rows = %v", labels)
	}
	if rows := h.ui.form.Rows(); !rows[4].Required || !rows[4].Missing {
		t.Fatalf("severity row = %+v", rows[4])
	}

	// Set optional properties first, then create: enter asks for the required Severity.
	form := h.ui.form
	h.do(func(*app.App) {
		rows := form.Rows()
		rows[2].Edit(func() {}) // Priority
		rows[1].Edit(func() {}) // Assignees
		rows[5].Edit(func() {}) // Areas
	}, '2', []string{"alice"}, []string{"backend"})
	h.do(func(*app.App) {
		missing, ok := form.Submit("Hotfix")
		if ok || missing != 4 {
			t.Errorf("submit = %d, %v", missing, ok)
		}
		form.Rows()[missing].Edit(func() {})
		if _, ok := form.Submit("Hotfix"); !ok {
			t.Error("submit failed with all required fields set")
		}
	}, '1')
	created := h.app.Detail
	server := h.fake.Task(created.ID)
	if server.Name != "Hotfix" || server.Priority == nil || server.Priority.Priority != "high" {
		t.Fatalf("server task = %+v", server)
	}
	if got := usernames(server.Assignees); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("assignees = %v", got)
	}
	if got := string(h.fake.Field(created.ID, "Severity").Value); got != "0" {
		t.Fatalf("severity = %s, want S1 (orderindex 0)", got)
	}
	if got := string(h.fake.Field(created.ID, "Areas").Value); got != `["l2"]` {
		t.Fatalf("areas = %s", got)
	}
}

// Pinned tasks survive restarts: they render from the cache at once and refresh after.
// A pinned task that was deleted in ClickUp gets unpinned.
func TestPinnedTasksPersistAcrossStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c, _ := cache.Open(path)
	h := newHarness(t, fake.Basic(5), c).boot()
	h.do((*app.App).TogglePin) // t1 from Mine
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.do(func(a *app.App) { a.Select(1) }) // t2, not in Mine
	h.do((*app.App).TogglePin)
	c.Close()

	srv := fake.Basic(5)
	srv.Tasks["t2"].Name = "Renamed elsewhere"
	srv.Remove("t1")
	c2, _ := cache.Open(path)
	defer c2.Close()
	h2 := newHarness(t, srv, c2)
	h2.async.UI(func() {
		h2.app.Boot()
		if len(h2.app.Pinned) != 2 || h2.app.Pinned[1].Name != "Task number 2" {
			t.Errorf("pinned from cache = %v", h2.app.Pinned)
		}
	})
	h2.async.wg.Wait()
	if len(h2.app.Pinned) != 1 || h2.app.Pinned[0].Name != "Renamed elsewhere" {
		t.Fatalf("pinned after refresh = %+v", h2.app.Pinned)
	}
}

// wednesday is the fixed "now" of the time tracking tests: Wednesday 23 September 2026 15:00.
var wednesday = time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local)

func at(day int, hour, minute int) time.Time {
	return time.Date(2026, 9, day, hour, minute, 0, 0, time.Local)
}

func durations(entries []clickup.TimeEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, (time.Duration(e.Duration.Int()) * time.Millisecond).String())
	}
	return out
}

func timeHarness(t *testing.T) *harness {
	srv := fake.Basic(5)
	srv.Entries = []clickup.TimeEntry{
		srv.Entry("t1", at(21, 9, 0), 2*time.Hour, "morning"),
		srv.Entry("t1", at(21, 14, 0), time.Hour, "afternoon"),
		srv.Entry("t3", at(22, 10, 0), 30*time.Minute, ""),
		srv.Entry("t3", at(14, 10, 0), time.Hour, "last week"),
	}
	h := newHarness(t, srv, nil)
	h.app.Now = func() time.Time { return wednesday }
	return h.boot()
}

func TestLogTimeOnTask(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).LogTime, "1h30 yesterday fixed login")
	e := h.fake.Entries[len(h.fake.Entries)-1]
	if e.TaskID() != "t1" || e.Description != "fixed login" || !e.StartTime().Equal(at(22, 9, 0)) ||
		time.Duration(e.Duration.Int())*time.Millisecond != 90*time.Minute {
		t.Fatalf("entry = %+v", e)
	}
	h.do((*app.App).LogTime, "45m") // no day: it ends now
	e = h.fake.Entries[len(h.fake.Entries)-1]
	if !e.StartTime().Equal(wednesday.Add(-45 * time.Minute)) {
		t.Fatalf("start = %v", e.StartTime())
	}
}

func TestTimesheetGridAndEditing(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).OpenTimesheet)
	rows, days, total := h.app.SheetRows()
	if len(rows) != 2 || rows[0].TaskID != "t1" || rows[0].Days[0] != 3*time.Hour || rows[1].Days[1] != 30*time.Minute {
		t.Fatalf("rows = %+v", rows)
	}
	if days[0] != 3*time.Hour || total != 3*time.Hour+30*time.Minute || h.app.Sheet.Col != 2 {
		t.Fatalf("days = %v, total = %v, today's column = %d", days, total, h.app.Sheet.Col)
	}

	// One entry in the cell: type new hours.
	h.do(func(a *app.App) { a.Sheet.Row, a.Sheet.Col = 1, 1; a.EditCell() }, "0:45")
	if got := durations(h.fake.Entries); got[2] != "45m0s" {
		t.Fatalf("durations = %v", got)
	}
	// Empty cell: log a new entry there, with a start time and a note.
	h.do(func(a *app.App) { a.Sheet.Col = 2; a.EditCell() }, "1:15 10:00 review")
	e := h.fake.Entries[len(h.fake.Entries)-1]
	if !e.StartTime().Equal(at(23, 10, 0)) || e.Description != "review" || e.TaskID() != "t3" {
		t.Fatalf("new entry = %+v", e)
	}
	// Two entries: pick the second, clear it to delete.
	h.do(func(a *app.App) { a.Sheet.Row, a.Sheet.Col = 0, 0; a.EditCell() }, '2', "")
	if n := len(h.fake.Entries); n != 4 || h.fake.Entries[0].Description != "morning" {
		t.Fatalf("entries = %+v", h.fake.Entries)
	}
	// Moving an entry to another day with a day word keeps its time of day.
	h.do(func(a *app.App) { a.EditCell() }, "2:00 tue")
	if e := h.fake.Entries[0]; !e.StartTime().Equal(at(22, 9, 0)) {
		t.Fatalf("moved entry starts %v", e.StartTime())
	}
	// Clear a cell (confirmed).
	h.do(func(a *app.App) { a.Sheet.Row, a.Sheet.Col = 0, 1; a.ClearCell() }, true)
	if slices.ContainsFunc(h.fake.Entries, func(e clickup.TimeEntry) bool { return e.TaskID() == "t1" && e.StartTime().Day() == 22 }) {
		t.Fatal("cell not cleared")
	}

	h.do(func(a *app.App) { a.ShiftWeek(-1) })
	if rows, _, total := h.app.SheetRows(); len(rows) != 1 || total != time.Hour {
		t.Fatalf("last week = %+v", rows)
	}
	h.do(func(a *app.App) { a.ShiftWeek(0) })
	if !h.app.Sheet.Week.Equal(at(21, 0, 0)) {
		t.Fatalf("this week = %v", h.app.Sheet.Week)
	}
}

func TestTimesheetAddTaskRowAndFailedLogReverts(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).OpenTimesheet)
	h.do((*app.App).AddSheetTask, "Task number 5")
	rows, _, _ := h.app.SheetRows()
	if len(rows) != 3 || rows[h.app.Sheet.Row].TaskID != "t5" {
		t.Fatalf("rows = %+v, selected %d", rows, h.app.Sheet.Row)
	}
	h.fake.FailUpdates = true
	h.do((*app.App).EditCell, "2h")
	if _, _, total := h.app.SheetRows(); total != 3*time.Hour+30*time.Minute {
		t.Fatalf("failed log wasn't reverted: total %v", total)
	}
}

func TestTimerAndTaskTimeMenu(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).ToggleTimer)
	if h.fake.Running == nil || h.app.Timer == nil || !h.app.TimerRunning() || !strings.Contains(style.Strip(h.app.TimerLine()), "DEV-1") {
		t.Fatalf("timer not running: %+v", h.app.Timer)
	}
	h.do((*app.App).ToggleTimer)
	if h.fake.Running != nil || h.app.Timer != nil {
		t.Fatal("timer not stopped")
	}
	if last := h.fake.Entries[len(h.fake.Entries)-1]; last.TaskID() != "t1" || last.Running() {
		t.Fatalf("stopped timer entry = %+v", last)
	}

	// w lists the task's entries, newest first (the stopped timer, afternoon, morning);
	// edit the "morning" one.
	h.do((*app.App).TaskTime, '3', "3:00 08:00 morning standup")
	morning := h.fake.Entries[0]
	if morning.Description != "morning standup" || !morning.StartTime().Equal(at(21, 8, 0)) ||
		time.Duration(morning.Duration.Int())*time.Millisecond != 3*time.Hour {
		t.Fatalf("edited entry = %+v", morning)
	}
}
