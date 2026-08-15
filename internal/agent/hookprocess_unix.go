//go:build darwin || linux

package agent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureHookProcess isolates the hook in a process group so cancellation
// stops descendants as well as the direct shell process.
func configureHookProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
