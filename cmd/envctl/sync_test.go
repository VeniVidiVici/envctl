package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigSyncStatusSeparatesTrackedAndUntracked(t *testing.T) {
	statuses, err := parseConfigSyncStatus(
		" M portable/ssh/config\x00?? private-key\x00R  new-name\x00old-name\x00",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(configSyncChangedPaths(statuses), ","); got !=
		"portable/ssh/config,new-name" {
		t.Fatalf("tracked paths = %q", got)
	}
	if got := strings.Join(configSyncUntracked(statuses), ","); got != "private-key" {
		t.Fatalf("untracked paths = %q", got)
	}
}

func TestConfirmConfigSyncDefaultsToNo(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := confirmConfigSync(strings.NewReader("\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed {
		t.Fatal("blank confirmation was accepted")
	}
	if !strings.Contains(output.String(), "[y/N]") {
		t.Fatalf("prompt = %q", output.String())
	}
}

func TestConfirmConfigSyncAcceptsYes(t *testing.T) {
	confirmed, err := confirmConfigSync(
		strings.NewReader("yes\n"),
		&bytes.Buffer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("yes confirmation was rejected")
	}
}

func TestTrustedEnvConfigOrigin(t *testing.T) {
	for _, origin := range []string{
		defaultEnvConfigRepo,
		"https://github.com/VeniVidiVici/env-config.git",
		"ssh://git@github.com/VeniVidiVici/env-config.git",
	} {
		if !trustedEnvConfigOrigin(origin) {
			t.Fatalf("trustedEnvConfigOrigin(%q) = false", origin)
		}
	}
	if trustedEnvConfigOrigin("git@example.com:other/config.git") {
		t.Fatal("untrusted origin was accepted")
	}
}

func TestCompactSyncErrorIsSingleLineAndBounded(t *testing.T) {
	got := compactSyncError(
		&syncTestError{message: strings.Repeat("x", 200) + "\nsecond line"},
	)
	if strings.Contains(got, "\n") || len(got) > 180 {
		t.Fatalf("compact error = %q", got)
	}
}

func TestFastForwardMatchingConfigWorktree(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	local := filepath.Join(root, "local")
	runSyncTestGit(t, root, "init", "--bare", remote)
	runSyncTestGit(t, root, "init", "-b", "main", seed)
	runSyncTestGit(t, seed, "config", "user.name", "Envctl Test")
	runSyncTestGit(t, seed, "config", "user.email", "envctl@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "config"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSyncTestGit(t, seed, "add", "config")
	runSyncTestGit(t, seed, "commit", "-m", "old")
	runSyncTestGit(t, seed, "remote", "add", "origin", remote)
	runSyncTestGit(t, seed, "push", "-u", "origin", "main")
	runSyncTestGit(t, root, "clone", "--branch", "main", remote, local)

	if err := os.WriteFile(filepath.Join(local, "config"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "config"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSyncTestGit(t, seed, "add", "config")
	runSyncTestGit(t, seed, "commit", "-m", "new")
	runSyncTestGit(t, seed, "push", "origin", "main")
	runSyncTestGit(t, local, "fetch", "origin", "main")

	matches, err := configSyncWorktreeMatchesRemote(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatal("matching incoming worktree was not recognized")
	}
	if err := fastForwardMatchingConfigWorktree(context.Background(), local); err != nil {
		t.Fatal(err)
	}
	status := runSyncTestGit(t, local, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("status after fast-forward = %q", status)
	}
	head := strings.TrimSpace(runSyncTestGit(t, local, "rev-parse", "HEAD"))
	incoming := strings.TrimSpace(runSyncTestGit(t, local, "rev-parse", "origin/main"))
	if head != incoming {
		t.Fatalf("HEAD = %s, origin/main = %s", head, incoming)
	}
}

func runSyncTestGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

type syncTestError struct {
	message string
}

func (e *syncTestError) Error() string {
	return e.message
}
