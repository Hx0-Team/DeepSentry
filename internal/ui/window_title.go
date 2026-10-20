package ui

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/term"
)

var windowTitleSession struct {
	sync.Mutex
	restore func()
}

// TerminalWindowTitle deliberately excludes cwd, model, task and subprocesses.
func TerminalWindowTitle() string { return "deepsentry-v" + Version + "-evolution" }

// SetTerminalWindowTitle only writes to an actual terminal, never JSON/pipes.
// The caller owns the session; fatal exits must restore it too.
func SetTerminalWindowTitle() func() {
	if !term.IsTerminal(int(os.Stdout.Fd())) || os.Getenv("TERM") == "dumb" {
		return func() {}
	}
	windowTitleSession.Lock()
	defer windowTitleSession.Unlock()
	if windowTitleSession.restore != nil {
		return func() {}
	}
	restoreNative := prepareNativeWindowTitle(TerminalWindowTitle())
	// Save/restore titles on terminals supporting the xterm title stack.
	_, _ = fmt.Fprintf(os.Stdout, "\x1b[22;0t\x1b]0;%s\x07", TerminalWindowTitle())
	var once sync.Once
	windowTitleSession.restore = func() {
		once.Do(func() {
			_, _ = fmt.Fprint(os.Stdout, "\x1b[23;0t")
			restoreNative()
		})
	}
	return RestoreTerminalWindowTitle
}

func RestoreTerminalWindowTitle() {
	windowTitleSession.Lock()
	restore := windowTitleSession.restore
	windowTitleSession.restore = nil
	windowTitleSession.Unlock()
	if restore != nil {
		restore()
	}
}
