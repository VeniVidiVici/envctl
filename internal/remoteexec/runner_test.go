package remoteexec

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestRunnerBuildsStrictNonInteractiveSSHCommand(t *testing.T) {
	commandRunner := &fakeCommandRunner{}
	runner := Runner{host: "macbook-pro", runner: commandRunner}
	if _, _, err := runner.Run(
		context.Background(),
		"brew", "install", "--formula", "hashicorp/tap/terraform",
	); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ConnectTimeout=8",
		"macbook-pro",
		"PATH=/opt/homebrew/bin:/usr/local/bin:$PATH " +
			"HOMEBREW_NO_AUTO_UPDATE=1 " +
			"brew install --formula hashicorp/tap/terraform",
	}
	if commandRunner.name != "ssh" ||
		!reflect.DeepEqual(commandRunner.args, want) {
		t.Fatalf(
			"command = %q %#v, want ssh %#v",
			commandRunner.name, commandRunner.args, want,
		)
	}
}

func TestRunnerRejectsAnythingOutsideNarrowHomebrewInstall(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"other executable", []string{"curl", "https://example.com"}},
		{"upgrade", []string{"brew", "upgrade", "--formula", "example"}},
		{"other flag", []string{"brew", "install", "--debug", "example"}},
		{"option injection", []string{"brew", "install", "--formula", "--debug"}},
		{"shell injection", []string{"brew", "install", "--formula", "x;uname"}},
		{"path traversal", []string{"brew", "install", "--formula", "a/../b"}},
		{"too many tap segments", []string{"brew", "install", "--formula", "a/b/c/d"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commandRunner := &fakeCommandRunner{}
			runner := Runner{host: "safe-host", runner: commandRunner}
			executable := test.args[0]
			if _, _, err := runner.Run(
				context.Background(), executable, test.args[1:]...,
			); err == nil {
				t.Fatal("Run() error = nil, want rejection")
			}
			if commandRunner.name != "" {
				t.Fatalf("command ran: %#v", commandRunner)
			}
		})
	}
}

func TestRunnerBuildsExactBunGlobalCommand(t *testing.T) {
	commandRunner := &fakeCommandRunner{}
	runner := Runner{host: "matilda", runner: commandRunner}
	if _, _, err := runner.Run(
		context.Background(),
		"bun",
		"add", "--global", "--ignore-scripts",
		"--no-progress", "--no-summary", "typescript",
	); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	command := commandRunner.args[len(commandRunner.args)-1]
	want := "PATH=$HOME/.bun/bin:/opt/homebrew/bin:/usr/local/bin:$PATH " +
		"bun add --global --ignore-scripts --no-progress --no-summary typescript"
	if command != want {
		t.Fatalf("remote command = %q, want %q", command, want)
	}
}

func TestRunnerRejectsUnsafeBunCommands(t *testing.T) {
	tests := [][]string{
		{"install", "--global", "--ignore-scripts", "--no-progress", "--no-summary", "x"},
		{"add", "--global", "--trust", "--no-progress", "--no-summary", "x"},
		{"add", "--global", "--ignore-scripts", "--no-progress", "--no-summary", "--verbose"},
		{"add", "--global", "--ignore-scripts", "--no-progress", "--no-summary", "x;uname"},
	}
	for _, args := range tests {
		commandRunner := &fakeCommandRunner{}
		runner := Runner{host: "matilda", runner: commandRunner}
		if _, _, err := runner.Run(
			context.Background(), "bun", args...,
		); err == nil {
			t.Fatalf("Run(%#v) error = nil, want rejection", args)
		}
		if commandRunner.name != "" {
			t.Fatalf("command ran for %#v", args)
		}
	}
}

func TestRunnerAllowsOnlyReadOnlyMASPreflightCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			"mas", []string{"config", "--json"},
			"PATH=/opt/homebrew/bin:/usr/local/bin:$PATH mas config --json",
		},
		{
			"mas", []string{"lookup", "--json", "497799835"},
			"PATH=/opt/homebrew/bin:/usr/local/bin:$PATH mas lookup --json 497799835",
		},
		{"sudo", []string{"-n", "true"}, "sudo -n true"},
	}
	for _, test := range tests {
		commandRunner := &fakeCommandRunner{}
		runner := Runner{host: "matilda", runner: commandRunner}
		if _, _, err := runner.Run(
			context.Background(), test.name, test.args...,
		); err != nil {
			t.Fatalf("Run(%s %#v) error = %v", test.name, test.args, err)
		}
		got := commandRunner.args[len(commandRunner.args)-1]
		if got != test.want {
			t.Fatalf("remote command = %q, want %q", got, test.want)
		}
	}
}

func TestRunnerRejectsStateChangingMASCommands(t *testing.T) {
	tests := [][]string{
		{"get", "497799835"},
		{"install", "497799835"},
		{"update", "497799835"},
		{"lookup", "--json", "1;uname"},
	}
	for _, args := range tests {
		commandRunner := &fakeCommandRunner{}
		runner := Runner{host: "matilda", runner: commandRunner}
		if _, _, err := runner.Run(
			context.Background(), "mas", args...,
		); err == nil {
			t.Fatalf("Run(mas %#v) error = nil, want rejection", args)
		}
		if commandRunner.name != "" {
			t.Fatalf("command ran for %#v", args)
		}
	}
}

func TestNewRejectsUnsafeHost(t *testing.T) {
	for _, host := range []string{"", "-option", "host name", "host;uname"} {
		if _, err := New(host); err == nil {
			t.Fatalf("New(%q) error = nil, want rejection", host)
		}
	}
}

func TestBrewCommandAcceptsCoreAndTappedTargets(t *testing.T) {
	for _, target := range []string{
		"loc",
		"gcc@14",
		"coder/coder/coder",
		"jandedobbeleer/oh-my-posh/oh-my-posh",
	} {
		command, err := brewCommand([]string{"install", "--formula", target})
		if err != nil {
			t.Fatalf("brewCommand(%q) error = %v", target, err)
		}
		if !strings.HasSuffix(command, "--formula "+target) {
			t.Fatalf("command = %q", command)
		}
	}
}

type fakeCommandRunner struct {
	name string
	args []string
}

func (r *fakeCommandRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) (string, string, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return "installed", "", nil
}
