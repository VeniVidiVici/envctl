package remoteexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var (
	hostPattern = regexp.MustCompile(
		`^[A-Za-z0-9][A-Za-z0-9._-]*$`,
	)
	targetPattern = regexp.MustCompile(
		`^[A-Za-z0-9][A-Za-z0-9@+._-]*(/[A-Za-z0-9][A-Za-z0-9@+._-]*){0,2}$`,
	)
	bunTargetPattern = regexp.MustCompile(
		`^(?:@[A-Za-z0-9][A-Za-z0-9._-]*/)?[A-Za-z0-9][A-Za-z0-9._-]*$`,
	)
	miseTargetPattern = regexp.MustCompile(
		`^[A-Za-z0-9][A-Za-z0-9+._-]*@[A-Za-z0-9][A-Za-z0-9.+_-]*$`,
	)
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

type ExecRunner struct{}

func (ExecRunner) Run(
	ctx context.Context,
	name string,
	args ...string,
) (string, string, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

type Runner struct {
	host   string
	runner CommandRunner
}

func New(host string) (Runner, error) {
	if !hostPattern.MatchString(host) {
		return Runner{}, fmt.Errorf("unsafe SSH host %q", host)
	}
	return Runner{host: host, runner: ExecRunner{}}, nil
}

func (r Runner) Run(
	ctx context.Context,
	name string,
	args ...string,
) (string, string, error) {
	if r.runner == nil {
		return "", "", errors.New("remote command runner is required")
	}
	remoteCommand, err := packageCommand(name, args)
	if err != nil {
		return "", "", err
	}
	return r.runner.Run(
		ctx,
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ConnectTimeout=8",
		r.host,
		remoteCommand,
	)
}

func packageCommand(name string, args []string) (string, error) {
	switch name {
	case "brew":
		return brewCommand(args)
	case "bun":
		return bunCommand(args)
	case "mise":
		return miseCommand(args)
	case "mas":
		return masReadCommand(args)
	case "sudo":
		return sudoReadCommand(args)
	default:
		return "", fmt.Errorf("unsupported remote executable %q", name)
	}
}

func miseCommand(args []string) (string, error) {
	if len(args) != 3 ||
		args[0] != "install" ||
		args[1] != "--yes" ||
		!miseTargetPattern.MatchString(args[2]) ||
		strings.Contains(args[2], "..") {
		return "", fmt.Errorf("unsupported or unsafe remote Mise argv %q", args)
	}
	return "PATH=/opt/homebrew/bin:/usr/local/bin:$PATH " +
		"mise install --yes " + args[2], nil
}

func masReadCommand(args []string) (string, error) {
	base := "PATH=/opt/homebrew/bin:/usr/local/bin:$PATH mas "
	if len(args) == 2 && args[0] == "config" && args[1] == "--json" {
		return base + "config --json", nil
	}
	if len(args) == 3 &&
		args[0] == "lookup" &&
		args[1] == "--json" &&
		numericID(args[2]) {
		return base + "lookup --json " + args[2], nil
	}
	return "", fmt.Errorf("unsupported remote mas preflight argv %q", args)
}

func sudoReadCommand(args []string) (string, error) {
	if len(args) == 2 && args[0] == "-n" && args[1] == "true" {
		return "sudo -n true", nil
	}
	return "", fmt.Errorf("unsupported remote sudo preflight argv %q", args)
}

func numericID(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func brewCommand(args []string) (string, error) {
	if len(args) != 3 || args[0] != "install" {
		return "", fmt.Errorf("unsupported remote Homebrew argv %q", args)
	}
	if args[1] != "--formula" && args[1] != "--cask" {
		return "", fmt.Errorf("unsupported remote Homebrew kind flag %q", args[1])
	}
	if !targetPattern.MatchString(args[2]) ||
		strings.Contains(args[2], "..") {
		return "", fmt.Errorf("unsafe remote Homebrew target %q", args[2])
	}
	return "PATH=/opt/homebrew/bin:/usr/local/bin:$PATH " +
		"HOMEBREW_NO_AUTO_UPDATE=1 " +
		"brew install " + args[1] + " " + args[2], nil
}

func bunCommand(args []string) (string, error) {
	if len(args) != 6 ||
		args[0] != "add" ||
		args[1] != "--global" ||
		args[2] != "--ignore-scripts" ||
		args[3] != "--no-progress" ||
		args[4] != "--no-summary" {
		return "", fmt.Errorf("unsupported remote Bun argv %q", args)
	}
	if !bunTargetPattern.MatchString(args[5]) ||
		strings.Contains(args[5], "..") {
		return "", fmt.Errorf("unsafe remote Bun target %q", args[5])
	}
	return "PATH=$HOME/.bun/bin:/opt/homebrew/bin:/usr/local/bin:$PATH " +
		"bun add --global --ignore-scripts --no-progress --no-summary " +
		args[5], nil
}
