package main

import (
	"bufio"
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

func TestDryRunReportListsUntrackedFiles(t *testing.T) {
	var output bytes.Buffer
	err := writeConfigSyncReport(
		&output,
		configSyncReport{
			Mode:           "dry-run",
			UntrackedFiles: []string{"portable/example/a", "portable/example/b"},
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"2 new file(s)",
		"portable/example/a",
		"portable/example/b",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("dry-run report missing %q: %q", expected, output.String())
		}
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

func TestReviewConfigSyncUntrackedCanIgnoreOnlyLocally(t *testing.T) {
	root := t.TempDir()
	runSyncTestGit(t, root, "init", "-b", "main")
	path := filepath.Join(root, "generated", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := reviewConfigSyncUntracked(
		context.Background(),
		root,
		bufio.NewReader(strings.NewReader("l\n")),
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := configSyncStatuses(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(configSyncUntracked(statuses)) != 0 {
		t.Fatalf("untracked after local ignore = %#v", statuses)
	}
	exclude := runSyncTestGit(t, root, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(strings.TrimSpace(exclude)) {
		exclude = filepath.Join(root, strings.TrimSpace(exclude))
	}
	contents, err := os.ReadFile(strings.TrimSpace(exclude))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "/generated/") {
		t.Fatalf("local excludes = %q", contents)
	}
}

func TestReviewConfigSyncUntrackedCanAddSharedIgnore(t *testing.T) {
	root := t.TempDir()
	runSyncTestGit(t, root, "init", "-b", "main")
	path := filepath.Join(root, "scratch.log")
	if err := os.WriteFile(path, []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := reviewConfigSyncUntracked(
		context.Background(),
		root,
		bufio.NewReader(strings.NewReader("g\n")),
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	status := runSyncTestGit(t, root, "status", "--porcelain")
	if !strings.Contains(status, "A  .gitignore") ||
		strings.Contains(status, "scratch.log") {
		t.Fatalf("status after shared ignore = %q", status)
	}
	contents, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "/scratch.log\n" {
		t.Fatalf("shared ignore = %q", contents)
	}
}

func TestRebaseConfigSyncCombinesIndependentChanges(t *testing.T) {
	remote, seed, local := newSyncTestRepositories(t)
	if err := os.WriteFile(filepath.Join(local, "local"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSyncTestGit(t, local, "add", "local")
	runSyncTestGit(t, local, "commit", "-m", "local")
	if err := os.WriteFile(filepath.Join(seed, "incoming"), []byte("incoming\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSyncTestGit(t, seed, "add", "incoming")
	runSyncTestGit(t, seed, "commit", "-m", "incoming")
	runSyncTestGit(t, seed, "push", "origin", "main")
	runSyncTestGit(t, local, "fetch", "origin", "main")

	if err := rebaseConfigSync(
		context.Background(), local, &bytes.Buffer{}, &bytes.Buffer{},
	); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"local", "incoming"} {
		if _, err := os.Stat(filepath.Join(local, name)); err != nil {
			t.Fatalf("%s missing after rebase: %v", name, err)
		}
	}
	if remote == "" {
		t.Fatal("empty fixture remote")
	}
}

func TestRebaseConfigSyncAbortsConflictAndPreservesLocalCommit(t *testing.T) {
	_, seed, local := newSyncTestRepositories(t)
	if err := os.WriteFile(filepath.Join(local, "base"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSyncTestGit(t, local, "add", "base")
	runSyncTestGit(t, local, "commit", "-m", "local")
	localHead := strings.TrimSpace(runSyncTestGit(t, local, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(seed, "base"), []byte("incoming\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSyncTestGit(t, seed, "add", "base")
	runSyncTestGit(t, seed, "commit", "-m", "incoming")
	runSyncTestGit(t, seed, "push", "origin", "main")
	runSyncTestGit(t, local, "fetch", "origin", "main")

	err := rebaseConfigSync(
		context.Background(), local, &bytes.Buffer{}, &bytes.Buffer{},
	)
	if err == nil || !strings.Contains(err.Error(), "local commit was preserved") {
		t.Fatalf("rebase error = %v", err)
	}
	head := strings.TrimSpace(runSyncTestGit(t, local, "rev-parse", "HEAD"))
	if head != localHead {
		t.Fatalf("HEAD after aborted rebase = %s, want %s", head, localHead)
	}
	contents, readErr := os.ReadFile(filepath.Join(local, "base"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "local\n" {
		t.Fatalf("local content after aborted rebase = %q", contents)
	}
}

func newSyncTestRepositories(t *testing.T) (remote, seed, local string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	seed = filepath.Join(root, "seed")
	local = filepath.Join(root, "local")
	runSyncTestGit(t, root, "init", "--bare", remote)
	runSyncTestGit(t, root, "init", "-b", "main", seed)
	runSyncTestGit(t, seed, "config", "user.name", "Envctl Test")
	runSyncTestGit(t, seed, "config", "user.email", "envctl@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "base"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSyncTestGit(t, seed, "add", "base")
	runSyncTestGit(t, seed, "commit", "-m", "base")
	runSyncTestGit(t, seed, "remote", "add", "origin", remote)
	runSyncTestGit(t, seed, "push", "-u", "origin", "main")
	runSyncTestGit(t, root, "clone", "--branch", "main", remote, local)
	runSyncTestGit(t, local, "config", "user.name", "Envctl Test")
	runSyncTestGit(t, local, "config", "user.email", "envctl@example.invalid")
	return remote, seed, local
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
