package runtimepath

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPrependsSafeMacAndUserPathsWithoutDuplicates(t *testing.T) {
	got := Build(
		"/Users/example",
		"/custom/bin:/opt/homebrew/bin:/usr/bin",
	)
	parts := filepath.SplitList(got)
	if len(parts) < 4 {
		t.Fatalf("PATH = %q", got)
	}
	if parts[0] != "/Users/example/.local/bin" ||
		parts[1] != "/Users/example/.opencode/bin" ||
		parts[2] != "/Users/example/.bun/bin" {
		t.Fatalf("leading paths = %#v", parts[:3])
	}
	if strings.Count(got, "/opt/homebrew/bin") != 1 {
		t.Fatalf("Homebrew path duplicated: %q", got)
	}
	if !strings.Contains(got, "/custom/bin") {
		t.Fatalf("inherited path was dropped: %q", got)
	}
}
