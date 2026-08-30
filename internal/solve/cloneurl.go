package solve

import "strings"

// CloneURLWithToken embeds the GitHub token in an https clone URL so the
// git CLI can authenticate. Other URL schemes (ssh, file://) are passed
// through unchanged; git's own credentials handling applies there.
func CloneURLWithToken(cloneURL, token string) string {
	if token == "" {
		return cloneURL
	}
	if strings.HasPrefix(cloneURL, "https://") {
		return "https://x-access-token:" + token + "@" + strings.TrimPrefix(cloneURL, "https://")
	}
	return cloneURL
}