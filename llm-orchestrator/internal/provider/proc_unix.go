//go:build unix

package provider

import (
	"os/exec"
	"syscall"
)

// killsProcessGroup: canceling a call kills the CLI and its children.
const killsProcessGroup = true

// isolate starts the CLI in its own process group and makes cancellation
// kill the whole group, so servers the agent left running die with it.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
