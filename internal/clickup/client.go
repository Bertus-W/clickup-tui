package clickup

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultBaseURL = "https://api.clickup.com/api/v2"

var customIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-\d+$`)

// APIError is returned for non-2xx responses. Match it with errors.AsType[*clickup.APIError].
type APIError struct {
	Method, Path string
	Status       int
	Message      string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%d: %s", e.Status, e.Message)
}

// RequestLog describes one HTTP exchange; the TUI shows these in its command log.
type RequestLog struct {
	Method, Path string
	Status       int  // 0 on network errors
	Canceled     bool // superseded by newer work (e.g. scrolling on); not a failure
	Duration     time.Duration
}

type Client struct {
	BaseURL   string
	HTTP      *http.Client
	OnRequest func(RequestLog)

	token string
	sem   chan struct{}
	sleep func(context.Context, time.Duration) error
}

func New(token string) *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		token:   token,
		sem:     make(chan struct{}, 8),
		sleep:   sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do performs a request with retries on network errors, 429 and 5xx.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	// A POST that failed after it may have reached ClickUp is not retried: that could create
	// the task, comment or time entry twice. A 429 means it was rejected, so that's safe.
	idempotent := method != http.MethodPost
	const attempts = 4
	for attempt := range attempts {
		last := attempt == attempts-1
		backoff := time.Duration(400<<attempt) * time.Millisecond

		status, data, header, err := c.once(ctx, method, path, query, payload)
		switch {
		case err != nil:
			if last || ctx.Err() != nil || !idempotent {
				return fmt.Errorf("network error: %w", err)
			}
		case status == http.StatusTooManyRequests && !last:
			if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				backoff = min(max(time.Until(time.Unix(reset, 0)), time.Second), 30*time.Second)
			}
		case status >= 500 && !last && idempotent:
		case status >= 400:
			var e struct {
				Err string `json:"err"`
			}
			_ = json.Unmarshal(data, &e)
			msg := e.Err
			if msg == "" {
				msg = string(data[:min(len(data), 200)])
			}
			return &APIError{Method: method, Path: path, Status: status, Message: msg}
		default:
			if out == nil || len(data) == 0 {
				return nil
			}
			return json.Unmarshal(data, out)
		}
		if err := c.sleep(ctx, backoff); err != nil {
			return err
		}
	}
	return fmt.Errorf("%s %s: retries exhausted", method, path)
}

func (c *Client) once(ctx context.Context, method, path string, query url.Values, payload []byte) (int, []byte, http.Header, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}
	u := c.BaseURL + path
	if strings.HasPrefix(path, "/v3/") { // the few v3 endpoints: .../api/v2 → .../api/v3/...
		u = strings.TrimSuffix(c.BaseURL, "/v2") + path
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.log(RequestLog{Method: method, Path: path, Duration: time.Since(start), Canceled: ctx.Err() != nil})
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	c.log(RequestLog{Method: method, Path: path, Status: resp.StatusCode, Duration: time.Since(start)})
	return resp.StatusCode, data, resp.Header, err
}

func (c *Client) log(entry RequestLog) {
	if c.OnRequest != nil {
		c.OnRequest(entry)
	}
}

// --- identity / hierarchy -----------------------------------------------------------

func (c *Client) User(ctx context.Context) (User, error) {
	var out struct{ User User }
	err := c.do(ctx, http.MethodGet, "/user", nil, nil, &out)
	return out.User, err
}

func (c *Client) Teams(ctx context.Context) ([]Team, error) {
	var out struct{ Teams []Team }
	err := c.do(ctx, http.MethodGet, "/team", nil, nil, &out)
	return out.Teams, err
}

var notArchived = url.Values{"archived": {"false"}}

func (c *Client) Spaces(ctx context.Context, teamID string) ([]Space, error) {
	var out struct{ Spaces []Space }
	err := c.do(ctx, http.MethodGet, "/team/"+teamID+"/space", notArchived, nil, &out)
	return out.Spaces, err
}

func (c *Client) Folders(ctx context.Context, spaceID string) ([]Folder, error) {
	var out struct{ Folders []Folder }
	err := c.do(ctx, http.MethodGet, "/space/"+spaceID+"/folder", notArchived, nil, &out)
	return out.Folders, err
}

func (c *Client) FolderlessLists(ctx context.Context, spaceID string) ([]ListRef, error) {
	var out struct{ Lists []ListRef }
	err := c.do(ctx, http.MethodGet, "/space/"+spaceID+"/list", notArchived, nil, &out)
	return out.Lists, err
}

// Hierarchy fetches spaces, then every space's folders and lists concurrently.
func (c *Client) Hierarchy(ctx context.Context, teamID string) ([]Space, error) {
	spaces, err := c.Spaces(ctx, teamID)
	if err != nil {
		return nil, err
	}
	errs := make([]error, len(spaces))
	var wg sync.WaitGroup
	for i := range spaces {
		wg.Go(func() {
			space := &spaces[i]
			var folderErr, listErr error
			var inner sync.WaitGroup
			inner.Go(func() { space.Folders, folderErr = c.Folders(ctx, space.ID) })
			inner.Go(func() { space.Lists, listErr = c.FolderlessLists(ctx, space.ID) })
			inner.Wait()
			errs[i] = cmp.Or(folderErr, listErr)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	for i := range spaces {
		spaces[i].Folders = nonNil(spaces[i].Folders)
		spaces[i].Lists = nonNil(spaces[i].Lists)
	}
	return spaces, nil
}

func nonNil[S ~[]E, E any](s S) S {
	if s == nil {
		return S{}
	}
	return s
}

func (c *Client) GetList(ctx context.Context, listID string) (List, error) {
	var out List
	err := c.do(ctx, http.MethodGet, "/list/"+listID, nil, nil, &out)
	return out, err
}

func (c *Client) CreateList(ctx context.Context, spaceID, name, content string) (List, error) {
	var out List
	err := c.do(ctx, http.MethodPost, "/space/"+spaceID+"/list", nil, map[string]string{"name": name, "content": content}, &out)
	return out, err
}

func (c *Client) ListFields(ctx context.Context, listID string) ([]CustomField, error) {
	var out struct{ Fields []CustomField }
	err := c.do(ctx, http.MethodGet, "/list/"+listID+"/field", nil, nil, &out)
	return out.Fields, err
}

// CreateField creates a custom field on a list. Not in ClickUp's published docs, but supported.
func (c *Client) CreateField(ctx context.Context, listID string, field CustomField) error {
	return c.do(ctx, http.MethodPost, "/list/"+listID+"/field", nil, field, nil)
}

// --- tasks ---------------------------------------------------------------------------

type pageResponse struct {
	Tasks    []Task `json:"tasks"`
	LastPage *bool  `json:"last_page"`
}

// pages iterates over a paginated task endpoint, yielding one page at a time.
func (c *Client) pages(ctx context.Context, path string, query url.Values) iter.Seq2[[]Task, error] {
	return func(yield func([]Task, error) bool) {
		for page := 0; ; page++ {
			q := maps.Clone(query)
			q.Set("page", strconv.Itoa(page))
			var out pageResponse
			if err := c.do(ctx, http.MethodGet, path, q, nil, &out); err != nil {
				yield(nil, err)
				return
			}
			if !yield(out.Tasks, nil) {
				return
			}
			if len(out.Tasks) == 0 || out.LastPage == nil || *out.LastPage {
				return
			}
		}
	}
}

func taskQuery(includeClosed bool) url.Values {
	return url.Values{
		"subtasks":                     {"true"},
		"include_closed":               {strconv.FormatBool(includeClosed)},
		"include_markdown_description": {"true"},
	}
}

// ListTasks iterates over the pages of tasks in a list (including subtasks).
func (c *Client) ListTasks(ctx context.Context, listID string, includeClosed bool) iter.Seq2[[]Task, error] {
	return c.pages(ctx, "/list/"+listID+"/task", taskQuery(includeClosed))
}

// AssignedTasks iterates over the pages of tasks assigned to a user across the workspace.
func (c *Client) AssignedTasks(ctx context.Context, teamID string, userID int64, includeClosed bool) iter.Seq2[[]Task, error] {
	q := taskQuery(includeClosed)
	q.Set("assignees[]", strconv.FormatInt(userID, 10))
	return c.pages(ctx, "/team/"+teamID+"/task", q)
}

// GetTask fetches a task by id, or by custom id (ABC-123) when teamID is given.
func (c *Client) GetTask(ctx context.Context, taskID, teamID string) (Task, error) {
	q := url.Values{"include_markdown_description": {"true"}, "include_subtasks": {"true"}}
	if teamID != "" && customIDPattern.MatchString(taskID) {
		q.Set("custom_task_ids", "true")
		q.Set("team_id", teamID)
	}
	var out Task
	err := c.do(ctx, http.MethodGet, "/task/"+taskID, q, nil, &out)
	return out, err
}

// Comments returns all comments of a task. ClickUp returns the newest 25 per request;
// older pages are fetched with start/start_id of the oldest comment so far.
func (c *Client) Comments(ctx context.Context, taskID string) ([]Comment, error) {
	var all []Comment
	q := url.Values{}
	for range 40 { // at most 1000 comments
		var out struct{ Comments []Comment }
		if err := c.do(ctx, http.MethodGet, "/task/"+taskID+"/comment", q, nil, &out); err != nil {
			return nonNil(all), err
		}
		all = append(all, out.Comments...)
		if len(out.Comments) < 25 {
			break
		}
		oldest := out.Comments[len(out.Comments)-1]
		q = url.Values{"start": {string(oldest.Date)}, "start_id": {string(oldest.ID)}}
	}
	return nonNil(all), nil
}

// CreateComment posts a comment. With mentions it's sent as rich parts, so ClickUp tags
// (and notifies) the people mentioned; plain text otherwise.
func (c *Client) CreateComment(ctx context.Context, taskID, text string, parts []CommentPart) error {
	body := map[string]any{"comment_text": text, "notify_all": false}
	if slices.ContainsFunc(parts, func(p CommentPart) bool { return p.Type == "tag" }) {
		body = map[string]any{"comment": parts, "notify_all": false}
	}
	return c.do(ctx, http.MethodPost, "/task/"+taskID+"/comment", nil, body, nil)
}

// UpdateTask sends a partial update; fields follow the API's PUT /task body.
func (c *Client) UpdateTask(ctx context.Context, taskID string, fields map[string]any) (Task, error) {
	var out Task
	err := c.do(ctx, http.MethodPut, "/task/"+taskID, nil, fields, &out)
	return out, err
}

func (c *Client) CreateTask(ctx context.Context, listID string, fields map[string]any) (Task, error) {
	var out Task
	err := c.do(ctx, http.MethodPost, "/list/"+listID+"/task", nil, fields, &out)
	return out, err
}

// MoveTask moves a task to another list (its home list). Only API v3 can do this.
func (c *Client) MoveTask(ctx context.Context, teamID, taskID, listID string) error {
	return c.do(ctx, http.MethodPut, "/v3/workspaces/"+teamID+"/tasks/"+taskID+"/home_list/"+listID, nil, nil, nil)
}

func (c *Client) DeleteTask(ctx context.Context, taskID string) error {
	return c.do(ctx, http.MethodDelete, "/task/"+taskID, nil, nil, nil)
}

func (c *Client) SetField(ctx context.Context, taskID, fieldID string, value any, valueOptions map[string]any) error {
	body := map[string]any{"value": value}
	if valueOptions != nil {
		body["value_options"] = valueOptions
	}
	return c.do(ctx, http.MethodPost, "/task/"+taskID+"/field/"+fieldID, nil, body, nil)
}

func (c *Client) ClearField(ctx context.Context, taskID, fieldID string) error {
	return c.do(ctx, http.MethodDelete, "/task/"+taskID+"/field/"+fieldID, nil, nil, nil)
}
