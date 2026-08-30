package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/pefman/Shipyard/internal/guardrails"
)

// resetGuardrailEnv clears the guardrail environment so a test sees
// only what it sets itself.
func resetGuardrailEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envRepos, "")
	t.Setenv(envLabels, "")
	t.Setenv(envMaxPRs, "")
	t.Setenv("SHIPYARD_MODE", "")
	t.Setenv("SHIPYARD_GITHUB_TOKEN", "gh-test-token")
	t.Setenv("SHIPYARD_AI_ENDPOINT", "http://127.0.0.1:1/v1")
	t.Setenv("SHIPYARD_AI_MODEL", "mock-model")
}

// TestPrepareListenEnvOnlyReposGuardsRun: a run guarded purely through
// SHIPYARD_REPOS must reach the loop with a configured allowlist — not
// be refused as unguarded, and not re-derive the allowlist from the
// (empty) flag. This is the flag/env → listen.Options mapping the
// guardrail gate depends on.
func TestPrepareListenEnvOnlyReposGuardsRun(t *testing.T) {
	resetGuardrailEnv(t)
	t.Setenv(envRepos, "towner/trepo, other/repo")

	run, err := prepareListen([]string{"--repo", "towner/trepo", "--interval", "1h"})
	if err != nil {
		t.Fatalf("prepareListen with env-only repos: %v, want the run to start", err)
	}
	if len(run.Options.Repos) != 2 || run.Options.Repos[0] != "other/repo" || run.Options.Repos[1] != "towner/trepo" {
		t.Errorf("Options.Repos = %v, want the env allowlist to reach the loop", run.Options.Repos)
	}
	if run.Options.Unguarded {
		t.Error("a guarded run must not carry the unguarded acknowledgment")
	}
	// And the loop-level gate must pass for exactly these options.
	allow, err := guardrails.NewAllow(run.Options.Repos, run.Options.Labels)
	if err != nil {
		t.Fatalf("rebuilding the loop gate: %v", err)
	}
	if err := allow.Gate(run.Options.Unguarded); err != nil {
		t.Errorf("the loop would refuse this run as %v", err)
	}
	if !allow.RepoAllowed("towner", "trepo") {
		t.Errorf("loop gate must allow the watched repo (allowlist %v)", run.Options.Repos)
	}
	if allow.RepoAllowed("outsider", "repo") {
		t.Errorf("loop gate must keep refusing repos outside the allowlist")
	}
}

// TestPrepareListenLabelFlagGuardsRun: the repeatable --label flag is a
// label allowlist, so a --label-only run is guarded — not refused as
// unguarded — and the labels reach the loop.
func TestPrepareListenLabelFlagGuardsRun(t *testing.T) {
	resetGuardrailEnv(t)

	run, err := prepareListen([]string{"--repo", "towner/trepo", "--label", "shipyard", "--label", "bug"})
	if err != nil {
		t.Fatalf("prepareListen with --label only: %v, want the run to start", err)
	}
	if len(run.Options.Labels) != 2 {
		t.Errorf("Options.Labels = %v, want both --label entries", run.Options.Labels)
	}
	if allow, _ := guardrails.NewAllow(run.Options.Repos, run.Options.Labels); !allow.LabelsAllowed([]string{"Shipyard"}) {
		t.Error("a 'Shipyard' label should pass the loop filter case-insensitively")
	}
}

// TestPrepareListenLabelsUnionEnvAndFlag: --labels (or its env) and
// --label combine into one allowlist, flag entries included in the
// guardrail gate.
func TestPrepareListenLabelsUnionEnvAndFlag(t *testing.T) {
	resetGuardrailEnv(t)
	t.Setenv(envLabels, "env-label")

	run, err := prepareListen([]string{"--repo", "towner/trepo", "--label", "flag-label"})
	if err != nil {
		t.Fatalf("prepareListen: %v", err)
	}
	got := map[string]bool{}
	for _, l := range run.Options.Labels {
		got[l] = true
	}
	for _, want := range []string{"env-label", "flag-label"} {
		if !got[want] {
			t.Errorf("Options.Labels = %v, want the union of env and flag entries", run.Options.Labels)
		}
	}
}

// TestPrepareListenDryRunDefaultIsSafe: with no allowlist anywhere, the
// default listen run is a dry run and must start — a fresh installation
// pointed at a repo must not open pull requests until the operator
// deliberately goes live.
func TestPrepareListenDryRunDefaultIsSafe(t *testing.T) {
	resetGuardrailEnv(t)

	run, err := prepareListen([]string{"--repo", "towner/trepo"})
	if err != nil {
		t.Fatalf("prepareListen with no allowlists: %v, want the dry-run default to start", err)
	}
	if !run.Options.DryRun {
		t.Error("Options.DryRun = false, want the dry-run default")
	}
	if run.Options.Unguarded {
		t.Error("a dry run must not need the unguarded acknowledgment")
	}
}

// TestPrepareListenUnguardedLiveRefused: going live without a repo or
// label allowlist is refused, via the --live flag or SHIPYARD_MODE=live,
// unless the operator acknowledges it.
func TestPrepareListenUnguardedLiveRefused(t *testing.T) {
	resetGuardrailEnv(t)

	if _, err := prepareListen([]string{"--repo", "towner/trepo", "--live"}); !errors.Is(err, guardrails.ErrUnguarded) {
		t.Fatalf("prepareListen with --live: %v, want the unguarded refusal", err)
	}
	t.Setenv("SHIPYARD_MODE", "live")
	if _, err := prepareListen([]string{"--repo", "towner/trepo"}); !errors.Is(err, guardrails.ErrUnguarded) {
		t.Fatalf("prepareListen with SHIPYARD_MODE=live: %v, want the unguarded refusal", err)
	}
	if _, err := prepareListen([]string{"--repo", "towner/trepo", "--live", "--i-know-this-is-unguarded"}); err != nil {
		t.Fatalf("prepareListen with --live and the flag: %v, want the run to proceed", err)
	}
	t.Setenv("SHIPYARD_MODE", "")
	if _, err := prepareListen([]string{"--repo", "towner/trepo"}); err != nil {
		t.Fatalf("prepareListen back in the dry-run default: %v, want the run to proceed", err)
	}
}

// TestPrepareListenVerbose: --verbose (or SHIPYARD_VERBOSE) lands in
// listen.Options; off by default, and an explicit --verbose=false beats
// the environment, flag-over-env like the other settings.
func TestPrepareListenVerbose(t *testing.T) {
	resetGuardrailEnv(t)
	t.Setenv("SHIPYARD_VERBOSE", "")

	run, err := prepareListen([]string{"--repo", "towner/trepo"})
	if err != nil {
		t.Fatalf("prepareListen: %v", err)
	}
	if run.Options.Verbose {
		t.Error("Options.Verbose = true, want verbose off by default")
	}

	t.Setenv("SHIPYARD_VERBOSE", "1")
	run, err = prepareListen([]string{"--repo", "towner/trepo"})
	if err != nil {
		t.Fatalf("prepareListen with SHIPYARD_VERBOSE=1: %v", err)
	}
	if !run.Options.Verbose {
		t.Error("SHIPYARD_VERBOSE=1 must land in the loop as verbose on")
	}

	// An explicit flag wins over the environment.
	run, err = prepareListen([]string{"--repo", "towner/trepo", "--verbose=false"})
	if err != nil {
		t.Fatalf("prepareListen with --verbose=false over env: %v", err)
	}
	if run.Options.Verbose {
		t.Error("an explicit --verbose=false must beat SHIPYARD_VERBOSE=1")
	}
}

// TestPrepareListenExplicitAll: --all starts a live run with no
// allowlist (the run is deliberately unguarded, on purpose), and it is
// a configuration error combined with any allowlist source: --repos,
// --labels, or the repeatable --label.
func TestPrepareListenExplicitAll(t *testing.T) {
	resetGuardrailEnv(t)

	run, err := prepareListen([]string{"--repo", "towner/trepo", "--live", "--all"})
	if err != nil {
		t.Fatalf("prepareListen with --live --all: %v, want the run to start", err)
	}
	if !run.Options.All {
		t.Error("Options.All = false, want --all to reach the loop")
	}
	if run.Options.Unguarded {
		t.Error("--all must not be folded into the hidden alias's option")
	}
	for _, args := range [][]string{
		{"--repo", "towner/trepo", "--live", "--all", "--repos", "towner/trepo"},
		{"--repo", "towner/trepo", "--live", "--all", "--labels", "bug"},
		{"--repo", "towner/trepo", "--live", "--all", "--label", "bug"},
	} {
		if _, err := prepareListen(args); err == nil || !strings.Contains(err.Error(), "conflict") {
			t.Errorf("prepareListen %v = %v, want the --all/allowlist conflict", args, err)
		}
	}
}

// TestPrepareListenModeResolution: the mapping from --live/--dry-run
// flags and SHIPYARD_MODE onto the loop's DryRun option (flag wins over
// environment; --live and --dry-run conflict).
func TestPrepareListenModeResolution(t *testing.T) {
	resetGuardrailEnv(t)
	t.Setenv(envRepos, "towner/trepo") // guarded, so live modes may start

	run, err := prepareListen([]string{"--repo", "towner/trepo", "--live"})
	if err != nil {
		t.Fatalf("prepareListen with --live: %v", err)
	}
	if run.Options.DryRun {
		t.Error("--live must land in the loop as a live run")
	}
	run, err = prepareListen([]string{"--repo", "towner/trepo", "--dry-run"})
	if err != nil {
		t.Fatalf("prepareListen with --dry-run: %v", err)
	}
	if !run.Options.DryRun {
		t.Error("--dry-run must land in the loop as a dry run")
	}
	t.Setenv("SHIPYARD_MODE", "live")
	run, err = prepareListen([]string{"--repo", "towner/trepo"})
	if err != nil {
		t.Fatalf("prepareListen with SHIPYARD_MODE=live: %v", err)
	}
	if run.Options.DryRun {
		t.Error("SHIPYARD_MODE=live must land in the loop as a live run")
	}
	// The flag wins over the environment.
	run, err = prepareListen([]string{"--repo", "towner/trepo", "--dry-run"})
	if err != nil {
		t.Fatalf("prepareListen with --dry-run over live env: %v", err)
	}
	if !run.Options.DryRun {
		t.Error("the --dry-run flag must beat SHIPYARD_MODE=live")
	}
	if _, err := prepareListen([]string{"--repo", "towner/trepo", "--live", "--dry-run"}); err == nil {
		t.Error("--live and --dry-run together: expected an error")
	}
}

// TestPrepareListenAgentContextWindow: --agent-context-window (or its
// env) lands in the agent config as the context window declared to the
// agent (flag wins over environment; both unset leaves the piagent
// default to be applied).
func TestPrepareListenAgentContextWindow(t *testing.T) {
	resetGuardrailEnv(t)
	t.Setenv("SHIPYARD_AGENT_CONTEXT_WINDOW", "")

	run, err := prepareListen([]string{"--repo", "towner/trepo"})
	if err != nil {
		t.Fatalf("prepareListen: %v", err)
	}
	if run.Options.AgentConfig.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 (the piagent default applies)", run.Options.AgentConfig.ContextWindow)
	}

	t.Setenv("SHIPYARD_AGENT_CONTEXT_WINDOW", "4096")
	run, err = prepareListen([]string{"--repo", "towner/trepo"})
	if err != nil {
		t.Fatalf("prepareListen with env: %v", err)
	}
	if run.Options.AgentConfig.ContextWindow != 4096 {
		t.Errorf("ContextWindow = %d, want 4096 from the environment", run.Options.AgentConfig.ContextWindow)
	}

	// The flag wins over the environment.
	run, err = prepareListen([]string{"--repo", "towner/trepo", "--agent-context-window", "8192"})
	if err != nil {
		t.Fatalf("prepareListen with the flag: %v", err)
	}
	if run.Options.AgentConfig.ContextWindow != 8192 {
		t.Errorf("ContextWindow = %d, want 8192 from the flag", run.Options.AgentConfig.ContextWindow)
	}
}
