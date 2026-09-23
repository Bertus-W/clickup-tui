package clickup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

type handlerTransport struct{ h http.HandlerFunc }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h(rec, r)
	return rec.Result(), nil
}

func testClient(h http.HandlerFunc) *Client {
	c := New("pk_test")
	c.BaseURL = "http://clickup.test/api/v2"
	c.HTTP = &http.Client{Transport: handlerTransport{h}}
	return c
}

// Rate-limited requests wait until X-RateLimit-Reset. synctest's fake clock makes the
// 10 seconds of waiting instant.
func TestRetriesHonourRateLimitReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		c := testClient(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls < 3 {
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(5*time.Second).Unix(), 10))
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.Write([]byte(`{"user":{"id":1,"username":"me"}}`))
		})
		start := time.Now()
		u, err := c.User(t.Context())
		if err != nil || u.Username != "me" {
			t.Fatalf("user = %+v, err = %v", u, err)
		}
		if calls != 3 {
			t.Fatalf("calls = %d", calls)
		}
		if waited := time.Since(start); waited < 8*time.Second || waited > 12*time.Second {
			t.Fatalf("waited %v, want about 10s", waited)
		}
	})
}

func TestClientErrorsAreTyped(t *testing.T) {
	c := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"err":"Task not found","ECODE":"ITEM_013"}`))
	})
	_, err := c.GetTask(t.Context(), "nope", "")
	e, ok := errors.AsType[*APIError](err)
	if !ok || e.Status != 404 || e.Message != "Task not found" {
		t.Fatalf("err = %#v", err)
	}
}

func TestPagesIterateUntilLastPage(t *testing.T) {
	c := testClient(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		last := page == "2"
		w.Write([]byte(`{"tasks":[{"id":"t` + page + `","name":"x","orderindex":"1.00"}],"last_page":` + strconv.FormatBool(last) + `}`))
	})
	var ids []string
	for page, err := range c.ListTasks(t.Context(), "L", false) {
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range page {
			ids = append(ids, task.ID)
		}
	}
	if len(ids) != 3 || ids[2] != "t2" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestFlexibleJSON(t *testing.T) {
	var task Task
	raw := `{"id":"a","status":{"status":"x","orderindex":2},"orderindex":"3.5","due_date":null,"parent":null,
		"custom_fields":[{"id":"f","name":"Sev","type":"drop_down","value":1}]}`
	if err := jsonUnmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	if task.OrderIndex != 3.5 || task.Status.OrderIndex != 2 || task.DueDate != "" || !task.CustomFields[0].IsSet() {
		t.Fatalf("task = %+v", task)
	}
}

// A request cancelled because newer work superseded it is logged as such, not as a failure.
func TestCanceledRequestsAreMarked(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var logs []RequestLog
	c := New("pk_test")
	c.BaseURL = "http://clickup.test/api/v2"
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel() // the user scrolled on while this was in flight
		return nil, r.Context().Err()
	})}
	c.OnRequest = func(l RequestLog) { logs = append(logs, l) }
	if _, err := c.Comments(ctx, "t"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(logs) != 1 || !logs[0].Canceled {
		t.Fatalf("logs = %+v", logs)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A POST that fails with a 5xx may still have been saved: retrying it would create the task,
// comment or time entry twice. GETs are retried.
func TestPostsAreNotRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := map[string]int{}
		c := testClient(func(w http.ResponseWriter, r *http.Request) {
			calls[r.Method]++
			w.WriteHeader(http.StatusBadGateway)
		})
		if _, err := c.CreateTask(t.Context(), "L", map[string]any{"name": "x"}); err == nil {
			t.Fatal("expected an error")
		}
		if _, err := c.GetTask(t.Context(), "t", ""); err == nil {
			t.Fatal("expected an error")
		}
		if calls["POST"] != 1 || calls["GET"] != 4 {
			t.Fatalf("calls = %v, want 1 POST and 4 GETs", calls)
		}
	})
}
