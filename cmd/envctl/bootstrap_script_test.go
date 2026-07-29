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
		"Command Line Tools installation complete; continuing bootstrap",
		"brew install age gh git gnupg go sops",
		"Launching interactive onboarding and guided setup",
		`"$ENVCTL_BINARY" onboard \`,
		`--setup`,
		`--auto`,
		`"${BASH_SOURCE[0]}" == "$0"`,
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("bootstrap script is missing %q", fragment)
		}
	}
}

func TestBootstrapContinuesAfterCommandLineToolsInstall(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "installed")
	xcodeSelect := filepath.Join(bin, "xcode-select")
	stub := `#!/bin/bash
if [[ "$1" == "-p" ]]; then
	test -f "$ENVCTL_TEST_CLT_MARKER"
	exit
fi
if [[ "$1" == "--install" ]]; then
	touch "$ENVCTL_TEST_CLT_MARKER"
	exit
fi
exit 2
`
	if err := os.WriteFile(xcodeSelect, []byte(stub), 0o700); err != nil {
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
		`source "$1"; interactive_terminal() { return 0; }; ensure_command_line_tools`,
		"bootstrap-test",
		scriptPath,
	)
	command.Env = append(
		os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ENVCTL_TEST_CLT_MARKER="+marker,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("continue after Command Line Tools: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Command Line Tools install was not requested: %v", err)
	}
}

func TestBootstrapPreservesOnboardingChangesAcrossFastForward(t *testing.T) {
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
	if err != nil {
		t.Fatalf("advanced dirty checkout failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Preserving local onboarding changes") {
		t.Fatalf("bootstrap output = %s", output)
	}
	if _, err := os.Stat(filepath.Join(machines, "new-mac.yaml")); err != nil {
		t.Fatalf("onboarding file was not preserved after fast-forward: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(checkout, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(readme) != "advanced\n" {
		t.Fatalf("README after fast-forward = %q", readme)
	}

	if err := os.WriteFile(
		filepath.Join(checkout, "README.md"),
		[]byte("local edit\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(seed, "README.md"),
		[]byte("advanced again\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "commit", "-m", "advance again")
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
		t.Fatalf("conflicting dirty checkout unexpectedly succeeded:\n%s", output)
	}
	readme, readErr := os.ReadFile(filepath.Join(checkout, "README.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(readme) != "local edit\n" {
		t.Fatalf("conflicting local edit was changed: %q", readme)
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
