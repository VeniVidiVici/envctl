package appsetting

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type fakeRunner struct {
	value string
	calls [][]string
}

func (r *fakeRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if name == "/usr/bin/defaults" && len(args) > 0 {
		switch args[0] {
		case "read":
			if r.value == "" {
				return nil, errors.New("domain/default pair does not exist")
			}
			return []byte(r.value + "\n"), nil
		case "write":
			r.value = "1"
			return nil, nil
		}
	}
	if name == "/usr/bin/open" {
		return nil, nil
	}
	if name == "/usr/bin/id" {
		return []byte("501\n"), nil
	}
	if name == "/bin/launchctl" {
		return []byte("service registered\n"), nil
	}
	if name == "/usr/bin/env" &&
		len(args) > 1 &&
		args[0] == "TAILSCALE_BE_CLI=1" &&
		args[1] == "/Applications/Tailscale.app/Contents/MacOS/Tailscale" {
		return []byte(
			`{"BackendState":"Running","Self":{"Online":true}}`,
		), nil
	}
	return nil, errors.New("unexpected command")
}

func tailscaleSpec() model.AppSettingSpec {
	return model.AppSettingSpec{
		ID:                 "tailscale-start-on-login",
		Kind:               model.AppSettingTailscaleStartOnLogin,
		PackageID:          "tailscale-app",
		VerifyAfterRestart: true,
	}
}

func TestPlanReportsMissingAndRestartVerification(t *testing.T) {
	runner := &fakeRunner{}
	plan := New(runner).Plan(context.Background(), []model.AppSettingSpec{
		tailscaleSpec(),
	})
	if !plan.Ready || len(plan.Actions) != 1 || len(plan.Findings) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	if !plan.Actions[0].VerifyAfterRestart ||
		plan.Findings[0].Status != StatusMissing {
		t.Fatalf("action/finding = %#v / %#v", plan.Actions[0], plan.Findings[0])
	}
}

func TestApplyWritesPreferenceLaunchesAppAndVerifies(t *testing.T) {
	runner := &fakeRunner{}
	result, err := New(runner).Apply(
		context.Background(),
		[]model.AppSettingSpec{tailscaleSpec()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || len(result.Applied) != 1 {
		t.Fatalf("result = %#v", result)
	}
	wantWrite := []string{
		"/usr/bin/defaults", "write", tailscaleDomain, tailscaleKey, "-bool", "true",
	}
	wantOpen := []string{
		"/usr/bin/open", "-gj", "-a", "/Applications/Tailscale.app",
	}
	var wrote, opened bool
	for _, call := range runner.calls {
		wrote = wrote || reflect.DeepEqual(call, wantWrite)
		opened = opened || reflect.DeepEqual(call, wantOpen)
	}
	if !wrote || !opened {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestSatisfiedPlanDoesNotRelaunchApp(t *testing.T) {
	runner := &fakeRunner{value: "1"}
	manager := New(runner)
	plan := manager.Plan(context.Background(), []model.AppSettingSpec{tailscaleSpec()})
	if !plan.Ready || len(plan.Actions) != 0 ||
		plan.Findings[0].Status != StatusSatisfied {
		t.Fatalf("plan = %#v", plan)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), " open ") {
			t.Fatalf("unexpected launch call %#v", call)
		}
	}
}

func TestRestartVerificationChecksPreferenceHelperAndOnlineState(t *testing.T) {
	runner := &fakeRunner{value: "1"}
	checks := New(runner).VerifyAfterRestart(
		context.Background(),
		[]model.AppSettingSpec{tailscaleSpec()},
	)
	if len(checks) != 3 {
		t.Fatalf("checks = %#v", checks)
	}
	for _, check := range checks {
		if !check.Passed {
			t.Fatalf("failed check = %#v", check)
		}
	}
}
