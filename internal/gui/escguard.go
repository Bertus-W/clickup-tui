package gui

import (
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

// escGuard mends escape sequences the terminal sent in pieces. tcell gives up on a sequence
// when its rest arrives over 50ms after the ESC (a busy machine, switching windows): the ESC
// then comes as the Esc key and the rest as typed keys. A focus report, ESC [ I, would press
// Esc (clearing the filter) and "[" (switching to Mine); a mouse report, ESC [ < 0 ; 12 ; 5 M,
// would type digits (focusing panels) and "m" (move task).
//
// So after an Esc the guard waits a moment for such a rest. When it comes, the Esc and the
// rest are dropped, and a focus report goes on as the focus event it was.
type escGuard struct {
	tcell.Screen
	wait   time.Duration
	once   sync.Once
	events chan tcell.Event
	held   []tcell.Event // read while waiting, not part of a sequence: next in line
}

// escRestWait is how long after an Esc a sequence's rest may still arrive. An Esc you press
// waits this long, and "[" typed within it after Esc is lost: neither is noticeable.
const escRestWait = 100 * time.Millisecond

func guardEscapes(s tcell.Screen) *escGuard { return &escGuard{Screen: s, wait: escRestWait} }

func (s *escGuard) PollEvent() tcell.Event {
	s.once.Do(func() {
		s.events = make(chan tcell.Event, 256)
		go func() {
			defer close(s.events)
			for {
				ev := s.Screen.PollEvent()
				if ev == nil { // the screen was finalized
					return
				}
				s.events <- ev
			}
		}()
	})
	for {
		ev, ok := s.next(-1)
		if !ok {
			return nil
		}
		if k, isKey := ev.(*tcell.EventKey); !isKey || k.Key() != tcell.KeyEscape || k.Modifiers() != tcell.ModNone {
			return ev
		}
		after, ok := s.next(s.wait)
		if !ok {
			return ev // a real Esc
		}
		intro := runeOf(after)
		if intro != '[' && intro != 'O' {
			s.held = append(s.held, after)
			return ev
		}
		// The rest of a CSI (ESC [ … final) or SS3 (ESC O x) sequence.
		seq := []rune{intro}
		for {
			n, ok := s.next(s.wait)
			if !ok {
				break
			}
			r := runeOf(n)
			if r == 0 {
				s.held = append(s.held, n)
				break
			}
			seq = append(seq, r)
			if intro == 'O' || (r >= 0x40 && r <= 0x7e) { // SS3 is one rune; CSI ends on a final byte
				break
			}
		}
		switch string(seq) {
		case "[I":
			return tcell.NewEventFocus(true)
		case "[O":
			return tcell.NewEventFocus(false)
		}
		// anything else (a mouse report, a key) is dropped: what it meant is lost
	}
}

// next is the next event: one held back first, else from the screen, waiting at most wait
// (forever when negative). ok is false on a timeout or once the screen is gone.
func (s *escGuard) next(wait time.Duration) (tcell.Event, bool) {
	if len(s.held) > 0 {
		ev := s.held[0]
		s.held = s.held[1:]
		return ev, true
	}
	if wait < 0 {
		ev, ok := <-s.events
		return ev, ok
	}
	select {
	case ev, ok := <-s.events:
		return ev, ok
	case <-time.After(wait):
		return nil, false
	}
}

// runeOf is the character a key event types, 0 for anything else.
func runeOf(ev tcell.Event) rune {
	if k, ok := ev.(*tcell.EventKey); ok && k.Key() == tcell.KeyRune && k.Modifiers() == tcell.ModNone {
		return k.Rune()
	}
	return 0
}
