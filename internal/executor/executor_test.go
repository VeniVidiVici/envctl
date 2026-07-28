package executor

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestPlanBuildsExactHomebrewCommands(t *testing.T) {
	tests := []struct {
		name   string
		action model.Action
		args   []string
	}{
		{
			name: "core formula",
			action: installAction(
				model.KindFormula, "homebrew/core", "loc",
			),
			args: []string{"install", "--formula", "loc"},
		},
		{
			name: "core cask",
			action: installAction(
				model.KindCask, "homebrew/cask", "firefox",
			),
			args: []string{"install", "--cask", "firefox"},
		},
		{
			name: "third party formula",
			action: installAction(
				model.KindFormula, "oven-sh/bun", "bun",
			),
			args: []string{"install", "--formula", "oven-sh/bun/bun"},
		},
		{
			name: "third party cask",
			action: installAction(
				model.KindCask, "example/tools", "example-app",
			),
			args: []string{"install", "--cask", "example/tools/example-app"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands, err := New(nil, nil).Plan([]model.Action{test.action})
			if err != nil {
				t.Fatalf("Plan() error = %v", err)
			}
			if len(commands) != 1 || commands[0].Name != "brew" ||
				!reflect.DeepEqual(commands[0].Args, test.args) {
				t.Fatalf("commands = %#v, want args %#v", commands, test.args)
			}
		})
	}
}

func TestPlanBuildsExactBunGlobalCommand(t *testing.T) {
	action := model.Action{
		Sequence: 1, Type: model.ActionInstall, PackageID: "intelephense",
		Manager: model.ManagerBun, Kind: model.KindTool,
		Package: "intelephense", Risk: model.RiskLow, Reversible: true,
	}
	commands, err := New(nil, nil).Plan([]model.Action{action})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	want := []string{
		"add", "--global", "--ignore-scripts",
		"--no-progress", "--no-summary", "intelephense",
	}
	if len(commands) != 1 || commands[0].Name != "bun" ||
		!reflect.DeepEqual(commands[0].Args, want) {
		t.Fatalf("commands = %#v, want args %#v", commands, want)
	}
}

func TestPlanBuildsExactMiseRuntimeCommand(t *testing.T) {
	action := model.Action{
		Sequence: 1, Type: model.ActionInstall, PackageID: "node-runtime",
		Manager: model.ManagerMise, Kind: model.KindTool,
		Package: "node", Version: "24", Risk: model.RiskLow,
		Reversible: true,
	}
	commands, err := New(nil, nil).Plan([]model.Action{action})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	want := []string{"install", "--yes", "node@24"}
	if len(commands) != 1 || commands[0].Name != "mise" ||
		!reflect.DeepEqual(commands[0].Args, want) {
		t.Fatalf("commands = %#v, want args %#v", commands, want)
	}
}

func TestPlanRejectsUnsafeMiseActions(t *testing.T) {
	base := model.Action{
		Sequence: 1, Type: model.ActionInstall, PackageID: "node-runtime",
		Manager: model.ManagerMise, Kind: model.KindTool,
		Package: "node", Version: "24", Risk: model.RiskLow,
	}
	tests := []struct {
		name   string
		mutate func(*model.Action)
	}{
		{"wrong kind", func(action *model.Action) {
			action.Kind = model.KindFormula
		}},
		{"source", func(action *model.Action) {
			action.Source = "registry"
		}},
		{"unsafe tool", func(action *model.Action) {
			action.Package = "--jobs"
		}},
		{"unsafe version", func(action *model.Action) {
			action.Version = "24;uname"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action := base
			test.mutate(&action)
			if _, err := New(nil, nil).Plan([]model.Action{action}); err == nil {
				t.Fatal("Plan() error = nil, want rejection")
			}
		})
	}
}

func TestPlanRejectsUnsafeBunActions(t *testing.T) {
	base := model.Action{
		Sequence: 1, Type: model.ActionInstall, PackageID: "example",
		Manager: model.ManagerBun, Kind: model.KindTool,
		Package: "example", Risk: model.RiskLow,
	}
	tests := []struct {
		name   string
		mutate func(*model.Action)
	}{
		{"wrong kind", func(action *model.Action) {
			action.Kind = model.KindFormula
		}},
		{"source", func(action *model.Action) {
			action.Source = "https://example.com/package.tgz"
		}},
		{"option injection", func(action *model.Action) {
			action.Package = "--verbose"
		}},
		{"shell injection", func(action *model.Action) {
			action.Package = "example;uname"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action := base
			test.mutate(&action)
			if _, err := New(nil, nil).Plan([]model.Action{action}); err == nil {
				t.Fatal("Plan() error = nil, want rejection")
			}
		})
	}
}

func TestPlanRejectsUnsafeOrUnsupportedActionsBeforeExecution(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.Action)
	}{
		{"manual review", func(action *model.Action) {
			action.Type = model.ActionReview
		}},
		{"source repair", func(action *model.Action) {
			action.Type = model.ActionReinstallFromSource
		}},
		{"review required", func(action *model.Action) {
			action.RequiresReview = true
		}},
		{"privilege required", func(action *model.Action) {
			action.RequiresPrivilege = true
		}},
		{"non-low risk", func(action *model.Action) {
			action.Risk = model.RiskMedium
		}},
		{"other manager", func(action *model.Action) {
			action.Manager = model.ManagerMAS
		}},
		{"unsafe package", func(action *model.Action) {
			action.Package = "--debug"
		}},
		{"empty source", func(action *model.Action) {
			action.Source = ""
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action := installAction(model.KindFormula, "homebrew/core", "loc")
			test.mutate(&action)
			if _, err := New(nil, nil).Plan([]model.Action{action}); err == nil {
				t.Fatal("Plan() error = nil, want rejection")
			}
		})
	}
}

func TestApplyFailsFastAndJournalsResult(t *testing.T) {
	runner := &fakeRunner{errors: []error{errors.New("install failed")}}
	journal := &fakeJournal{}
	actions := []model.Action{
		installAction(model.KindFormula, "homebrew/core", "one"),
		installAction(model.KindFormula, "homebrew/core", "two"),
	}
	actions[1].Sequence = 2
	report, err := New(runner, journal).Apply(context.Background(), actions)
	if err == nil {
		t.Fatal("Apply() error = nil, want execution failure")
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if len(report.Results) != 1 || report.Results[0].Status != StatusFailed {
		t.Fatalf("report = %#v", report)
	}
	if !reflect.DeepEqual(journal.starts, []int{1}) ||
		!reflect.DeepEqual(journal.finishes, []string{"1:failed"}) ||
		!reflect.DeepEqual(journal.skips, []int{2}) {
		t.Fatalf("journal = %#v", journal)
	}
}

func TestApplyValidatesWholePlanBeforeRunning(t *testing.T) {
	runner := &fakeRunner{}
	journal := &fakeJournal{}
	actions := []model.Action{
		installAction(model.KindFormula, "homebrew/core", "one"),
		installAction(model.KindFormula, "homebrew/core", "two"),
	}
	actions[1].Sequence = 2
	actions[1].Type = model.ActionReview
	if _, err := New(runner, journal).Apply(
		context.Background(), actions,
	); err == nil {
		t.Fatal("Apply() error = nil, want validation failure")
	}
	if runner.calls != 0 || len(journal.starts) != 0 {
		t.Fatalf("execution started: runner=%d journal=%#v", runner.calls, journal)
	}
}

func installAction(
	kind model.PackageKind,
	source, name string,
) model.Action {
	return model.Action{
		Sequence: 1, Type: model.ActionInstall, PackageID: name,
		Manager: model.ManagerBrew, Kind: kind, Source: source, Package: name,
		Risk: model.RiskLow, Reversible: true,
	}
}

type fakeRunner struct {
	calls  int
	errors []error
}

func (r *fakeRunner) Run(
	_ context.Context,
	_ string,
	_ ...string,
) (string, string, error) {
	index := r.calls
	r.calls++
	if index < len(r.errors) && r.errors[index] != nil {
		return "", "brew failed", r.errors[index]
	}
	return "installed", "", nil
}

type fakeJournal struct {
	starts   []int
	finishes []string
	skips    []int
}

func (j *fakeJournal) StartAction(_ context.Context, sequence int) error {
	j.starts = append(j.starts, sequence)
	return nil
}

func (j *fakeJournal) FinishAction(
	_ context.Context,
	sequence int,
	status, _ string,
) error {
	j.finishes = append(j.finishes, string(rune('0'+sequence))+":"+status)
	return nil
}

func (j *fakeJournal) SkipAction(
	_ context.Context,
	sequence int,
	_ string,
) error {
	j.skips = append(j.skips, sequence)
	return nil
}
