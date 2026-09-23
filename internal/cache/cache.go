// Package cache is a tiny SQLite-backed key/value store so every view can render
// instantly from the last known state while fresh data loads in the background.
package cache

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Cache struct {
	mu sync.Mutex
	db *sql.DB
}

// Open opens (and creates) the cache at path; ":memory:" gives a throwaway cache.
func Open(path string) (*Cache, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one connection keeps ":memory:" a single database
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value BLOB NOT NULL, updated INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Cache{db: db}, nil
}

func (c *Cache) Close() error { return c.db.Close() }

func (c *Cache) raw(key string) ([]byte, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var data []byte
	var updated int64
	err := c.db.QueryRow(`SELECT value, updated FROM kv WHERE key = ?`, key).Scan(&data, &updated)
	if err != nil {
		return nil, time.Time{}, false
	}
	return data, time.UnixMilli(updated), true
}

func (c *Cache) putRaw(key string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec(`INSERT OR REPLACE INTO kv (key, value, updated) VALUES (?, ?, ?)`,
		key, data, time.Now().UnixMilli())
	return err
}

func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.db.Exec(`DELETE FROM kv WHERE key = ?`, key)
}

// Get returns the cached value for key and when it was stored.
func Get[T any](c *Cache, key string) (value T, updated time.Time, ok bool) {
	data, updated, ok := c.raw(key)
	if !ok || json.Unmarshal(data, &value) != nil {
		var zero T
		return zero, time.Time{}, false
	}
	return value, updated, true
}

// Value is Get without the timestamp, falling back to def.
func Value[T any](c *Cache, key string, def T) T {
	if v, _, ok := Get[T](c, key); ok {
		return v
	}
	return def
}

func Put[T any](c *Cache, key string, value T) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.putRaw(key, data)
}

// Update is one step of a stale-while-revalidate load.
type Update[T any] struct {
	Value T
	Fresh bool // false for the cached value, true for data just fetched
}

// SWR yields the cached value (if any) immediately, then fetches and yields the
// fresh value, but only when it differs from what was cached. A fetch error is
// yielded after the cached value, so callers keep showing stale data.
//
//	for u, err := range cache.SWR(ctx, c, "teams", fetchTeams) { ... }
func SWR[T any](ctx context.Context, c *Cache, key string, fetch func(context.Context) (T, error)) iter.Seq2[Update[T], error] {
	return func(yield func(Update[T], error) bool) {
		cachedRaw, _, hit := c.raw(key)
		if hit {
			var cached T
			if json.Unmarshal(cachedRaw, &cached) == nil && !yield(Update[T]{Value: cached}, nil) {
				return
			}
		}
		fresh, err := fetch(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				var zero Update[T]
				yield(zero, err)
			}
			return
		}
		data, err := json.Marshal(fresh)
		if err != nil {
			yield(Update[T]{}, err)
			return
		}
		_ = c.putRaw(key, data)
		if !hit || !bytes.Equal(data, cachedRaw) {
			yield(Update[T]{Value: fresh, Fresh: true}, nil)
		}
	}
}
