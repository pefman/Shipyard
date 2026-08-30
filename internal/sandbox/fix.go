package sandbox

import "strings"

// FixCommands returns the command lines that verify a patch inside the
// named sandbox image: build and test steps, where the image's
// toolchain has a reliable equivalent. Unknown images (custom --image
// values, the ubuntu fallback) return nil: the fix step then only
// applies the patch.
//
// Known families are matched on the image name prefix, so pinned tags
// like golang:1.23 keep working:
//
//	golang  →  go build ./... ; go test ./...
//	python  →  python -m compileall -q . (bytecode compile; __pycache__
//	           artifacts are removed again so the host-side commit does
//	           not pick them up)
//	node    →  npm install (no lockfile written) ; npm test (skipped
//	           when the package has no test script) ; node_modules
//	           removed again
//	rust    →  cargo build ; cargo test (out-of-tree target dir)
func FixCommands(image string) []string {
	switch {
	case strings.HasPrefix(image, "golang"):
		return []string{"go build ./...", "go test ./..."}
	case strings.HasPrefix(image, "python"):
		return []string{
			"python -m compileall -q .",
			"find . -type d -name __pycache__ -prune -exec rm -rf {} +",
		}
	case strings.HasPrefix(image, "node"):
		return []string{
			"npm install --no-audit --no-fund --no-package-lock",
			"npm run test --if-present",
			"rm -rf node_modules",
		}
	case strings.HasPrefix(image, "rust"), strings.HasPrefix(image, "rustlang"):
		return []string{
			"CARGO_TARGET_DIR=/tmp/shipyard-cargo cargo build",
			"CARGO_TARGET_DIR=/tmp/shipyard-cargo cargo test",
		}
	default:
		return nil
	}
}
