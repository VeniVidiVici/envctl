package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	sourceEnvironment     = "ENVCTL_SOURCE"
	publicRepoEnvironment = "ENVCTL_PUBLIC_REPO"
	defaultPublicRepo     = "https://github.com/VeniVidiVici/envctl.git"
	defaultPublicRef      = "main"
)

func runSourceInstall(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	syncSource bool,
) error {
	name := "rebuild"
	if syncSource {
		name = "update"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourceFlag := flags.String(
		"source", "", "envctl source checkout; normally discovered automatically",
	)
	ref := defaultPublicRef
	if syncSource {
		flags.StringVar(
			&ref,
			"ref",
			defaultPublicRef,
			"branch to fast-forward before building",
		)
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments", name)
	}

	sourceRoot, err := resolveEnvctlSourceRoot(*sourceFlag)
	if err != nil {
		return err
	}
	if syncSource {
		if err := syncEnvctlSource(ctx, sourceRoot, ref, stdout, stderr); err != nil {
			return err
		}
	}
	target, err := installedEnvctlPath()
	if err != nil {
		return err
	}
	if err := buildAndInstallEnvctl(ctx, sourceRoot, target, stderr); err != nil {
		return err
	}
	revision, err := sourceRevision(ctx, sourceRoot)
	if err != nil {
		return err
	}
	verb := "Rebuilt"
	if syncSource {
		verb = "Updated"
	}
	fmt.Fprintf(
		stdout,
		"%s envctl successfully.\n  source: %s\n  revision: %s\n  installed: %s\n",
		verb,
		sourceRoot,
		revision,
		target,
	)
	return nil
}

func sourceRevision(ctx context.Context, sourceRoot string) (string, error) {
	revision, err := gitOutput(ctx, sourceRoot, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	revision = strings.TrimSpace(revision)
	status, err := gitOutput(ctx, sourceRoot, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) != "" {
		revision += "-dirty"
	}
	return revision, nil
}

func resolveEnvctlSourceRoot(explicit string) (string, error) {
	if explicit != "" {
		return requireEnvctlSourceRoot(explicit)
	}
	if configured := os.Getenv(sourceEnvironment); configured != "" {
		return requireEnvctlSourceRoot(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	candidates := []string{
		filepath.Join(home, ".local", "share", "envctl", "repos", "envctl"),
		filepath.Join(home, "Documents", "envctl"),
	}
	if workingDirectory, err := os.Getwd(); err == nil {
		candidates = append(candidates, workingDirectory)
	}
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil || seen[absolute] {
			continue
		}
		seen[absolute] = true
		if isEnvctlSourceRoot(absolute) {
			return absolute, nil
		}
	}
	return "", fmt.Errorf(
		"could not find the envctl source checkout; set %s or pass --source",
		sourceEnvironment,
	)
}

func requireEnvctlSourceRoot(value string) (string, error) {
	expanded, err := expandHome(value)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve envctl source: %w", err)
	}
	if !isEnvctlSourceRoot(absolute) {
		return "", fmt.Errorf("not an envctl source checkout: %s", absolute)
	}
	return absolute, nil
}

func isEnvctlSourceRoot(root string) bool {
	gitInfo, err := os.Lstat(filepath.Join(root, ".git"))
	if err != nil || (!gitInfo.IsDir() && !gitInfo.Mode().IsRegular()) {
		return false
	}
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return false
	}
	return bytes.HasPrefix(
		module,
		[]byte("module github.com/VeniVidiVici/envctl\n"),
	)
}

func syncEnvctlSource(
	ctx context.Context,
	sourceRoot, ref string,
	stdout, stderr io.Writer,
) error {
	if strings.TrimSpace(ref) == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid update branch %q", ref)
	}
	status, err := gitOutput(ctx, sourceRoot, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf(
			"source checkout has local changes; commit them or use envctl rebuild: %s",
			sourceRoot,
		)
	}
	branch, err := gitOutput(ctx, sourceRoot, "branch", "--show-current")
	if err != nil {
		return err
	}
	if strings.TrimSpace(branch) != ref {
		return fmt.Errorf(
			"source checkout is on branch %q, not %q; use envctl rebuild for the current branch",
			strings.TrimSpace(branch),
			ref,
		)
	}
	origin, err := gitOutput(ctx, sourceRoot, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if !trustedEnvctlOrigin(strings.TrimSpace(origin)) {
		return fmt.Errorf(
			"refusing to update from untrusted origin %q",
			strings.TrimSpace(origin),
		)
	}

	fmt.Fprintf(stdout, "Updating source checkout at %s\n", sourceRoot)
	if err := executeGit(ctx, sourceRoot, stdout, stderr, "fetch", "origin", ref); err != nil {
		return err
	}
	return executeGit(
		ctx,
		sourceRoot,
		stdout,
		stderr,
		"merge",
		"--ff-only",
		"origin/"+ref,
	)
}

func trustedEnvctlOrigin(origin string) bool {
	if configured := os.Getenv(publicRepoEnvironment); configured != "" {
		return origin == configured
	}
	switch origin {
	case defaultPublicRepo,
		"git@github.com:VeniVidiVici/envctl.git",
		"ssh://git@github.com/VeniVidiVici/envctl.git":
		return true
	default:
		return false
	}
}

func installedEnvctlPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	target := filepath.Join(home, ".local", "bin", "envctl")
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("installed envctl is not a regular file: %s", target)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect installed envctl: %w", err)
	}
	return target, nil
}

func buildAndInstallEnvctl(
	ctx context.Context,
	sourceRoot, target string,
	stderr io.Writer,
) error {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		return errors.New("Go is unavailable; install it before rebuilding envctl")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create envctl binary directory: %w", err)
	}
	candidate, err := os.CreateTemp(filepath.Dir(target), ".envctl.update.*")
	if err != nil {
		return fmt.Errorf("create temporary envctl binary: %w", err)
	}
	candidatePath := candidate.Name()
	if err := candidate.Close(); err != nil {
		os.Remove(candidatePath)
		return fmt.Errorf("close temporary envctl binary: %w", err)
	}
	defer os.Remove(candidatePath)

	fmt.Fprintf(stderr, "Building envctl from %s\n", sourceRoot)
	command := exec.CommandContext(
		ctx,
		goBinary,
		"build",
		"-trimpath",
		"-o",
		candidatePath,
		"./cmd/envctl",
	)
	command.Dir = sourceRoot
	command.Env = cleanGoEnvironment(os.Environ())
	command.Stdout = stderr
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build envctl: %w", err)
	}
	if err := os.Chmod(candidatePath, 0o755); err != nil {
		return fmt.Errorf("make envctl executable: %w", err)
	}
	help := exec.CommandContext(ctx, candidatePath, "help")
	output, err := help.Output()
	if err != nil {
		return fmt.Errorf("verify rebuilt envctl: %w", err)
	}
	if !bytes.Contains(output, []byte("envctl update")) {
		return errors.New("rebuilt binary failed its command verification")
	}
	if err := os.Rename(candidatePath, target); err != nil {
		return fmt.Errorf("install rebuilt envctl: %w", err)
	}
	return nil
}

func cleanGoEnvironment(environment []string) []string {
	blocked := map[string]bool{
		"GOBIN":       true,
		"GOENV":       true,
		"GOFLAGS":     true,
		"GOROOT":      true,
		"GOTOOLDIR":   true,
		"GOTOOLCHAIN": true,
		"GOWORK":      true,
	}
	cleaned := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[name] {
			cleaned = append(cleaned, entry)
		}
	}
	return append(
		cleaned,
		"GOENV=off",
		"GOFLAGS=",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
	)
}

func gitOutput(
	ctx context.Context,
	sourceRoot string,
	args ...string,
) (string, error) {
	commandArgs := append([]string{"-C", sourceRoot}, args...)
	output, err := exec.CommandContext(ctx, "git", commandArgs...).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return "", fmt.Errorf(
				"git %s: %w: %s",
				strings.Join(args, " "),
				err,
				detail,
			)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(output), nil
}

func executeGit(
	ctx context.Context,
	sourceRoot string,
	stdout, stderr io.Writer,
	args ...string,
) error {
	commandArgs := append([]string{"-C", sourceRoot}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
