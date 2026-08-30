package piagent

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// The bundled pi agent runtime (Dockerfile, lock files, offline npm
// cache, launcher). It is embedded so every shipyard build — binary,
// container image, or source checkout — carries the agent: building a
// sandbox wrapper image never fetches the agent from the network.
//
//go:embed all:agent
var agentRuntime embed.FS

// runtimeFS scopes the embedded runtime to its agent/ subtree.
var runtimeFS fs.FS = func() fs.FS {
	sub, err := fs.Sub(agentRuntime, "agent")
	if err != nil {
		panic(err)
	}
	return sub
}()

// pibaseFor returns the Node image the piagent build stage uses: the
// glibc variant by default, the alpine variant for alpine-based base
// images (a glibc node binary would not run on musl).
func pibaseFor(base string) string {
	if strings.Contains(base, "alpine") {
		return "node:24-alpine"
	}
	return "node:24-bookworm-slim"
}

// WrapperImageName returns the name of the built-in wrapper image for
// a base image: the base image plus the pi runtime, e.g.
// golang:1.22 → shipyard-sandbox/golang-1.22:pi-0.84.4.
func WrapperImageName(base string) string {
	clean := regexp.MustCompile(`[^\w.-]+`).ReplaceAllString(base, "-")
	return "shipyard-sandbox/" + clean + ":pi-" + Version
}

// BuildWrapperImage returns the wrapper image for base (building it
// when the local Docker install does not have it yet). The build runs
// fully offline with respect to the agent: the npm tree installs from
// the bundled cache; only the base and Node images are pulled.
func BuildWrapperImage(ctx context.Context, base string) (string, error) {
	docker, err := ensureDockerCLI(ctx)
	if err != nil {
		return "", err
	}
	name := WrapperImageName(base)
	if imageExists(ctx, docker, name) {
		return name, nil
	}

	dir, err := os.MkdirTemp("", "shipyard-pi-agent-")
	if err != nil {
		return "", fmt.Errorf("agent: making a build context: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := materializeContext(dir); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, docker,
		"build",
		"--build-arg", "BASE="+base,
		"--build-arg", "PIBASE="+pibaseFor(base),
		"--quiet",
		"-t", name,
		"-f", filepath.Join(dir, "Dockerfile"),
		dir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("building the built-in agent image for %s: %s", base, msg)
	}
	return name, nil
}

// materializeContext writes the embedded runtime (Dockerfile, package
// files, launcher, and the npm cache extracted from its tarball) into
// dir.
func materializeContext(dir string) error {
	if err := fs.WalkDir(runtimeFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(runtimeFS, p)
		if err != nil {
			return err
		}
		return writeFileMode(filepath.Join(dir, p), data, p)
	}); err != nil {
		return fmt.Errorf("agent: writing the build context: %w", err)
	}
	return nil
}

// writeFileMode writes data to path; the launcher keeps its execute
// bit (the embed does not preserve modes).
func writeFileMode(path string, data []byte, src string) error {
	mode := os.FileMode(0o644)
	if strings.HasSuffix(src, ".sh") {
		mode = 0o755
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}

// imageExists reports whether the image is in the local Docker
// install.
func imageExists(ctx context.Context, docker, name string) bool {
	cmd := exec.CommandContext(ctx, docker, "image", "inspect", name)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run() == nil
}

// ensureDockerCLI locates the docker CLI and verifies the daemon
// answers.
func ensureDockerCLI(ctx context.Context) (string, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("Docker is required to run the agent in a sandbox container (install Docker or run without it to fall back to a native run)")
	}
	cmd := exec.CommandContext(ctx, bin, "info")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("the Docker daemon is not reachable (required for sandboxed agent runs)")
	}
	return bin, nil
}
