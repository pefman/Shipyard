// Package repo normalizes the many spellings a user might paste into
// --repo into the canonical owner/repo pair Shipyard works with.
package repo

import (
	"fmt"
	"net/url"
	"strings"
)

// Host is the only host Shipyard supports: every accepted --repo form must
// point at github.com (case-insensitive).
const Host = "github.com"

// Accepted describes the input forms Normalize understands, phrased for use
// in error messages.
const Accepted = "owner/repo, a https://github.com/owner/repo URL, or git@github.com:owner/repo"

// Normalize turns any accepted spelling of a GitHub repository into the
// canonical owner/repo pair:
//
//	pefman/test123
//	https://github.com/pefman/test123[.git]
//	git@github.com:pefman/test123[.git]
//	ssh://git@github.com/pefman/test123[.git]
//	github.com/pefman/test123[.git]
//
// A trailing ".git" is stripped; the host is matched case-insensitively.
// Input that does not match an accepted form yields an error that lists
// the accepted forms.
func Normalize(s string) (owner, repo string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("invalid --repo %q: expected %s", s, Accepted)
	}

	if scheme, rest, ok := strings.Cut(s, "://"); ok {
		return fromURL(s, strings.ToLower(scheme), rest)
	}
	if at, rest, ok := strings.Cut(s, "@"); ok {
		// scp-style: git@github.com:owner/repo
		if at != "git" || !strings.ContainsRune(rest, ':') {
			return "", "", invalid(s)
		}
		return hostAndPath(s, rest)
	}
	return hostAndPath(s, s)
}

// fromURL handles http://, https://, and ssh:// URLs.
func fromURL(orig, scheme, rest string) (owner, repo string, err error) {
	if scheme != "https" && scheme != "http" && scheme != "ssh" {
		return "", "", fmt.Errorf("invalid --repo %q: unsupported scheme %q, expected %s",
			orig, scheme, Accepted)
	}
	u, err := url.Parse(scheme + "://" + rest)
	if err != nil || u.Host == "" {
		return "", "", invalid(orig)
	}
	return hostAndPath(orig, u.Hostname()+u.Path)
}

// hostAndPath parses the "host/owner/repo" or "host:owner/repo" part of the
// input (or a bare "owner/repo") and enforces the supported host.
func hostAndPath(orig, s string) (owner, repo string, err error) {
	// scp-style "host:path" is equivalent to "host/path".
	s = strings.Replace(s, ":", "/", 1)
	parts := strings.Split(strings.Trim(s, "/"), "/")
	// A leading segment that looks like a hostname (contains a dot) is
	// treated as the host prefix and must be github.com; a bare
	// "owner/repo" without a host is accepted as-is.
	if len(parts) > 0 && strings.Contains(parts[0], ".") {
		if !strings.EqualFold(parts[0], Host) {
			return "", "", fmt.Errorf("invalid --repo %q: only %s repositories are supported (host %q)",
				orig, Host, parts[0])
		}
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return "", "", invalid(orig)
	}
	owner, repo = parts[0], parts[1]
	repo = strings.TrimSuffix(repo, ".git")
	if owner == "" || repo == "" {
		return "", "", invalid(orig)
	}
	return owner, repo, nil
}

// invalid formats the standard error listing the accepted forms.
func invalid(s string) error {
	return fmt.Errorf("invalid --repo %q: expected %s", s, Accepted)
}