package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubDocker is a fake docker CLI for tests: it covers the subset of the
// CLI Run uses. `info` succeeds unless STUB_DOCKER_DAEMON_DOWN is set;
// `run` executes the container's /bin/sh script directly, in the
// directory the mount points at (so the "container" behaves like the
// mounted workdir); `rm` always succeeds. Every invocation is appended
// to STUB_DOCKER_LOG so tests can assert on the CLI calls.
const stubDocker = `#!/bin/sh
# Stub docker binary for sandbox tests.
if [ -n "${STUB_DOCKER_LOG:-}" ]; then
  printf 'docker %s\n' "$*" >> "$STUB_DOCKER_LOG"
fi
case "$1" in
  info)
    if [ -n "${STUB_DOCKER_DAEMON_DOWN:-}" ]; then
      echo "Cannot connect to the Docker daemon" >&2
      exit 1
    fi
    exit 0
    ;;
  run)
    shift
    if [ -n "${STUB_DOCKER_RUN_FAIL:-}" ]; then
      echo "stub: $STUB_DOCKER_RUN_FAIL" >&2
      exit "${STUB_DOCKER_RUN_FAIL_EXIT:-125}"
    fi
    v=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --name|--entrypoint|--add-host) shift 2 ;;
        -v) v="$2"; shift 2 ;;
        -w|-e) shift 2 ;;
        --rm) shift ;;
        -*) shift ;;
        *) break ;;
      esac
    done
    # Remaining args are the entrypoint command line: -c <script>
    shift
    [ "$1" = "-c" ] && shift
    script="$1"
    cd "${v%%:*}" || exit 127
    exec /bin/sh -c "$script"
    ;;
  rm)
    exit 0
    ;;
  *)
    echo "stub docker: unexpected command: $1" >&2
    exit 125
    ;;
esac
`

// installStub puts the stub docker binary first on PATH and resets the
// stub's failure knobs.
func installStub(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stubDocker), 0o755); err != nil {
		t.Fatalf("writing stub docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_DOCKER_LOG", filepath.Join(dir, "docker-stub.log"))
	t.Setenv("STUB_DOCKER_DAEMON_DOWN", "")
	t.Setenv("STUB_DOCKER_RUN_FAIL", "")
	t.Setenv("STUB_DOCKER_RUN_FAIL_EXIT", "")
}

func stubLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("STUB_DOCKER_LOG"))
	if err != nil {
		t.Fatalf("reading stub log: %v", err)
	}
	return string(data)
}

func newWorkdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type lineBuf struct {
	mu    sync.Mutex
	lines []string
}

func (b *lineBuf) logf(format string, args ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, fmt.Sprintf(format, args...))
}

func (b *lineBuf) has(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range b.lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func TestRunStreamsOutputAndCleansUp(t *testing.T) {
	installStub(t)
	work := newWorkdir(t)
	lb := &lineBuf{}
	res, err := Run(context.Background(), RunSpec{
		Image:    "golang:1.22",
		Workdir:  work,
		Commands: []string{`printf 'hello sandbox\n'`, `printf 'proof' > proof.txt`},
		Env:      []string{"FOO=bar"},
		Log:      lb.logf,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Errorf("OK = false, want true: %+v", res.Steps)
	}
	if res.Image != "golang:1.22" {
		t.Errorf("Image = %q, want golang:1.22", res.Image)
	}
	for i, s := range res.Steps {
		if !s.Ran || s.ExitCode != 0 {
			t.Errorf("step %d = %+v, want ran with exit 0", i, s)
		}
	}
	if !lb.has("hello sandbox") {
		t.Errorf("step output not streamed to the logger: %v", lb.lines)
	}
	// The workdir is mounted read-write: files the container created
	// must show up on the host.
	proof, err := os.ReadFile(filepath.Join(work, "proof.txt"))
	if err != nil || string(proof) != "proof" {
		t.Errorf("proof.txt = %q, err %v; want \"proof\" (read-write mount)", proof, err)
	}
	lg := stubLog(t)
	if !strings.Contains(lg, "docker run") {
		t.Errorf("stub log has no docker run call:\n%s", lg)
	}
	if !strings.Contains(lg, "FOO=bar") {
		t.Errorf("stub log has no -e FOO=bar:\n%s", lg)
	}
	if !strings.Contains(lg, "docker rm -f") {
		t.Errorf("container not removed on clean exit:\n%s", lg)
	}
}

func TestRunAutoDetectsImage(t *testing.T) {
	installStub(t)
	work := newWorkdir(t)
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), RunSpec{
		Workdir:  work,
		Commands: []string{"true"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Image != GolangImage {
		t.Errorf("Image = %q, want %q (auto-detected from go.mod)", res.Image, GolangImage)
	}
	if !res.OK {
		t.Errorf("OK = false, want true: %+v", res.Steps)
	}
}

func TestRunStopsAtFailedStep(t *testing.T) {
	installStub(t)
	work := newWorkdir(t)
	res, err := Run(context.Background(), RunSpec{
		Workdir:  work,
		Commands: []string{"true", "exit 3", "touch not-run"},
	})
	if err != nil {
		t.Fatalf("a failed step is reported in the result, not as an error: %v", err)
	}
	if res.OK {
		t.Error("OK = true, want false")
	}
	want := []struct {
		ran  bool
		code int
	}{
		{true, 0},
		{true, 3},
		{false, -1},
	}
	for i, w := range want {
		s := res.Steps[i]
		if s.Ran != w.ran || s.ExitCode != w.code {
			t.Errorf("step %d = {ran:%t code:%d}, want {ran:%t code:%d}", i, s.Ran, s.ExitCode, w.ran, w.code)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "not-run")); !os.IsNotExist(err) {
		t.Errorf("step after the failed one ran (not-run exists, err %v)", err)
	}
}

func TestRunExtraHosts(t *testing.T) {
	installStub(t)
	work := newWorkdir(t)
	res, err := Run(context.Background(), RunSpec{
		Image:      "golang:1.22",
		Workdir:    work,
		Commands:   []string{"true"},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Errorf("OK = false, want true: %+v", res.Steps)
	}
	lg := stubLog(t)
	if !strings.Contains(lg, "--add-host host.docker.internal:host-gateway") {
		t.Errorf("stub docker saw no --add-host entry for the container:\n%s", lg)
	}
}

func TestRunDockerNotInstalled(t *testing.T) {
	// An empty PATH: no docker binary anywhere.
	t.Setenv("PATH", t.TempDir())
	work := newWorkdir(t)
	_, err := Run(context.Background(), RunSpec{Workdir: work, Commands: []string{"true"}})
	if err == nil || err.Error() != DockerRequired {
		t.Fatalf("err = %v, want %q", err, DockerRequired)
	}
}

func TestRunDaemonDown(t *testing.T) {
	installStub(t)
	t.Setenv("STUB_DOCKER_DAEMON_DOWN", "1")
	work := newWorkdir(t)
	_, err := Run(context.Background(), RunSpec{Workdir: work, Commands: []string{"true"}})
	if err == nil || err.Error() != DockerRequired {
		t.Fatalf("err = %v, want %q", err, DockerRequired)
	}
	// Docker is installed but the daemon is down: no run attempt.
	if lg := stubLog(t); strings.Contains(lg, "docker run") {
		t.Errorf("docker run was attempted with the daemon down:\n%s", lg)
	}
}

func TestRunDockerRunFails(t *testing.T) {
	installStub(t)
	t.Setenv("STUB_DOCKER_RUN_FAIL", "pull access denied for nosuchimage:0")
	t.Setenv("STUB_DOCKER_RUN_FAIL_EXIT", "125")
	work := newWorkdir(t)
	lb := &lineBuf{}
	_, err := Run(context.Background(), RunSpec{
		Image:    "nosuchimage:0",
		Workdir:  work,
		Commands: []string{"true"},
		Log:      lb.logf,
	})
	if err == nil {
		t.Fatal("want an error when docker run itself fails")
	}
	if strings.Contains(err.Error(), DockerRequired) {
		t.Errorf("must not claim Docker is missing: %v", err)
	}
	if !strings.Contains(err.Error(), "docker run failed") {
		t.Errorf("error does not say docker run failed: %v", err)
	}
	if !lb.has("pull access denied") {
		t.Errorf("docker's stderr not streamed to the logger: %v", lb.lines)
	}
	if !strings.Contains(stubLog(t), "docker rm -f") {
		t.Error("container not removed after a failed run")
	}
}

func TestRunCancelled(t *testing.T) {
	installStub(t)
	work := newWorkdir(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := Run(ctx, RunSpec{Workdir: work, Commands: []string{"sleep 30"}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want an error after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want wrapped context.Canceled", err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("run took %v after cancellation; the process tree was not killed promptly", elapsed)
	}
	if !strings.Contains(stubLog(t), "docker rm -f") {
		t.Errorf("container not removed after cancellation:\n%s", stubLog(t))
	}
}

func TestRunValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := Run(ctx, RunSpec{Commands: []string{"true"}}); err == nil {
		t.Error("empty Workdir accepted, want error")
	}
	if _, err := Run(ctx, RunSpec{Workdir: "/definitely/not/here", Commands: []string{"true"}}); err == nil {
		t.Error("missing workdir accepted, want error")
	}
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, RunSpec{Workdir: file, Commands: []string{"true"}}); err == nil {
		t.Error("file workdir accepted, want error")
	}
	if _, err := Run(ctx, RunSpec{Workdir: newWorkdir(t), Env: []string{"NOEQ"}}); err == nil {
		t.Error("malformed Env entry accepted, want error")
	}
	if _, err := Run(ctx, RunSpec{Workdir: newWorkdir(t), ExtraHosts: []string{"nocolon"}}); err == nil {
		t.Error("malformed ExtraHosts entry accepted, want error")
	}
}

func TestBuildScript(t *testing.T) {
	script := buildScript([]string{"go test ./..."})
	want := "ec=0\n" +
		"sh -c 'go test ./...'\n" +
		"ec=$?\n" +
		"printf '__shipyard__ step=0 exit=%s\\n' \"$ec\" >&2\n" +
		"[ \"$ec\" -eq 0 ] || exit \"$ec\"\n"
	if script != want {
		t.Errorf("buildScript:\n%s\nwant:\n%s", script, want)
	}
}

func TestShQuote(t *testing.T) {
	cases := map[string]string{
		"plain":             "'plain'",
		`apply the 'patch'`: `'apply the '\''patch'\'''`,
		"":                  "''",
	}
	for in, want := range cases {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
