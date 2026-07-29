package setupui

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type fakeCommandFactory struct {
	phase Phase
	err   error
}

func (f *fakeCommandFactory) Command(phase Phase) (*exec.Cmd, error) {
	f.phase = phase
	if f.err != nil {
		return nil, f.err
	}
	return exec.CommandContext(context.Background(), "true"), nil
}

func TestProcessFactoryKeepsMachineJSONOutOfTerminal(t *testing.T) {
	command, err := (ProcessFactory{
		Context:    context.Background(),
		Executable: "true",
	}).Command(Phase{Command: []string{"--version"}})
	if err != nil {
		t.Fatal(err)
	}
	if command.Stdin != nil || command.Stdout == nil || command.Stderr != nil {
		t.Fatalf(
			"process streams = stdin %v stdout %v stderr %v",
			command.Stdin,
			command.Stdout,
			command.Stderr,
		)
	}
}

func TestModelEnforcesPhaseOrderingAndConfirmation(t *testing.T) {
	factory := &fakeCommandFactory{}
	model := New("example-mac", []Phase{
		{
			ID: PhaseRecovery, Label: "Credential recovery",
			Status: StatusReady, Actions: 2, Command: []string{"recovery"},
		},
		{
			ID: PhaseLinks, Label: "Portable links",
			Status: StatusReady, Actions: 1,
			Dependencies: []PhaseID{PhaseRecovery},
			Command:      []string{"links"},
		},
	}, factory)
	model.cursor = 1
	model.Update(keyPress("enter"))
	if model.confirming ||
		!strings.Contains(model.statusMessage, "earlier required") {
		t.Fatalf("blocked dependency model = %#v", model)
	}
	model.cursor = 0
	model.Update(keyPress("enter"))
	if !model.confirming {
		t.Fatalf("phase did not request confirmation: %#v", model)
	}
	_, command := model.Update(keyPress("y"))
	if command == nil || !model.running || factory.phase.ID != PhaseRecovery {
		t.Fatalf("execution = command %v model %#v factory %#v", command, model, factory)
	}
	model.Update(phaseFinishedMsg{id: PhaseRecovery})
	if model.phases[0].Status != StatusCompleted ||
		model.phases[0].Actions != 0 ||
		model.running {
		t.Fatalf("completed model = %#v", model)
	}
	model.cursor = 1
	model.Update(keyPress("enter"))
	if !model.confirming {
		t.Fatalf("unlocked phase did not request confirmation: %#v", model)
	}
}

func TestReviewPhaseRunsWithoutMutationConfirmation(t *testing.T) {
	factory := &fakeCommandFactory{}
	model := New("example-mac", []Phase{{
		ID: PhaseMAS, Label: "Mac App Store",
		Status: StatusReview, Command: []string{"apply", "--dry-run"},
	}}, factory)
	_, command := model.Update(keyPress("enter"))
	if command == nil || model.confirming || !model.running {
		t.Fatalf("review execution = command %v model %#v", command, model)
	}
	model.Update(phaseFinishedMsg{id: PhaseMAS})
	if model.phases[0].Status != StatusReviewed {
		t.Fatalf("reviewed model = %#v", model)
	}
}

func TestAutomaticRunsReadyPhasesInOrder(t *testing.T) {
	factory := &fakeCommandFactory{}
	model := New("example-mac", []Phase{
		{
			ID: PhaseRecovery, Label: "Credential recovery",
			Status: StatusReady, Actions: 2, Command: []string{"recovery"},
		},
		{
			ID: PhaseLinks, Label: "Portable links",
			Status: StatusReady, Actions: 1,
			Dependencies: []PhaseID{PhaseRecovery},
			Command:      []string{"links"},
		},
	}, factory)
	launches := 0
	model.launchProcess = func(
		command *exec.Cmd,
		callback tea.ExecCallback,
	) tea.Cmd {
		launches++
		return func() tea.Msg {
			return callback(command.Run())
		}
	}
	model.Automatic()

	command := model.Init()
	if command == nil || !model.running ||
		factory.phase.ID != PhaseRecovery || model.confirming ||
		launches != 1 {
		t.Fatalf(
			"initial automatic phase = command %v model %#v factory %#v launches %d",
			command, model, factory, launches,
		)
	}
	finished, ok := command().(phaseFinishedMsg)
	if !ok || finished.id != PhaseRecovery || finished.err != nil {
		t.Fatalf("initial automatic command result = %#v", finished)
	}
	_, command = model.Update(finished)
	if command == nil || !model.running ||
		factory.phase.ID != PhaseLinks ||
		model.phases[0].Status != StatusCompleted ||
		launches != 2 {
		t.Fatalf(
			"second automatic phase = command %v model %#v factory %#v launches %d",
			command, model, factory, launches,
		)
	}
	finished, ok = command().(phaseFinishedMsg)
	if !ok || finished.id != PhaseLinks || finished.err != nil {
		t.Fatalf("second automatic command result = %#v", finished)
	}
	_, command = model.Update(finished)
	if command == nil || model.running ||
		model.phases[1].Status != StatusCompleted ||
		!strings.Contains(model.statusMessage, "completed") {
		t.Fatalf("automatic completion = command %v model %#v", command, model)
	}
	if model.cursor != len(model.phases)-1 {
		t.Fatalf("completion cursor = %d, want %d", model.cursor, len(model.phases)-1)
	}
	if content := model.View().Content; !strings.Contains(
		content, "Automatic setup completed",
	) {
		t.Fatalf("completion view = %q", content)
	}
}

func TestViewClampsCursorPastFinalPhase(t *testing.T) {
	model := New("example-mac", []Phase{{
		ID: PhaseMAS, Label: "Mac App Store",
		Description: "Review App Store applications.",
		Status:      StatusReviewed,
	}}, &fakeCommandFactory{})
	model.cursor = len(model.phases)
	model.statusMessage = "Automatic setup completed"

	content := model.View().Content
	for _, expected := range []string{
		"Mac App Store",
		"Review App Store applications.",
		"Automatic setup completed",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("completion view does not contain %q:\n%s", expected, content)
		}
	}
}

func TestAutomaticStopsAtBlockedPhase(t *testing.T) {
	factory := &fakeCommandFactory{}
	model := New("example-mac", []Phase{{
		ID: PhaseRecovery, Label: "Credential recovery",
		Status: StatusBlocked, Blockers: 1,
		Diagnostics: []string{"gpg is unavailable"},
	}}, factory).Automatic()

	if command := model.Init(); command != nil || model.running {
		t.Fatalf("blocked automatic setup = command %v model %#v", command, model)
	}
	if !strings.Contains(model.statusMessage, "automatic setup stopped") {
		t.Fatalf("blocked status = %q", model.statusMessage)
	}
	if !strings.Contains(model.View().Content, "gpg is unavailable") {
		t.Fatalf("blocked diagnostics are not visible:\n%s", model.View().Content)
	}
}

func TestViewShowsUnifiedSetupState(t *testing.T) {
	model := New("example-mac", []Phase{
		{
			ID: PhaseRecovery, Label: "Credential recovery",
			Description: "Restore encrypted local credentials.",
			Status:      StatusBlocked,
			Blockers:    1,
			Diagnostics: []string{"gpg-keyring (tool-missing): gpg is unavailable"},
		},
		{
			ID: PhaseHomebrew, Label: "Homebrew packages",
			Description: "Install missing formulae and casks.",
			Status:      StatusReady, Actions: 12,
			Dependencies: []PhaseID{PhaseRecovery},
			Command:      []string{"apply", "--manager", "brew"},
		},
	}, &fakeCommandFactory{})
	content := model.View().Content
	for _, expected := range []string{
		"envctl setup",
		"example-mac",
		"Credential recovery",
		"Diagnostics:",
		"gpg-keyring (tool-missing): gpg is unavailable",
		"Homebrew packages",
		"12 action(s)",
		"j/k move",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("view does not contain %q:\n%s", expected, content)
		}
	}
}

func keyPress(value string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: rune(value[0]), Text: value}
}
