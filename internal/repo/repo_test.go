package repo

import (
	"strings"
	"testing"
)

func TestNormalizeAccepted(t *testing.T) {
	const wantOwner = "pefman"
	const wantRepo = "test123"

	accepted := []string{
		"pefman/test123",
		"pefman/test123.git",
		"https://github.com/pefman/test123",
		"https://github.com/pefman/test123.git",
		"https://github.com/pefman/test123/",
		"http://github.com/pefman/test123.git",
		"git@github.com:pefman/test123",
		"git@github.com:pefman/test123.git",
		"ssh://git@github.com/pefman/test123",
		"ssh://git@github.com/pefman/test123.git",
		"ssh://github.com/pefman/test123",
		"github.com/pefman/test123",
		"github.com/pefman/test123.git",
		// host matching is case-insensitive
		"https://GitHub.com/pefman/test123",
		"HTTPS://GITHUB.COM/pefman/test123",
		"git@GITHUB.COM:pefman/test123",
		"Github.com/pefman/test123",
		// surrounding whitespace is tolerated
		"  pefman/test123  ",
	}
	for _, in := range accepted {
		owner, name, err := Normalize(in)
		if err != nil {
			t.Errorf("Normalize(%q) unexpected error: %v", in, err)
			continue
		}
		if owner != wantOwner || name != wantRepo {
			t.Errorf("Normalize(%q) = (%q, %q), want (%q, %q)",
				in, owner, name, wantOwner, wantRepo)
		}
	}
}

func TestNormalizeInvalid(t *testing.T) {
	invalid := []struct {
		in      string
		contain string // substring the error must mention
	}{
		{"", ""},
		{"   ", ""},
		{"test123", ""},        // no slash
		{"/test123", ""},       // empty owner
		{"pefman/", ""},        // empty repo
		{".git", ""},           // empty owner and repo
		{"pefman", ""},
		{"https://gitlab.com/pefman/test123", "gitlab.com"}, // unknown host
		{"https://gitlab.com/pefman/test123.git", "gitlab.com"},
		{"git@gitlab.com:pefman/test123", "gitlab.com"},     // unknown host, scp
		{"ssh://git@gitlab.com/pefman/test123", "gitlab.com"},
		{"gitlab.com/pefman/test123", "gitlab.com"}, // no-scheme unknown host
		{"git+https://github.com/pefman/test123", "scheme"}, // unsupported scheme
		{"ftp://github.com/pefman/test123", "scheme"},
		{"https://github.com/pefman", ""},          // missing repo segment
		{"https://github.com/pefman/a/b", ""},      // too many segments
		{"git@github.com/pefman/test123", ""},      // git@ but no colon
	}
	for _, tc := range invalid {
		owner, name, err := Normalize(tc.in)
		if err == nil {
			t.Errorf("Normalize(%q) = (%q, %q), want error", tc.in, owner, name)
			continue
		}
		if tc.contain != "" && !containsFold(err.Error(), tc.contain) {
			t.Errorf("Normalize(%q) error %q does not mention %q", tc.in, err, tc.contain)
		}
	}
}

// TestNormalizeErrorsListAcceptedForms checks the error message names the
// accepted spellings so users can fix the input without reading source.
func TestNormalizeErrorsListAcceptedForms(t *testing.T) {
	_, _, err := Normalize("nonsense")
	if err == nil {
		t.Fatal("Normalize(nonsense) succeeded, want error")
	}
	for _, want := range []string{"owner/repo", "https://github.com/", "git@github.com:"} {
		if !containsFold(err.Error(), want) {
			t.Errorf("error %q does not list accepted form %q", err, want)
		}
	}
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 ||
		strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}