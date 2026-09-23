package gui

import (
	"context"
	"sync"

	"github.com/jesseduffield/gocui"
)

// async implements app.Async on top of gocui. gocui's own Update gives no ordering
// guarantee (each call spawns a goroutine), so UI callbacks go through an unbounded
// FIFO drained by a single forwarder: results are applied in the order they were produced.
type async struct {
	g       *gocui.Gui
	mu      sync.Mutex
	pending []func()
	wake    chan struct{}
	cancels map[string]context.CancelFunc
}

func newAsync(g *gocui.Gui) *async {
	a := &async{g: g, wake: make(chan struct{}, 1), cancels: map[string]context.CancelFunc{}}
	go a.forward()
	return a
}

func (a *async) Go(group string, fn func(context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	if group != "" {
		a.mu.Lock()
		if prev := a.cancels[group]; prev != nil {
			prev()
		}
		a.cancels[group] = cancel
		a.mu.Unlock()
	}
	go func() {
		defer cancel()
		fn(ctx)
	}()
}

// UI never blocks, so it is safe to call from the UI goroutine itself.
func (a *async) UI(fn func()) {
	a.mu.Lock()
	a.pending = append(a.pending, fn)
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *async) forward() {
	for range a.wake {
		a.mu.Lock()
		batch := a.pending
		a.pending = nil
		a.mu.Unlock()
		if len(batch) == 0 {
			continue
		}
		a.g.UpdateAsync(func(*gocui.Gui) error {
			for _, fn := range batch {
				fn()
			}
			return nil
		})
	}
}
