package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/onboard"
	"github.com/VeniVidiVici/envctl/internal/portablelink"
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

func TestConfigValidateLoadsEveryMachine(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"envctl.yaml": `
version: 1
catalog: catalog/packages.yaml
profiles: profiles
machines: machines
`,
		"catalog/packages.yaml": `
version: 1
packages:
  fzf:
    manager: brew
    kind: formula
    source: homebrew/core
    package: fzf
    update_policy: managed
`,
		"profiles/shared.yaml": `
version: 1
name: shared
packages:
  - fzf
`,
		"machines/one.yaml": `
version: 1
id: one
profiles:
  - shared
access:
  type: local
`,
		"machines/two.yaml": `
version: 1
id: two
profiles:
  - shared
access:
  type: ssh
  host: two
`,
	}
	for relative, contents := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	err := run(
		context.Background(),
		[]string{"config", "validate", "--config", root, "--json"},
		&stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("run(config validate) error = %v", err)
	}
	var got configValidationResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if !got.Valid || strings.Join(got.Machines, ",") != "one,two" {
		t.Fatalf("validation result = %#v", got)
	}
}

func TestLinksApplyRunsVerifiedJournalledTransaction(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("portable-link local identity integration requires macOS")
	}
	ctx := context.Background()
	identity, err := onboard.Detect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "env-config")
	source := filepath.Join(root, "portable", "example")
	oldSource := filepath.Join(home, "legacy", "example")
	target := filepath.Join(home, ".config", "example", "config")
	statePath := filepath.Join(home, ".local", "state", "envctl", "state.db")
	writeMainTestFile(t, source, "new\n")
	writeMainTestFile(t, oldSource, "old\n")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	oldRelative, err := filepath.Rel(filepath.Dir(target), oldSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldRelative, target); err != nil {
		t.Fatal(err)
	}
	writeMainTestFile(t, filepath.Join(root, "envctl.yaml"), fmt.Sprintf(`
version: 1
catalog: catalog/packages.yaml
profiles: profiles
machines: machines
state:
  database: %s
`, statePath))
	writeMainTestFile(t, filepath.Join(root, "catalog", "packages.yaml"), `
version: 1
packages:
  example:
    manager: manual
    kind: tool
    package: example
    update_policy: external
links:
  example:
    source: portable/example
    target: ~/.config/example/config
    kind: file
`)
	writeMainTestFile(t, filepath.Join(root, "profiles", "shared.yaml"), `
version: 1
name: shared
links:
  - example
`)
	writeMainTestFile(t, filepath.Join(root, "machines", "example.yaml"), fmt.Sprintf(`
version: 1
id: example
match:
  hardware_uuid_sha256: %s
profiles:
  - shared
access:
  type: ssh
  host: example
`, identity.HardwareUUIDSHA256))

	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{
		"links", "apply",
		"--config", root,
		"--machine", "example",
		"--local",
		"--yes",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("links apply error = %v\nstderr = %s", err, stderr.String())
	}
	var response linkApplyResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result == nil || !response.Result.Verified ||
		response.RunID == "" || response.PlanID == "" ||
		response.VerificationSnapshotID == "" {
		t.Fatalf("response = %#v", response)
	}
	observation := portablelink.Collect([]model.LinkSpec{{
		ID: "example", Source: source, Target: target, Kind: model.LinkKindFile,
	}})[0]
	if observation.ResolvedTarget != source {
		t.Fatalf("observation = %#v", observation)
	}
	backup := response.Plan.Actions[0].BackupPath
	if info, err := os.Lstat(backup); err != nil ||
		info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("backup = %s, %v, %v", backup, info, err)
	}
	state, err := openState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	history, err := state.History(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Command != "links apply" ||
		history[0].Status != "completed" || history[0].ActionCount != 1 {
		t.Fatalf("history = %#v", history)
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

func writeMainTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
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
