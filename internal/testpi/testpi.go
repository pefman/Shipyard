// Package testpi provides the test doubles for the built-in pi agent
// and (in combination with the sandbox tests' docker stub) lets the
// agent-based solving flow be tested end to end without a Docker
// daemon and without a live model: a stub `pi` binary on PATH emits
// pi's JSON event stream and behaves according to PI_STUB_*
// environment knobs.
package testpi

import (
	"os"
	"path/filepath"
	"testing"
)

// StubScript is the fake pi binary. Behavior knobs (environment
// variables, read when the "agent" starts):
//
//	PI_STUB_MODE          fix (default) | noop | fail
//	PI_STUB_TURNS         assistant turns to emit (default 1)
//	PI_STUB_SLEEP         seconds to sleep inside each turn (budget tests)
//	PI_STUB_EXIT          exit code after the stream (default 0)
//	PI_STUB_FIX_FILE      file "fix" mode writes (default hello.py)
//	PI_STUB_FIX_CONTENT   its contents (default: the hello.py fix)
//
// The stub locates the agent config directory via PI_CODING_AGENT_DIR
// (set by shipyard); inside the stub docker "container" that path
// (/work/…) does not exist on the host, so it falls back to
// <pwd>/.shipyard-pi, which the docker stub's cwd points at.
const StubScript = `#!/bin/sh
# Fake built-in pi agent for Shipyard tests.
cfg="${PI_CODING_AGENT_DIR:-}"
[ -n "$cfg" ] && [ ! -d "$cfg" ] && cfg=""
[ -z "$cfg" ] && cfg="$(pwd)/.shipyard-pi"

emit() { printf '%s\n' "$1"; }
emit '{"type":"session","version":3,"id":"stub","timestamp":"t","cwd":"."}'
emit '{"type":"agent_start"}'
emit '{"type":"turn_start"}'
emit '{"type":"message_start","message":{"role":"user","content":[{"type":"text","text":"task"}]}}'
emit '{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"task"}]}}'
turns="${PI_STUB_TURNS:-1}"
i=0
while [ "$i" -lt "$turns" ]; do
  i=$((i+1))
  case "${PI_STUB_MODE:-fix}" in
    noop)
      ;;
    fail)
      exit "${PI_STUB_EXIT:-3}"
      ;;
    *)
      file="${PI_STUB_FIX_FILE:-hello.py}"
      content="${PI_STUB_FIX_CONTENT:-}"
      [ -z "$content" ] && content='def greet(name):
    if not name:
        return "Hello, stranger"
    return "Hello, " + name
'
      printf '%s' "$content" > "$file" || exit 1
      ;;
  esac
  [ -n "${PI_STUB_SLEEP:-}" ] && sleep "$PI_STUB_SLEEP"
  emit "{\"type\":\"message_start\",\"message\":{\"role\":\"assistant\",\"content\":[]}}"
  emit "{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"stub agent: done (turn $i)\"}],\"stopReason\":\"stop\"}}"
  emit "{\"type\":\"turn_end\",\"message\":{\"role\":\"assistant\"},\"toolResults\":[]}"
done
emit '{"type":"agent_end","messages":[],"willRetry":false}'
emit '{"type":"agent_settled"}'
exit "${PI_STUB_EXIT:-0}"
`

// Install puts the stub pi binary first on PATH and resets the stub's
// knobs to their defaults.
func Install(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(StubScript), 0o755); err != nil {
		t.Fatalf("writing stub pi: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, k := range []string{
		"PI_STUB_MODE", "PI_STUB_TURNS", "PI_STUB_SLEEP",
		"PI_STUB_EXIT", "PI_STUB_FIX_FILE", "PI_STUB_FIX_CONTENT",
	} {
		t.Setenv(k, "")
	}
}

// DockerStub is a fake docker CLI: `info`/`rm` succeed (no build ever
// happens in tests), `image inspect` succeeds unless
// STUB_DOCKER_IMAGE_PRESENT=0, `build` succeeds, `run` executes the
// container's /bin/sh script directly in the directory the mount
// points at (so the "container" behaves like the mounted workdir).
// Every invocation is appended to STUB_DOCKER_LOG.
const DockerStub = `#!/bin/sh
if [ -n "${STUB_DOCKER_LOG:-}" ]; then
  printf 'docker %s\n' "$*" >> "$STUB_DOCKER_LOG"
fi
case "$1" in
  info|build|rm)
    exit 0
    ;;
  image)
    [ "${STUB_DOCKER_IMAGE_PRESENT:-1}" = "1" ] || exit 1
    exit 0
    ;;
  run)
    shift
    v=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --name|--entrypoint|--build-arg|-t|--add-host) shift 2 ;;
        -v) v="$2"; shift 2 ;;
        -w|-e) shift 2 ;;
        --rm) shift ;;
        -*) shift ;;
        *) break ;;
      esac
    done
    shift
    [ "$1" = "-c" ] && shift
    script="$1"
    cd "${v%%:*}" || exit 127
    exec /bin/sh -c "$script"
    ;;
  *)
    exit 125
    ;;
esac
`

// InstallDocker puts the stub docker binary first on PATH (preserving
// a stub pi installed earlier) and returns the stub log path.
func InstallDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(DockerStub), 0o755); err != nil {
		t.Fatalf("writing stub docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stubLog := filepath.Join(dir, "docker-stub.log")
	t.Setenv("STUB_DOCKER_LOG", stubLog)
	t.Setenv("STUB_DOCKER_IMAGE_PRESENT", "1")
	return stubLog
}
