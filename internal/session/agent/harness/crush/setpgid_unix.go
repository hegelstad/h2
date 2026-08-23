//go:build !windows

package crush

import (
	"os/exec"
	"syscall"
)

// setChildProcessGroup starts cmd in its own process group so the kill ladder
// can signal the whole tree (crush spawns tool subprocesses).
func setChildProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
