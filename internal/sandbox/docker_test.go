package sandbox

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDockerAvailable(t *testing.T) {
	installStub(t)
	if !DockerAvailable(context.Background()) {
		t.Error("DockerAvailable = false with the stub daemon up, want true")
	}
}

func TestDockerAvailableDaemonDown(t *testing.T) {
	installStub(t)
	t.Setenv("STUB_DOCKER_DAEMON_DOWN", "1")
	if DockerAvailable(context.Background()) {
		t.Error("DockerAvailable = true with the daemon down, want false")
	}
}

func TestDockerAvailableNotInstalled(t *testing.T) {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "nonexistent"))
	if DockerAvailable(context.Background()) {
		t.Error("DockerAvailable = true with no docker on PATH, want false")
	}
}
