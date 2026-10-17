//go:build !windows

package sandbox

import (
	"os/exec"
	"syscall"
)

// prepareProcessGroup runs the command in its own process group and kills the
// whole group on cancellation so background children do not outlive the
// timeout.
func prepareProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
