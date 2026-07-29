package mas

import (
	"errors"
	"fmt"
	"strings"

	"github.com/VeniVidiVici/envctl/internal/executor"
	"github.com/VeniVidiVici/envctl/internal/model"
)

type Installation struct {
	Action    model.Action
	Command   executor.Command
	OwnedOnly bool
}

type DeferredInstallation struct {
	Action model.Action
	Reason string
}

func PlanInstallations(
	actions []model.Action,
	preflight PreflightReport,
) ([]Installation, []DeferredInstallation, error) {
	apps := make(map[string]AppPreflight, len(preflight.Apps))
	for _, app := range preflight.Apps {
		if app.PackageID == "" {
			return nil, nil, errors.New(
				"Mac App Store preflight contains an app without a package id",
			)
		}
		if _, exists := apps[app.PackageID]; exists {
			return nil, nil, fmt.Errorf(
				"Mac App Store preflight contains duplicate package id %q",
				app.PackageID,
			)
		}
		apps[app.PackageID] = app
	}

	var installations []Installation
	var deferred []DeferredInstallation
	for _, action := range actions {
		if action.Manager != model.ManagerMAS ||
			action.Kind != model.KindApp ||
			action.Source != "mac-app-store" ||
			!validAdamID(action.Package) {
			return nil, nil, fmt.Errorf(
				"invalid Mac App Store action %q",
				action.PackageID,
			)
		}
		app, ok := apps[action.PackageID]
		if !ok {
			return nil, nil, fmt.Errorf(
				"Mac App Store preflight is missing package %q",
				action.PackageID,
			)
		}
		if app.AdamID != action.Package {
			return nil, nil, fmt.Errorf(
				"Mac App Store preflight identity mismatch for %q",
				action.PackageID,
			)
		}
		if len(app.Blockers) > 0 {
			deferred = append(deferred, DeferredInstallation{
				Action: action,
				Reason: strings.Join(app.Blockers, "; "),
			})
			continue
		}
		if !app.Available || !app.Compatible {
			return nil, nil, fmt.Errorf(
				"Mac App Store preflight for %q is incomplete",
				action.PackageID,
			)
		}
		if err := validateCandidate(app); err != nil {
			return nil, nil, fmt.Errorf(
				"Mac App Store command for %q: %w",
				action.PackageID,
				err,
			)
		}
		installations = append(installations, Installation{
			Action: action,
			Command: executor.Command{
				Sequence:  action.Sequence,
				PackageID: action.PackageID,
				Name:      app.CandidateCommand[0],
				Args:      append([]string(nil), app.CandidateCommand[1:]...),
			},
			OwnedOnly: app.RequiresOwnership,
		})
	}
	return installations, deferred, nil
}

func validateCandidate(app AppPreflight) error {
	if len(app.CandidateCommand) != 3 ||
		app.CandidateCommand[0] != "mas" ||
		app.CandidateCommand[2] != app.AdamID {
		return errors.New("candidate command does not match the app identity")
	}
	expectedVerb := "get"
	if app.RequiresOwnership {
		expectedVerb = "install"
	}
	if app.CandidateCommand[1] != expectedVerb {
		return fmt.Errorf(
			"candidate command uses %q instead of %q",
			app.CandidateCommand[1],
			expectedVerb,
		)
	}
	return nil
}
