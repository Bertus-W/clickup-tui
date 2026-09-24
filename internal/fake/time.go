package fake

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
)

// Entry makes a finished time entry on a task.
func (s *Server) Entry(taskID string, start time.Time, d time.Duration, note string) clickup.TimeEntry {
	s.nextEntry++
	e := clickup.TimeEntry{
		ID: clickup.FlexString("e" + strconv.Itoa(s.nextEntry)), User: Me, Description: note,
		Start:    clickup.FlexString(strconv.FormatInt(start.UnixMilli(), 10)),
		End:      clickup.FlexString(strconv.FormatInt(start.Add(d).UnixMilli(), 10)),
		Duration: clickup.FlexString(strconv.FormatInt(d.Milliseconds(), 10)),
	}
	if t := s.Tasks[taskID]; t != nil {
		e.Task = &clickup.EntryTask{ID: clickup.FlexString(t.ID), CustomID: t.CustomID, Name: t.Name, Status: t.Status}
		e.Location.ListName = t.List.Name
	}
	return e
}

func (s *Server) entry(id string) int {
	return slices.IndexFunc(s.Entries, func(e clickup.TimeEntry) bool { return string(e.ID) == id })
}

func (s *Server) timeRoutes(handle func(string, func(http.ResponseWriter, *http.Request) any)) {
	const p = "/api/v2/team/{team}/time_entries"
	handle("GET "+p, func(_ http.ResponseWriter, r *http.Request) any {
		q := r.URL.Query()
		from, _ := strconv.ParseInt(q.Get("start_date"), 10, 64)
		to, _ := strconv.ParseInt(q.Get("end_date"), 10, 64)
		out := []clickup.TimeEntry{}
		for _, e := range s.Entries {
			if start := e.Start.Int(); start >= from && start < to && (q.Get("task_id") == "" || e.TaskID() == q.Get("task_id")) {
				out = append(out, e)
			}
		}
		return map[string]any{"data": out}
	})
	handle("POST "+p, func(_ http.ResponseWriter, r *http.Request) any {
		if s.FailUpdates {
			return apiErr{400, "nope"}
		}
		var c clickup.EntryChange
		_ = json.NewDecoder(r.Body).Decode(&c)
		if s.Tasks[c.TaskID] == nil {
			return apiErr{404, "Task not found"}
		}
		e := s.Entry(c.TaskID, time.UnixMilli(c.Start), time.Duration(c.Duration)*time.Millisecond, c.Description)
		s.Entries = append(s.Entries, e)
		return map[string]any{"data": e}
	})
	handle("PUT "+p+"/{id}", func(_ http.ResponseWriter, r *http.Request) any {
		i := s.entry(r.PathValue("id"))
		if i < 0 {
			return apiErr{404, "not found"}
		}
		if s.FailUpdates {
			return apiErr{400, "nope"}
		}
		var c clickup.EntryChange
		_ = json.NewDecoder(r.Body).Decode(&c)
		e := &s.Entries[i]
		e.Start = clickup.FlexString(strconv.FormatInt(c.Start, 10))
		e.End = clickup.FlexString(strconv.FormatInt(c.End, 10))
		e.Duration = clickup.FlexString(strconv.FormatInt(c.Duration, 10))
		e.Description = c.Description
		return map[string]any{"data": []clickup.TimeEntry{*e}}
	})
	handle("DELETE "+p+"/{id}", func(_ http.ResponseWriter, r *http.Request) any {
		i := s.entry(r.PathValue("id"))
		if i < 0 {
			return apiErr{404, "not found"}
		}
		s.Entries = slices.Delete(s.Entries, i, i+1)
		return map[string]any{"data": []any{}}
	})
	handle("GET "+p+"/current", func(http.ResponseWriter, *http.Request) any {
		return map[string]any{"data": s.Running}
	})
	handle("POST "+p+"/start", func(_ http.ResponseWriter, r *http.Request) any {
		var body struct {
			TID string `json:"tid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.stop()
		now := s.Now()
		e := s.Entry(body.TID, now, 0, "")
		e.Duration = clickup.FlexString(strconv.FormatInt(-now.UnixMilli(), 10))
		e.End = ""
		s.Running = &e
		return map[string]any{"data": e}
	})
	handle("POST "+p+"/stop", func(http.ResponseWriter, *http.Request) any {
		if s.Running == nil {
			return apiErr{400, "No timer running"}
		}
		return map[string]any{"data": s.stop()}
	})
}

// stop turns the running timer into a finished entry (at least a minute, to be visible).
func (s *Server) stop() clickup.TimeEntry {
	if s.Running == nil {
		return clickup.TimeEntry{}
	}
	e := *s.Running
	d := max(s.Now().Sub(e.StartTime()), time.Minute)
	e.Duration = clickup.FlexString(strconv.FormatInt(d.Milliseconds(), 10))
	e.End = clickup.FlexString(strconv.FormatInt(e.StartTime().Add(d).UnixMilli(), 10))
	s.Entries = append(s.Entries, e)
	s.Running = nil
	return e
}
