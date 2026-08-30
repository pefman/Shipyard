package main

import (
	"errors"
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
	t.Setenv("SHIPYARD_GITHUB_TOKEN", "gh-test-token")
	t.Setenv("SHIPYARD_AI_ENDPOINT", "http://127.0.0.1:1/v1")
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

// TestPrepareListenUnguardedRefused: with no allowlist anywhere and no
// acknowledgment flag, preparation itself refuses the run.
func TestPrepareListenUnguardedRefused(t *testing.T) {
	resetGuardrailEnv(t)

	if _, err := prepareListen([]string{"--repo", "towner/trepo"}); !errors.Is(err, guardrails.ErrUnguarded) {
		t.Fatalf("prepareListen = %v, want the unguarded refusal", err)
	}
	if _, err := prepareListen([]string{"--repo", "towner/trepo", "--i-know-this-is-unguarded"}); err != nil {
		t.Fatalf("prepareListen with the flag: %v, want the run to proceed", err)
	}
}
