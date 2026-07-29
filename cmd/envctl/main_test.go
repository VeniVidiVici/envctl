package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/executor"
	"github.com/VeniVidiVici/envctl/internal/mas"
	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/onboard"
	"github.com/VeniVidiVici/envctl/internal/portablelink"
	"github.com/VeniVidiVici/envctl/internal/setupui"
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

func TestLocalSetupCommandBuildsScopedVerifiedApply(t *testing.T) {
	got := localSetupCommand(
		"apply", "", "/config", "machine", model.ManagerMise, true,
	)
	want := []string{
		"apply",
		"--config", "/config",
		"--machine", "machine",
		"--local",
		"--manager", "mise",
		"--yes",
		"--setup-progress",
		"--json",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestSetupApplySummaryIsCompactAndHumanReadable(t *testing.T) {
	var output bytes.Buffer
	err := writeSetupApplySummary(&output, applyResponse{
		Mode:     "apply",
		Manager:  model.ManagerBrew,
		Verified: true,
		Execution: executor.Report{Results: []executor.Result{{
			Status: executor.StatusCompleted,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, expected := range []string{
		"Homebrew packages",
		"verified",
		"1 install(s) completed",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("summary does not contain %q: %q", expected, got)
		}
	}
	if strings.Contains(got, `"findings"`) {
		t.Fatalf("summary contains machine JSON: %q", got)
	}
}

func TestMASApplyContinuesWhenPaidAppIsNotOwned(t *testing.T) {
	runner := &mainTestRunner{
		results: []mainTestRunResult{
			{stdout: "Installed Free App\n"},
			{stderr: "This redownload is not available", err: errors.New("exit 1")},
		},
	}
	journal := &mainTestJournal{}
	installations := []mas.Installation{
		{
			Action: model.Action{
				Sequence: 1, PackageID: "free", Package: "111",
			},
			Command: executor.Command{
				Sequence: 1, PackageID: "free",
				Name: "mas", Args: []string{"get", "111"},
			},
		},
		{
			Action: model.Action{
				Sequence: 2, PackageID: "paid", Package: "222",
			},
			Command: executor.Command{
				Sequence: 2, PackageID: "paid",
				Name: "mas", Args: []string{"install", "222"},
			},
			OwnedOnly: true,
		},
	}

	report, completed, blocked, err := applyMASInstallations(
		context.Background(),
		runner,
		journal,
		installations,
	)
	if err != nil {
		t.Fatalf("applyMASInstallations() error = %v", err)
	}
	if len(report.Results) != 2 ||
		report.Results[0].Status != executor.StatusCompleted ||
		report.Results[1].Status != executor.StatusSkipped {
		t.Fatalf("report = %#v", report)
	}
	if len(completed) != 1 || completed[0].PackageID != "free" {
		t.Fatalf("completed = %#v", completed)
	}
	if len(blocked) != 1 ||
		blocked[0].Action.PackageID != "paid" ||
		!strings.Contains(blocked[0].Reason, "purchase it in the App Store") {
		t.Fatalf("blocked = %#v", blocked)
	}
	if strings.Join(journal.finishes, ",") != "1:completed,2:skipped" {
		t.Fatalf("journal finishes = %#v", journal.finishes)
	}
}

func TestMASSetupSummaryReportsInstalledAndDeferredApps(t *testing.T) {
	var output bytes.Buffer
	action := model.Action{PackageID: "paid", Package: "222"}
	err := writeSetupApplySummary(&output, applyResponse{
		Mode:    "apply",
		Manager: model.ManagerMAS,
		Execution: executor.Report{Results: []executor.Result{{
			Status: executor.StatusCompleted,
		}}},
		BlockedActions: []blockedAction{{
			Action: action,
			Reason: "purchase it in the App Store",
		}},
		MASPreflight: &mas.PreflightReport{Apps: []mas.AppPreflight{{
			PackageID: "paid",
			Name:      "Paid App",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, expected := range []string{
		"Mac App Store apps: 1 installed; 1 need manual action",
		"Paid App: purchase it in the App Store",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("summary does not contain %q:\n%s", expected, got)
		}
	}
}

func TestSetupJSONPlansPhasesWithoutCreatingState(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("setup local identity integration requires macOS")
	}
	ctx := context.Background()
	identity, err := onboard.Detect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "env-config")
	statePath := filepath.Join(home, ".local", "state", "envctl", "state.db")
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
  mise-config:
    source: portable/mise
    target: ~/.config/mise
    kind: directory
`)
	writeMainTestFile(t, filepath.Join(root, "profiles", "shared.yaml"), `
version: 1
name: shared
packages:
  - example
links:
  - mise-config
`)
	writeMainTestFile(
		t,
		filepath.Join(root, "machines", "example.yaml"),
		fmt.Sprintf(`
version: 1
id: example
match:
  hardware_uuid_sha256: %s
profiles:
  - shared
access:
  type: local
`, identity.HardwareUUIDSHA256),
	)
	writeMainTestFile(
		t,
		filepath.Join(root, "portable", "mise", "config.toml"),
		"[tools]\n",
	)

	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{
		"setup",
		"--config", root,
		"--machine", "example",
		"--local",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf(
			"setup error = %v\nstdout = %s\nstderr = %s",
			err, stdout.String(), stderr.String(),
		)
	}
	var response setupResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Phases) != 8 {
		t.Fatalf("phases = %#v", response.Phases)
	}
	var links, manual setupui.Phase
	for _, phase := range response.Phases {
		if phase.ID == setupui.PhaseLinks {
			links = phase
		}
		if phase.ID == setupui.PhaseManual {
			manual = phase
		}
	}
	if links.Status != setupui.StatusReady || links.Actions != 1 {
		t.Fatalf("link phase = %#v", links)
	}
	if manual.Status != setupui.StatusBlocked || manual.Actions != 1 {
		t.Fatalf("manual phase = %#v", manual)
	}
	if _, err := os.Lstat(statePath); !os.IsNotExist(err) {
		t.Fatalf("setup JSON created state: %v", err)
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

func TestRecoveryPlanUsesPinnedAgeIdentityAndEmitsNoPlaintext(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("recovery local identity integration requires macOS")
	}
	ctx := context.Background()
	identity, err := onboard.Detect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOPS_AGE_KEY", "inherited-value-must-not-be-used")
	t.Setenv("SOPS_AGE_KEY_FILE", filepath.Join(home, "wrong-identity"))
	root := filepath.Join(home, "env-config")
	source := filepath.Join(root, "secrets", "example.sops.env")
	target := filepath.Join(home, ".config", "example", "env")
	identityPath := filepath.Join(home, ".config", "sops", "age", "keys.txt")
	writeMainTestFile(t, source, "encrypted-placeholder")
	writeMainTestFile(t, target, "private-value")
	writeMainTestFile(t, identityPath, "age-identity")
	writeMainTestFile(t, filepath.Join(root, "envctl.yaml"), `
version: 1
catalog: catalog/packages.yaml
profiles: profiles
machines: machines
recovery_root: ~/Recovery
`)
	writeMainTestFile(t, filepath.Join(root, "catalog", "packages.yaml"), `
version: 1
packages:
  example:
    manager: manual
    kind: tool
    package: example
    update_policy: external
recoveries:
  example:
    kind: sops-file
    source: secrets/example.sops.env
    target: ~/.config/example/env
    format: dotenv
    mode: "0600"
`)
	writeMainTestFile(t, filepath.Join(root, "profiles", "shared.yaml"), `
version: 1
name: shared
recoveries:
  - example
`)
	writeMainTestFile(
		t,
		filepath.Join(root, "machines", "example.yaml"),
		fmt.Sprintf(`
version: 1
id: example
match:
  hardware_uuid_sha256: %s
profiles:
  - shared
access:
  type: local
`, identity.HardwareUUIDSHA256),
	)
	binDirectory := filepath.Join(home, "bin")
	sops := filepath.Join(binDirectory, "sops")
	writeMainTestFile(t, sops, `#!/bin/sh
test -z "${SOPS_AGE_KEY:-}" || exit 10
test -f "$SOPS_AGE_KEY_FILE" || exit 11
printf '%s' 'private-value'
`)
	if err := os.Chmod(sops, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{
		"recovery", "plan",
		"--config", root,
		"--machine", "example",
		"--local",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("recovery plan error = %v\nstderr = %s", err, stderr.String())
	}
	var response recoveryPlanResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Plan.Ready || response.Plan.Summary.Satisfied != 1 {
		t.Fatalf("response = %#v", response)
	}
	if strings.Contains(stdout.String(), "private-value") ||
		strings.Contains(stdout.String(), "age-identity") {
		t.Fatalf("recovery output exposed plaintext: %s", stdout.String())
	}
}

func TestRecoveryApplyRunsVerifiedJournalledTransaction(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("recovery local identity integration requires macOS")
	}
	ctx := context.Background()
	identity, err := onboard.Detect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOPS_AGE_KEY", "inherited-value-must-not-be-used")
	t.Setenv("SOPS_AGE_KEY_FILE", filepath.Join(home, "wrong-identity"))
	root := filepath.Join(home, "env-config")
	source := filepath.Join(root, "secrets", "example.sops.env")
	target := filepath.Join(home, ".config", "example", "env")
	statePath := filepath.Join(home, ".local", "state", "envctl", "state.db")
	identityPath := filepath.Join(home, ".config", "sops", "age", "keys.txt")
	writeMainTestFile(t, source, "encrypted-placeholder")
	writeMainTestFile(t, target, "old-private-value")
	writeMainTestFile(t, identityPath, "age-identity")
	writeMainTestFile(t, filepath.Join(root, "envctl.yaml"), fmt.Sprintf(`
version: 1
catalog: catalog/packages.yaml
profiles: profiles
machines: machines
recovery_root: ~/Recovery
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
recoveries:
  example:
    kind: sops-file
    source: secrets/example.sops.env
    target: ~/.config/example/env
    format: dotenv
    mode: "0600"
`)
	writeMainTestFile(t, filepath.Join(root, "profiles", "shared.yaml"), `
version: 1
name: shared
recoveries:
  - example
`)
	writeMainTestFile(
		t,
		filepath.Join(root, "machines", "example.yaml"),
		fmt.Sprintf(`
version: 1
id: example
match:
  hardware_uuid_sha256: %s
profiles:
  - shared
access:
  type: local
`, identity.HardwareUUIDSHA256),
	)
	binDirectory := filepath.Join(home, "bin")
	sops := filepath.Join(binDirectory, "sops")
	writeMainTestFile(t, sops, `#!/bin/sh
test -z "${SOPS_AGE_KEY:-}" || exit 10
test "$SOPS_AGE_KEY_FILE" = "$HOME/.config/sops/age/keys.txt" || exit 11
printf '%s' 'new-private-value'
`)
	if err := os.Chmod(sops, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{
		"recovery", "apply",
		"--config", root,
		"--machine", "example",
		"--local",
		"--yes",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf(
			"recovery apply error = %v\nstdout = %s\nstderr = %s",
			err,
			stdout.String(),
			stderr.String(),
		)
	}
	var response recoveryApplyResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result == nil || !response.Result.Verified ||
		response.RunID == "" || response.PlanID == "" ||
		response.VerificationSnapshotID == "" {
		t.Fatalf("response = %#v", response)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "new-private-value" {
		t.Fatalf("target = %q, %v", raw, err)
	}
	backup := response.Plan.Actions[0].BackupPath
	raw, err = os.ReadFile(backup)
	if err != nil || string(raw) != "old-private-value" {
		t.Fatalf("backup = %q, %v", raw, err)
	}
	if strings.Contains(stdout.String(), "new-private-value") ||
		strings.Contains(stdout.String(), "old-private-value") ||
		strings.Contains(stdout.String(), "age-identity") {
		t.Fatalf("recovery output exposed plaintext: %s", stdout.String())
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
	if len(history) != 1 || history[0].Command != "recovery apply" ||
		history[0].Status != "completed" || history[0].ActionCount != 1 {
		t.Fatalf("history = %#v", history)
	}
}

func TestRecoveryApplyDryRunDoesNotCreateStateOrTarget(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("recovery local identity integration requires macOS")
	}
	ctx := context.Background()
	identity, err := onboard.Detect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "env-config")
	source := filepath.Join(root, "secrets", "example.sops.env")
	target := filepath.Join(home, ".config", "example", "env")
	statePath := filepath.Join(home, ".local", "state", "envctl", "state.db")
	writeMainTestFile(t, source, "encrypted-placeholder")
	writeMainTestFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"age-identity",
	)
	writeMainTestFile(t, filepath.Join(root, "envctl.yaml"), fmt.Sprintf(`
version: 1
catalog: catalog/packages.yaml
profiles: profiles
machines: machines
recovery_root: ~/Recovery
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
recoveries:
  example:
    kind: sops-file
    source: secrets/example.sops.env
    target: ~/.config/example/env
    format: dotenv
    mode: "0600"
`)
	writeMainTestFile(t, filepath.Join(root, "profiles", "shared.yaml"), `
version: 1
name: shared
recoveries:
  - example
`)
	writeMainTestFile(
		t,
		filepath.Join(root, "machines", "example.yaml"),
		fmt.Sprintf(`
version: 1
id: example
match:
  hardware_uuid_sha256: %s
profiles:
  - shared
access:
  type: local
`, identity.HardwareUUIDSHA256),
	)
	binDirectory := filepath.Join(home, "bin")
	sops := filepath.Join(binDirectory, "sops")
	writeMainTestFile(t, sops, "#!/bin/sh\nprintf '%s' 'private-value'\n")
	if err := os.Chmod(sops, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"recovery", "apply",
		"--config", root,
		"--machine", "example",
		"--local",
		"--dry-run",
		"--json",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("recovery dry-run error = %v\nstderr = %s", err, stderr.String())
	}
	var response recoveryApplyResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Mode != "dry-run" || !response.Plan.Ready ||
		len(response.Plan.Actions) != 1 || response.Result != nil {
		t.Fatalf("response = %#v", response)
	}
	for _, path := range []string{target, statePath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run created %s: %v", path, err)
		}
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
	for _, value := range []string{"", "brew", "bun", "custom", "mas"} {
		if _, err := parseApplyManager(value); err != nil {
			t.Fatalf("parseApplyManager(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"manual", "all"} {
		if _, err := parseApplyManager(value); err == nil {
			t.Fatalf("parseApplyManager(%q) error = nil", value)
		}
	}
}

func TestManualSetupPhaseOnlyCountsExplicitManualItems(t *testing.T) {
	plan := model.Plan{Findings: []model.Finding{
		{
			Status: model.FindingMissing,
			Desired: &model.PackageSpec{
				ID: "runtime", Manager: model.ManagerMise,
			},
		},
		{
			Status: model.FindingMissing,
			Desired: &model.PackageSpec{
				ID: "external", Manager: model.ManagerManual,
			},
		},
	}}
	phase := buildManualSetupPhase(plan)
	if phase.Actions != 1 || phase.Blockers != 1 ||
		phase.Status != setupui.StatusBlocked {
		t.Fatalf("phase = %#v", phase)
	}
}

func TestMASExecutionRequiresLocalMachineBeforeConfigAccess(t *testing.T) {
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
		!strings.Contains(err.Error(), "Mac App Store execution requires --local") {
		t.Fatalf("run(MAS execution) error = %v", err)
	}
}

type mainTestRunResult struct {
	stdout string
	stderr string
	err    error
}

type mainTestRunner struct {
	results []mainTestRunResult
	calls   int
}

func (r *mainTestRunner) Run(
	_ context.Context,
	_ string,
	_ ...string,
) (string, string, error) {
	result := r.results[r.calls]
	r.calls++
	return result.stdout, result.stderr, result.err
}

type mainTestJournal struct {
	starts   []int
	finishes []string
}

func (j *mainTestJournal) StartAction(
	_ context.Context,
	sequence int,
) error {
	j.starts = append(j.starts, sequence)
	return nil
}

func (j *mainTestJournal) FinishAction(
	_ context.Context,
	sequence int,
	status, _ string,
) error {
	j.finishes = append(j.finishes, fmt.Sprintf("%d:%s", sequence, status))
	return nil
}

func (*mainTestJournal) SkipAction(
	context.Context,
	int,
	string,
) error {
	return nil
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
