// Package fake is an in-memory ClickUp API used by the tests and by `cu demo`.
package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
)

var (
	Me    = clickup.User{ID: 42, Username: "tester", Email: "t@example.com", Color: "#ff00ff", Initials: "TE"}
	Other = clickup.User{ID: 7, Username: "alice", Email: "a@example.com", Color: "#00ffff", Initials: "AL"}

	Statuses = []clickup.Status{
		{ID: "st1", Status: "to do", Type: "open", OrderIndex: 0, Color: "#87909e"},
		{ID: "st2", Status: "in progress", Type: "custom", OrderIndex: 1, Color: "#1090e0"},
		{ID: "st3", Status: "complete", Type: "closed", OrderIndex: 2, Color: "#008844"},
	}
)

const (
	TeamID  = "T1"
	SpaceID = "S1"
	ListID  = "L1"
)

type Server struct {
	mu          sync.Mutex
	order       []string
	Tasks       map[string]*clickup.Task
	Comments    map[string][]clickup.Comment
	Fields      []clickup.CustomField // field definitions on the list
	Requests    []string
	FailUpdates bool
	FailField   string              // task updates that set this field fail
	Entries     []clickup.TimeEntry // time tracking
	Running     *clickup.TimeEntry
	Mentions    []clickup.CommentPart // the mentions of every comment posted
	nextEntry   int

	mux *http.ServeMux
}

// New starts a server with the given tasks.
func New(tasks ...clickup.Task) *Server {
	s := &Server{Tasks: map[string]*clickup.Task{}, Comments: map[string][]clickup.Comment{}}
	for _, t := range tasks {
		s.add(t)
	}
	s.mux = http.NewServeMux()
	s.routes(s.mux)
	return s
}

// roundTripper serves requests in memory: no sockets, so it also works inside testing/synctest.
type roundTripper struct{ h http.Handler }

func (rt roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	rec := httptest.NewRecorder()
	rt.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

// Client returns an API client wired straight into this server.
func (s *Server) Client() *clickup.Client {
	c := clickup.New("pk_test")
	c.BaseURL = "http://clickup.fake/api/v2"
	c.HTTP = &http.Client{Transport: roundTripper{s.mux}}
	return c
}

// Requests made so far matching method and path.
func (s *Server) Count(request string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.Requests {
		if r == request {
			n++
		}
	}
	return n
}

// Task returns a copy of a task's server-side state.
func (s *Server) Task(id string) clickup.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.Tasks[id]
}

// Remove deletes a task server-side, as if someone deleted it in ClickUp.
func (s *Server) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Tasks, id)
	s.order = slices.DeleteFunc(s.order, func(x string) bool { return x == id })
}

// Field returns a task's server-side custom field.
func (s *Server) Field(taskID, name string) clickup.CustomField {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.Tasks[taskID].CustomFields {
		if f.Name == name {
			return f
		}
	}
	return clickup.CustomField{}
}

func (s *Server) add(t clickup.Task) {
	if t.List.ID == "" {
		t.List = clickup.Ref{ID: ListID, Name: "Backlog"}
	}
	if t.DateUpdated == "" {
		t.DateUpdated = "1700000000000"
	}
	if t.URL == "" {
		t.URL = "https://app.clickup.com/t/" + t.ID
	}
	if t.Assignees == nil {
		t.Assignees = []clickup.User{}
	}
	s.Tasks[t.ID] = &t
	s.order = append(s.order, t.ID)
}

func (s *Server) routes(mux *http.ServeMux) {
	handle := func(pattern string, h func(w http.ResponseWriter, r *http.Request) any) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
			out := h(w, r)
			if e, ok := out.(apiErr); ok {
				w.WriteHeader(e.status)
				out = map[string]string{"err": e.msg}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		})
	}
	const p = "/api/v2"
	handle("GET "+p+"/user", func(http.ResponseWriter, *http.Request) any { return map[string]any{"user": Me} })
	handle("GET "+p+"/team", func(http.ResponseWriter, *http.Request) any {
		return map[string]any{"teams": []clickup.Team{{ID: TeamID, Name: "Acme", Members: []clickup.Member{{User: Me}, {User: Other}}}}}
	})
	handle("GET "+p+"/team/{team}/space", func(http.ResponseWriter, *http.Request) any {
		return map[string]any{"spaces": []map[string]string{{"id": SpaceID, "name": "Engineering"}}}
	})
	handle("GET "+p+"/space/{space}/folder", func(http.ResponseWriter, *http.Request) any {
		return map[string]any{"folders": []clickup.Folder{{ID: "F1", Name: "Product", Lists: []clickup.ListRef{{ID: ListID, Name: "Backlog"}}}}}
	})
	handle("GET "+p+"/space/{space}/list", func(http.ResponseWriter, *http.Request) any {
		return map[string]any{"lists": []clickup.ListRef{{ID: "L2", Name: "Inbox"}}}
	})
	handle("GET "+p+"/list/{list}", func(_ http.ResponseWriter, r *http.Request) any {
		return clickup.List{ID: clickup.FlexString(r.PathValue("list")), Name: "Backlog", Statuses: Statuses}
	})
	handle("GET "+p+"/list/{list}/task", func(_ http.ResponseWriter, r *http.Request) any {
		inList := func(t *clickup.Task) bool { return string(t.List.ID) == r.PathValue("list") }
		return map[string]any{"tasks": s.visible(r, inList), "last_page": true}
	})
	handle("PUT /api/v3/workspaces/{team}/tasks/{id}/home_list/{list}", func(_ http.ResponseWriter, r *http.Request) any {
		t := s.Tasks[r.PathValue("id")]
		if t == nil {
			return apiErr{404, "Task not found"}
		}
		if s.FailUpdates {
			return apiErr{400, "nope"}
		}
		list := r.PathValue("list")
		t.List = clickup.Ref{ID: clickup.FlexString(list), Name: map[string]string{ListID: "Backlog", "L2": "Inbox"}[list]}
		return map[string]any{}
	})
	handle("GET "+p+"/team/{team}/task", func(_ http.ResponseWriter, r *http.Request) any {
		uid, _ := strconv.ParseInt(r.URL.Query().Get("assignees[]"), 10, 64)
		mine := func(t *clickup.Task) bool {
			return slices.ContainsFunc(t.Assignees, func(u clickup.User) bool { return u.ID == uid })
		}
		return map[string]any{"tasks": s.visible(r, mine), "last_page": true}
	})
	handle("POST "+p+"/list/{list}/task", func(_ http.ResponseWriter, r *http.Request) any {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		var fields struct {
			Parent       clickup.FlexString `json:"parent"`
			CustomFields []struct {
				ID    string          `json:"id"`
				Value json.RawMessage `json:"value"`
			} `json:"custom_fields"`
		}
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &fields)
		id := fmt.Sprintf("t%d", len(s.order)+1)
		s.add(clickup.Task{ID: id, CustomID: fmt.Sprintf("DEV-%d", len(s.order)+1), Status: Statuses[0],
			Parent: fields.Parent, OrderIndex: clickup.FlexFloat(len(s.order) + 1), CustomFields: slices.Clone(s.Fields)})
		t := s.Tasks[id]
		if statuses, ok := body["status"]; ok {
			body["status"] = statuses
		}
		if assignees, ok := body["assignees"]; ok { // create takes plain ids, update takes add/rem
			body["assignees"] = json.RawMessage(`{"add":` + string(assignees) + `,"rem":[]}`)
		}
		s.update(t, body)
		for _, cf := range fields.CustomFields {
			if f, ok := t.Field(cf.ID); ok {
				setFieldValue(f, cf.Value)
			}
		}
		return t
	})
	handle("GET "+p+"/task/{id}", func(_ http.ResponseWriter, r *http.Request) any {
		t := s.lookup(r)
		if t == nil {
			return apiErr{404, "Task not found"}
		}
		out := *t
		out.Subtasks = []clickup.Task{}
		for _, id := range s.order {
			if string(s.Tasks[id].Parent) == t.ID {
				out.Subtasks = append(out.Subtasks, *s.Tasks[id])
			}
		}
		return out
	})
	handle("DELETE "+p+"/task/{id}", func(_ http.ResponseWriter, r *http.Request) any {
		id := r.PathValue("id")
		delete(s.Tasks, id)
		s.order = slices.DeleteFunc(s.order, func(x string) bool { return x == id })
		return map[string]any{}
	})
	handle("PUT "+p+"/task/{id}", func(_ http.ResponseWriter, r *http.Request) any {
		t := s.lookup(r)
		if t == nil {
			return apiErr{404, "Task not found"}
		}
		if s.FailUpdates {
			return apiErr{400, "nope"}
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body[s.FailField]; ok {
			return apiErr{400, "nope"}
		}
		s.update(t, body)
		t.DateUpdated = clickup.FlexString(strconv.FormatInt(time.Now().UnixMilli(), 10))
		out := *t
		out.MarkdownDescription = "" // like the real API, updates don't echo rendered markdown
		return out
	})
	handle("GET "+p+"/task/{id}/comment", func(_ http.ResponseWriter, r *http.Request) any {
		comments := s.Comments[r.PathValue("id")]
		if comments == nil {
			comments = []clickup.Comment{}
		}
		return map[string]any{"comments": comments}
	})
	handle("POST "+p+"/task/{id}/comment", func(_ http.ResponseWriter, r *http.Request) any {
		var body struct {
			Text  string                `json:"comment_text"`
			Parts []clickup.CommentPart `json:"comment"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Rich comments, the way ClickUp stores them: the parts in order with each mention's
		// user filled in, and a comment_text that has the mentions at the end.
		var mentions string
		if body.Parts != nil {
			body.Text = ""
		}
		for i, part := range body.Parts {
			if part.Type == "tag" && part.User != nil {
				name := userByID(part.User.ID).Username
				body.Parts[i].Text, body.Parts[i].User.Username = "@"+name, name
				mentions += "@" + name
			} else {
				body.Text += part.Text
			}
		}
		body.Text += mentions
		s.Mentions = slices.Concat(s.Mentions, slices.DeleteFunc(slices.Clone(body.Parts), func(p clickup.CommentPart) bool { return p.Type != "tag" }))
		id := r.PathValue("id")
		c := clickup.Comment{ID: clickup.FlexString(fmt.Sprint(len(s.Comments[id]) + 1)), CommentText: body.Text, Parts: body.Parts, User: Me,
			Date: clickup.FlexString(strconv.FormatInt(time.Now().UnixMilli(), 10))}
		s.Comments[id] = append(s.Comments[id], c)
		return map[string]any{"id": c.ID}
	})
	handle("POST "+p+"/task/{id}/field/{field}", func(_ http.ResponseWriter, r *http.Request) any {
		if s.FailUpdates {
			return apiErr{400, "nope"}
		}
		var body struct {
			Value json.RawMessage `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f := s.field(r)
		if f == nil {
			return apiErr{404, "field not found"}
		}
		setFieldValue(f, body.Value)
		return map[string]any{}
	})
	handle("DELETE "+p+"/task/{id}/field/{field}", func(_ http.ResponseWriter, r *http.Request) any {
		if s.FailUpdates {
			return apiErr{400, "nope"}
		}
		if f := s.field(r); f != nil {
			f.Value = nil
		}
		return map[string]any{}
	})
	handle("GET "+p+"/list/{list}/field", func(http.ResponseWriter, *http.Request) any {
		return map[string]any{"fields": s.Fields}
	})
	s.timeRoutes(handle)
}

// setFieldValue stores a value the way the real API reports it back.
func setFieldValue(f *clickup.CustomField, value json.RawMessage) {
	{
		body := struct{ Value json.RawMessage }{value}
		switch f.Type {
		case "drop_down": // the real API reports dropdown values by orderindex
			var id string
			_ = json.Unmarshal(body.Value, &id)
			for _, o := range f.TypeConfig.Options {
				if o.ID == id {
					f.Value = json.RawMessage(strconv.Itoa(int(o.OrderIndex)))
				}
			}
		case "users":
			var change struct{ Add, Rem []int64 }
			_ = json.Unmarshal(body.Value, &change)
			var users []clickup.User
			_ = json.Unmarshal(f.Value, &users)
			users = slices.DeleteFunc(users, func(u clickup.User) bool { return slices.Contains(change.Rem, u.ID) })
			for _, id := range change.Add {
				users = append(users, userByID(id))
			}
			f.Value, _ = json.Marshal(users)
		default:
			f.Value = body.Value
		}
	}
}

type apiErr struct {
	status int
	msg    string
}

func userByID(id int64) clickup.User {
	if id == Other.ID {
		return Other
	}
	return Me
}

func (s *Server) visible(r *http.Request, keep func(*clickup.Task) bool) []clickup.Task {
	closed := r.URL.Query().Get("include_closed") == "true"
	out := []clickup.Task{}
	for _, id := range s.order {
		t := s.Tasks[id]
		if keep(t) && (closed || !t.Status.Closed()) {
			out = append(out, *t)
		}
	}
	return out
}

func (s *Server) lookup(r *http.Request) *clickup.Task {
	id := r.PathValue("id")
	if r.URL.Query().Get("custom_task_ids") == "true" {
		for _, t := range s.Tasks {
			if t.CustomID == id {
				return t
			}
		}
	}
	return s.Tasks[id]
}

func (s *Server) field(r *http.Request) *clickup.CustomField {
	t := s.Tasks[r.PathValue("id")]
	if t == nil {
		return nil
	}
	f, _ := t.Field(r.PathValue("field"))
	return f
}

func (s *Server) update(t *clickup.Task, body map[string]json.RawMessage) {
	if raw, ok := body["status"]; ok {
		var name string
		_ = json.Unmarshal(raw, &name)
		for _, st := range Statuses {
			if st.Status == name {
				t.Status = st
			}
		}
	}
	if raw, ok := body["name"]; ok {
		_ = json.Unmarshal(raw, &t.Name)
	}
	if raw, ok := body["markdown_content"]; ok {
		_ = json.Unmarshal(raw, &t.MarkdownDescription)
	}
	if raw, ok := body["priority"]; ok {
		var p *int
		_ = json.Unmarshal(raw, &p)
		t.Priority = nil
		if p != nil {
			name := []string{"", "urgent", "high", "normal", "low"}[*p]
			t.Priority = &clickup.Priority{ID: clickup.FlexString(strconv.Itoa(*p)), Priority: name, Color: "#ffffff"}
		}
	}
	if raw, ok := body["due_date"]; ok {
		var due clickup.FlexString
		_ = json.Unmarshal(raw, &due)
		t.DueDate = due
	}
	if raw, ok := body["assignees"]; ok {
		var change struct{ Add, Rem []int64 }
		_ = json.Unmarshal(raw, &change)
		t.Assignees = slices.DeleteFunc(t.Assignees, func(u clickup.User) bool { return slices.Contains(change.Rem, u.ID) })
		for _, id := range change.Add {
			t.Assignees = append(t.Assignees, userByID(id))
		}
	}
}
