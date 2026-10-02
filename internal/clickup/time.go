package clickup

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TimeEntry is a tracked time interval. A running timer has a negative duration.
type TimeEntry struct {
	ID          FlexString `json:"id"`
	Task        *EntryTask `json:"task,omitzero"`
	User        User       `json:"user"`
	Billable    bool       `json:"billable"`
	Start       FlexString `json:"start"`
	End         FlexString `json:"end,omitzero"`
	Duration    FlexString `json:"duration"`
	Description string     `json:"description,omitzero"`
	Location    struct {
		ListID   FlexString `json:"list_id,omitzero"`
		ListName string     `json:"list_name,omitzero"`
	} `json:"task_location,omitzero"`
	TaskURL string     `json:"task_url,omitzero"`
	At      FlexString `json:"at,omitzero"` // when it was logged (or last changed)
}

type EntryTask struct {
	ID       FlexString `json:"id"`
	CustomID string     `json:"custom_id,omitzero"`
	Name     string     `json:"name"`
	Status   Status     `json:"status,omitzero"`
}

func (e TimeEntry) StartTime() time.Time { return time.UnixMilli(e.Start.Int()) }

// Length is the tracked duration; for a running timer, the time since it started.
func (e TimeEntry) Length(now time.Time) time.Duration {
	if d := e.Duration.Int(); d >= 0 {
		return time.Duration(d) * time.Millisecond
	}
	return now.Sub(e.StartTime())
}

func (e TimeEntry) Running() bool { return e.Duration.Int() < 0 }

func (e TimeEntry) TaskID() string {
	if e.Task == nil {
		return ""
	}
	return string(e.Task.ID)
}

func ms(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

// TimeEntries lists time entries between from and to, of one task if taskID is set. They're my
// own, unless assignees says whose (seeing others' needs a workspace admin or owner).
func (c *Client) TimeEntries(ctx context.Context, teamID string, from, to time.Time, taskID string, assignees ...int64) ([]TimeEntry, error) {
	q := url.Values{"start_date": {ms(from)}, "end_date": {ms(to)}, "include_location_names": {"true"}}
	if taskID != "" {
		q.Set("task_id", taskID)
	}
	if len(assignees) > 0 {
		ids := make([]string, len(assignees))
		for i, id := range assignees {
			ids[i] = strconv.FormatInt(id, 10)
		}
		q.Set("assignee", strings.Join(ids, ","))
	}
	var out struct{ Data []TimeEntry }
	err := c.do(ctx, http.MethodGet, "/team/"+teamID+"/time_entries", q, nil, &out)
	return nonNil(out.Data), err
}

// EntryChange is what CreateTimeEntry and UpdateTimeEntry send.
type EntryChange struct {
	TaskID      string `json:"tid,omitzero"`
	Start       int64  `json:"start"`
	End         int64  `json:"end,omitzero"`
	Duration    int64  `json:"duration"`
	Description string `json:"description"`
	Billable    bool   `json:"billable"`
}

func (c *Client) CreateTimeEntry(ctx context.Context, teamID string, e EntryChange) (TimeEntry, error) {
	var out struct{ Data TimeEntry }
	err := c.do(ctx, http.MethodPost, "/team/"+teamID+"/time_entries", nil, e, &out)
	return out.Data, err
}

func (c *Client) UpdateTimeEntry(ctx context.Context, teamID, entryID string, e EntryChange) error {
	return c.do(ctx, http.MethodPut, "/team/"+teamID+"/time_entries/"+entryID, nil, e, nil)
}

func (c *Client) DeleteTimeEntry(ctx context.Context, teamID, entryID string) error {
	return c.do(ctx, http.MethodDelete, "/team/"+teamID+"/time_entries/"+entryID, nil, nil, nil)
}

// CurrentTimer returns the running timer, or nil.
func (c *Client) CurrentTimer(ctx context.Context, teamID string) (*TimeEntry, error) {
	var out struct{ Data *TimeEntry }
	err := c.do(ctx, http.MethodGet, "/team/"+teamID+"/time_entries/current", nil, nil, &out)
	if out.Data != nil && out.Data.ID == "" {
		out.Data = nil
	}
	return out.Data, err
}

func (c *Client) StartTimer(ctx context.Context, teamID, taskID string) (TimeEntry, error) {
	var out struct{ Data TimeEntry }
	err := c.do(ctx, http.MethodPost, "/team/"+teamID+"/time_entries/start", nil, map[string]any{"tid": taskID}, &out)
	return out.Data, err
}

func (c *Client) StopTimer(ctx context.Context, teamID string) (TimeEntry, error) {
	var out struct{ Data TimeEntry }
	err := c.do(ctx, http.MethodPost, "/team/"+teamID+"/time_entries/stop", nil, nil, &out)
	return out.Data, err
}

// StatusTime is a status a task has been in: since when, and for how long in all.
type StatusTime struct {
	Status    string `json:"status"`
	Color     string `json:"color"`
	Type      string `json:"type"`
	TotalTime struct {
		ByMinute FlexFloat  `json:"by_minute"`
		Since    FlexString `json:"since"`
	} `json:"total_time"`
}

// TimeInStatus is a task's status history (ClickUp's Time in Status, on paid plans only).
type TimeInStatus struct {
	Current StatusTime   `json:"current_status"`
	History []StatusTime `json:"status_history"`
}

func (c *Client) TimeInStatus(ctx context.Context, taskID string) (TimeInStatus, error) {
	var out TimeInStatus
	err := c.do(ctx, http.MethodGet, "/task/"+taskID+"/time_in_status", nil, nil, &out)
	return out, err
}

// NotOnPlan reports whether err means the workspace's plan doesn't include the feature.
func NotOnPlan(err error) bool {
	e, ok := errors.AsType[*APIError](err)
	return ok && e.Status == http.StatusForbidden && strings.Contains(strings.ToLower(e.Message), "plan")
}
