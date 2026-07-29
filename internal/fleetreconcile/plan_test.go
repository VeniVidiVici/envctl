package fleetreconcile

import (
	"reflect"
	"strings"
	"testing"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/store"
)

func TestBuildPlansSharedAdoptionAndExactRemoval(t *testing.T) {
	inventory := model.Inventory{Packages: []model.InstalledPackage{
		{
			Manager: model.ManagerBrew, Kind: model.KindCask,
			Source: "homebrew/cask", Package: "firefox",
			Version: "1.0", Application: "/Applications/Firefox.app",
		},
		{
			Manager: model.ManagerMAS, Kind: model.KindApp,
			Source: "mac-app-store", Package: "12345",
			Application: "/Applications/Example App.app",
		},
	}}
	decisions := []store.Decision{
		{
			MachineID:    "example",
			InventoryKey: "brew|cask|homebrew/cask|firefox",
			Value:        "adopt",
		},
		{
			MachineID:    "example",
			InventoryKey: "mas|app|mac-app-store|12345",
			Value:        "remove",
		},
	}
	plan := Build(
		"example", "shared", decisions, inventory,
		map[string]model.PackageSpec{},
		envconfig.Profile{Name: "shared"},
	)
	if !plan.Ready || len(plan.Actions) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	adopt := plan.Actions[0]
	if adopt.Status != StatusPlanned || adopt.PackageID != "firefox" ||
		!adopt.CatalogAdd || !adopt.ProfileAdd ||
		adopt.Spec.UpdatePolicy != model.UpdateInstallOnly {
		t.Fatalf("adopt action = %#v", adopt)
	}
	remove := plan.Actions[1]
	if remove.Status != StatusPlanned || remove.Command == nil ||
		remove.Command.Name != "sudo" ||
		!reflect.DeepEqual(
			remove.Command.Args,
			[]string{"mas", "uninstall", "12345"},
		) {
		t.Fatalf("remove action = %#v", remove)
	}
}

func TestBuildReusesMatchingCatalogEntryAndAvoidsDuplicateProfile(t *testing.T) {
	item := model.InstalledPackage{
		Manager: model.ManagerBrew, Kind: model.KindFormula,
		Source: "homebrew/core", Package: "jq",
	}
	plan := Build(
		"example", "shared",
		[]store.Decision{{
			MachineID: "example", InventoryKey: InventoryKey(item), Value: "adopt",
		}},
		model.Inventory{Packages: []model.InstalledPackage{item}},
		map[string]model.PackageSpec{
			"jq": {
				ID: "jq", Manager: model.ManagerBrew, Kind: model.KindFormula,
				Source: "homebrew/core", Package: "jq",
				UpdatePolicy: model.UpdateManaged,
			},
		},
		envconfig.Profile{Name: "shared", Packages: []string{"jq"}},
	)
	if !plan.Ready || len(plan.Actions) != 1 ||
		plan.Actions[0].Status != StatusNoop {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestBuildReusesCatalogIdentityWhenConservativeInferredPolicyDiffers(t *testing.T) {
	item := model.InstalledPackage{
		Manager: model.ManagerBrew, Kind: model.KindCask,
		Source: "homebrew/cask", Package: "firefox",
	}
	plan := Build(
		"example", "shared",
		[]store.Decision{{
			MachineID: "example", InventoryKey: InventoryKey(item), Value: "adopt",
		}},
		model.Inventory{Packages: []model.InstalledPackage{item}},
		map[string]model.PackageSpec{
			"firefox": {
				ID: "firefox", Manager: model.ManagerBrew, Kind: model.KindCask,
				Source: "homebrew/cask", Package: "firefox",
				UpdatePolicy: model.UpdateManaged,
			},
		},
		envconfig.Profile{Name: "shared"},
	)
	action := plan.Actions[0]
	if !plan.Ready || action.PackageID != "firefox" ||
		action.CatalogAdd || !action.ProfileAdd {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestBuildDoesNotBlockOnStaleInformationalDecision(t *testing.T) {
	plan := Build(
		"example", "shared",
		[]store.Decision{{
			MachineID:    "example",
			InventoryKey: "brew|formula|homebrew/core|gone",
			Value:        "ignore",
		}},
		model.Inventory{}, nil, envconfig.Profile{Name: "shared"},
	)
	if !plan.Ready || len(plan.Actions) != 1 ||
		plan.Actions[0].Status != StatusIgnored {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestBuildBlocksStaleAndUnsupportedCustomRemoval(t *testing.T) {
	custom := model.InstalledPackage{
		Manager: model.ManagerCustom, Kind: model.KindTool,
		Package: "unknown",
	}
	plan := Build(
		"example", "shared",
		[]store.Decision{
			{
				MachineID:    "example",
				InventoryKey: InventoryKey(custom),
				Value:        "remove",
			},
			{
				MachineID:    "example",
				InventoryKey: "brew|formula|homebrew/core|gone",
				Value:        "adopt",
			},
		},
		model.Inventory{Packages: []model.InstalledPackage{custom}},
		nil,
		envconfig.Profile{Name: "shared"},
	)
	if plan.Ready || len(plan.Blockers) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	if !strings.Contains(strings.Join(plan.Blockers, "\n"), "not supported") ||
		!strings.Contains(strings.Join(plan.Blockers, "\n"), "refresh") {
		t.Fatalf("blockers = %#v", plan.Blockers)
	}
}
