package main

import (
	"context"
	"flag"
	"os"

	"github.com/pefman/Shipyard/internal/auth"
)

// defaultGitHubClientID is the client ID of the pre-registered GitHub
// OAuth App that ships with shipyard, so `shipyard login` works with zero
// configuration. Client IDs are public identifiers, not secrets. Users
// with their own OAuth App can override via --github-client-id or
// SHIPYARD_GITHUB_CLIENT_ID.
const defaultGitHubClientID = "Iv23lipRhtA8srclwbp3"

// resolveClientID applies the client-ID precedence: the --github-client-id
// flag value, then the SHIPYARD_GITHUB_CLIENT_ID environment variable, then
// the built-in default.
func resolveClientID(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("SHIPYARD_GITHUB_CLIENT_ID"); env != "" {
		return env
	}
	return defaultGitHubClientID
}

// runLogin implements `shipyard login`: the GitHub OAuth device flow, or
// a fast-path verification of an already-stored token.
func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	clientID := fs.String("github-client-id", "", "GitHub OAuth App client ID (env SHIPYARD_GITHUB_CLIENT_ID; default: the built-in pre-registered app)")
	force := fs.Bool("force", false, "redo the device flow even if a valid stored token exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	id := resolveClientID(*clientID)

	if _, err := auth.Run(context.Background(), auth.Deps{
		ClientID: id,
		Force:    *force,
	}); err != nil {
		return err
	}
	// Run already printed the outcome ("Logged in as @x" or the
	// already-logged-in note) to stdout.
	return nil
}
