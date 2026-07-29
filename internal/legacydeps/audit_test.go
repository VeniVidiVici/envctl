package legacydeps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuditFindsLegacyLinksAndText(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "Documents", "env")
	config := filepath.Join(home, ".local", "share", "envctl", "repos", "env-config")
	mustWrite(t, filepath.Join(legacy, "dotfiles", "ghostty", "config"), "theme = nord\n")
	mustWrite(t, filepath.Join(config, "portable", "shell", ".zshrc"),
		"export LEGACY_ENV_HOME=\"$HOME/Documents/env\"\n")
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(legacy, "dotfiles", "ghostty"),
		filepath.Join(home, ".config", "ghostty"),
	); err != nil {
		t.Fatal(err)
	}

	auditor, err := New(home, legacy, config)
	if err != nil {
		t.Fatal(err)
	}
	report, err := auditor.Audit()
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.Dependencies != 2 {
		t.Fatalf("report = %#v", report)
	}
	if report.Findings[0].Kind != FindingSymlink ||
		report.Findings[1].Kind != FindingText {
		t.Fatalf("findings = %#v", report.Findings)
	}
}

func TestAuditIgnoresHistoricalAndBinaryState(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "Documents", "env")
	config := filepath.Join(home, ".local", "share", "envctl", "repos", "env-config")
	mustWrite(t, filepath.Join(home, ".codex", "history", "old.txt"), legacy)
	mustWrite(t, filepath.Join(home, ".config", "zed", "prompts", "data.mdb"), legacy)
	mustWrite(t, filepath.Join(config, "portable", "ghostty", "config"), "theme = nord\n")

	auditor, err := New(home, legacy, config)
	if err != nil {
		t.Fatal(err)
	}
	report, err := auditor.Audit()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.Dependencies != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestAuditFindsBrokenLegacyLink(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "Documents", "env")
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(legacy, "missing"),
		filepath.Join(home, ".config", "missing"),
	); err != nil {
		t.Fatal(err)
	}
	auditor, err := New(home, legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	report, err := auditor.Audit()
	if err != nil {
		t.Fatal(err)
	}
	if report.Dependencies != 1 ||
		report.Findings[0].Kind != FindingSymlink {
		t.Fatalf("report = %#v", report)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
