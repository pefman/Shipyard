package piagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pefman/Shipyard/internal/testpi"
)

func testConfig() Config {
	return Config{
		Provider: "custom",
		Endpoint: "http://127.0.0.1:8765/v1",
		APIKey:   "",
		Model:    "mock-model",
	}
}

func TestValidate(t *testing.T) {
	cfg := testConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	cfg.Endpoint = ""
	if err := cfg.Validate(); err == nil {
		t.Error("missing endpoint accepted")
	}
	cfg = testConfig()
	cfg.Model = ""
	if err := cfg.Validate(); err == nil {
		t.Error("missing model accepted")
	}
}

func TestPrepareWritesConfigAndTask(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	if err := Prepare(work, "the task", cfg); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	task, err := os.ReadFile(filepath.Join(work, DirName, TaskFile))
	if err != nil || string(task) != "the task" {
		t.Errorf("task file = %q, err %v", task, err)
	}
	models, err := os.ReadFile(filepath.Join(work, DirName, "models.json"))
	if err != nil {
		t.Fatalf("models.json: %v", err)
	}
	for _, want := range []string{
		`"shipyard"`,
		`"baseUrl": "http://127.0.0.1:8765/v1"`,
		`"api": "openai-completions"`,
		`"id": "mock-model"`,
		`"reasoning": false`,
	} {
		if !strings.Contains(string(models), want) {
			t.Errorf("models.json missing %q:\n%s", want, models)
		}
	}
	exclude, err := os.ReadFile(filepath.Join(work, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("no .git/info/exclude: %v", err)
	}
	if !strings.Contains(string(exclude), DirName) {
		t.Errorf("config dir not git-excluded:\n%s", exclude)
	}
	// Idempotent: preparing again must not duplicate the exclude entry.
	if err := Prepare(work, "the task", cfg); err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	exclude, _ = os.ReadFile(filepath.Join(work, ".git", "info", "exclude"))
	if n := strings.Count(string(exclude), DirName); n != 1 {
		t.Errorf("exclude entry duplicated: %q", exclude)
	}
}

func TestPrepareKeyedEndpoint(t *testing.T) {
	work := t.TempDir()
	cfg := testConfig()
	cfg.APIKey = "sk-123"
	if err := Prepare(work, "task", cfg); err != nil {
		t.Fatal(err)
	}
	models, _ := os.ReadFile(filepath.Join(work, DirName, "models.json"))
	if !strings.Contains(string(models), `"apiKey": "sk-123"`) {
		t.Errorf("key not written to models.json:\n%s", models)
	}
}

func TestPrepareKeylessUsesPlaceholder(t *testing.T) {
	work := t.TempDir()
	if err := Prepare(work, "task", testConfig()); err != nil {
		t.Fatal(err)
	}
	models, _ := os.ReadFile(filepath.Join(work, DirName, "models.json"))
	if !strings.Contains(string(models), `"apiKey": "none"`) {
		t.Errorf("keyless endpoint should get the placeholder key:\n%s", models)
	}
}

// TestModelsJSONContextWindow: an operator-set context window lands in
// the agent's models.json (it drives the agent's built-in compaction),
// and the default applies otherwise — a local model with a smaller
// real window must be declared with it.
func TestModelsJSONContextWindow(t *testing.T) {
	work := t.TempDir()
	cfg := testConfig()
	cfg.ContextWindow = 4096
	if err := Prepare(work, "task", cfg); err != nil {
		t.Fatal(err)
	}
	models, _ := os.ReadFile(filepath.Join(work, DirName, "models.json"))
	if !strings.Contains(string(models), `"contextWindow": 4096`) {
		t.Errorf("declared context window not in models.json:\n%s", models)
	}

	work2 := t.TempDir()
	if err := Prepare(work2, "task", testConfig()); err != nil {
		t.Fatal(err)
	}
	models2, _ := os.ReadFile(filepath.Join(work2, DirName, "models.json"))
	want := fmt.Sprintf(`"contextWindow": %d`, DefaultContextWindow)
	if !strings.Contains(string(models2), want) {
		t.Errorf("default context window not in models.json (want %s):\n%s", want, models2)
	}
}

func TestAgentCommandAndArgs(t *testing.T) {
	cmd := AgentCommand("mock-model")
	for _, want := range []string{
		"pi --mode json", "--no-session", "--no-extensions", "--no-skills",
		"--no-prompt-templates", "--no-themes", "--no-approve",
		"--provider shipyard", "--model 'mock-model'",
		"-p -- @" + DirName + "/" + TaskFile,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("AgentCommand missing %q:\n%s", want, cmd)
		}
	}
	args := AgentArgs("mock-model")
	want := []string{
		"--mode", "json",
		"--no-session",
		"--no-extensions", "--no-skills",
		"--no-prompt-templates", "--no-themes", "--no-approve",
		"--provider", "shipyard",
		"--model", "mock-model",
		"-p", "--",
		"@" + DirName + "/" + TaskFile,
	}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Errorf("AgentArgs = %v, want %v", args, want)
	}
	if got := ContainerEnv(); strings.Join(got, ";") != "PI_CODING_AGENT_DIR=/work/"+DirName+";PI_OFFLINE=1" {
		t.Errorf("ContainerEnv = %v", got)
	}
}

func TestWrapperImageNameAndPibase(t *testing.T) {
	if got := WrapperImageName("golang:1.22"); got != "shipyard-sandbox/golang-1.22:pi-0.84.4" {
		t.Errorf("WrapperImageName(golang:1.22) = %q", got)
	}
	if got := WrapperImageName("mycorp/python:3.12-slim"); got != "shipyard-sandbox/mycorp-python-3.12-slim:pi-0.84.4" {
		t.Errorf("WrapperImageName(mycorp/python:3.12-slim) = %q", got)
	}
	if got := pibaseFor("golang:1.22"); got != "node:24-bookworm-slim" {
		t.Errorf("pibaseFor(golang) = %q", got)
	}
	if got := pibaseFor("node:20-alpine"); got != "node:24-alpine" {
		t.Errorf("pibaseFor(alpine) = %q", got)
	}
}

func TestBuildWrapperImageUsesDocker(t *testing.T) {
	testpi.InstallDocker(t)
	stubLog := os.Getenv("STUB_DOCKER_LOG")
	name, err := BuildWrapperImage(context.Background(), "golang:1.22")
	if err != nil {
		t.Fatalf("BuildWrapperImage: %v", err)
	}
	if name != "shipyard-sandbox/golang-1.22:pi-0.84.4" {
		t.Errorf("name = %q", name)
	}
	data, _ := os.ReadFile(stubLog)
	if !strings.Contains(string(data), "docker info") {
		t.Errorf("docker was not checked:\n%s", data)
	}
	if !strings.Contains(string(data), "docker image inspect") {
		t.Errorf("the existing wrapper image was not checked first:\n%s", data)
	}
	// The stub answers image inspect with success: no build.
	if strings.Contains(string(data), "docker build") {
		t.Errorf("a build was attempted although the image 'exists':\n%s", data)
	}
}

// TestBuildWrapperImageBuildsWhenMissing: when the wrapper image is
// not in the local Docker install, the embedded build context is
// materialized and docker build is invoked with the right base/pibase
// arguments and tag.
func TestBuildWrapperImageBuildsWhenMissing(t *testing.T) {
	testpi.InstallDocker(t)
	t.Setenv("STUB_DOCKER_IMAGE_PRESENT", "0")
	stubLog := os.Getenv("STUB_DOCKER_LOG")

	name, err := BuildWrapperImage(context.Background(), "golang:1.22")
	if err != nil {
		t.Fatalf("BuildWrapperImage: %v", err)
	}
	if name != "shipyard-sandbox/golang-1.22:pi-0.84.4" {
		t.Errorf("name = %q", name)
	}
	data, _ := os.ReadFile(stubLog)
	log := string(data)
	for _, want := range []string{
		"docker build",
		"--build-arg BASE=golang:1.22",
		"--build-arg PIBASE=node:24-bookworm-slim",
		"-t shipyard-sandbox/golang-1.22:pi-0.84.4",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("stub docker saw no %q:\n%s", want, log)
		}
	}
}

func TestMaterializeContext(t *testing.T) {
	dir := t.TempDir()
	if err := materializeContext(dir); err != nil {
		t.Fatalf("materializeContext: %v", err)
	}
	for _, want := range []string{"Dockerfile", "package.json", "package-lock.json", "npm-cache.tar.gz", "pi-launcher.sh", "README.md"} {
		fi, err := os.Stat(filepath.Join(dir, want))
		if err != nil {
			t.Errorf("context missing %s: %v", want, err)
			continue
		}
		if want == "pi-launcher.sh" && fi.Mode()&0o100 == 0 {
			t.Errorf("pi-launcher.sh is not executable")
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil || !strings.Contains(string(data), "npm ci --offline") {
		t.Errorf("Dockerfile does not install the agent offline: %v", err)
	}
}

// TestRunNative covers the native path: the stub pi binary on PATH
// plays the built-in agent, edits the checkout, and streams its events.
func TestRunNative(t *testing.T) {
	testpi.Install(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "hello.py"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logs := &logBuf{}
	outcome, err := DefaultRunner.RunAgent(context.Background(), RunSpec{
		Workdir: work,
		Task:    "the task",
		Config:  testConfig(),
		Log:     logs.logf,
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if outcome.Container {
		t.Error("native run reported as container run")
	}
	if outcome.Turns != 1 {
		t.Errorf("Turns = %d, want 1", outcome.Turns)
	}
	if !strings.Contains(outcome.Summary, "stub agent") {
		t.Errorf("Summary = %q", outcome.Summary)
	}
	data, err := os.ReadFile(filepath.Join(work, "hello.py"))
	if err != nil || !strings.Contains(string(data), "Hello, stranger") {
		t.Errorf("the agent's change is not in the workdir: %q, %v", data, err)
	}
	if !logs.has("agent: turn 1 done") || !logs.has("agent: finished (1 turn)") {
		t.Errorf("progress lines missing:\n%s", logs)
	}
	if !logs.has("natively on the host") {
		t.Errorf("native-run line missing:\n%s", logs)
	}
}

// TestRunNativeNoPIOnPATH: without the binary and without Docker the
// native run must fail with the install hint.
func TestRunNativeNoPIOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	work := t.TempDir()
	_, err := DefaultRunner.RunAgent(context.Background(), RunSpec{
		Workdir: work,
		Task:    "task",
		Config:  testConfig(),
	})
	if err == nil || !strings.Contains(err.Error(), "npm install -g") {
		t.Errorf("err = %v, want the pi install hint", err)
	}
}

func TestRunNativeAgentFails(t *testing.T) {
	testpi.Install(t)
	t.Setenv("PI_STUB_MODE", "fail")
	work := t.TempDir()
	_, err := DefaultRunner.RunAgent(context.Background(), RunSpec{
		Workdir: work,
		Task:    "task",
		Config:  testConfig(),
	})
	if err == nil || !strings.Contains(err.Error(), "exited 3") {
		t.Errorf("err = %v, want the failed exit code", err)
	}
}

func TestRunNativeTurnBudget(t *testing.T) {
	testpi.Install(t)
	t.Setenv("PI_STUB_TURNS", "5")
	t.Setenv("PI_STUB_SLEEP", "0.2")
	work := t.TempDir()
	_, err := DefaultRunner.RunAgent(context.Background(), RunSpec{
		Workdir:  work,
		Task:     "task",
		Config:   testConfig(),
		MaxTurns: 2,
		Timeout:  30 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "turn budget") {
		t.Errorf("err = %v, want the turn budget error", err)
	}
}

func TestRunNativeWallClockBudget(t *testing.T) {
	testpi.Install(t)
	t.Setenv("PI_STUB_SLEEP", "10")
	work := t.TempDir()
	start := time.Now()
	_, err := DefaultRunner.RunAgent(context.Background(), RunSpec{
		Workdir:  work,
		Task:     "task",
		Config:   testConfig(),
		MaxTurns: 100,
		Timeout:  300 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "wall-clock") {
		t.Errorf("err = %v, want the wall-clock budget error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("run took %v after the wall-clock cap", elapsed)
	}
}

// TestRunContainer covers the container path with the stub docker
// binary: the stub "container" executes the agent command (the stub pi
// on PATH) in the mounted directory.
func TestRunContainer(t *testing.T) {
	testpi.Install(t)
	testpi.InstallDocker(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "hello.py"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logs := &logBuf{}
	outcome, err := DefaultRunner.RunAgent(context.Background(), RunSpec{
		Workdir:   work,
		Task:      "the task",
		Config:    testConfig(),
		BaseImage: "golang:1.22",
		Log:       logs.logf,
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !outcome.Container {
		t.Error("container run reported as native")
	}
	if outcome.Image != "shipyard-sandbox/golang-1.22:pi-0.84.4" {
		t.Errorf("Image = %q (the built-in wrapper image)", outcome.Image)
	}
	if outcome.Turns != 1 {
		t.Errorf("Turns = %d, want 1", outcome.Turns)
	}
	data, _ := os.ReadFile(filepath.Join(work, "hello.py"))
	if !strings.Contains(string(data), "Hello, stranger") {
		t.Errorf("the agent's change did not reach the checkout through the container: %q", data)
	}
	stubLog, _ := os.ReadFile(os.Getenv("STUB_DOCKER_LOG"))
	if !strings.Contains(string(stubLog), "docker run") {
		t.Errorf("docker run was never called:\n%s", stubLog)
	}
	if !strings.Contains(string(stubLog), "PI_CODING_AGENT_DIR=/work/.shipyard-pi") {
		t.Errorf("the agent's config dir was not passed to the container:\n%s", stubLog)
	}
}

// TestRunContainerAgentFails: a failing agent step must fail the run
// before any verification step runs.
func TestRunContainerAgentFails(t *testing.T) {
	testpi.Install(t)
	testpi.InstallDocker(t)
	t.Setenv("PI_STUB_MODE", "fail")
	work := t.TempDir()
	_, err := DefaultRunner.RunAgent(context.Background(), RunSpec{
		Workdir:   work,
		Task:      "task",
		Config:    testConfig(),
		BaseImage: "golang:1.22",
	})
	if err == nil || !strings.Contains(err.Error(), "the agent (step 1 of 1) exited 3") {
		t.Errorf("err = %v, want the failed agent step", err)
	}
}

// TestRunContainerVerifyStepFails: a verification step after the agent
// step fails (here: `false`); the agent itself succeeded.
func TestRunContainerVerifyStepFails(t *testing.T) {
	testpi.Install(t)
	testpi.InstallDocker(t)
	work := t.TempDir()
	_, err := DefaultRunner.RunAgent(context.Background(), RunSpec{
		Workdir:        work,
		Task:           "task",
		Config:         testConfig(),
		BaseImage:      "golang:1.22",
		VerifyCommands: []string{"false"},
	})
	if err == nil || !strings.Contains(err.Error(), "verify step 2 (step 2 of 2) exited 1") {
		t.Errorf("err = %v, want the failed verify step", err)
	}
}

// logBuf collects formatted log lines (the sink and the test share it;
// the container path invokes the logger from several pumps
// concurrently, so access is locked).
type logBuf struct {
	mu    sync.Mutex
	lines []string
}

func (b *logBuf) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	b.mu.Lock()
	b.lines = append(b.lines, line)
	b.mu.Unlock()
}

func (b *logBuf) has(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range b.lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func (b *logBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}
