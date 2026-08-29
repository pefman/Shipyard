//go:build windows

package sandbox

import "os/exec"

// ownProcessGroup is a no-op on Windows: process-group management is
// not available there, and Docker for Windows runs its containers in
// its own namespace anyway.
func ownProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills cmd itself only.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
