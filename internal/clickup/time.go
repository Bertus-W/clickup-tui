package clickup

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
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
	TaskURL string `json:"task_url,omitzero"`
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

// TimeEntries lists my time entries between from and to; taskID narrows it to one task.
func (c *Client) TimeEntries(ctx context.Context, teamID string, from, to time.Time, taskID string) ([]TimeEntry, error) {
	q := url.Values{"start_date": {ms(from)}, "end_date": {ms(to)}}
	if taskID != "" {
		q.Set("task_id", taskID)
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
