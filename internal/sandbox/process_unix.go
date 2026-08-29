//go:build !windows

package sandbox

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts cmd in its own process group so a cancellation
// can kill the whole tree: the docker CLI, the container-side script
// host process, and any step it forked.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills cmd and every process in its group.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
