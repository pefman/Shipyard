package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveImageWithSource(t *testing.T) {
	if img, src := ResolveImageWithSource("my/image:1", ""); img != "my/image:1" || src != "flag" {
		t.Errorf("explicit = (%q, %q), want (my/image:1, flag)", img, src)
	}

	gomod := t.TempDir()
	if err := os.WriteFile(filepath.Join(gomod, "go.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if img, src := ResolveImageWithSource("", gomod); img != GolangImage || src != "auto" {
		t.Errorf("auto-detect = (%q, %q), want (%s, auto)", img, src, GolangImage)
	}

	empty := t.TempDir()
	if img, src := ResolveImageWithSource("", empty); img != FallbackImage || src != "auto" {
		t.Errorf("fallback = (%q, %q), want (%s, auto)", img, src, FallbackImage)
	}
}
