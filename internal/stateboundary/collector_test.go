package stateboundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbsentMachineLocalPathIsAllowed(t *testing.T) {
	home := t.TempDir()
	collector := NewCollector(home, []Spec{{
		ID:          "example",
		Path:        filepath.Join(home, ".local", "share", "example"),
		AllowAbsent: true,
	}})
	if issues := collector.Collect(); len(issues) != 0 {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestRealMachineLocalDirectoryIsHealthy(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".local", "share", "example")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	collector := NewCollector(home, []Spec{{ID: "example", Path: path}})
	if issues := collector.Collect(); len(issues) != 0 {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestDanglingStateSymlinkIsReported(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(home, ".local", "share")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "example")
	if err := os.Symlink("../../missing/example", path); err != nil {
		t.Fatal(err)
	}
	issues := NewCollector(home, []Spec{{
		ID: "example", Path: path, AllowAbsent: true,
	}}).Collect()
	if len(issues) != 1 ||
		!strings.Contains(issues[0].Message, "symbolic link") ||
		!strings.Contains(issues[0].Message, "../../missing/example") {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestSymlinkedAncestorIsReported(t *testing.T) {
	home := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(home, ".local")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".local", "share", "example")
	issues := NewCollector(home, []Spec{{
		ID: "example", Path: path, AllowAbsent: true,
	}}).Collect()
	if len(issues) != 1 ||
		!strings.Contains(issues[0].Message, filepath.Join(home, ".local")) {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestNonDirectoryStatePathIsReported(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(home, ".local", "share")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "example")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	issues := NewCollector(home, []Spec{{ID: "example", Path: path}}).Collect()
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "not a directory") {
		t.Fatalf("issues = %#v", issues)
	}
}
