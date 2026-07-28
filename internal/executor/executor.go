package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/VeniVidiVici/envctl/internal/model"
)

const (
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

var (
	packagePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@+._-]*$`)
	sourcePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	bunPackagePattern = regexp.MustCompile(
		`^(?:@[A-Za-z0-9][A-Za-z0-9._-]*/)?[A-Za-z0-9][A-Za-z0-9._-]*$`,
	)
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

type Journal interface {
	StartAction(ctx context.Context, sequence int) error
	FinishAction(ctx context.Context, sequence int, status, errorSummary string) error
	SkipAction(ctx context.Context, sequence int, reason string) error
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

type Command struct {
	Sequence  int      `json:"sequence"`
	PackageID string   `json:"package_id"`
	Name      string   `json:"name"`
	Args      []string `json:"args"`
}

type Result struct {
	Command
	Status       string `json:"status"`
	Stdout       string `json:"stdout,omitempty"`
	Stderr       string `json:"stderr,omitempty"`
	ErrorSummary string `json:"error,omitempty"`
}

type Report struct {
	Commands []Command `json:"commands"`
	Results  []Result  `json:"results,omitempty"`
}

type Executor struct {
	runner  Runner
	journal Journal
}

func New(runner Runner, journal Journal) Executor {
	return Executor{runner: runner, journal: journal}
}

func (e Executor) Plan(actions []model.Action) ([]Command, error) {
	commands := make([]Command, 0, len(actions))
	for _, action := range actions {
		command, err := commandFor(action)
		if err != nil {
			return nil, fmt.Errorf("action %d (%s): %w",
				action.Sequence, action.PackageID, err)
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func (e Executor) Apply(
	ctx context.Context,
	actions []model.Action,
) (Report, error) {
	if e.runner == nil {
		return Report{}, errors.New("executor runner is required")
	}
	if e.journal == nil {
		return Report{}, errors.New("executor journal is required")
	}
	commands, err := e.Plan(actions)
	if err != nil {
		return Report{}, err
	}
	report := Report{Commands: commands}
	for index, command := range commands {
		if err := e.journal.StartAction(ctx, command.Sequence); err != nil {
			return report, fmt.Errorf("journal action %d start: %w",
				command.Sequence, err)
		}
		stdout, stderr, runErr := e.runner.Run(
			ctx, command.Name, command.Args...,
		)
		result := Result{
			Command: command,
			Stdout:  truncate(stdout, 4000),
			Stderr:  truncate(stderr, 4000),
		}
		if runErr != nil {
			result.Status = StatusFailed
			result.ErrorSummary = errorSummary(runErr, stderr)
			report.Results = append(report.Results, result)
			if journalErr := e.journal.FinishAction(
				ctx, command.Sequence, StatusFailed, result.ErrorSummary,
			); journalErr != nil {
				return report, fmt.Errorf(
					"run action %d: %v; journal failure: %w",
					command.Sequence, runErr, journalErr,
				)
			}
			for _, remaining := range commands[index+1:] {
				if journalErr := e.journal.SkipAction(
					ctx, remaining.Sequence,
					"not attempted because an earlier action failed",
				); journalErr != nil {
					return report, fmt.Errorf(
						"run action %d: %v; skip action %d: %w",
						command.Sequence, runErr, remaining.Sequence, journalErr,
					)
				}
			}
			return report, fmt.Errorf("run action %d (%s): %w",
				command.Sequence, command.PackageID, runErr)
		}
		result.Status = StatusCompleted
		report.Results = append(report.Results, result)
		if err := e.journal.FinishAction(
			ctx, command.Sequence, StatusCompleted, "",
		); err != nil {
			return report, fmt.Errorf("journal action %d completion: %w",
				command.Sequence, err)
		}
	}
	return report, nil
}

func commandFor(action model.Action) (Command, error) {
	if action.Sequence <= 0 {
		return Command{}, errors.New("sequence must be positive")
	}
	if action.Type != model.ActionInstall {
		return Command{}, fmt.Errorf("unsupported action type %q", action.Type)
	}
	if action.RequiresReview {
		return Command{}, errors.New("review-required action cannot be applied")
	}
	if action.RequiresPrivilege {
		return Command{}, errors.New("privileged action cannot be applied")
	}
	if action.Risk != model.RiskLow {
		return Command{}, fmt.Errorf("unsupported risk %q", action.Risk)
	}
	switch action.Manager {
	case model.ManagerBrew:
		return homebrewCommand(action)
	case model.ManagerBun:
		return bunCommand(action)
	default:
		return Command{}, fmt.Errorf("unsupported manager %q", action.Manager)
	}
}

func homebrewCommand(action model.Action) (Command, error) {
	if !packagePattern.MatchString(action.Package) {
		return Command{}, fmt.Errorf("unsafe package identity %q", action.Package)
	}

	var kindFlag, target string
	switch action.Kind {
	case model.KindFormula:
		kindFlag = "--formula"
		if action.Source == "homebrew/core" {
			target = action.Package
		} else {
			target = tappedTarget(action.Source, action.Package)
		}
	case model.KindCask:
		kindFlag = "--cask"
		if action.Source == "homebrew/cask" {
			target = action.Package
		} else {
			target = tappedTarget(action.Source, action.Package)
		}
	default:
		return Command{}, fmt.Errorf("unsupported Homebrew kind %q", action.Kind)
	}
	if target == "" {
		return Command{}, fmt.Errorf("invalid Homebrew source %q", action.Source)
	}
	return Command{
		Sequence: action.Sequence, PackageID: action.PackageID,
		Name: "brew", Args: []string{"install", kindFlag, target},
	}, nil
}

func bunCommand(action model.Action) (Command, error) {
	if action.Kind != model.KindTool {
		return Command{}, fmt.Errorf("unsupported Bun kind %q", action.Kind)
	}
	if action.Source != "" {
		return Command{}, fmt.Errorf("unsupported Bun source %q", action.Source)
	}
	if !bunPackagePattern.MatchString(action.Package) {
		return Command{}, fmt.Errorf("unsafe Bun package identity %q", action.Package)
	}
	return Command{
		Sequence: action.Sequence, PackageID: action.PackageID,
		Name: "bun",
		Args: []string{
			"add", "--global", "--ignore-scripts",
			"--no-progress", "--no-summary", action.Package,
		},
	}, nil
}

func tappedTarget(source, name string) string {
	if !sourcePattern.MatchString(source) ||
		source == "homebrew/core" || source == "homebrew/cask" {
		return ""
	}
	return source + "/" + name
}

func errorSummary(runErr error, stderr string) string {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = runErr.Error()
	} else {
		detail = runErr.Error() + ": " + detail
	}
	return truncate(detail, 1000)
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
