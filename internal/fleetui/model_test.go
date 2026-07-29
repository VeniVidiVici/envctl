package fleetui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/VeniVidiVici/envctl/internal/fleetreconcile"
	"github.com/VeniVidiVici/envctl/internal/model"
)

type fakeWriter struct {
	machine string
	key     string
	value   string
	profile string
	err     error
}

type fakeReconciler struct {
	plan    fleetreconcile.Plan
	err     error
	command *exec.Cmd
}

func (r fakeReconciler) Preview(string) (fleetreconcile.Plan, error) {
	return r.plan, r.err
}

func (r fakeReconciler) Command(string) (*exec.Cmd, error) {
	if r.command == nil {
		return exec.Command("/usr/bin/true"), r.err
	}
	return r.command, r.err
}

func (w *fakeWriter) SaveDecision(machine, key, value, profile string) error {
	w.machine = machine
	w.key = key
	w.value = value
	w.profile = profile
	return w.err
}

func TestDecisionIsSavedOnlyForExtraFinding(t *testing.T) {
	writer := &fakeWriter{}
	item := model.InstalledPackage{
		Manager: model.ManagerBrew, Kind: model.KindFormula,
		Source: "homebrew/core", Package: "example", Version: "1.0",
	}
	view := New([]Machine{{
		ID: "example-mac", Profiles: []string{"shared"},
		Plan: model.Plan{
			Summary: model.PlanSummary{Extra: 1},
			Findings: []model.Finding{{
				Status: model.FindingExtra, Installed: []model.InstalledPackage{item},
				Detail: "installed on request but absent from desired state",
			}},
		},
	}}, nil, writer)

	_, command := view.Update(keyMessage("a"))
	if command == nil {
		t.Fatal("adopt key produced no command")
	}
	message := command()
	view.Update(message)

	if writer.machine != "example-mac" ||
		writer.key != "brew|formula|homebrew/core|example" ||
		writer.value != "adopt" ||
		writer.profile != "shared" {
		t.Fatalf("saved decision = %#v", writer)
	}
	if view.decisions[decisionMapKey("example-mac", writer.key)] != "adopt" {
		t.Fatalf("decision map = %#v", view.decisions)
	}
}

func TestAdoptPrefersSharedProfileOverMachineSpecificProfile(t *testing.T) {
	writer := &fakeWriter{}
	item := model.InstalledPackage{
		Manager: model.ManagerBrew, Kind: model.KindCask,
		Source: "homebrew/cask", Package: "example",
	}
	view := New([]Machine{{
		ID: "example-mac", Profiles: []string{"shared", "workstation"},
		Plan: model.Plan{Findings: []model.Finding{{
			Status:    model.FindingExtra,
			Installed: []model.InstalledPackage{item},
		}}},
	}}, nil, writer)

	_, command := view.Update(keyMessage("a"))
	view.Update(command())
	if writer.profile != "shared" {
		t.Fatalf("saved profile = %q, want shared", writer.profile)
	}
}

func TestDecisionFailureIsShown(t *testing.T) {
	writer := &fakeWriter{err: errors.New("database unavailable")}
	item := model.InstalledPackage{
		Manager: model.ManagerMAS, Kind: model.KindApp,
		Source: "mac-app-store", Package: "123",
	}
	view := New([]Machine{{
		ID: "example", Plan: model.Plan{
			Findings: []model.Finding{{
				Status: model.FindingExtra, Installed: []model.InstalledPackage{item},
			}},
		},
	}}, nil, writer)

	_, command := view.Update(keyMessage("i"))
	view.Update(command())
	if !strings.Contains(view.statusMessage, "database unavailable") {
		t.Fatalf("status message = %q", view.statusMessage)
	}
}

func TestViewContainsFleetSummary(t *testing.T) {
	view := New([]Machine{{
		ID: "example-mac",
		Plan: model.Plan{
			Summary: model.PlanSummary{Satisfied: 4, Missing: 1},
			LinkSummary: &model.LinkPlanSummary{
				Satisfied: 0, Drifted: 1,
			},
			LinkFindings: []model.LinkFinding{{
				Status: model.LinkFindingWrongTarget,
				LinkID: "example-config",
				Detail: "portable target points to a different source",
			}},
			Findings: []model.Finding{{
				Status: model.FindingMissing,
				Desired: &model.PackageSpec{
					ID: "missing", Manager: model.ManagerBrew,
					Kind: model.KindFormula, Package: "missing",
				},
				PackageID: "missing",
				Detail:    "desired package is not installed",
			}},
		},
	}}, nil, &fakeWriter{})

	content := view.View().Content
	for _, expected := range []string{
		"envctl fleet", "example-mac", "Missing: 1", "missing", "saved snapshot",
		"Portable links: 0/1", "example-config", "wrong-target",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("view does not contain %q:\n%s", expected, content)
		}
	}
}

func TestReconciliationPreviewRequiresYAndUpdatesResolvedExtra(t *testing.T) {
	item := model.InstalledPackage{
		Manager: model.ManagerBrew, Kind: model.KindCask,
		Source: "homebrew/cask", Package: "firefox",
	}
	action := fleetreconcile.Action{
		Sequence: 1, MachineID: "example", InventoryKey: InventoryKey(item),
		Decision: "adopt", Status: fleetreconcile.StatusPlanned,
		Installed: item, PackageID: "firefox", Profile: "shared",
	}
	view := New([]Machine{{
		ID: "example",
		Plan: model.Plan{
			Summary: model.PlanSummary{Extra: 1},
			Findings: []model.Finding{{
				Status:    model.FindingExtra,
				Installed: []model.InstalledPackage{item},
			}},
		},
	}}, []Decision{{
		MachineID: "example", InventoryKey: InventoryKey(item), Value: "adopt",
	}}, &fakeWriter{}).WithReconciler(fakeReconciler{
		plan: fleetreconcile.Plan{
			MachineID: "example", Profile: "shared",
			Actions: []fleetreconcile.Action{action}, Ready: true,
		},
	})
	view.launchProcess = func(
		_ *exec.Cmd, callback tea.ExecCallback,
	) tea.Cmd {
		return func() tea.Msg { return callback(nil) }
	}

	_, previewCommand := view.Update(keyMessage("r"))
	view.Update(previewCommand())
	if !view.confirming ||
		!strings.Contains(view.View().Content, "Press y to apply") {
		t.Fatalf("preview was not shown:\n%s", view.View().Content)
	}
	_, applyCommand := view.Update(keyMessage("y"))
	if applyCommand == nil || !view.running {
		t.Fatal("confirmed reconciliation did not start")
	}
	view.Update(applyCommand())
	if view.running || view.preview != nil ||
		len(view.machines[0].Plan.Findings) != 0 ||
		view.machines[0].Plan.Summary.Extra != 0 ||
		view.machines[0].Plan.Summary.Satisfied != 1 {
		t.Fatalf("model was not updated after reconciliation: %#v", view)
	}
}

func TestBlockedReconciliationCannotBeConfirmed(t *testing.T) {
	view := New([]Machine{{ID: "example"}}, nil, &fakeWriter{}).
		WithReconciler(fakeReconciler{plan: fleetreconcile.Plan{
			MachineID: "example", Profile: "shared", Ready: false,
			Blockers: []string{"custom removal is unsupported"},
		}})
	_, previewCommand := view.Update(keyMessage("r"))
	view.Update(previewCommand())
	if view.confirming {
		t.Fatal("blocked reconciliation entered confirmation state")
	}
	if !strings.Contains(view.View().Content, "Blocked: 1") {
		t.Fatalf("blocked preview missing:\n%s", view.View().Content)
	}
}

func keyMessage(value string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Text: value, Code: []rune(value)[0]})
}
