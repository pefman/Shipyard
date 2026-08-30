// Package guardrails holds Shipyard's safety contract for unattended
// operation: repo and label allowlists that limit which issues the tool
// may solve, and a per-run cap on how many pull requests it may open.
//
// The policy is simple: Shipyard only acts on issues the operator has
// opted in for (an allowed repository and/or an allowed label), and a
// single run can never open more than --max-prs pull requests. A run
// with neither allowlist set is "unguarded" and is refused unless the
// operator explicitly acknowledges it with --i-know-this-is-unguarded.
// That refusal applies to live runs only: dry runs commit nothing and
// open no pull requests, so they need no allowlist (and listen starts
// in dry-run mode by default — see Mode).
package guardrails

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// DefaultMaxPRs is the pull-request budget for a run when neither the
// --max-prs flag nor SHIPYARD_MAX_PRS is set.
const DefaultMaxPRs = 3

// EnvMode is the environment variable that selects the run mode
// ("live" or "dry-run"); it is overridden by an explicit --live /
// --dry-run flag.
const EnvMode = "SHIPYARD_MODE"

// Mode is how far a run may go: dry runs run the full solving flow but
// stop before any commit, push, or pull request; live runs deliver.
type Mode string

// The two run modes.
const (
	ModeLive   Mode = "live"
	ModeDryRun Mode = "dry-run"
)

// ParseMode parses a SHIPYARD_MODE value ("live" or "dry-run",
// case-insensitive). An empty or unrecognized value is an error.
func ParseMode(value string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "live":
		return ModeLive, nil
	case "dry-run":
		return ModeDryRun, nil
	default:
		return "", fmt.Errorf("invalid %s value %q: expected live or dry-run", EnvMode, value)
	}
}

// ResolveMode decides the run mode of one command: an explicit --live
// or --dry-run flag wins, then the SHIPYARD_MODE environment value, then
// the command's default mode (live for solve, dry-run for listen). An
// empty modeEnv means the environment variable is unset; an empty def
// means "live".
func ResolveMode(live, dryRun bool, modeEnv string, def Mode) (Mode, error) {
	if live && dryRun {
		return "", errors.New("--live and --dry-run conflict: pass one or the other (or set " + EnvMode + ")")
	}
	if dryRun {
		return ModeDryRun, nil
	}
	if live {
		return ModeLive, nil
	}
	if modeEnv != "" {
		return ParseMode(modeEnv)
	}
	if def == "" {
		return ModeLive, nil
	}
	return def, nil
}

// ErrUnguarded is returned when a live run has neither a repo nor a
// label allowlist set and the operator has not acknowledged the risk
// with --i-know-this-is-unguarded.
var ErrUnguarded = errors.New(`no repo or label allowlist is set: this run would be unguarded and could act on any issue in the repository.

Shipyard only solves issues you have opted in for:
  --repos   comma-separated owner/repo allowlist (env SHIPYARD_REPOS)
  --labels  comma-separated label allowlist  (env SHIPYARD_LABELS)
When either is set, only issues in allowed repos carrying an allowed
label are solved. With neither set, passing
--i-know-this-is-unguarded acknowledges the risk and lets the run
proceed.`)

// ParseList splits a comma-separated value (a flag or an env var) into
// trimmed, non-empty entries.
func ParseList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Allow is a parsed repo/label allowlist. Either list may be empty; an
// empty list means "no restriction on that axis".
type Allow struct {
	// Repos is the allowlist of repositories, each normalized to
	// lowercase owner/repo. Empty means every repository is allowed.
	Repos []string
	// Labels is the allowlist of issue labels, case-insensitive.
	// Empty means every label is allowed.
	Labels []string
}

// NewAllow validates and normalizes raw allowlist entries. Repository
// entries must look like owner/repo (both parts non-empty); label
// entries must be non-empty after trimming.
func NewAllow(repos, labels []string) (*Allow, error) {
	a := &Allow{}
	for _, r := range repos {
		// Whitespace anywhere in the entry is dropped, so " Owner /
		// Repo " means owner/repo.
		r = strings.Map(func(rune rune) rune {
			if unicode.IsSpace(rune) {
				return -1
			}
			return rune
		}, strings.ToLower(r))
		parts := strings.Split(r, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid repo allowlist entry %q: expected owner/repo", r)
		}
		if !contains(a.Repos, r) {
			a.Repos = append(a.Repos, r)
		}
	}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !containsFold(a.Labels, l) {
			a.Labels = append(a.Labels, l)
		}
	}
	sort.Strings(a.Repos)
	sort.Slice(a.Labels, func(i, j int) bool {
		return strings.ToLower(a.Labels[i]) < strings.ToLower(a.Labels[j])
	})
	return a, nil
}

// Configured reports whether at least one allowlist is set. A run that
// is not configured is unguarded.
func (a *Allow) Configured() bool {
	if a == nil {
		return false
	}
	return len(a.Repos) > 0 || len(a.Labels) > 0
}

// RepoAllowed reports whether owner/repo passes the repo allowlist.
// With no repo allowlist set, every repo is allowed.
func (a *Allow) RepoAllowed(owner, repo string) bool {
	if a == nil || len(a.Repos) == 0 {
		return true
	}
	return contains(a.Repos, strings.ToLower(owner+"/"+repo))
}

// LabelsAllowed reports whether the issue carries at least one allowed
// label. With no label allowlist set, every issue passes.
func (a *Allow) LabelsAllowed(issueLabels []string) bool {
	if a == nil || len(a.Labels) == 0 {
		return true
	}
	for _, have := range issueLabels {
		for _, want := range a.Labels {
			if strings.EqualFold(have, want) {
				return true
			}
		}
	}
	return false
}

// Gate applies the unguarded rule: a run with neither allowlist set
// may proceed only if the operator passed --i-know-this-is-unguarded.
// It returns ErrUnguarded (or nil); the caller logs the loud warning
// in both refusal and acknowledgment cases.
func (a *Allow) Gate(iKnowThisIsUnguarded bool) error {
	if a.Configured() || iKnowThisIsUnguarded {
		return nil
	}
	return ErrUnguarded
}

// Summary is a one-line rendering of the guardrails, for audit output.
func (a *Allow) Summary() string {
	if a == nil {
		return "unguarded (no allowlists)"
	}
	parts := []string{}
	if len(a.Repos) > 0 {
		parts = append(parts, "repos: "+strings.Join(a.Repos, ", "))
	}
	if len(a.Labels) > 0 {
		parts = append(parts, "labels: "+strings.Join(a.Labels, ", "))
	}
	if len(parts) == 0 {
		return "unguarded (no allowlists)"
	}
	return strings.Join(parts, "; ")
}

// PRCap is a per-run counter bounding how many pull requests a run may
// open. It is safe for concurrent use.
type PRCap struct {
	mu     sync.Mutex
	opened int
	max    int
}

// NewPRCap creates a cap. A negative max selects DefaultMaxPRs; zero
// means the run may open no pull requests at all.
func NewPRCap(max int) *PRCap {
	if max < 0 {
		max = DefaultMaxPRs
	}
	return &PRCap{max: max}
}

// Max is the configured budget.
func (c *PRCap) Max() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.max
}

// CanOpen reports whether the run may open one more pull request.
func (c *PRCap) CanOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opened < c.max
}

// Opened records one more opened pull request.
func (c *PRCap) Opened() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opened++
}

// Count is how many pull requests this run has opened so far.
func (c *PRCap) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opened
}

// Exhausted reports whether the budget is used up.
func (c *PRCap) Exhausted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opened >= c.max
}

// ParseMaxPRs converts a --max-prs / SHIPYARD_MAX_PRS value to an int.
// An empty value means "unset" and returns -1 (the caller then applies
// DefaultMaxPRs).
func ParseMaxPRs(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return -1, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("invalid --max-prs / SHIPYARD_MAX_PRS value %q: want a number of pull requests", value)
	}
	if n < 0 {
		return 0, fmt.Errorf("--max-prs must not be negative (got %d)", n)
	}
	return n, nil
}

func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

func containsFold(values []string, v string) bool {
	for _, x := range values {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
