package onboardui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/onboard"
)

type fakeWriter struct {
	machine envconfig.Machine
	replace bool
	path    string
	err     error
}

func (w *fakeWriter) Write(
	_ string,
	machine envconfig.Machine,
	replace bool,
) (string, error) {
	w.machine = machine
	w.replace = replace
	return w.path, w.err
}

func TestNeedsConfirmationWritesOnlyAfterY(t *testing.T) {
	writer := &fakeWriter{path: "machines/ai.yaml"}
	model := New(onboard.Result{
		Status:    onboard.StatusNeedsConfirmation,
		MachineID: "ai",
		Proposal: &envconfig.Machine{
			ID:       "ai",
			Profiles: []string{"shared"},
			Access:   envconfig.Access{Type: "local"},
		},
		ProposalPath:      "machines/ai.yaml",
		AvailableProfiles: []string{"shared"},
	}, "/config", writer)

	_, command := model.Update(keyMessage("w"))
	if command != nil || !model.confirming {
		t.Fatalf("write request = command %v, confirming %v", command, model.confirming)
	}
	_, command = model.Update(keyMessage("y"))
	if command == nil {
		t.Fatal("confirmation produced no write command")
	}
	model.Update(command())
	if writer.machine.ID != "ai" || !writer.replace {
		t.Fatalf("writer = %#v", writer)
	}
	if !strings.Contains(model.statusMessage, "machines/ai.yaml") {
		t.Fatalf("status = %q", model.statusMessage)
	}
}

func TestNewMachineRequiresProfile(t *testing.T) {
	model := New(onboard.Result{
		Status: onboard.StatusUnmatched,
		Proposal: &envconfig.Machine{
			ID: "new-mac", Access: envconfig.Access{Type: "ssh", Host: "new-mac"},
		},
		AvailableProfiles: []string{"shared", "laptop"},
	}, "/config", &fakeWriter{})

	model.Update(keyMessage("w"))
	if model.confirming || !strings.Contains(model.statusMessage, "at least one") {
		t.Fatalf("model = %#v", model)
	}
	model.Update(keyMessage("space"))
	model.Update(keyMessage("w"))
	if !model.confirming ||
		strings.Join(model.result.Proposal.Profiles, ",") != "shared" {
		t.Fatalf("model = %#v", model)
	}
}

func TestViewExplainsNoPackageChanges(t *testing.T) {
	model := New(onboard.Result{
		Status: onboard.StatusUnmatched,
		Identity: onboard.Identity{
			Hostname: "new-mac", HardwareUUIDSHA256: strings.Repeat("a", 64),
		},
		Proposal: &envconfig.Machine{
			ID: "new-mac", Profiles: []string{"shared"},
			Access: envconfig.Access{Type: "ssh", Host: "new-mac"},
		},
		ProposalPath:      "machines/new-mac.yaml",
		AvailableProfiles: []string{"shared"},
	}, "/config", &fakeWriter{})

	content := model.View().Content
	for _, expected := range []string{
		"envctl onboard", "new machine", "machines/new-mac.yaml",
		"no commit, push, package install, or link changes",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("view does not contain %q:\n%s", expected, content)
		}
	}
}

func TestMatchedViewShowsLocalPlanSummary(t *testing.T) {
	view := New(onboard.Result{
		Status:    onboard.StatusMatched,
		MachineID: "example",
		Identity: onboard.Identity{
			HardwareUUIDSHA256: strings.Repeat("a", 64),
		},
		Plan: &model.Plan{
			Summary: model.PlanSummary{
				Satisfied: 10, Missing: 2, Extra: 3, Actions: 2,
			},
			LinkSummary: &model.LinkPlanSummary{
				Satisfied: 4, Drifted: 1,
			},
		},
	}, "/config", &fakeWriter{})

	content := view.View().Content
	for _, expected := range []string{
		"matched example", "10 satisfied", "2 missing", "3 extra",
		"Proposed package actions: 2", "Portable links: 4 satisfied",
		"Preview only", "--machine example --local --dry-run --json",
		"envctl links apply",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("view does not contain %q:\n%s", expected, content)
		}
	}
}

func keyMessage(value string) tea.KeyPressMsg {
	if value == "space" {
		return tea.KeyPressMsg(tea.Key{Code: tea.KeySpace})
	}
	return tea.KeyPressMsg(tea.Key{Text: value, Code: []rune(value)[0]})
}
