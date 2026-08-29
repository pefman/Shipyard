package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectImage(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"go", []string{"go.mod"}, GolangImage},
		{"pyproject", []string{"pyproject.toml"}, PythonImage},
		{"requirements", []string{"requirements.txt"}, PythonImage},
		{"node", []string{"package.json"}, NodeImage},
		{"rust", []string{"Cargo.toml"}, RustImage},
		{"fallback", nil, FallbackImage},
		{"go beats node", []string{"go.mod", "package.json"}, GolangImage},
		{"python beats node", []string{"pyproject.toml", "package.json"}, PythonImage},
		{"node beats rust", []string{"package.json", "Cargo.toml"}, NodeImage},
		{"unrelated files fall back", []string{"README.md", "Makefile"}, FallbackImage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := DetectImage(dir); got != tc.want {
				t.Errorf("DetectImage(%q) = %q, want %q", dir, got, tc.want)
			}
		})
	}
}

func TestResolveImagePriority(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. The explicit choice wins over everything.
	if got := ResolveImage("alpine:3.20", dir); got != "alpine:3.20" {
		t.Errorf("explicit: ResolveImage = %q, want alpine:3.20", got)
	}
	// 2. The per-repo config hook is empty for now, so an empty
	// explicit choice falls through to auto-detection.
	if got := ResolveImage("", dir); got != GolangImage {
		t.Errorf("auto-detect: ResolveImage = %q, want %q", got, GolangImage)
	}
}
