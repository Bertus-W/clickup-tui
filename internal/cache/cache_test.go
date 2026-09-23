package cache

import (
	"context"
	"errors"
	"testing"
)

func TestStaleWhileRevalidate(t *testing.T) {
	c, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect := func(fetch func(context.Context) ([]string, error)) (updates []Update[[]string], errs []error) {
		for u, err := range SWR(t.Context(), c, "k", fetch) {
			if err != nil {
				errs = append(errs, err)
				continue
			}
			updates = append(updates, u)
		}
		return
	}
	fetch := func(v ...string) func(context.Context) ([]string, error) {
		return func(context.Context) ([]string, error) { return v, nil }
	}

	// Cold: only the fresh value.
	if u, _ := collect(fetch("a")); len(u) != 1 || !u[0].Fresh {
		t.Fatalf("cold = %+v", u)
	}
	// Warm and unchanged: only the cached value, no redundant re-render.
	if u, _ := collect(fetch("a")); len(u) != 1 || u[0].Fresh {
		t.Fatalf("unchanged = %+v", u)
	}
	// Warm and changed: cached, then fresh.
	if u, _ := collect(fetch("b")); len(u) != 2 || u[0].Value[0] != "a" || u[1].Value[0] != "b" {
		t.Fatalf("changed = %+v", u)
	}
	// Fetch error after the cached value.
	boom := errors.New("offline")
	u, errs := collect(func(context.Context) ([]string, error) { return nil, boom })
	if len(u) != 1 || u[0].Value[0] != "b" || len(errs) != 1 {
		t.Fatalf("offline = %+v %v", u, errs)
	}
	if got := Value(c, "k", []string{}); got[0] != "b" {
		t.Fatalf("stored = %v", got)
	}
}
