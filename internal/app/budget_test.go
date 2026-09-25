package app_test

import (
	"sort"
	"strings"
	"testing"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/fake"
)

// requests counts the fake server's requests since the given point, grouped by endpoint.
func requests(srv *fake.Server, from int) (int, string) {
	counts := map[string]int{}
	for _, r := range srv.Requests[from:] {
		parts := strings.Split(r, "/")
		for i, p := range parts { // /task/t3/comment → /task/{id}/comment
			if i > 0 && (strings.HasPrefix(p, "t") && len(p) <= 4 || strings.HasPrefix(p, "c") && len(p) <= 3) {
				parts[i] = "{id}"
			}
		}
		counts[strings.Join(parts, "/")]++
	}
	var lines []string
	for k, v := range counts {
		lines = append(lines, "  "+strings.Repeat(" ", 3-len(itoa(v)))+itoa(v)+" "+k)
	}
	sort.Strings(lines)
	return len(srv.Requests) - from, strings.Join(lines, "\n")
}

func itoa(n int) string {
	s := ""
	for ; n > 0 || s == ""; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

// A session's API budget: start, look at ten tasks, go back to the first, sit for five minutes.
func TestRequestBudget(t *testing.T) {
	srv := fake.Basic(40)
	h := newHarness(t, srv, nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	n, detail := requests(srv, 0)
	t.Logf("start and open a list: %d requests\n%s", n, detail)

	mark := len(srv.Requests)
	for i := range 10 {
		h.do(func(a *app.App) { a.Select(i) })
	}
	n, detail = requests(srv, mark)
	t.Logf("look at 10 tasks: %d requests\n%s", n, detail)
	mark = len(srv.Requests)
	for i := range 10 {
		h.do(func(a *app.App) { a.Select(9 - i) })
	}
	n, detail = requests(srv, mark)
	t.Logf("and back up through the same 10: %d requests\n%s", n, detail)
	if n != 0 {
		t.Errorf("revisiting tasks seen a moment ago cost %d requests, want 0", n)
	}

	for i := range 3 {
		h.do(func(a *app.App) { a.Select(i * 2) })
		h.do((*app.App).TogglePin)
	}
	mark = len(srv.Requests)
	for range 5 {
		h.do((*app.App).AutoRefresh)
	}
	n, detail = requests(srv, mark)
	t.Logf("five minutes idle with 3 pinned: %d requests\n%s", n, detail)
	if n != 5 { // one list refresh a minute; the pinned tasks come with it
		t.Errorf("five idle minutes cost %d requests, want 5", n)
	}
}
