package sandbox

import (
	"reflect"
	"testing"
)

func TestFixCommands(t *testing.T) {
	for _, tc := range []struct {
		image string
		want  []string
	}{
		{"golang:1.22", []string{"go build ./...", "go test ./..."}},
		{"golang:1.23", []string{"go build ./...", "go test ./..."}}, // pinned tags keep working
		{"python:3.12", []string{
			"python -m compileall -q .",
			"find . -type d -name __pycache__ -prune -exec rm -rf {} +",
		}},
		{"node:20", []string{
			"npm install --no-audit --no-fund --no-package-lock",
			"npm run test --if-present",
			"rm -rf node_modules",
		}},
		{"rust:1.79", []string{
			"CARGO_TARGET_DIR=/tmp/shipyard-cargo cargo build",
			"CARGO_TARGET_DIR=/tmp/shipyard-cargo cargo test",
		}},
		{"rustlang:1", []string{
			"CARGO_TARGET_DIR=/tmp/shipyard-cargo cargo build",
			"CARGO_TARGET_DIR=/tmp/shipyard-cargo cargo test",
		}},
		{"ubuntu:24.04", nil}, // fallback: patch applies only
		{"my/custom-image:1", nil},
		{"", nil},
	} {
		if got := FixCommands(tc.image); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("FixCommands(%q) = %v, want %v", tc.image, got, tc.want)
		}
	}
}
