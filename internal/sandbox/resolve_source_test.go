package sandbox

import (
	"context"
	"os"
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

func TestResolveImageWithSource(t *testing.T) {
	if img, src := ResolveImageWithSource("my/image:1", ""); img != "my/image:1" || src != "flag" {
		t.Errorf("explicit = (%q, %q), want (my/image:1, flag)", img, src)
	}

	gomod := t.TempDir()
	if err := os.WriteFile(filepath.Join(gomod, "go.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if img, src := ResolveImageWithSource("", gomod); img != GolangImage || src != "auto" {
		t.Errorf("auto-detect = (%q, %q), want (%s, auto)", img, src, GolangImage)
	}

	empty := t.TempDir()
	if img, src := ResolveImageWithSource("", empty); img != FallbackImage || src != "auto" {
		t.Errorf("fallback = (%q, %q), want (%s, auto)", img, src, FallbackImage)
	}
}
