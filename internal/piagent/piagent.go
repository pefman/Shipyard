// Package piagent embeds the pi coding agent as Shipyard's solving
// engine. The agent runs on the issue's checkout: inside the sandbox
// container for runs that can use Docker (the wrapper image adds the
// built-in pi runtime to the language image, built from the vendor
// bundle embedded in this package), or natively on the host when
// Docker is unavailable.
//
// Per run, shipyard prepares a small agent config directory in the
// checkout (<workdir>/.shipyard-pi, git-excluded via .git/info/exclude):
// a models.json that maps shipyard's provider configuration
// (endpoint, key, model — including custom OpenAI-compatible endpoints
// such as local inference servers) onto one provider, and a task.md
// carrying the task prompt (issue plus instructions). The agent is
// launched in print/JSON mode with its built-in tools, context
// compaction enabled (pi's built-in behavior for long sessions), and
// extensions, skills, prompt templates, and themes disabled so the run
// is hermetic: the agent reads and edits the repository and runs its
// checks itself.
//
// Progress is streamed from the agent's JSON event stream into the
// caller's logger (rendered human lines, raw lines with --verbose),
// and two budgets cap one run: a turn cap (assistant turns observed on
// the event stream) and a wall-clock cap.
package piagent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pefman/Shipyard/internal/sandbox"
)

// Version is the pinned release of the built-in pi agent (it also pins
// the vendor bundle in agent/ and the wrapper image tag).
const Version = "0.84.4"

// DirName is the per-checkout agent config directory shipyard creates
// (<workdir>/.shipyard-pi). It is excluded from the repo via
// .git/info/exclude so the agent's config can never be committed.
const DirName = ".shipyard-pi"

// TaskFile is the task prompt file inside DirName; the agent run reads
// it as its message (@<task file> in the pi command line).
const TaskFile = "task.md"

const (
	// ProviderID is the provider id under which shipyard registers the
	// configured model in the agent's models.json.
	ProviderID = "shipyard"
	// defaultAPIKey is the placeholder written for keyless endpoints
	// (local servers that ignore the Authorization header); pi requires
	// some credential for a configured provider's models to be usable.
	defaultAPIKey = "none"
)

// Budget defaults, applied when the caller leaves them at zero:
// DefaultMaxTurns caps the assistant turns one issue may consume, and
// DefaultTimeout caps the agent run's wall clock.
const (
	DefaultMaxTurns = 30
	DefaultTimeout  = 30 * time.Minute
)

// DefaultContextWindow is the context window shipyard declares for the
// configured model in the agent's models.json when the operator does
// not override it (Config.ContextWindow). The agent triggers its
// built-in context compaction off this *declared* size, so local
// models with a smaller real window must be configured with it —
// otherwise a big repo or issue overflows the model's real context
// before the compaction ever kicks in.
const DefaultContextWindow = 128000

// Config is the agent's model/endpoint configuration, mapped from
// shipyard's provider flags (see internal/config).
type Config struct {
	// Provider is the resolved provider name (openai, xai, custom) —
	// for log lines only; the endpoint is what the agent uses.
	Provider string
	// Endpoint is the base URL of the OpenAI-compatible endpoint.
	Endpoint string
	// APIKey is the key sent to the endpoint; empty for keyless
	// custom endpoints (a placeholder is sent).
	APIKey string
	// Model is the model id sent to the endpoint.
	Model string
	// ContextWindow declares the model's context window (tokens) in
	// the agent's models.json; the agent compacts its context off this
	// declared size. Zero (the default) uses DefaultContextWindow —
	// right for large models, but local models with a smaller real
	// window must set this (e.g. 32768 for a 32k local model).
	ContextWindow int
}

// Validate reports missing required values.
func (c Config) Validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("agent: no AI endpoint configured (set --ai-endpoint or SHIPYARD_AI_ENDPOINT)")
	}
	if c.Model == "" {
		return fmt.Errorf("agent: no model configured (set --ai-model or SHIPYARD_AI_MODEL)")
	}
	return nil
}

// Prepare writes the per-run agent configuration into the checkout at
// workdir: DirName with models.json (the endpoint/model mapping) and
// task.md (the task prompt), and makes sure DirName is git-excluded
// via .git/info/exclude so the agent's config can never end up in a
// commit. It is idempotent within and across runs.
func Prepare(workdir string, task string, cfg Config) error {
	dir := ConfigDir(workdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("agent: creating %s: %w", dir, err)
	}
	models, err := modelsJSON(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(models), 0o600); err != nil {
		return fmt.Errorf("agent: writing models.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, TaskFile), []byte(task), 0o600); err != nil {
		return fmt.Errorf("agent: writing %s: %w", TaskFile, err)
	}
	if err := gitExclude(workdir, DirName); err != nil {
		return fmt.Errorf("agent: git-excluding %s: %w", DirName, err)
	}
	return nil
}

// gitExclude appends pattern to the checkout's .git/info/exclude
// (the per-clone ignore file, which is never committed) unless it is
// already there.
func gitExclude(workdir, pattern string) error {
	gitDir := filepath.Join(workdir, ".git")
	// .git may be a file (worktree/submodule): honor its gitdir: pointer.
	if data, err := os.ReadFile(gitDir); err == nil {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:"); ok {
			if target := strings.TrimSpace(rest); target != "" {
				gitDir = target
			}
		}
	}
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return fmt.Errorf("creating .git/info: %w", err)
	}
	exclude := filepath.Join(infoDir, "exclude")
	existing, _ := os.ReadFile(exclude)
	trimmed := strings.TrimSpace(string(existing))
	if trimmed == pattern || trimmed == pattern+"/" || strings.Contains(string(existing), pattern+"/") {
		return nil
	}
	entry := pattern + "/\n"
	if trimmed != "" && !strings.HasSuffix(string(existing), "\n") {
		entry = "\n" + entry
	}
	if err := os.WriteFile(exclude, append([]byte(existing), []byte(entry)...), 0o644); err != nil {
		return fmt.Errorf("writing .git/info/exclude: %w", err)
	}
	return nil
}

// ConfigDir returns the agent config directory for a checkout.
func ConfigDir(workdir string) string { return filepath.Join(workdir, DirName) }

// modelsJSON renders the agent's models.json: one provider (ProviderID)
// with the configured endpoint, key, and model. The model is marked
// non-reasoning: shipyard wants predictable token behavior on any
// endpoint (strong or local), so the agent never spends its budget on
// extended thinking. The declared context window (ContextWindow, or
// DefaultContextWindow) drives the agent's built-in compaction.
func modelsJSON(cfg Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	key := cfg.APIKey
	if key == "" {
		key = defaultAPIKey
	}
	window := cfg.ContextWindow
	if window <= 0 {
		window = DefaultContextWindow
	}
	var b strings.Builder
	b.WriteString("{\n  \"providers\": {\n    ")
	fmt.Fprintf(&b, "%q: {\n", ProviderID)
	b.WriteString("      \"name\": \"Shipyard AI\",\n")
	fmt.Fprintf(&b, "      \"baseUrl\": %s,\n", jsonString(cfg.Endpoint))
	b.WriteString("      \"api\": \"openai-completions\",\n")
	fmt.Fprintf(&b, "      \"apiKey\": %s,\n", jsonString(key))
	b.WriteString("      \"models\": [\n")
	fmt.Fprintf(&b, "        { \"id\": %s, \"name\": %s, \"reasoning\": false, \"input\": [\"text\"], \"contextWindow\": %d, \"maxTokens\": 16384 }\n",
		jsonString(cfg.Model), jsonString(cfg.Model), window)
	b.WriteString("      ]\n    }\n  }\n}\n")
	return b.String(), nil
}

// AgentCommand returns the pi command line that runs the prepared task
// in JSON event-stream mode, with a hermetic tool/resource set and no
// saved session, as one /bin/sh command line (for the container run):
//
//	pi --mode json --no-session --no-extensions --no-skills
//	     --no-prompt-templates --no-themes --no-approve
//	     --provider shipyard --model <model> -p -- @.shipyard-pi/task.md
func AgentCommand(model string) string {
	return "pi --mode json --no-session --no-extensions --no-skills" +
		" --no-prompt-templates --no-themes --no-approve" +
		fmt.Sprintf(" --provider %s --model %s", ProviderID, shQuote(model)) +
		" -p -- @" + DirName + "/" + TaskFile
}

// shQuote wraps s in single quotes so it embeds unchanged in a /bin/sh
// command line.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// AgentArgs returns the same command as exec arguments (for the native
// on-host run).
func AgentArgs(model string) []string {
	return []string{
		"--mode", "json",
		"--no-session",
		"--no-extensions", "--no-skills",
		"--no-prompt-templates", "--no-themes", "--no-approve",
		"--provider", ProviderID,
		"--model", model,
		"-p", "--",
		"@" + DirName + "/" + TaskFile,
	}
}

// AgentEnv returns the extra environment for one agent run on the
// host: the config directory for the checkout at workdir and offline
// mode (pi performs no startup network operations: no update checks,
// no model catalog refresh, no telemetry).
func AgentEnv(workdir string) []string {
	return []string{
		"PI_CODING_AGENT_DIR=" + ConfigDir(workdir),
		"PI_OFFLINE=1",
	}
}

// ContainerEnv returns the extra environment for the agent run inside
// the sandbox container, where the checkout is mounted at /work.
func ContainerEnv() []string {
	return []string{
		"PI_CODING_AGENT_DIR=/work/" + DirName,
		"PI_OFFLINE=1",
	}
}

// Runner runs the built-in agent on a checkout.
type Runner interface {
	// RunAgent prepares the run (config + task) in spec.Workdir and
	// executes the agent: inside the wrapper image for spec.BaseImage
	// (the agent step plus any verify commands in one container run)
	// or natively on the host when BaseImage is empty. It returns the
	// run outcome (image, turns, summary). A budget cap (turns or wall
	// clock) surfaces as the wrapped context error.
	//
	// For container runs, a host-loopback endpoint
	// (http://localhost:<port>…, 127.0.0.1, ::1) is remapped to
	// HostGatewayName before the agent config is written, and the run
	// starts with a from-sandbox TCP probe to that address: the same
	// address the agent's models.json carries, checked in the same
	// container run, so "the check passed but the agent cannot reach
	// the server" cannot happen (see endpoint.go).
	RunAgent(ctx context.Context, spec RunSpec) (*RunOutcome, error)
}

// RunSpec configures one agent run.
type RunSpec struct {
	// Workdir is the checkout the agent works in.
	Workdir string
	// Task is the task prompt written to the agent's task file.
	Task string
	// Config is the endpoint/model configuration.
	Config Config
	// BaseImage names the language image the agent runs in ("" runs
	// the agent natively on the host, which requires the pi binary on
	// PATH). On the container path the built-in wrapper image for
	// BaseImage is used.
	BaseImage string
	// VerifyCommands are run after the agent step, in the same
	// container run (container path only; ignored natively): the
	// repository's build/test steps (see sandbox.FixCommands).
	VerifyCommands []string
	// MaxTurns caps assistant turns; <= 0 uses DefaultMaxTurns.
	MaxTurns int
	// Timeout caps the agent run's wall clock; <= 0 uses DefaultTimeout.
	Timeout time.Duration
	// Verbose logs the raw agent event lines in addition to the
	// rendered progress lines.
	Verbose bool
	// Log receives progress output; nil discards it.
	Log func(format string, args ...any)

	// Test seams: nil selects the real implementations.
	// RunInSandbox executes the container run (nil uses sandbox.Run).
	RunInSandbox func(ctx context.Context, spec sandbox.RunSpec) (*sandbox.RunResult, error)
	// BuildImage builds/returns the wrapper image for a base image
	// (nil uses BuildWrapperImage).
	BuildImage func(ctx context.Context, base string) (string, error)
	// ExecNative runs the native pi process; nil uses the default
	// (the pi binary on PATH).
	ExecNative func(ctx context.Context, workdir string, env []string, args []string, line func(stream, line string)) (int, error)
}

// RunOutcome reports what an agent run produced.
type RunOutcome struct {
	// Container is true when the agent ran inside the sandbox
	// container (false: natively on the host).
	Container bool
	// Image is the image the container run used (wrapper image);
	// empty for native runs.
	Image string
	// Turns is how many assistant turns the agent completed before it
	// finished (or was stopped).
	Turns int
	// Summary is the agent's final text (its summary of the work).
	Summary string
	// StoppedByBudget reports whether the run ended on a budget cap
	// (turn or wall clock) rather than on the agent finishing.
	StoppedByBudget bool
}

// runner is the Runner implementation.
type runner struct{}

// DefaultRunner is the Runner that uses the built-in pi runtime
// (wrapper image in the container, pi on PATH natively).
var DefaultRunner Runner = runner{}

func (runner) RunAgent(ctx context.Context, spec RunSpec) (*RunOutcome, error) {
	maxTurns := spec.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	log := spec.Log
	if log == nil {
		log = func(string, ...any) {}
	}
	// The config the run actually uses: for container runs a loopback
	// endpoint is remapped to HostGatewayName on a local copy of the
	// config (spec.Config is never mutated, and native runs keep the
	// original endpoint — on the host, localhost is correct there).
	cfg := spec.Config
	var probe *probeTarget
	if spec.BaseImage != "" {
		if remapped, ok := remapLoopbackEndpoint(cfg.Endpoint); ok {
			log("agent: remapping loopback AI endpoint %s → %s for the sandbox (a host-local model server must bind a non-loopback interface, e.g. 0.0.0.0)", cfg.Endpoint, remapped)
			cfg.Endpoint = remapped
			probe = probeForRemap(spec.Config.Endpoint, remapped)
		}
	}
	if err := Prepare(spec.Workdir, spec.Task, cfg); err != nil {
		return nil, err
	}
	runSpec := spec
	runSpec.Config = cfg
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sink := newEventSink(log, spec.Verbose, maxTurns, cancel)

	if spec.BaseImage == "" {
		return runNative(runCtx, cancel, runSpec, sink, log)
	}
	return runContainer(runCtx, cancel, runSpec, probe, sink, log)
}

// runNative executes the pi binary on the host in the checkout.
func runNative(ctx context.Context, cancel context.CancelFunc, spec RunSpec, sink *eventSink, log func(string, ...any)) (*RunOutcome, error) {
	exec := spec.ExecNative
	if exec == nil {
		exec = defaultExecNative
	}
	log("agent: running the built-in pi agent natively on the host (pi %s)", Version)
	code, err := exec(ctx, spec.Workdir, AgentEnv(spec.Workdir), AgentArgs(spec.Config.Model), sink.onLine)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if sink.turnCapHit || errors.Is(ctxErr, context.DeadlineExceeded) {
				// A real budget stop (turn cap, or the run's own
				// wall-clock deadline): report it as a budget error.
				return nil, budgetError(sink, ctxErr)
			}
			// The parent context went away (e.g. a listen shutdown):
			// report the cancellation itself, not a budget stop.
			return nil, ctxErr
		}
		return nil, fmt.Errorf("agent: %w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("agent: pi exited %d: the agent run failed (see the agent log lines above)", code)
	}
	out := &RunOutcome{Container: false, Turns: sink.turns, Summary: sink.summary}
	if sink.budgetHit {
		out.StoppedByBudget = true
		return out, budgetError(sink, context.Canceled)
	}
	return out, nil
}

// runContainer executes the agent (plus the verify commands) inside
// the sandbox container on the wrapper image for spec.BaseImage.
// probe is non-nil when a loopback endpoint was remapped: the run
// then starts with a from-sandbox TCP probe to the remapped address,
// and a failed probe stops the run with an actionable error before
// the agent runs.
func runContainer(ctx context.Context, cancel context.CancelFunc, spec RunSpec, probe *probeTarget, sink *eventSink, log func(string, ...any)) (*RunOutcome, error) {
	build := spec.BuildImage
	if build == nil {
		build = BuildWrapperImage
	}
	wrapper, err := build(ctx, spec.BaseImage)
	if err != nil {
		return nil, fmt.Errorf("agent: preparing the sandbox image: %w", err)
	}
	run := spec.RunInSandbox
	if run == nil {
		run = sandbox.Run
	}
	log("agent: running the built-in pi agent in %s (base %s, pi %s)", wrapper, spec.BaseImage, Version)

	commands := make([]string, 0, len(spec.VerifyCommands)+2)
	if probe != nil {
		commands = append(commands, probeCommand(probe.host, probe.port))
	}
	commands = append(commands, AgentCommand(spec.Config.Model))
	commands = append(commands, spec.VerifyCommands...)
	res, err := run(ctx, sandbox.RunSpec{
		Image:    wrapper,
		Workdir:  spec.Workdir,
		Commands: commands,
		Env:      ContainerEnv(),
		// Emitted on every container run: host.docker.internal must
		// resolve on all platforms (Linux does not define it by
		// default), which is what makes remapped — and
		// user-supplied host.docker.internal — endpoints work from
		// inside the sandbox.
		ExtraHosts: []string{HostGatewayName + ":host-gateway"},
		// The sandbox streams merged stdout/stderr through its logger;
		// forward each line to the event sink (the JSON events are on
		// stdout; non-JSON lines are just logged).
		Log: func(format string, args ...any) { sink.onLine("stdout", fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if sink.turnCapHit || errors.Is(ctxErr, context.DeadlineExceeded) {
				// A real budget stop (turn cap, or the run's own
				// wall-clock deadline): report it as a budget error.
				return nil, budgetError(sink, ctxErr)
			}
			// The parent context went away (e.g. a listen shutdown):
			// report the cancellation itself, not a budget stop.
			return nil, ctxErr
		}
		return nil, fmt.Errorf("agent: %w", err)
	}
	if !res.OK {
		agentStep := 0
		if probe != nil {
			agentStep = 1
		}
		for i, s := range res.Steps {
			if !s.Ran {
				return nil, fmt.Errorf("agent run in sandbox: step %d of %d (%q) did not run", i+1, len(res.Steps), s.Command)
			}
			if s.ExitCode != 0 {
				if probe != nil && i == 0 {
					return nil, errors.New(probeFailureMessage(probe.original, probe.remapped, probe.host+":"+probe.port))
				}
				name := "the agent"
				if i > agentStep {
					name = fmt.Sprintf("verify step %d", i+1)
				}
				return nil, fmt.Errorf("agent run in sandbox: %s (step %d of %d) exited %d: %s", name, i+1, len(res.Steps), s.ExitCode, s.Command)
			}
		}
	}
	out := &RunOutcome{Container: true, Image: wrapper, Turns: sink.turns, Summary: sink.summary}
	if sink.budgetHit {
		out.StoppedByBudget = true
		return out, budgetError(sink, context.Canceled)
	}
	return out, nil
}

// budgetError renders a budget-capped run: which cap hit, plus the
// context error.
func budgetError(sink *eventSink, ctxErr error) error {
	if sink.turnCapHit {
		return fmt.Errorf("agent stopped: reached the turn budget (%d turns) before the issue was resolved — raise --agent-max-turns for harder issues (no commit, push, or PR was made)", sink.maxTurns)
	}
	return fmt.Errorf("agent stopped: wall-clock budget exhausted before the issue was resolved — raise --agent-timeout for harder issues (no commit, push, or PR was made): %w", ctxErr)
}

// defaultExecNative runs the pi binary on PATH in workdir, streaming
// each output line (stream is "stdout" or "stderr") to line. It
// returns the exit code, or an error when the process could not be
// started (e.g. pi is not installed) or exited abnormally.
func defaultExecNative(ctx context.Context, workdir string, env []string, args []string, line func(stream, l string)) (int, error) {
	bin, err := exec.LookPath("pi")
	if err != nil {
		return 0, fmt.Errorf("the built-in agent needs the pi binary on PATH for native runs (no Docker available): install it with 'npm install -g --ignore-scripts @earendil-works/pi-coding-agent', or provide Docker for sandboxed runs")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("preparing run: %w", err)
	}
	errOut, err := cmd.StderrPipe()
	if err != nil {
		return 0, fmt.Errorf("preparing run: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting pi: %w", err)
	}
	var waitErr error
	done := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() {
		pumpLines(out, "stdout", line)
		pumps.Done()
	}()
	go func() {
		pumpLines(errOut, "stderr", line)
		pumps.Done()
	}()
	<-done
	// Join the output pumps before returning: the caller reads the
	// event sink's state (turns, summary, budget) right away, and a
	// pump still flushing could update it a beat late — including a
	// turn-budget stop, which would turn a budget-exhausted run into
	// a "successful" one.
	pumps.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, fmt.Errorf("%w", ctxErr)
	}
	if waitErr == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, fmt.Errorf("pi exited abnormally: %w", waitErr)
}

// pumpLines forwards r's lines to emit until EOF. A trailing partial
// line without a newline is still delivered.
func pumpLines(r io.Reader, stream string, emit func(stream, line string)) {
	br := bufio.NewReaderSize(r, 1024*1024)
	for {
		line, err := br.ReadString('\n')
		emit(stream, strings.TrimSuffix(line, "\n"))
		if err != nil {
			return
		}
	}
}

// jsonString renders s as a JSON string literal.
func jsonString(s string) string {
	return fmt.Sprintf("%q", s)
}
