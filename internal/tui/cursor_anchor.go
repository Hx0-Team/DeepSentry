package tui

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// macOS IMEs position their candidate window from the terminal's real cursor,
// not from the reverse-video cursor cell painted by focusedInputRows. Bubble
// Tea intentionally parks the real cursor at the beginning of the last screen
// row after every frame, so an IME anchor has to run just after that frame.
//
// Keep a single debounced pair of timers instead of starting several goroutines
// for every keystroke. Each cursor escape is one small atomic Write, and cancel
// waits on the same mutex so no stale timer can move the shell cursor after TUI
// shutdown.
var inputIMEAnchor = struct {
	sync.Mutex
	generation uint64
	timers     []*time.Timer
}{}

func cancelInputCursorAnchor() {
	inputIMEAnchor.Lock()
	defer inputIMEAnchor.Unlock()
	inputIMEAnchor.generation++
	for _, timer := range inputIMEAnchor.timers {
		timer.Stop()
	}
	inputIMEAnchor.timers = nil
}

func (m AgentModel) scheduleInputCursorAnchor() {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	row, col, ok := m.inputCursorAnchor()
	if !ok {
		cancelInputCursorAnchor()
		return
	}

	inputIMEAnchor.Lock()
	inputIMEAnchor.generation++
	generation := inputIMEAnchor.generation
	for _, timer := range inputIMEAnchor.timers {
		timer.Stop()
	}
	inputIMEAnchor.timers = inputIMEAnchor.timers[:0]
	seq := []byte(fmt.Sprintf("\x1b[%d;%dH", row, col))
	for _, delay := range []time.Duration{24 * time.Millisecond, 52 * time.Millisecond} {
		timer := time.AfterFunc(delay, func() {
			inputIMEAnchor.Lock()
			defer inputIMEAnchor.Unlock()
			if inputIMEAnchor.generation != generation {
				return
			}
			_, _ = os.Stdout.Write(seq)
		})
		inputIMEAnchor.timers = append(inputIMEAnchor.timers, timer)
	}
	inputIMEAnchor.Unlock()
}
