package appsetting

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/VeniVidiVici/envctl/internal/model"
)

const (
	tailscaleDomain = "io.tailscale.ipn.macsys"
	tailscaleKey    = "TailscaleStartOnLogin"
)

type FindingStatus string

const (
	StatusSatisfied FindingStatus = "satisfied"
	StatusMissing   FindingStatus = "missing"
	StatusBlocked   FindingStatus = "blocked"
)

type Finding struct {
	ID                 string               `json:"id"`
	Kind               model.AppSettingKind `json:"kind"`
	Status             FindingStatus        `json:"status"`
	Detail             string               `json:"detail"`
	VerifyAfterRestart bool                 `json:"verify_after_restart,omitempty"`
}

type Action struct {
	ID                 string               `json:"id"`
	Kind               model.AppSettingKind `json:"kind"`
	Description        string               `json:"description"`
	Reversible         bool                 `json:"reversible"`
	VerifyAfterRestart bool                 `json:"verify_after_restart,omitempty"`
}

type Plan struct {
	Findings []Finding `json:"findings"`
	Actions  []Action  `json:"actions"`
	Blockers []Finding `json:"blockers,omitempty"`
	Ready    bool      `json:"ready"`
}

type AppliedSetting struct {
	ID            string               `json:"id"`
	Kind          model.AppSettingKind `json:"kind"`
	PreviousValue string               `json:"previous_value,omitempty"`
	CurrentValue  string               `json:"current_value"`
}

type Result struct {
	Applied  []AppliedSetting `json:"applied"`
	Verified bool             `json:"verified"`
}

type RestartCheck struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type Manager struct {
	runner Runner
}

func New(runner Runner) *Manager {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Manager{runner: runner}
}

func (m *Manager) Plan(
	ctx context.Context,
	specs []model.AppSettingSpec,
) Plan {
	plan := Plan{Ready: true}
	for _, spec := range specs {
		finding := m.inspect(ctx, spec)
		plan.Findings = append(plan.Findings, finding)
		switch finding.Status {
		case StatusSatisfied:
		case StatusMissing:
			plan.Actions = append(plan.Actions, Action{
				ID:                 spec.ID,
				Kind:               spec.Kind,
				Description:        actionDescription(spec.Kind),
				Reversible:         true,
				VerifyAfterRestart: spec.VerifyAfterRestart,
			})
		default:
			plan.Blockers = append(plan.Blockers, finding)
			plan.Ready = false
		}
	}
	return plan
}

func (m *Manager) Apply(
	ctx context.Context,
	specs []model.AppSettingSpec,
) (Result, error) {
	plan := m.Plan(ctx, specs)
	if !plan.Ready {
		return Result{}, fmt.Errorf(
			"app settings have %d blocker(s)",
			len(plan.Blockers),
		)
	}
	result := Result{}
	for _, action := range plan.Actions {
		spec := findSpec(specs, action.ID)
		previous, _ := m.read(ctx, spec)
		if err := m.apply(ctx, spec); err != nil {
			return result, fmt.Errorf("apply app setting %q: %w", spec.ID, err)
		}
		current, err := m.read(ctx, spec)
		if err != nil || current != "1" {
			if err == nil {
				err = fmt.Errorf("read back value %q", current)
			}
			return result, fmt.Errorf("verify app setting %q: %w", spec.ID, err)
		}
		result.Applied = append(result.Applied, AppliedSetting{
			ID:            spec.ID,
			Kind:          spec.Kind,
			PreviousValue: previous,
			CurrentValue:  current,
		})
	}
	verification := m.Plan(ctx, specs)
	result.Verified = verification.Ready && len(verification.Actions) == 0
	if !result.Verified {
		return result, fmt.Errorf("app settings did not verify after apply")
	}
	return result, nil
}

func (m *Manager) VerifyAfterRestart(
	ctx context.Context,
	specs []model.AppSettingSpec,
) []RestartCheck {
	var checks []RestartCheck
	for _, spec := range specs {
		if !spec.VerifyAfterRestart {
			continue
		}
		switch spec.Kind {
		case model.AppSettingTailscaleStartOnLogin:
			checks = append(checks, m.verifyTailscaleAfterRestart(ctx, spec)...)
		default:
			checks = append(checks, RestartCheck{
				ID:     spec.ID,
				Passed: false,
				Detail: "no restart verifier is registered for " + string(spec.Kind),
			})
		}
	}
	return checks
}

func (m *Manager) verifyTailscaleAfterRestart(
	ctx context.Context,
	spec model.AppSettingSpec,
) []RestartCheck {
	var checks []RestartCheck
	finding := m.inspect(ctx, spec)
	checks = append(checks, RestartCheck{
		ID:     spec.ID + ".preference",
		Passed: finding.Status == StatusSatisfied,
		Detail: finding.Detail,
	})
	uidOutput, uidErr := m.runner.Run(ctx, "/usr/bin/id", "-u")
	uid := strings.TrimSpace(string(uidOutput))
	helperPassed := uidErr == nil && uid != ""
	helperDetail := "Tailscale login helper is registered"
	if helperPassed {
		output, err := m.runner.Run(
			ctx,
			"/bin/launchctl",
			"print",
			"gui/"+uid+"/io.tailscale.ipn.macsys.login-item-helper",
		)
		helperPassed = err == nil
		if err != nil {
			helperDetail = commandError(
				"inspect Tailscale login helper",
				output,
				err,
			).Error()
		}
	} else {
		helperDetail = commandError(
			"determine user id for Tailscale login helper",
			uidOutput,
			uidErr,
		).Error()
	}
	checks = append(checks, RestartCheck{
		ID:     spec.ID + ".login-helper",
		Passed: helperPassed,
		Detail: helperDetail,
	})

	statusOutput, statusErr := m.runner.Run(
		ctx,
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		"status",
		"--json",
	)
	online := false
	statusDetail := "Tailscale is online after login"
	if statusErr == nil {
		var status struct {
			BackendState string `json:"BackendState"`
			Self         struct {
				Online bool `json:"Online"`
			} `json:"Self"`
		}
		if err := json.Unmarshal(statusOutput, &status); err != nil {
			statusDetail = "decode Tailscale status: " + err.Error()
		} else {
			online = status.BackendState == "Running" && status.Self.Online
			if !online {
				statusDetail = fmt.Sprintf(
					"Tailscale is not online (backend %q, self online %t)",
					status.BackendState,
					status.Self.Online,
				)
			}
		}
	} else {
		statusDetail = commandError(
			"read Tailscale status",
			statusOutput,
			statusErr,
		).Error()
	}
	checks = append(checks, RestartCheck{
		ID:     spec.ID + ".online",
		Passed: online,
		Detail: statusDetail,
	})
	return checks
}

func (m *Manager) inspect(
	ctx context.Context,
	spec model.AppSettingSpec,
) Finding {
	finding := Finding{
		ID:                 spec.ID,
		Kind:               spec.Kind,
		VerifyAfterRestart: spec.VerifyAfterRestart,
	}
	value, err := m.read(ctx, spec)
	switch {
	case err == nil && value == "1":
		finding.Status = StatusSatisfied
		finding.Detail = "Tailscale is configured to start when the user logs in"
	case err == nil:
		finding.Status = StatusMissing
		finding.Detail = "Tailscale start on login is disabled"
	case spec.Kind == model.AppSettingTailscaleStartOnLogin:
		finding.Status = StatusMissing
		finding.Detail = "Tailscale start on login has not been configured"
	default:
		finding.Status = StatusBlocked
		finding.Detail = err.Error()
	}
	return finding
}

func (m *Manager) read(
	ctx context.Context,
	spec model.AppSettingSpec,
) (string, error) {
	switch spec.Kind {
	case model.AppSettingTailscaleStartOnLogin:
		output, err := m.runner.Run(
			ctx,
			"/usr/bin/defaults",
			"read",
			tailscaleDomain,
			tailscaleKey,
		)
		return strings.TrimSpace(string(output)), err
	default:
		return "", fmt.Errorf("unsupported app setting kind %q", spec.Kind)
	}
}

func (m *Manager) apply(
	ctx context.Context,
	spec model.AppSettingSpec,
) error {
	switch spec.Kind {
	case model.AppSettingTailscaleStartOnLogin:
		if output, err := m.runner.Run(
			ctx,
			"/usr/bin/defaults",
			"write",
			tailscaleDomain,
			tailscaleKey,
			"-bool",
			"true",
		); err != nil {
			return commandError("configure Tailscale start on login", output, err)
		}
		if output, err := m.runner.Run(
			ctx,
			"/usr/bin/open",
			"-gj",
			"-a",
			"/Applications/Tailscale.app",
		); err != nil {
			return commandError("launch Tailscale to register its login helper", output, err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported app setting kind %q", spec.Kind)
	}
}

func findSpec(
	specs []model.AppSettingSpec,
	id string,
) model.AppSettingSpec {
	for _, spec := range specs {
		if spec.ID == id {
			return spec
		}
	}
	return model.AppSettingSpec{}
}

func actionDescription(kind model.AppSettingKind) string {
	switch kind {
	case model.AppSettingTailscaleStartOnLogin:
		return "enable Tailscale start on login and register its login helper"
	default:
		return "apply app setting"
	}
}

func commandError(action string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if err == nil {
		err = fmt.Errorf("command failed")
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, detail)
}
