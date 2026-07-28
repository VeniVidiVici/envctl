package runtimepath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyProvidesCleanAccountXDGDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("PATH", "/usr/bin:/bin")

	if err := Apply(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("XDG_CONFIG_HOME"); got != filepath.Join(home, ".config") {
		t.Fatalf("XDG_CONFIG_HOME = %q", got)
	}
	if !strings.HasPrefix(
		os.Getenv("PATH"),
		filepath.Join(home, ".local", "bin")+string(os.PathListSeparator),
	) {
		t.Fatalf("PATH = %q", os.Getenv("PATH"))
	}
}

func TestApplyPreservesExplicitXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	t.Setenv("PATH", "/usr/bin:/bin")

	if err := Apply(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("XDG_CONFIG_HOME"); got != "/custom/config" {
		t.Fatalf("XDG_CONFIG_HOME = %q", got)
	}
}

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
		parts[2] != "/Users/example/.bun/bin" ||
		parts[3] != "/Users/example/.local/share/mise/shims" {
		t.Fatalf("leading paths = %#v", parts[:4])
	}
	if strings.Count(got, "/opt/homebrew/bin") != 1 {
		t.Fatalf("Homebrew path duplicated: %q", got)
	}
	if !strings.Contains(got, "/custom/bin") {
		t.Fatalf("inherited path was dropped: %q", got)
	}
}
