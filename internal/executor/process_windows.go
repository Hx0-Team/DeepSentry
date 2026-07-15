//go:build windows

package executor

import "os/exec"

func configureCommandProcessGroup(_ *exec.Cmd) {}

func killCommandProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
