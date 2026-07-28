package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(help) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "envctl audit") {
		t.Fatalf("help output = %q", stdout.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"unknown"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run(unknown) error = nil, want error")
	}
}

func TestVerifyActionsRequiresSatisfiedFinding(t *testing.T) {
	actions := []model.Action{{PackageID: "loc"}}
	if verified, err := verifyActions(actions, model.Plan{
		Findings: []model.Finding{{
			PackageID: "loc", Status: model.FindingMissing,
		}},
	}); err == nil || verified {
		t.Fatalf("verifyActions() = %v, %v; want false, error", verified, err)
	}
	if verified, err := verifyActions(actions, model.Plan{
		Findings: []model.Finding{{
			PackageID: "loc", Status: model.FindingSatisfied,
		}},
	}); err != nil || !verified {
		t.Fatalf("verifyActions() = %v, %v; want true, nil", verified, err)
	}
}

func TestRequireCollectorReportsFailure(t *testing.T) {
	inventory := model.Inventory{Errors: []model.CollectorError{{
		Collector: "homebrew", Message: "brew unavailable",
	}}}
	if err := requireCollector(inventory, "homebrew"); err == nil ||
		!strings.Contains(err.Error(), "brew unavailable") {
		t.Fatalf("requireCollector() error = %v", err)
	}
}

func TestClassifyActionsSeparatesExecutableAndBlocked(t *testing.T) {
	actions := []model.Action{
		{
			Sequence: 1, Type: model.ActionInstall, PackageID: "loc",
			Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "homebrew/core", Package: "loc", Risk: model.RiskLow,
		},
		{
			Sequence: 2, Type: model.ActionInstall, PackageID: "slack",
			Manager: model.ManagerMAS, Kind: model.KindApp,
			Package: "803453959", Risk: model.RiskLow,
		},
	}
	commands, blocked := classifyActions(actions)
	if len(commands) != 1 || commands[0].PackageID != "loc" {
		t.Fatalf("commands = %#v", commands)
	}
	if len(blocked) != 1 ||
		blocked[0].Action.PackageID != "slack" ||
		!strings.Contains(blocked[0].Reason, `unsupported manager "mas"`) {
		t.Fatalf("blocked = %#v", blocked)
	}
}

func TestSelectActionsScopesOneManagerAndDefersOthers(t *testing.T) {
	actions := []model.Action{
		{Sequence: 1, PackageID: "bun-tool", Manager: model.ManagerBun},
		{Sequence: 2, PackageID: "store-app", Manager: model.ManagerMAS},
		{Sequence: 3, PackageID: "formula", Manager: model.ManagerBrew},
	}
	selected, deferred := selectActions(actions, model.ManagerBun)
	if len(selected) != 1 || selected[0].PackageID != "bun-tool" {
		t.Fatalf("selected = %#v", selected)
	}
	if len(deferred) != 2 ||
		deferred[0].PackageID != "store-app" ||
		deferred[1].PackageID != "formula" {
		t.Fatalf("deferred = %#v", deferred)
	}
}

func TestParseApplyManagerAllowsOnlyImplementedManagers(t *testing.T) {
	for _, value := range []string{"", "brew", "bun", "mas"} {
		if _, err := parseApplyManager(value); err != nil {
			t.Fatalf("parseApplyManager(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"custom", "all"} {
		if _, err := parseApplyManager(value); err == nil {
			t.Fatalf("parseApplyManager(%q) error = nil", value)
		}
	}
}

func TestMASExecutionIsRefusedBeforeConfigOrSSH(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(
		context.Background(),
		[]string{
			"apply",
			"--config", "/definitely/missing",
			"--machine", "remote",
			"--manager", "mas",
			"--yes",
			"--json",
		},
		&stdout, &stderr,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "Mac App Store execution is not enabled") {
		t.Fatalf("run(MAS execution) error = %v", err)
	}
}

func TestInventoryWarningDistinguishesToolProbeIssue(t *testing.T) {
	got := inventoryWarning(model.CollectorError{
		Collector: "custom.opencode", Message: "version probe failed",
	})
	if got != "custom.opencode probe issue: version probe failed" {
		t.Fatalf("inventoryWarning() = %q", got)
	}
}

func TestInventoryWarningDistinguishesStateBoundaryIssue(t *testing.T) {
	got := inventoryWarning(model.CollectorError{
		Collector: "state-boundary.opencode-data",
		Message:   "machine-local path is a symbolic link",
	})
	if got != "state-boundary.opencode-data safety issue: machine-local path is a symbolic link" {
		t.Fatalf("inventoryWarning() = %q", got)
	}
}

func TestDecodeLinkSpecsRejectsPathOutsideHome(t *testing.T) {
	raw, err := json.Marshal([]model.LinkSpec{{
		ID: "unsafe", Source: "/tmp/source", Target: "/tmp/target",
		Kind: model.LinkKindFile, Digest: strings.Repeat("a", 64),
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = decodeLinkSpecs(base64.RawURLEncoding.EncodeToString(raw))
	if err == nil || !strings.Contains(err.Error(), "unsafe portable link") {
		t.Fatalf("decodeLinkSpecs() error = %v", err)
	}
}
