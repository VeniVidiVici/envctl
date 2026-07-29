package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapPinsSOPSIdentity(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "scripts", "bootstrap-macos")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read bootstrap script: %v", err)
	}
	script := string(raw)
	required := []string{
		`export SOPS_AGE_KEY_FILE="$LOCAL_AGE_IDENTITY"`,
		"SOPS_AGE_KEY_CMD",
		"SOPS_AGE_SSH_PRIVATE_KEY_CMD",
	}
	for _, fragment := range required {
		if !strings.Contains(script, fragment) {
			t.Fatalf("bootstrap script does not pin SOPS identity: missing %q", fragment)
		}
	}
}

func TestBootstrapLaunchesSingleInteractiveSetupFlow(t *testing.T) {
	script := readBootstrapScript(t)
	for _, fragment := range []string{
		"Launching interactive onboarding and guided setup",
		`"$ENVCTL_BINARY" onboard \`,
		`--setup`,
		`"${BASH_SOURCE[0]}" == "$0"`,
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("bootstrap script is missing %q", fragment)
		}
	}
}

func TestBootstrapPreservesAlignedOnboardingChanges(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	checkout := filepath.Join(root, "checkout")
	runGit(t, root, "init", "--bare", remote)
	runGit(t, root, "init", "-b", "main", seed)
	if err := os.WriteFile(
		filepath.Join(seed, "README.md"),
		[]byte("initial\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "commit", "-m", "initial")
	runGit(t, seed, "remote", "add", "origin", remote)
	runGit(t, seed, "push", "-u", "origin", "main")
	runGit(t, root, "clone", "--branch", "main", remote, checkout)

	machines := filepath.Join(checkout, "machines")
	if err := os.MkdirAll(machines, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(machines, "new-mac.yaml"),
		[]byte("id: new-mac\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	scriptPath, err := filepath.Abs(
		filepath.Join("..", "..", "scripts", "bootstrap-macos"),
	)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		"/bin/bash",
		"-c",
		`source "$1"; sync_checkout "$2" main "$3" "" true`,
		"bootstrap-test",
		scriptPath,
		remote,
		checkout,
	)
	command.Env = append(os.Environ(), "HOME="+filepath.Join(root, "home"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("preserve aligned checkout: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Preserving local onboarding changes") {
		t.Fatalf("bootstrap output = %s", output)
	}
	if _, err := os.Stat(filepath.Join(machines, "new-mac.yaml")); err != nil {
		t.Fatalf("onboarding file was not preserved: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(seed, "README.md"),
		[]byte("advanced\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "commit", "-m", "advance")
	runGit(t, seed, "push", "origin", "main")

	command = exec.Command(
		"/bin/bash",
		"-c",
		`source "$1"; sync_checkout "$2" main "$3" "" true`,
		"bootstrap-test",
		scriptPath,
		remote,
		checkout,
	)
	command.Env = append(os.Environ(), "HOME="+filepath.Join(root, "home"))
	output, err = command.CombinedOutput()
	if err == nil {
		t.Fatalf("advanced dirty checkout unexpectedly succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "origin/main has advanced") {
		t.Fatalf("advanced checkout error = %s", output)
	}
}

func readBootstrapScript(t *testing.T) string {
	t.Helper()
	scriptPath := filepath.Join("..", "..", "scripts", "bootstrap-macos")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read bootstrap script: %v", err)
	}
	return string(raw)
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=envctl test",
		"GIT_AUTHOR_EMAIL=envctl-test@example.invalid",
		"GIT_COMMITTER_NAME=envctl test",
		"GIT_COMMITTER_EMAIL=envctl-test@example.invalid",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}
