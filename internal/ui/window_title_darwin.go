//go:build darwin

package ui

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Terminal adds cwd and the active child process to OSC titles by default.
// Override only the tab identified by this process's stdout TTY, not a shared
// profile, front window or other tabs. Restore its original flags on exit.
func prepareNativeWindowTitle(title string) func() {
	noop := func() {}
	if os.Getenv("TERM_PROGRAM") != "Apple_Terminal" || os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != "" {
		return noop
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cmd := exec.CommandContext(ctx, "/usr/bin/tty")
	cmd.Stdin = os.Stdout
	out, err := cmd.Output()
	cancel()
	tty := strings.TrimSpace(string(out))
	if err != nil || !strings.HasPrefix(tty, "/dev/") {
		return noop
	}
	out, err = runTerminalTitleScript(terminalTitleSetScript, tty, title)
	if err != nil {
		return noop
	}
	state := strings.SplitN(strings.TrimSuffix(string(out), "\n"), "\n", 6)
	if len(state) != 6 {
		return noop
	}
	for _, flag := range state[:5] {
		if flag != "true" && flag != "false" {
			return noop
		}
	}
	return func() { _, _ = runTerminalTitleScript(terminalTitleRestoreScript, append([]string{tty}, state...)...) }
}

func runTerminalTitleScript(script string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/osascript", append([]string{"-e", script, "--"}, args...)...).Output()
}

const terminalTitleSetScript = `on run argv
  tell application "Terminal"
    if not running then return ""
    repeat with w in windows
      repeat with t in tabs of w
        if tty of t is item 1 of argv then
          set saved to (title displays device name of t as text) & linefeed & (title displays shell path of t as text) & linefeed & (title displays file name of t as text) & linefeed & (title displays window size of t as text) & linefeed & (title displays custom title of t as text) & linefeed & (custom title of t)
          set title displays device name of t to false
          set title displays shell path of t to false
          set title displays file name of t to false
          set title displays window size of t to true
          set title displays custom title of t to true
          set custom title of t to item 2 of argv
          return saved
        end if
      end repeat
    end repeat
  end tell
  return ""
end run`

const terminalTitleRestoreScript = `on run argv
  tell application "Terminal"
    if not running then return
    repeat with w in windows
      repeat with t in tabs of w
        if tty of t is item 1 of argv then
          set title displays device name of t to (item 2 of argv is "true")
          set title displays shell path of t to (item 3 of argv is "true")
          set title displays file name of t to (item 4 of argv is "true")
          set title displays window size of t to (item 5 of argv is "true")
          set title displays custom title of t to (item 6 of argv is "true")
          set custom title of t to item 7 of argv
          return
        end if
      end repeat
    end repeat
  end tell
end run`
