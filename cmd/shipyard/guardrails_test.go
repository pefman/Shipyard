package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pefman/Shipyard/internal/guardrails"
)

// stderrOf temporarily reroutes os.Stderr and returns what fn writes to
// it, so the audit-line output of applyGuardrails is testable.
func stderrOf(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stderr: %v", err)
	}
	return string(out)
}

// TestApplyGuardrailsResolution covers the flag-wins-over-environment
// rule and the environment fallbacks for the guardrail settings.
func TestApplyGuardrailsResolution(t *testing.T) {
	t.Setenv("SHIPYARD_REPOS", "")
	t.Setenv("SHIPYARD_LABELS", "")
	t.Setenv("SHIPYARD_MAX_PRS", "")
	t.Setenv("SHIPYARD_MODE", "")

	g := guardrailInput{owner: "towner", repo: "trepo", dryRun: true, quiet: true, maxPRsFlag: -1}

	// An unguarded dry run opens nothing, so it needs no acknowledgment
	// and the default pull-request budget.
	if _, max, err := applyGuardrails(guardrailInput{owner: "towner", repo: "trepo", dryRun: true, quiet: true, maxPRsFlag: -1}); err != nil {
		t.Fatalf("unguarded dry run: %v, want it to be accepted", err)
	} else if max != 3 {
		t.Errorf("max-prs = %d, want the default 3", max)
	}
	// An unguarded live run without the flag is refused ...
	if _, _, err := applyGuardrails(guardrailInput{owner: "towner", repo: "trepo", dryRun: false, quiet: true, maxPRsFlag: -1}); !errors.Is(err, guardrails.ErrUnguarded) {
		t.Fatalf("unguarded live run without the flag: %v, want the unguarded refusal", err)
	}
	// ... but the acknowledgment flag lets it proceed.
	if _, _, err := applyGuardrails(guardrailInput{owner: "towner", repo: "trepo", dryRun: false, quiet: true, unguarded: true, maxPRsFlag: -1}); err != nil {
		t.Fatalf("unguarded live run acknowledged with the flag: %v", err)
	}

	// Environment fallbacks.
	t.Setenv("SHIPYARD_REPOS", "towner/trepo")
	allow, _, err := applyGuardrails(g)
	if err != nil {
		t.Fatalf("repos from env: %v", err)
	}
	if !allow.RepoAllowed("towner", "trepo") || allow.RepoAllowed("other", "repo") {
		t.Errorf("env repo allowlist not applied: %v", allow.Repos)
	}

	// The flag wins over the environment.
	t.Setenv("SHIPYARD_LABELS", "env-label")
	allow, _, err = applyGuardrails(guardrailInput{labelsFlag: "flag-label", owner: "towner", repo: "trepo", dryRun: true, quiet: true})
	if err != nil {
		t.Fatalf("labels flag over env: %v", err)
	}
	if len(allow.Labels) != 1 || allow.Labels[0] != "flag-label" {
		t.Errorf("Labels = %v, want [flag-label] (flag wins over SHIPYARD_LABELS)", allow.Labels)
	}

	// The pull-request budget: flag, then env, then the default.
	t.Setenv("SHIPYARD_MAX_PRS", "7")
	if _, max, err := applyGuardrails(g); err != nil || max != 7 {
		t.Errorf("max-prs from env = (%d, %v), want 7", max, err)
	}
	if _, max, err := applyGuardrails(guardrailInput{maxPRsFlag: 2, owner: "towner", repo: "trepo", dryRun: true, quiet: true}); err != nil || max != 2 {
		t.Errorf("max-prs flag over env = (%d, %v), want 2", max, err)
	}

	// A malformed SHIPYARD_MAX_PRS is a configuration error.
	t.Setenv("SHIPYARD_MAX_PRS", "three")
	if _, _, err := applyGuardrails(g); err == nil {
		t.Error("malformed SHIPYARD_MAX_PRS: expected an error")
	}

	// A live run with a zero budget is refused.
	t.Setenv("SHIPYARD_MAX_PRS", "")
	if _, _, err := applyGuardrails(guardrailInput{maxPRsFlag: 0, owner: "towner", repo: "trepo", dryRun: false, unguarded: true, quiet: true}); err == nil {
		t.Error("live run with --max-prs 0: expected an error")
	}
}

// TestApplyGuardrailsRepoGate: the target repo must be on the repo
// allowlist, from flag or environment.
func TestApplyGuardrailsRepoGate(t *testing.T) {
	t.Setenv("SHIPYARD_REPOS", "")
	t.Setenv("SHIPYARD_LABELS", "")
	t.Setenv("SHIPYARD_MAX_PRS", "")

	if _, _, err := applyGuardrails(guardrailInput{reposFlag: "other/repo", owner: "towner", repo: "trepo", dryRun: true, quiet: true}); err == nil {
		t.Error("a repo outside the allowlist: expected an error")
	}
	if _, _, err := applyGuardrails(guardrailInput{reposFlag: "towner/trepo", owner: "towner", repo: "trepo", dryRun: true, quiet: true}); err != nil {
		t.Errorf("a repo on the allowlist: %v, want nil", err)
	}
	// Invalid allowlist entries are a configuration error.
	if _, _, err := applyGuardrails(guardrailInput{reposFlag: "justowner", owner: "towner", repo: "trepo", dryRun: true, quiet: true}); err == nil {
		t.Error("an invalid repo allowlist entry: expected an error")
	}
}

// TestApplyGuardrailsExplicitAll: --all marks the repo/label axis as
// explicitly unrestricted ("no allowlist, on purpose"): an unguarded
// live run starts, the audit line says the run is deliberately
// unguarded, and --all combined with a set allowlist is a
// configuration error.
func TestApplyGuardrailsExplicitAll(t *testing.T) {
	t.Setenv(envRepos, "")
	t.Setenv(envLabels, "")
	t.Setenv(envMaxPRs, "")

	// --all alone lets an unguarded live run start ...
	if _, _, err := applyGuardrails(guardrailInput{owner: "towner", repo: "trepo", dryRun: false, quiet: true, all: true, maxPRsFlag: -1}); err != nil {
		t.Fatalf("--all alone: %v, want the run to start", err)
	}
	// ... and the audit line reports the explicitly unrestricted axis.
	out := stderrOf(t, func() {
		if _, _, err := applyGuardrails(guardrailInput{owner: "towner", repo: "trepo", dryRun: false, all: true, maxPRsFlag: 2}); err != nil {
			t.Errorf("--all (audit line): %v", err)
		}
	})
	if !strings.Contains(out, "guardrails: NONE (explicit --all); max-prs: 2") {
		t.Errorf("audit output = %q, want the explicit --all audit line", out)
	}
	// --all conflicts with any set allowlist: flag or environment.
	if _, _, err := applyGuardrails(guardrailInput{reposFlag: "towner/trepo", all: true, owner: "towner", repo: "trepo", dryRun: true, quiet: true}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("--all + --repos = %v, want the conflict error", err)
	}
	if _, _, err := applyGuardrails(guardrailInput{labelsFlag: "bug", all: true, owner: "towner", repo: "trepo", dryRun: true, quiet: true}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("--all + --labels = %v, want the conflict error", err)
	}
	t.Setenv(envRepos, "towner/trepo")
	if _, _, err := applyGuardrails(guardrailInput{all: true, owner: "towner", repo: "trepo", dryRun: true, quiet: true}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("--all + SHIPYARD_REPOS = %v, want the conflict error", err)
	}
}
