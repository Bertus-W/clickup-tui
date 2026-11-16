package gui

import (
	"slices"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// pieces is a screen whose input arrives in the given bursts, a pause between them.
type pieces struct {
	tcell.Screen
	ch chan tcell.Event
}

func (p pieces) PollEvent() tcell.Event { return <-p.ch }

func key(r rune) tcell.Event { return tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone) }

var escKey = tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)

// A focus or mouse report split after its ESC doesn't come through as keys; a real Esc and
// "[" typed later do.
func TestEscGuardMendsSplitSequences(t *testing.T) {
	in := pieces{ch: make(chan tcell.Event, 64)}
	g := &escGuard{Screen: in, wait: 20 * time.Millisecond}
	for _, ev := range []tcell.Event{
		escKey, key('['), key('I'), // focus in, split after the ESC
		escKey, key('['), key('<'), key('0'), key(';'), key('3'), key(';'), key('4'), key('M'), // a click
		key('j'),
		escKey, // a real Esc
	} {
		in.ch <- ev
	}
	var got []string
	name := func(ev tcell.Event) string {
		switch ev := ev.(type) {
		case *tcell.EventFocus:
			if ev.Focused {
				return "focus"
			}
			return "blur"
		case *tcell.EventKey:
			if ev.Key() == tcell.KeyEscape {
				return "esc"
			}
			return string(ev.Rune())
		}
		return "?"
	}
	for range 3 {
		got = append(got, name(g.PollEvent()))
	}
	go func() { time.Sleep(50 * time.Millisecond); in.ch <- key('[') }() // typed after the Esc
	got = append(got, name(g.PollEvent()))
	if want := []string{"focus", "j", "esc", "["}; !slices.Equal(got, want) {
		t.Fatalf("events %q, want %q", got, want)
	}
}
