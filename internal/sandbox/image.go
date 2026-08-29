package sandbox

import (
	"os"
	"path/filepath"
)

// The images auto-detection picks, one per recognized language. Tags pin
// a minor version so container builds are reproducible across machines.
const (
	// GolangImage is used when the repository has a go.mod.
	GolangImage = "golang:1.22"
	// PythonImage is used when the repository has a pyproject.toml or
	// requirements.txt.
	PythonImage = "python:3.12"
	// NodeImage is used when the repository has a package.json.
	NodeImage = "node:20"
	// RustImage is used when the repository has a Cargo.toml.
	RustImage = "rust:1.79"
	// FallbackImage is used when no marker file is present.
	FallbackImage = "ubuntu:24.04"
)

// autoDetect lists the marker files DetectImage looks for, in priority
// order: the first file that exists in the repository wins.
var autoDetect = []struct {
	files []string
	image string
}{
	{[]string{"go.mod"}, GolangImage},
	{[]string{"pyproject.toml", "requirements.txt"}, PythonImage},
	{[]string{"package.json"}, NodeImage},
	{[]string{"Cargo.toml"}, RustImage},
}

// DetectImage picks a sandbox image from the repository contents in
// dir: the first marker file that exists (in Go, Python, Node, Rust
// order) wins, and FallbackImage is used when none is present.
func DetectImage(dir string) string {
	for _, m := range autoDetect {
		for _, f := range m.files {
			if fileExists(dir, f) {
				return m.image
			}
		}
	}
	return FallbackImage
}

// imageFromRepoConfig returns the image the repository configured for
// itself (shipyard.yaml, when that format lands). The config file is
// not implemented yet; its lookup plugs in here, and until then the
// resolution falls through to auto-detection.
func imageFromRepoConfig(dir string) string { return "" }

// ResolveImage picks the image a run uses, in priority order:
//
//  1. explicit — the caller's choice (the --image flag);
//  2. the per-repo config (shipyard.yaml, not yet in place);
//  3. auto-detection from the repository contents (DetectImage).
//
// It is a pure function over the repository directory, so it is
// unit-testable without Docker.
func ResolveImage(explicit, repoDir string) string {
	if explicit != "" {
		return explicit
	}
	if img := imageFromRepoConfig(repoDir); img != "" {
		return img
	}
	return DetectImage(repoDir)
}

// fileExists reports whether dir/name is a regular file.
func fileExists(dir, name string) bool {
	fi, err := os.Stat(filepath.Join(dir, name))
	return err == nil && fi.Mode().IsRegular()
}
