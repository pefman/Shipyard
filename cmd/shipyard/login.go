package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/pefman/Shipyard/internal/auth"
)

// runLogin implements `shipyard login`: the GitHub OAuth device flow, or
// a fast-path verification of an already-stored token.
func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	clientID := fs.String("github-client-id", "", "GitHub OAuth App client ID (env SHIPYARD_GITHUB_CLIENT_ID)")
	force := fs.Bool("force", false, "redo the device flow even if a valid stored token exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	id := *clientID
	if id == "" {
		id = os.Getenv("SHIPYARD_GITHUB_CLIENT_ID")
	}
	if id == "" {
		return fmt.Errorf("a GitHub OAuth App client ID is required: pass --github-client-id or set SHIPYARD_GITHUB_CLIENT_ID")
	}

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
