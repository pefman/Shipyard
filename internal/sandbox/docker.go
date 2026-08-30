package sandbox

import (
	"context"
	"io"
	"os/exec"
)

// DockerAvailable reports whether the docker CLI is on PATH and its
// daemon answers — i.e. whether a live run can execute the fix step in
// a sandbox. Callers use it to decide up front whether to fall back to
// the native path; Run re-checks on its own, so a daemon that dies
// mid-run still surfaces DockerRequired.
func DockerAvailable(ctx context.Context) bool {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return false
	}
	cmd := exec.CommandContext(ctx, bin, "info")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run() == nil
}
