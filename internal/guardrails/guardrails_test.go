package guardrails

import (
	"testing"
)

func TestParseList(t *testing.T) {
	got := ParseList(" a/b ,, c/d ,  ")
	if len(got) != 2 || got[0] != "a/b" || got[1] != "c/d" {
		t.Errorf("ParseList = %v, want [a/b c/d]", got)
	}
	if got := ParseList(""); len(got) != 0 {
		t.Errorf("ParseList(\"\") = %v, want empty", got)
	}
}

func TestNewAllowNormalizes(t *testing.T) {
	a, err := NewAllow([]string{" Owner / Repo ", "a/b", "a/b"}, []string{"Bug", "bug", "  shipyard "})
	if err != nil {
		t.Fatalf("NewAllow: %v", err)
	}
	if len(a.Repos) != 2 || a.Repos[0] != "a/b" || a.Repos[1] != "owner/repo" {
		t.Errorf("Repos = %v, want [a/b owner/repo] (deduped, normalized)", a.Repos)
	}
	if len(a.Labels) != 2 {
		t.Errorf("Labels = %v, want 2 entries (case-insensitive dedup)", a.Labels)
	}
}

func TestNewAllowRejectsBadRepoEntries(t *testing.T) {
	for _, entry := range []string{"justowner", "a/b/c", "/repo", "owner/"} {
		if _, err := NewAllow([]string{entry}, nil); err == nil {
			t.Errorf("NewAllow(%q): expected an error for an invalid repo entry", entry)
		}
	}
}

func TestAllowConfigured(t *testing.T) {
	var nilAllow *Allow
	if nilAllow.Configured() {
		t.Error("nil Allow.Configured = true, want false")
	}
	if (&Allow{}).Configured() {
		t.Error("empty Allow.Configured = true, want false")
	}
	a, _ := NewAllow([]string{"o/r"}, nil)
	if !a.Configured() {
		t.Error("repo-only Allow.Configured = false, want true")
	}
	b, _ := NewAllow(nil, []string{"shipyard"})
	if !b.Configured() {
		t.Error("label-only Allow.Configured = false, want true")
	}
}

func TestRepoAllowed(t *testing.T) {
	a, _ := NewAllow([]string{"octo/repo", "other/thing"}, nil)
	if !a.RepoAllowed("octo", "repo") {
		t.Error("octo/repo should be allowed")
	}
	// Repository matching is case-insensitive, like GitHub's.
	if !a.RepoAllowed("Octo", "REPO") {
		t.Error("repo allowlist matching should be case-insensitive")
	}
	if a.RepoAllowed("octo", "other") {
		t.Error("octo/other is not in the allowlist")
	}
	if !(&Allow{}).RepoAllowed("anyone", "anything") {
		t.Error("with no repo allowlist, every repo should be allowed")
	}
	var nilAllow *Allow
	if !nilAllow.RepoAllowed("anyone", "anything") {
		t.Error("a nil Allow should allow every repo")
	}
}

func TestLabelsAllowed(t *testing.T) {
	a, _ := NewAllow(nil, []string{"shipyard", "bug"})
	if !a.LabelsAllowed([]string{"design", "Shipyard"}) {
		t.Error("an issue with label 'shipyard' (any case) should pass")
	}
	if a.LabelsAllowed([]string{"design"}) {
		t.Error("an issue without any allowed label should fail")
	}
	if a.LabelsAllowed(nil) {
		t.Error("an unlabeled issue should not pass a label allowlist")
	}
	if !(&Allow{}).LabelsAllowed(nil) {
		t.Error("with no label allowlist, every issue should pass")
	}
}

func TestGate(t *testing.T) {
	a, _ := NewAllow([]string{"o/r"}, nil)
	if err := a.Gate(false); err != nil {
		t.Errorf("guarded run without the flag: %v, want nil", err)
	}
	unguarded := &Allow{}
	if err := unguarded.Gate(false); err == nil {
		t.Error("unguarded run without the flag must be refused")
	} else if err != ErrUnguarded {
		t.Errorf("unguarded refusal = %v, want ErrUnguarded", err)
	}
	if err := unguarded.Gate(true); err != nil {
		t.Errorf("unguarded run with --i-know-this-is-unguarded: %v, want nil", err)
	}
}

func TestSummary(t *testing.T) {
	a, _ := NewAllow([]string{"octo/repo"}, []string{"shipyard"})
	if got := a.Summary(); got != "repos: octo/repo; labels: shipyard" {
		t.Errorf("Summary = %q", got)
	}
	if got := (&Allow{}).Summary(); got != "unguarded (no allowlists)" {
		t.Errorf("Summary of empty Allow = %q", got)
	}
}

func TestPRCapDefault(t *testing.T) {
	c := NewPRCap(-1)
	if c.Max() != DefaultMaxPRs {
		t.Errorf("Max = %d, want the default %d", c.Max(), DefaultMaxPRs)
	}
	for i := 0; i < DefaultMaxPRs; i++ {
		if !c.CanOpen() {
			t.Fatalf("CanOpen = false after %d opened, want true", i)
		}
		c.Opened()
	}
	if c.CanOpen() || !c.Exhausted() {
		t.Errorf("after %d PRs the cap should be exhausted", DefaultMaxPRs)
	}
	if c.Count() != DefaultMaxPRs {
		t.Errorf("Count = %d, want %d", c.Count(), DefaultMaxPRs)
	}
}

func TestPRCapZero(t *testing.T) {
	c := NewPRCap(0)
	if c.CanOpen() {
		t.Error("a zero cap must not allow any pull request")
	}
	c.Opened() // record defensively; the cap stays exhausted
	if !c.Exhausted() {
		t.Error("a zero cap is always exhausted")
	}
}

func TestPRCapCustom(t *testing.T) {
	c := NewPRCap(2)
	c.Opened()
	if c.Count() != 1 || !c.CanOpen() {
		t.Fatalf("after 1 of 2: Count = %d, CanOpen = %v", c.Count(), c.CanOpen())
	}
	c.Opened()
	if c.CanOpen() || !c.Exhausted() {
		t.Error("after 2 of 2 the cap should be exhausted")
	}
}

func TestParseMaxPRs(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{
		{"", -1},
		{" ", -1},
		{"5", 5},
		{" 3 ", 3},
		{"0", 0},
	} {
		if got, err := ParseMaxPRs(tc.value); err != nil || got != tc.want {
			t.Errorf("ParseMaxPRs(%q) = (%d, %v), want (%d, nil)", tc.value, got, err, tc.want)
		}
	}
	for _, bad := range []string{"three", "-1"} {
		if _, err := ParseMaxPRs(bad); err == nil {
			t.Errorf("ParseMaxPRs(%q): expected an error", bad)
		}
	}
}

func TestParseMode(t *testing.T) {
	for value, want := range map[string]Mode{"live": ModeLive, " Live ": ModeLive, "DRY-RUN": ModeDryRun} {
		if got, err := ParseMode(value); err != nil || got != want {
			t.Errorf("ParseMode(%q) = (%v, %v), want (%v, nil)", value, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "production", "dryrun"} {
		if _, err := ParseMode(bad); err == nil {
			t.Errorf("ParseMode(%q): expected an error", bad)
		}
	}
}

func TestResolveMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		live    bool
		dryRun  bool
		modeEnv string
		def     Mode
		want    Mode
		wantErr bool
	}{
		{name: "listen default: dry-run", def: ModeDryRun, want: ModeDryRun},
		{name: "solve default: live", def: ModeLive, want: ModeLive},
		{name: "no default means live", want: ModeLive},
		{name: "--live flag", live: true, def: ModeDryRun, want: ModeLive},
		{name: "--dry-run flag", dryRun: true, def: ModeLive, want: ModeDryRun},
		{name: "live flag beats dry-run env", live: true, modeEnv: "dry-run", def: ModeDryRun, want: ModeLive},
		{name: "dry-run flag beats live env", dryRun: true, modeEnv: "live", def: ModeLive, want: ModeDryRun},
		{name: "env live", modeEnv: "live", def: ModeDryRun, want: ModeLive},
		{name: "env dry-run", modeEnv: "dry-run", def: ModeLive, want: ModeDryRun},
		{name: "live and dry-run conflict", live: true, dryRun: true, wantErr: true},
		{name: "invalid env value", modeEnv: "production", def: ModeDryRun, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveMode(tc.live, tc.dryRun, tc.modeEnv, tc.def)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveMode = %v, want an error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("ResolveMode = (%v, %v), want (%v, nil)", got, err, tc.want)
			}
		})
	}
}
