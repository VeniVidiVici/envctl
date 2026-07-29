package mas

import (
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestPlanInstallationsSeparatesFreeOwnedOnlyAndBlockedApps(t *testing.T) {
	actions := []model.Action{
		{
			Sequence: 1, PackageID: "free", Manager: model.ManagerMAS,
			Kind: model.KindApp, Source: "mac-app-store", Package: "111",
		},
		{
			Sequence: 2, PackageID: "paid", Manager: model.ManagerMAS,
			Kind: model.KindApp, Source: "mac-app-store", Package: "222",
		},
		{
			Sequence: 3, PackageID: "incompatible", Manager: model.ManagerMAS,
			Kind: model.KindApp, Source: "mac-app-store", Package: "333",
		},
	}
	preflight := PreflightReport{Apps: []AppPreflight{
		{
			PackageID: "free", AdamID: "111", Available: true,
			Compatible:       true,
			CandidateCommand: []string{"mas", "get", "111"},
		},
		{
			PackageID: "paid", AdamID: "222", Available: true,
			Compatible: true, RequiresOwnership: true,
			CandidateCommand: []string{"mas", "install", "222"},
		},
		{
			PackageID: "incompatible", AdamID: "333", Available: true,
			Blockers: []string{"requires macOS 27.0 or newer"},
		},
	}}

	installations, deferred, err := PlanInstallations(actions, preflight)
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 2 ||
		installations[0].Command.Args[0] != "get" ||
		installations[0].OwnedOnly ||
		installations[1].Command.Args[0] != "install" ||
		!installations[1].OwnedOnly {
		t.Fatalf("installations = %#v", installations)
	}
	if len(deferred) != 1 ||
		deferred[0].Action.PackageID != "incompatible" ||
		!strings.Contains(deferred[0].Reason, "macOS 27.0") {
		t.Fatalf("deferred = %#v", deferred)
	}
}

func TestPlanInstallationsRejectsAlteredCandidateCommand(t *testing.T) {
	action := model.Action{
		Sequence: 1, PackageID: "free", Manager: model.ManagerMAS,
		Kind: model.KindApp, Source: "mac-app-store", Package: "111",
	}
	_, _, err := PlanInstallations(
		[]model.Action{action},
		PreflightReport{Apps: []AppPreflight{{
			PackageID: "free", AdamID: "111", Available: true,
			Compatible:       true,
			CandidateCommand: []string{"mas", "get", "222"},
		}}},
	)
	if err == nil {
		t.Fatal("PlanInstallations() error = nil")
	}
}
