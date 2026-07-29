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
