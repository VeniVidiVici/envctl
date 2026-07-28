package planner

import (
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestBuildClassifiesHomebrewState(t *testing.T) {
	requested := true
	dependency := false
	desired := []model.PackageSpec{
		{
			ID: "inferred", Manager: model.ManagerBrew, Kind: model.KindUnknown,
			Package: "inferred", UpdatePolicy: model.UpdateManaged,
		},
		{
			ID: "missing", Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "homebrew/core", Package: "missing", UpdatePolicy: model.UpdateManaged,
		},
		{
			ID: "wrong-source", Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "wanted/tools", Package: "wrong-source", UpdatePolicy: model.UpdateManaged,
		},
		{
			ID: "store-app", Manager: model.ManagerMAS, Kind: model.KindApp,
			Package: "123456", UpdatePolicy: model.UpdateManaged,
		},
	}
	installed := []model.InstalledPackage{
		{
			Manager: model.ManagerBrew, Kind: model.KindCask,
			Source: "homebrew/cask", Package: "inferred", Version: "1.0",
		},
		{
			Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "other/tools", Package: "wrong-source", Version: "2.0", Requested: &requested,
		},
		{
			Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "homebrew/core", Package: "extra", Version: "3.0", Requested: &requested,
		},
		{
			Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "homebrew/core", Package: "dependency", Version: "4.0", Requested: &dependency,
		},
	}

	got := Build(desired, installed, []model.Manager{model.ManagerBrew})
	if got.Summary.Satisfied != 1 ||
		got.Summary.Missing != 1 ||
		got.Summary.Drifted != 1 ||
		got.Summary.Extra != 1 ||
		got.Summary.NotChecked != 1 {
		t.Fatalf("summary = %#v", got.Summary)
	}
	if got.Summary.Actions != 2 {
		t.Fatalf("actions = %d, want 2", got.Summary.Actions)
	}
	if got.Actions[0].Type != model.ActionInstall {
		t.Fatalf("first action = %#v", got.Actions[0])
	}
	if got.Actions[1].Type != model.ActionReinstallFromSource ||
		!got.Actions[1].RequiresReview {
		t.Fatalf("second action = %#v", got.Actions[1])
	}
}

func TestBuildPlansConfiguredMiseVersionDrift(t *testing.T) {
	desired := []model.PackageSpec{{
		ID: "node-runtime", Manager: model.ManagerMise,
		Kind: model.KindTool, Package: "node", Version: "24",
		UpdatePolicy: model.UpdateManaged,
	}}
	installed := []model.InstalledPackage{{
		Manager: model.ManagerMise, Kind: model.KindTool,
		Package: "node", Version: "22",
	}}
	plan := Build(desired, installed, []model.Manager{model.ManagerMise})
	if len(plan.Findings) != 1 ||
		plan.Findings[0].Status != model.FindingVersionDrift ||
		len(plan.Actions) != 1 ||
		plan.Actions[0].Type != model.ActionInstall ||
		plan.Actions[0].Version != "24" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestBuildRequiresReviewForUnresolvedMissingPackage(t *testing.T) {
	desired := []model.PackageSpec{{
		ID: "unknown", Manager: model.ManagerBrew, Kind: model.KindUnknown,
		Package: "unknown", UpdatePolicy: model.UpdateManaged,
	}}

	got := Build(desired, nil, []model.Manager{model.ManagerBrew})
	if len(got.Actions) != 1 || got.Actions[0].Type != model.ActionReview {
		t.Fatalf("actions = %#v", got.Actions)
	}
}

func TestBuildDoesNotProposeExtraRemoval(t *testing.T) {
	requested := true
	installed := []model.InstalledPackage{{
		Manager: model.ManagerBrew, Kind: model.KindFormula,
		Source: "homebrew/core", Package: "extra", Requested: &requested,
	}}

	got := Build(nil, installed, []model.Manager{model.ManagerBrew})
	if got.Summary.Extra != 1 {
		t.Fatalf("extra = %d, want 1", got.Summary.Extra)
	}
	if len(got.Actions) != 0 {
		t.Fatalf("actions = %#v, want none", got.Actions)
	}
}

func TestBuildChecksCollectedNonHomebrewManagers(t *testing.T) {
	desired := []model.PackageSpec{
		{
			ID: "store-app", Manager: model.ManagerMAS, Kind: model.KindApp,
			Source: "mac-app-store", Package: "123456", UpdatePolicy: model.UpdateManaged,
		},
		{
			ID: "bun-tool", Manager: model.ManagerBun, Kind: model.KindTool,
			Package: "bun-tool", UpdatePolicy: model.UpdateManaged,
		},
	}
	installed := []model.InstalledPackage{
		{
			Manager: model.ManagerMAS, Kind: model.KindApp,
			Source: "mac-app-store", Package: "123456",
		},
		{
			Manager: model.ManagerBun, Kind: model.KindTool,
			Package: "bun-tool",
		},
	}

	got := Build(desired, installed, []model.Manager{model.ManagerMAS, model.ManagerBun})
	if got.Summary.Satisfied != 2 || got.Summary.NotChecked != 0 {
		t.Fatalf("summary = %#v", got.Summary)
	}
}

func TestBuildUsesGenericInferenceDetailForCustomTool(t *testing.T) {
	desired := []model.PackageSpec{{
		ID: "claude", Manager: model.ManagerCustom, Kind: model.KindTool,
		Package: "claude", UpdatePolicy: model.UpdateManaged,
	}}
	installed := []model.InstalledPackage{{
		Manager: model.ManagerCustom, Kind: model.KindTool,
		Source: "executable", Package: "claude",
	}}
	got := Build(
		desired, installed, []model.Manager{model.ManagerCustom},
	)
	if len(got.Findings) != 1 ||
		got.Findings[0].Detail !=
			"installed; type or source inferred from collected inventory" {
		t.Fatalf("finding = %#v", got.Findings)
	}
}
