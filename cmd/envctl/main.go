package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeniVidiVici/envctl/internal/appsetting"
	"github.com/VeniVidiVici/envctl/internal/bootstrapverify"
	"github.com/VeniVidiVici/envctl/internal/bun"
	envconfig "github.com/VeniVidiVici/envctl/internal/config"
	"github.com/VeniVidiVici/envctl/internal/customtool"
	"github.com/VeniVidiVici/envctl/internal/decisionexport"
	"github.com/VeniVidiVici/envctl/internal/executor"
	"github.com/VeniVidiVici/envctl/internal/fleetreconcile"
	"github.com/VeniVidiVici/envctl/internal/fleetrefresh"
	"github.com/VeniVidiVici/envctl/internal/fleetui"
	"github.com/VeniVidiVici/envctl/internal/homebrew"
	"github.com/VeniVidiVici/envctl/internal/legacy"
	"github.com/VeniVidiVici/envctl/internal/legacydeps"
	"github.com/VeniVidiVici/envctl/internal/mas"
	"github.com/VeniVidiVici/envctl/internal/mise"
	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/onboard"
	"github.com/VeniVidiVici/envctl/internal/onboardui"
	"github.com/VeniVidiVici/envctl/internal/planner"
	"github.com/VeniVidiVici/envctl/internal/portablelink"
	"github.com/VeniVidiVici/envctl/internal/recovery"
	"github.com/VeniVidiVici/envctl/internal/remoteexec"
	"github.com/VeniVidiVici/envctl/internal/runtimepath"
	"github.com/VeniVidiVici/envctl/internal/setupui"
	"github.com/VeniVidiVici/envctl/internal/stateboundary"
	"github.com/VeniVidiVici/envctl/internal/store"
)

const usage = `envctl is a read-first macOS environment manager.

Usage:
  envctl audit --json [--state PATH] [--no-record]
  envctl onboard --config DIR [--json] [--machine ID] [--profiles A,B] [--setup] [--auto]
  envctl setup --config DIR --machine ID --local [--json] [--auto]
  envctl import-legacy --input PATH
  envctl legacy audit [--config DIR] [--root PATH] [--json]
  envctl config validate --config DIR --json
  envctl config resolve --config DIR --machine ID --json
  envctl plan (--config DIR --machine ID | --legacy PATH) --json [--inventory PATH]
  envctl apply --config DIR --machine ID [--local] [--manager brew|mise|bun|custom|mas] --json (--dry-run | --yes)
  envctl links apply --config DIR --machine ID --local --json (--dry-run | --yes)
  envctl app-settings apply --config DIR --machine ID --local --json (--dry-run | --yes)
  envctl bootstrap checkpoint --config DIR [--machine ID] [--json]
  envctl bootstrap verify --config DIR [--machine ID] [--json]
  envctl recovery plan --config DIR --machine ID --local --json
  envctl recovery apply --config DIR --machine ID --local --json (--dry-run | --yes)
  envctl history --json [--state PATH] [--limit N]
  envctl update [--source DIR] [--ref BRANCH]
  envctl rebuild [--source DIR]
  envctl sync [--config DIR] [--message TEXT] [--dry-run | --yes] [--json]
  envctl tui [--config DIR] [--inventory-dir DIR] [--state PATH]
  envctl fleet refresh [--config DIR] [--inventory-dir DIR] [--machines A,B] [--json]
  envctl fleet export-decisions [--config DIR] [--state PATH] [--json]
  envctl fleet reconcile [--config DIR] [--inventory-dir DIR] [--machine ID] (--dry-run | --yes) [--json]
`

func main() {
	if err := runtimepath.Apply(); err != nil {
		fmt.Fprintf(os.Stderr, "envctl: %v\n", err)
		os.Exit(1)
	}
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "envctl: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return nil
	}

	switch args[0] {
	case "audit":
		return runAudit(ctx, args[1:], stdout, stderr)
	case "onboard":
		return runOnboard(ctx, args[1:], stdout, stderr)
	case "setup":
		return runSetup(ctx, args[1:], stdout, stderr)
	case "import-legacy":
		return runImportLegacy(args[1:], stdout, stderr)
	case "legacy":
		return runLegacyCommand(args[1:], stdout, stderr)
	case "plan":
		return runPlan(ctx, args[1:], stdout, stderr)
	case "apply":
		return runApply(ctx, args[1:], stdout, stderr)
	case "links":
		return runLinks(ctx, args[1:], stdout, stderr)
	case "app-settings":
		return runAppSettings(ctx, args[1:], stdout, stderr)
	case "bootstrap":
		return runBootstrap(ctx, args[1:], stdout, stderr)
	case "recovery":
		return runRecovery(ctx, args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "history":
		return runHistory(ctx, args[1:], stdout, stderr)
	case "update":
		return runSourceInstall(ctx, args[1:], stdout, stderr, true)
	case "rebuild":
		return runSourceInstall(ctx, args[1:], stdout, stderr, false)
	case "sync":
		return runConfigSync(ctx, args[1:], os.Stdin, stdout, stderr)
	case "tui":
		return runFleetTUI(ctx, args[1:], stderr)
	case "fleet":
		return runFleet(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func runLegacyCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "audit" {
		return errors.New("usage: envctl legacy audit [--config DIR] [--root PATH] [--json]")
	}
	flags := flag.NewFlagSet("legacy audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	legacyRoot := flags.String("root", "", "legacy environment directory")
	asJSON := flags.Bool("json", false, "print the audit report as JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("legacy audit accepts no positional arguments")
	}
	resolvedConfig, err := resolveConfigRoot(*configRoot)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	resolvedLegacy := *legacyRoot
	if resolvedLegacy == "" {
		resolvedLegacy = filepath.Join(home, "Documents", "env")
	} else {
		resolvedLegacy, err = expandHome(resolvedLegacy)
		if err != nil {
			return err
		}
	}
	auditor, err := legacydeps.New(home, resolvedLegacy, resolvedConfig)
	if err != nil {
		return err
	}
	report, err := auditor.Audit()
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(stdout, report)
	}
	if report.Ready {
		fmt.Fprintf(
			stdout,
			"Legacy dependency audit: ready; no active references to %s\n",
			report.LegacyRoot,
		)
		return nil
	}
	fmt.Fprintf(
		stdout,
		"Legacy dependency audit: %d active reference(s) to %s\n",
		report.Dependencies,
		report.LegacyRoot,
	)
	for _, finding := range report.Findings {
		fmt.Fprintf(stdout, "  %s: %s\n", finding.Kind, finding.Path)
	}
	return nil
}

type setupResponse struct {
	MachineID    string          `json:"machine_id"`
	ConfigDigest string          `json:"config_digest"`
	Phases       []setupui.Phase `json:"phases"`
}

func runSetup(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	machineID := flags.String("machine", "", "machine id from the native config")
	localMachine := flags.Bool(
		"local",
		false,
		"require this Mac's registered identity and execute phases locally",
	)
	asJSON := flags.Bool("json", false, "print the unified setup plan as JSON")
	automatic := flags.Bool(
		"auto",
		false,
		"run executable phases in dependency order without per-phase confirmation",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configRoot == "" || *machineID == "" {
		return errors.New("--config and --machine are required")
	}
	if !*localMachine {
		return errors.New("setup currently requires --local")
	}
	if *asJSON && *automatic {
		return errors.New("--json and --auto cannot be combined")
	}
	loaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return err
	}
	if err := forceLocalMachine(ctx, &loaded); err != nil {
		return err
	}
	phases, err := buildSetupPhases(ctx, loaded)
	if err != nil {
		return err
	}
	response := setupResponse{
		MachineID: loaded.Machine.ID, ConfigDigest: loaded.Digest,
		Phases: phases,
	}
	if *asJSON {
		return encodeJSON(stdout, response)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate envctl executable: %w", err)
	}
	model := setupui.New(
		loaded.Machine.ID,
		phases,
		setupui.ProcessFactory{Context: ctx, Executable: executable},
	)
	if *automatic {
		model.Automatic()
	}
	return setupui.Run(model)
}

func buildSetupPhases(
	ctx context.Context,
	loaded envconfig.Loaded,
) ([]setupui.Phase, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	recoveryPhase, err := buildRecoverySetupPhase(ctx, home, loaded)
	if err != nil {
		return nil, err
	}
	linkPhase, err := buildLinkSetupPhase(home, loaded)
	if err != nil {
		return nil, err
	}
	inventory, err := collectMachineInventory(ctx, loaded)
	if err != nil {
		return nil, fmt.Errorf("collect local setup inventory: %w", err)
	}
	packagePlan := planInventory(
		loaded.Desired.Packages,
		loaded.Desired.Links,
		inventory,
	)
	homebrewPhase := buildManagerSetupPhase(
		loaded,
		inventory,
		packagePlan,
		model.ManagerBrew,
		setupui.PhaseHomebrew,
		"Homebrew packages",
		"Install missing declared formulae and casks.",
		[]setupui.PhaseID{setupui.PhaseRecovery},
		true,
	)
	misePhase := buildManagerSetupPhase(
		loaded,
		inventory,
		packagePlan,
		model.ManagerMise,
		setupui.PhaseMise,
		"Mise runtimes",
		"Install declared runtimes before language-level tools.",
		[]setupui.PhaseID{
			setupui.PhaseRecovery,
			setupui.PhaseHomebrew,
		},
		true,
	)
	bunPhase := buildManagerSetupPhase(
		loaded,
		inventory,
		packagePlan,
		model.ManagerBun,
		setupui.PhaseBun,
		"Bun global tools",
		"Install declared global tools after Homebrew prerequisites.",
		[]setupui.PhaseID{setupui.PhaseMise},
		true,
	)
	customPhase := buildManagerSetupPhase(
		loaded,
		inventory,
		packagePlan,
		model.ManagerCustom,
		setupui.PhaseCustom,
		"Custom tools",
		"Install missing tools through envctl's fixed, reviewed installer registry.",
		[]setupui.PhaseID{setupui.PhaseHomebrew},
		true,
	)
	appPhase := buildAppSettingsSetupPhase(ctx, loaded)
	linkPhase.Dependencies = []setupui.PhaseID{
		setupui.PhaseRecovery,
		setupui.PhaseHomebrew,
		setupui.PhaseMise,
		setupui.PhaseBun,
		setupui.PhaseCustom,
		setupui.PhaseApps,
	}
	masPhase := buildManagerSetupPhase(
		loaded,
		inventory,
		packagePlan,
		model.ManagerMAS,
		setupui.PhaseMAS,
		"Mac App Store apps",
		"Install free and already-owned apps; defer purchases and incompatible apps.",
		[]setupui.PhaseID{setupui.PhaseHomebrew},
		true,
	)
	phases := []setupui.Phase{
		recoveryPhase,
		homebrewPhase,
		misePhase,
		bunPhase,
		customPhase,
		appPhase,
		linkPhase,
		masPhase,
		buildManualSetupPhase(packagePlan),
	}
	return phases, nil
}

func buildAppSettingsSetupPhase(
	ctx context.Context,
	loaded envconfig.Loaded,
) setupui.Phase {
	phase := setupui.Phase{
		ID:           setupui.PhaseApps,
		Label:        "App startup settings",
		Description:  "Apply reviewed startup settings for declared applications.",
		Status:       setupui.StatusSatisfied,
		Dependencies: []setupui.PhaseID{setupui.PhaseHomebrew},
	}
	if len(loaded.Desired.AppSettings) == 0 {
		phase.Description = "No application startup settings are declared."
		return phase
	}
	plan := appsetting.New(nil).Plan(ctx, loaded.Desired.AppSettings)
	phase.Actions = len(plan.Actions)
	phase.Blockers = len(plan.Blockers)
	for _, blocker := range plan.Blockers {
		phase.Diagnostics = append(
			phase.Diagnostics,
			blocker.ID+": "+blocker.Detail,
		)
	}
	switch {
	case !plan.Ready:
		phase.Status = setupui.StatusBlocked
	case len(plan.Actions) > 0:
		phase.Status = setupui.StatusReady
		phase.Command = []string{
			"app-settings", "apply",
			"--config", loaded.Root,
			"--machine", loaded.Machine.ID,
			"--local",
			"--json",
			"--yes",
		}
	}
	return phase
}

func buildRecoverySetupPhase(
	ctx context.Context,
	home string,
	loaded envconfig.Loaded,
) (setupui.Phase, error) {
	phase := setupui.Phase{
		ID:          setupui.PhaseRecovery,
		Label:       "Credential recovery",
		Description: "Restore encrypted machine-local credentials before configuration and package work.",
		Status:      setupui.StatusSatisfied,
	}
	if len(loaded.Desired.Recoveries) == 0 {
		phase.Description = "No credential recovery groups are declared."
		return phase, nil
	}
	transaction, err := recovery.NewTransaction(
		home,
		filepath.Join(
			home, ".local", "state", "envctl", "backups", "recovery",
		),
		filepath.Join(
			home, ".local", "state", "envctl", "staging", "recovery",
		),
	)
	if err != nil {
		return setupui.Phase{}, err
	}
	plan, _, err := transaction.Plan(ctx, loaded.Desired.Recoveries)
	if err != nil {
		return setupui.Phase{}, err
	}
	phase.Actions = len(plan.Actions)
	phase.Blockers = len(plan.Blockers)
	for _, blocker := range plan.Blockers {
		phase.Diagnostics = append(
			phase.Diagnostics,
			fmt.Sprintf(
				"%s (%s): %s",
				blocker.RecoveryID,
				blocker.Status,
				blocker.Detail,
			),
		)
	}
	switch {
	case phase.Blockers > 0:
		phase.Status = setupui.StatusBlocked
	case phase.Actions > 0:
		phase.Status = setupui.StatusReady
		phase.Command = localSetupCommand(
			"recovery", "apply", loaded.Root, loaded.Machine.ID, "",
			true,
		)
	}
	return phase, nil
}

func buildLinkSetupPhase(
	home string,
	loaded envconfig.Loaded,
) (setupui.Phase, error) {
	phase := setupui.Phase{
		ID:           setupui.PhaseLinks,
		Label:        "Portable configuration",
		Description:  "Create verified links to portable configuration files.",
		Status:       setupui.StatusSatisfied,
		Dependencies: []setupui.PhaseID{setupui.PhaseRecovery},
	}
	if len(loaded.Desired.Links) == 0 {
		phase.Description = "No portable configuration links are declared."
		return phase, nil
	}
	transaction, err := portablelink.NewTransaction(
		home,
		filepath.Join(
			home, ".local", "state", "envctl", "backups", "portable-links",
		),
	)
	if err != nil {
		return setupui.Phase{}, err
	}
	plan, err := transaction.Plan(loaded.Desired.Links)
	if err != nil {
		return setupui.Phase{}, err
	}
	phase.Actions = len(plan.Actions)
	phase.Blockers = len(plan.Blockers)
	for _, blocker := range plan.Blockers {
		phase.Diagnostics = append(
			phase.Diagnostics,
			fmt.Sprintf(
				"%s (%s): %s",
				blocker.LinkID,
				blocker.Status,
				blocker.Detail,
			),
		)
	}
	switch {
	case phase.Blockers > 0:
		phase.Status = setupui.StatusBlocked
	case phase.Actions > 0:
		phase.Status = setupui.StatusReady
		phase.Command = localSetupCommand(
			"links", "apply", loaded.Root, loaded.Machine.ID, "", true,
		)
	}
	return phase, nil
}

func buildManagerSetupPhase(
	loaded envconfig.Loaded,
	inventory model.Inventory,
	plan model.Plan,
	manager model.Manager,
	id setupui.PhaseID,
	label, description string,
	dependencies []setupui.PhaseID,
	mutating bool,
) setupui.Phase {
	phase := setupui.Phase{
		ID: id, Label: label, Description: description,
		Status: setupui.StatusSatisfied, Dependencies: dependencies,
	}
	desiredCount := desiredManagerCount(loaded.Desired.Packages, manager)
	if desiredCount == 0 {
		phase.Description = "No desired items are declared for this manager."
		return phase
	}
	selected, _ := selectActions(plan.Actions, manager)
	phase.Actions = len(selected)
	collector := collectorForApply(manager)
	if !hasCollector(inventory, collector) {
		phase.Actions = desiredCount
		phase.Status = setupui.StatusReady
		phase.Description += " Current inventory is unavailable; this phase replans after its prerequisites."
	} else if manager == model.ManagerMAS {
		if len(selected) > 0 {
			phase.Status = setupui.StatusReady
		}
	} else {
		_, blocked := classifyActions(selected)
		phase.Blockers = len(blocked)
		for _, item := range blocked {
			phase.Diagnostics = append(
				phase.Diagnostics,
				item.Action.PackageID+": "+item.Reason,
			)
		}
		switch {
		case phase.Blockers > 0:
			phase.Status = setupui.StatusBlocked
		case phase.Actions > 0:
			phase.Status = setupui.StatusReady
		}
	}
	if phase.Status == setupui.StatusReady ||
		phase.Status == setupui.StatusReview {
		phase.Command = localSetupCommand(
			"apply", "", loaded.Root, loaded.Machine.ID, manager, mutating,
		)
	}
	return phase
}

func buildManualSetupPhase(
	plan model.Plan,
) setupui.Phase {
	phase := setupui.Phase{
		ID:           setupui.PhaseManual,
		Label:        "Manual and external tools",
		Description:  "Review desired tools whose installers are intentionally outside envctl's execution boundary.",
		Status:       setupui.StatusSatisfied,
		Dependencies: []setupui.PhaseID{setupui.PhaseHomebrew, setupui.PhaseBun},
	}
	for _, finding := range plan.Findings {
		if finding.Desired == nil {
			continue
		}
		if finding.Desired.Manager != model.ManagerManual {
			continue
		}
		if finding.Status != model.FindingSatisfied {
			phase.Actions++
		}
	}
	if phase.Actions > 0 {
		phase.Status = setupui.StatusBlocked
		phase.Blockers = phase.Actions
	}
	return phase
}

func desiredManagerCount(
	desired []model.PackageSpec,
	manager model.Manager,
) int {
	count := 0
	for _, item := range desired {
		if item.Manager == manager {
			count++
		}
	}
	return count
}

func localSetupCommand(
	command, subcommand, configRoot, machineID string,
	manager model.Manager,
	mutating bool,
) []string {
	arguments := []string{command}
	if subcommand != "" {
		arguments = append(arguments, subcommand)
	}
	arguments = append(
		arguments,
		"--config", configRoot,
		"--machine", machineID,
		"--local",
	)
	if manager != "" {
		arguments = append(arguments, "--manager", string(manager))
	}
	if mutating {
		arguments = append(arguments, "--yes")
	} else {
		arguments = append(arguments, "--dry-run")
	}
	if command == "apply" {
		arguments = append(arguments, "--setup-progress")
	}
	return append(arguments, "--json")
}

func runOnboard(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("onboard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	machineID := flags.String("machine", "", "proposed machine id")
	profileSelection := flags.String(
		"profiles", "", "comma-separated profiles for a new machine",
	)
	asJSON := flags.Bool("json", false, "print the onboarding result as JSON")
	continueSetup := flags.Bool(
		"setup",
		false,
		"continue directly into guided setup after registration",
	)
	automaticSetup := flags.Bool(
		"auto",
		false,
		"run guided setup phases automatically; requires --setup",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configRoot == "" {
		return errors.New("--config is required")
	}
	if *asJSON && *continueSetup {
		return errors.New("--json and --setup cannot be combined")
	}
	if *automaticSetup && !*continueSetup {
		return errors.New("--auto requires --setup")
	}
	machines, err := envconfig.Machines(*configRoot)
	if err != nil {
		return err
	}
	profiles, err := envconfig.ProfileNames(*configRoot)
	if err != nil {
		return err
	}
	identity, err := onboard.Detect(ctx)
	if err != nil {
		return err
	}
	result, err := onboard.Resolve(
		identity,
		machines,
		profiles,
		*machineID,
		splitCommaSeparated(*profileSelection),
	)
	if err != nil {
		return err
	}
	if *continueSetup && result.Status == onboard.StatusMatched {
		setupArgs := []string{
			"--config", *configRoot,
			"--machine", result.MachineID,
			"--local",
		}
		if *automaticSetup {
			setupArgs = append(setupArgs, "--auto")
		}
		return runSetup(
			ctx,
			setupArgs,
			stdout,
			stderr,
		)
	}
	if result.Status == onboard.StatusMatched {
		loaded, err := envconfig.Load(*configRoot, result.MachineID)
		if err != nil {
			return err
		}
		inventory := collectInventory(ctx, loaded.Desired.Links)
		plan := planInventory(
			loaded.Desired.Packages,
			loaded.Desired.Links,
			inventory,
		)
		result.Plan = &plan
		if len(loaded.Desired.Recoveries) > 0 {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find home directory: %w", err)
			}
			recoveryPlanner, err := recovery.NewPlanner(home)
			if err != nil {
				return err
			}
			recoveryPlan := recoveryPlanner.Plan(ctx, loaded.Desired.Recoveries)
			result.RecoveryPlan = &recoveryPlan
		}
	}
	if *asJSON {
		return encodeJSON(stdout, result)
	}
	model := onboardui.New(result, *configRoot, onboardui.FileWriter{})
	if *continueSetup {
		model.ContinueIntoSetup()
	}
	if err := onboardui.Run(model); err != nil {
		return err
	}
	writtenMachineID := model.WrittenMachineID()
	if !*continueSetup || writtenMachineID == "" {
		return nil
	}
	fmt.Fprintf(
		stdout,
		"\nMachine %s registered locally. Launching guided setup.\n\n",
		writtenMachineID,
	)
	setupArgs := []string{
		"--config", *configRoot,
		"--machine", writtenMachineID,
		"--local",
	}
	if *automaticSetup {
		setupArgs = append(setupArgs, "--auto")
	}
	return runSetup(
		ctx,
		setupArgs,
		stdout,
		stderr,
	)
}

type recoveryPlanResponse struct {
	MachineID    string             `json:"machine_id"`
	ConfigDigest string             `json:"config_digest"`
	Plan         model.RecoveryPlan `json:"plan"`
}

type recoveryApplyResponse struct {
	Mode                   string                      `json:"mode"`
	MachineID              string                      `json:"machine_id"`
	ConfigDigest           string                      `json:"config_digest"`
	Status                 model.RecoveryPlan          `json:"status"`
	Plan                   recovery.TransactionPlan    `json:"plan"`
	Result                 *recovery.TransactionResult `json:"result,omitempty"`
	VerificationStatus     *model.RecoveryPlan         `json:"verification_status,omitempty"`
	RunID                  string                      `json:"run_id,omitempty"`
	PlanID                 string                      `json:"plan_id,omitempty"`
	VerificationSnapshotID string                      `json:"verification_snapshot_id,omitempty"`
}

func runRecovery(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	if len(args) == 0 {
		return errors.New(
			"usage: envctl recovery (plan | apply) --config DIR --machine ID " +
				"--local --json",
		)
	}
	switch args[0] {
	case "plan":
		return runRecoveryPlan(ctx, args[1:], stdout, stderr)
	case "apply":
		return runRecoveryApply(ctx, args[1:], stdout, stderr)
	default:
		return errors.New(
			"usage: envctl recovery (plan | apply) --config DIR --machine ID " +
				"--local --json",
		)
	}
}

func runRecoveryPlan(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("recovery plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	machineID := flags.String("machine", "", "machine id from the native config")
	localMachine := flags.Bool(
		"local", false,
		"require this Mac's registered identity and inspect it locally",
	)
	asJSON := flags.Bool("json", false, "print the recovery plan as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configRoot == "" || *machineID == "" {
		return errors.New("--config and --machine are required")
	}
	if !*localMachine {
		return errors.New("recovery planning currently requires --local")
	}
	if !*asJSON {
		return errors.New("recovery planning currently requires --json")
	}
	loaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return err
	}
	if err := forceLocalMachine(ctx, &loaded); err != nil {
		return err
	}
	if len(loaded.Desired.Recoveries) == 0 {
		return errors.New("machine has no desired recovery items")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	recoveryPlanner, err := recovery.NewPlanner(home)
	if err != nil {
		return err
	}
	return encodeJSON(stdout, recoveryPlanResponse{
		MachineID:    loaded.Machine.ID,
		ConfigDigest: loaded.Digest,
		Plan:         recoveryPlanner.Plan(ctx, loaded.Desired.Recoveries),
	})
}

func runRecoveryApply(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("recovery apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	machineID := flags.String("machine", "", "machine id from the native config")
	localMachine := flags.Bool(
		"local", false,
		"require this Mac's registered identity and execute locally",
	)
	backupDirectory := flags.String(
		"backup-dir", "", "credential backup directory inside the home directory",
	)
	stagingDirectory := flags.String(
		"staging-dir", "", "credential staging directory inside the home directory",
	)
	statePath := flags.String("state", "", "SQLite state database path")
	dryRun := flags.Bool(
		"dry-run",
		false,
		"print the transaction without changing credentials",
	)
	yes := flags.Bool(
		"yes",
		false,
		"confirm the validated credential-recovery transaction",
	)
	asJSON := flags.Bool(
		"json",
		false,
		"print the credential-recovery transaction as JSON",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configRoot == "" || *machineID == "" {
		return errors.New("--config and --machine are required")
	}
	if !*localMachine {
		return errors.New("credential recovery apply currently requires --local")
	}
	if !*asJSON {
		return errors.New("credential recovery apply currently requires --json")
	}
	if *dryRun == *yes {
		return errors.New("exactly one of --dry-run or --yes is required")
	}
	loaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return err
	}
	if err := forceLocalMachine(ctx, &loaded); err != nil {
		return err
	}
	if len(loaded.Desired.Recoveries) == 0 {
		return errors.New("machine has no desired recovery items")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	resolvedBackup, err := recoveryStatePath(
		home,
		*backupDirectory,
		"backups",
	)
	if err != nil {
		return err
	}
	resolvedStaging, err := recoveryStatePath(
		home,
		*stagingDirectory,
		"staging",
	)
	if err != nil {
		return err
	}
	transaction, err := recovery.NewTransaction(
		home,
		resolvedBackup,
		resolvedStaging,
	)
	if err != nil {
		return err
	}
	plan, statusPlan, err := transaction.Plan(ctx, loaded.Desired.Recoveries)
	if err != nil {
		return err
	}
	mode := "dry-run"
	if *yes {
		mode = "apply"
	}
	response := recoveryApplyResponse{
		Mode: mode, MachineID: loaded.Machine.ID,
		ConfigDigest: loaded.Digest, Status: statusPlan, Plan: plan,
	}
	if *dryRun {
		return encodeJSON(stdout, response)
	}
	if !plan.Ready {
		return fmt.Errorf(
			"refuse credential-recovery transaction with %d blocker(s)",
			len(plan.Blockers),
		)
	}
	reloaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return fmt.Errorf(
			"reload configuration before credential recovery apply: %w",
			err,
		)
	}
	if err := forceLocalMachine(ctx, &reloaded); err != nil {
		return err
	}
	if reloaded.Digest != loaded.Digest {
		return errors.New(
			"configuration changed during credential recovery planning; rerun recovery apply",
		)
	}
	databasePath := *statePath
	if databasePath == "" {
		databasePath = loaded.Database
	}
	state, err := openState(databasePath)
	if err != nil {
		return err
	}
	defer state.Close()
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read hostname: %w", err)
	}
	machine := store.MachineInfo{
		ID: loaded.Machine.ID, Hostname: hostname,
	}
	beforeInventory := model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"credential-recovery"},
		Recoveries:  statusPlan.Findings,
	}
	beforeSnapshot, err := state.RecordAudit(ctx, machine, beforeInventory)
	if err != nil {
		return err
	}
	record, err := state.RecordRecoveryPlan(
		ctx,
		loaded.Machine.ID,
		beforeSnapshot.ID,
		loaded.Digest,
		"recovery apply",
		false,
		plan,
	)
	if err != nil {
		return err
	}
	response.RunID = record.RunID
	response.PlanID = record.PlanID
	if err := state.BeginApply(ctx, record.RunID, record.PlanID); err != nil {
		return err
	}
	result, applyErr := transaction.Apply(
		ctx,
		plan,
		reloaded.Desired.Recoveries,
		stateRecoveryJournal{state: state, planID: record.PlanID},
	)
	response.Result = &result
	afterStatus := transactionStatusPlan(
		ctx,
		home,
		reloaded.Desired.Recoveries,
	)
	response.VerificationStatus = &afterStatus
	afterInventory := model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"credential-recovery"},
		Recoveries:  afterStatus.Findings,
	}
	afterSnapshot, snapshotErr := state.RecordAudit(
		ctx,
		machine,
		afterInventory,
	)
	if snapshotErr == nil {
		response.VerificationSnapshotID = afterSnapshot.ID
	}
	completionStatus := store.ActionStatusCompleted
	if applyErr != nil || !result.Verified || snapshotErr != nil ||
		!recoveryPlanSatisfied(afterStatus) {
		completionStatus = store.ActionStatusFailed
	}
	var completionErrors []error
	if applyErr != nil {
		completionErrors = append(completionErrors, applyErr)
		if err := state.SkipProposedActions(
			ctx,
			record.PlanID,
			applyErr.Error(),
		); err != nil {
			completionErrors = append(completionErrors, err)
		}
	}
	if applyErr == nil && !result.Verified {
		completionErrors = append(
			completionErrors,
			errors.New("credential recovery transaction was not verified"),
		)
	}
	if applyErr == nil && !recoveryPlanSatisfied(afterStatus) {
		completionErrors = append(
			completionErrors,
			errors.New(
				"credential recovery verification snapshot is not fully satisfied",
			),
		)
	}
	if snapshotErr != nil {
		completionErrors = append(
			completionErrors,
			fmt.Errorf(
				"record credential recovery verification snapshot: %w",
				snapshotErr,
			),
		)
	}
	verificationID := ""
	if snapshotErr == nil {
		verificationID = afterSnapshot.ID
	}
	if err := state.CompleteApply(
		ctx,
		record.RunID,
		record.PlanID,
		verificationID,
		completionStatus,
	); err != nil {
		completionErrors = append(completionErrors, err)
	}
	if err := encodeJSON(stdout, response); err != nil {
		return err
	}
	return errors.Join(completionErrors...)
}

func recoveryStatePath(home, override, category string) (string, error) {
	if override == "" {
		return filepath.Join(
			home,
			".local",
			"state",
			"envctl",
			category,
			"recovery",
		), nil
	}
	return expandHome(override)
}

func transactionStatusPlan(
	ctx context.Context,
	home string,
	specs []model.RecoverySpec,
) model.RecoveryPlan {
	planner, err := recovery.NewPlanner(home)
	if err != nil {
		return model.RecoveryPlan{
			Ready: false,
			Findings: []model.RecoveryFinding{{
				Status: model.RecoveryFindingBlocked,
				Detail: "credential recovery verification could not inspect the home directory",
			}},
			Summary: model.RecoveryPlanSummary{Blocked: 1},
		}
	}
	return planner.Plan(ctx, specs)
}

func recoveryPlanSatisfied(plan model.RecoveryPlan) bool {
	return plan.Summary.Satisfied == len(plan.Findings)
}

type stateRecoveryJournal struct {
	state  *store.Store
	planID string
}

func (j stateRecoveryJournal) StartRecovery(
	ctx context.Context,
	action recovery.RecoveryAction,
) error {
	return j.state.StartAction(ctx, j.planID, action.Sequence)
}

func (j stateRecoveryJournal) RecordRecoveryBackup(
	ctx context.Context,
	action recovery.RecoveryAction,
	originalPath, backupPath string,
) error {
	return j.state.RecordRecoveryBackup(
		ctx,
		j.planID,
		action.Sequence,
		originalPath,
		backupPath,
	)
}

func (j stateRecoveryJournal) CompleteRecovery(
	ctx context.Context,
	action recovery.RecoveryAction,
) error {
	return j.state.FinishAction(
		ctx,
		j.planID,
		action.Sequence,
		store.ActionStatusCompleted,
		"",
	)
}

func (j stateRecoveryJournal) FailRecovery(
	ctx context.Context,
	action recovery.RecoveryAction,
	reason string,
) error {
	return j.state.FinishAction(
		ctx,
		j.planID,
		action.Sequence,
		store.ActionStatusFailed,
		reason,
	)
}

func (j stateRecoveryJournal) RollBackRecovery(
	ctx context.Context,
	action recovery.RecoveryAction,
	reason string,
) error {
	return j.state.RollBackAction(
		ctx,
		j.planID,
		action.Sequence,
		reason,
	)
}

type linkApplyResponse struct {
	Mode                   string                          `json:"mode"`
	MachineID              string                          `json:"machine_id"`
	ConfigDigest           string                          `json:"config_digest"`
	Plan                   portablelink.TransactionPlan    `json:"plan"`
	Result                 *portablelink.TransactionResult `json:"result,omitempty"`
	RunID                  string                          `json:"run_id,omitempty"`
	PlanID                 string                          `json:"plan_id,omitempty"`
	VerificationSnapshotID string                          `json:"verification_snapshot_id,omitempty"`
}

type appSettingsApplyResponse struct {
	Mode         string             `json:"mode"`
	MachineID    string             `json:"machine_id"`
	ConfigDigest string             `json:"config_digest"`
	Plan         appsetting.Plan    `json:"plan"`
	Result       *appsetting.Result `json:"result,omitempty"`
}

type bootstrapCheckpointResponse struct {
	MachineID       string                     `json:"machine_id"`
	ConfigDigest    string                     `json:"config_digest"`
	CheckpointPath  string                     `json:"checkpoint_path"`
	Checkpoint      bootstrapverify.Checkpoint `json:"checkpoint"`
	Checks          []bootstrapverify.Check    `json:"checks"`
	Ready           bool                       `json:"ready"`
	RestartRequired bool                       `json:"restart_required"`
}

type combinedOutputRunner struct{}

func (combinedOutputRunner) Run(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func runBootstrap(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	if len(args) == 0 {
		return errors.New(
			"usage: envctl bootstrap (checkpoint | verify) " +
				"[--config DIR] [--machine ID] [--json]",
		)
	}
	switch args[0] {
	case "checkpoint":
		return runBootstrapCheckpoint(ctx, args[1:], stdout, stderr)
	case "verify":
		return runBootstrapVerify(ctx, args[1:], stdout, stderr)
	default:
		return errors.New(
			"usage: envctl bootstrap (checkpoint | verify) " +
				"[--config DIR] [--machine ID] [--json]",
		)
	}
}

func runBootstrapCheckpoint(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("bootstrap checkpoint", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String(
		"config", "", "native env-config directory; defaults to the standard checkout",
	)
	machineID := flags.String(
		"machine", "", "machine id; defaults to this Mac's registered identity",
	)
	asJSON := flags.Bool("json", false, "print the checkpoint as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("bootstrap checkpoint accepts no positional arguments")
	}
	loaded, err := loadLocalBootstrapMachine(ctx, *configRoot, *machineID)
	if err != nil {
		return err
	}
	checks, ready, err := bootstrapCoreChecks(ctx, loaded)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New(
			"bootstrap core setup is incomplete; finish the failed or pending setup phases before checkpointing",
		)
	}
	bootID, err := bootstrapverify.CurrentBootID(ctx, combinedOutputRunner{})
	if err != nil {
		return err
	}
	path, err := bootstrapCheckpointPath()
	if err != nil {
		return err
	}
	checkpoint := bootstrapverify.Checkpoint{
		MachineID:    loaded.Machine.ID,
		ConfigDigest: loaded.Digest,
		BootID:       bootID,
		CreatedAt:    time.Now().UTC(),
	}
	if existing, loadErr := bootstrapverify.Load(path); loadErr == nil &&
		existing.MachineID == checkpoint.MachineID &&
		existing.ConfigDigest == checkpoint.ConfigDigest &&
		existing.BootID == checkpoint.BootID &&
		existing.VerifiedAt == nil {
		checkpoint = existing
	}
	if err := bootstrapverify.SaveCheckpoint(path, checkpoint); err != nil {
		return err
	}
	response := bootstrapCheckpointResponse{
		MachineID:       loaded.Machine.ID,
		ConfigDigest:    loaded.Digest,
		CheckpointPath:  path,
		Checkpoint:      checkpoint,
		Checks:          checks,
		Ready:           true,
		RestartRequired: true,
	}
	if *asJSON {
		return encodeJSON(stdout, response)
	}
	fmt.Fprintf(
		stdout,
		"Bootstrap setup is ready for restart verification on %s.\n",
		loaded.Machine.ID,
	)
	printBootstrapChecks(stdout, checks)
	fmt.Fprintln(stdout)
	fmt.Fprintln(
		stdout,
		"Restart this Mac, then rerun the same bootstrap command.",
	)
	fmt.Fprintf(stdout, "Checkpoint: %s\n", path)
	return nil
}

func runBootstrapVerify(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("bootstrap verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String(
		"config", "", "native env-config directory; defaults to the standard checkout",
	)
	machineID := flags.String(
		"machine", "", "machine id; defaults to this Mac's registered identity",
	)
	asJSON := flags.Bool("json", false, "print the verification report as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("bootstrap verify accepts no positional arguments")
	}
	loaded, err := loadLocalBootstrapMachine(ctx, *configRoot, *machineID)
	if err != nil {
		return err
	}
	checkpointPath, err := bootstrapCheckpointPath()
	if err != nil {
		return err
	}
	checkpoint, err := bootstrapverify.Load(checkpointPath)
	if err != nil {
		return err
	}
	currentBootID, err := bootstrapverify.CurrentBootID(
		ctx,
		combinedOutputRunner{},
	)
	if err != nil {
		return err
	}
	report := bootstrapverify.Report{
		MachineID:      loaded.Machine.ID,
		ConfigDigest:   loaded.Digest,
		CheckpointPath: checkpointPath,
		PreviousBootID: checkpoint.BootID,
		CurrentBootID:  currentBootID,
		Restarted:      checkpoint.BootID != currentBootID,
		VerifiedAt:     time.Now().UTC(),
	}
	report.Checks = append(report.Checks, bootstrapverify.Check{
		ID:     "restart-observed",
		Status: boolCheckStatus(report.Restarted),
		Detail: boolDetail(
			report.Restarted,
			"the Mac restarted after the bootstrap checkpoint",
			"the Mac has not restarted since the bootstrap checkpoint",
		),
	})
	sameMachine := checkpoint.MachineID == loaded.Machine.ID
	report.Checks = append(report.Checks, bootstrapverify.Check{
		ID:     "machine-identity",
		Status: boolCheckStatus(sameMachine),
		Detail: boolDetail(
			sameMachine,
			"checkpoint belongs to "+loaded.Machine.ID,
			fmt.Sprintf(
				"checkpoint belongs to %q, not %q",
				checkpoint.MachineID,
				loaded.Machine.ID,
			),
		),
	})
	sameConfig := checkpoint.ConfigDigest == loaded.Digest
	report.Checks = append(report.Checks, bootstrapverify.Check{
		ID:     "config-digest",
		Status: boolCheckStatus(sameConfig),
		Detail: boolDetail(
			sameConfig,
			"configuration is unchanged since the checkpoint",
			"configuration changed after the checkpoint; apply it and create a new checkpoint",
		),
	})
	coreChecks, _, coreErr := bootstrapCoreChecks(ctx, loaded)
	if coreErr != nil {
		report.Checks = append(report.Checks, bootstrapverify.Check{
			ID: "core-setup", Status: bootstrapverify.StatusFailed,
			Detail: coreErr.Error(),
		})
	} else {
		report.Checks = append(report.Checks, coreChecks...)
	}
	for _, check := range appsetting.New(nil).VerifyAfterRestart(
		ctx,
		loaded.Desired.AppSettings,
	) {
		report.Checks = append(report.Checks, bootstrapverify.Check{
			ID:     check.ID,
			Status: boolCheckStatus(check.Passed),
			Detail: check.Detail,
		})
	}
	report.Checks = append(
		report.Checks,
		bootstrapLegacyCheck(loaded),
		bootstrapGitCheck(ctx, loaded),
		bootstrapShellCheck(ctx, loaded),
	)
	report.Ready = true
	for _, check := range report.Checks {
		if check.Status == bootstrapverify.StatusFailed {
			report.Ready = false
			break
		}
	}
	reportPath := filepath.Join(
		filepath.Dir(checkpointPath),
		"verification-report.json",
	)
	if err := bootstrapverify.SaveReport(reportPath, report); err != nil {
		return err
	}
	if report.Ready {
		verifiedAt := report.VerifiedAt
		checkpoint.VerifiedAt = &verifiedAt
		if err := bootstrapverify.SaveCheckpoint(
			checkpointPath,
			checkpoint,
		); err != nil {
			return err
		}
	}
	if *asJSON {
		if err := encodeJSON(stdout, report); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(
			stdout,
			"Post-restart bootstrap verification for %s\n",
			loaded.Machine.ID,
		)
		printBootstrapChecks(stdout, report.Checks)
		fmt.Fprintf(stdout, "\nReport: %s\n", reportPath)
	}
	if !report.Ready {
		return errors.New(
			"post-restart bootstrap verification failed; the checkpoint was kept for a retry",
		)
	}
	if !*asJSON {
		fmt.Fprintln(stdout, "Bootstrap verified and complete.")
	}
	return nil
}

func loadLocalBootstrapMachine(
	ctx context.Context,
	configRoot, machineID string,
) (envconfig.Loaded, error) {
	resolvedRoot, err := resolveConfigRoot(configRoot)
	if err != nil {
		return envconfig.Loaded{}, err
	}
	resolvedID := machineID
	if resolvedID == "" {
		machines, err := envconfig.Machines(resolvedRoot)
		if err != nil {
			return envconfig.Loaded{}, err
		}
		profiles, err := envconfig.ProfileNames(resolvedRoot)
		if err != nil {
			return envconfig.Loaded{}, err
		}
		identity, err := onboard.Detect(ctx)
		if err != nil {
			return envconfig.Loaded{}, err
		}
		result, err := onboard.Resolve(identity, machines, profiles, "", nil)
		if err != nil {
			return envconfig.Loaded{}, err
		}
		if result.Status != onboard.StatusMatched {
			return envconfig.Loaded{}, errors.New(
				"this Mac is not registered; run envctl onboard first",
			)
		}
		resolvedID = result.MachineID
	}
	loaded, err := envconfig.Load(resolvedRoot, resolvedID)
	if err != nil {
		return envconfig.Loaded{}, err
	}
	if err := forceLocalMachine(ctx, &loaded); err != nil {
		return envconfig.Loaded{}, err
	}
	return loaded, nil
}

func bootstrapCoreChecks(
	ctx context.Context,
	loaded envconfig.Loaded,
) ([]bootstrapverify.Check, bool, error) {
	phases, err := buildSetupPhases(ctx, loaded)
	if err != nil {
		return nil, false, err
	}
	ready := true
	checks := make([]bootstrapverify.Check, 0, len(phases))
	for _, phase := range phases {
		status := bootstrapverify.StatusPassed
		detail := phase.Label + " is satisfied"
		switch phase.ID {
		case setupui.PhaseMAS, setupui.PhaseManual:
			if phase.Status != setupui.StatusSatisfied {
				status = bootstrapverify.StatusWarned
				detail = fmt.Sprintf(
					"%s has %d manual or deferred action(s)",
					phase.Label,
					phase.Actions,
				)
			}
		default:
			if phase.Status != setupui.StatusSatisfied {
				status = bootstrapverify.StatusFailed
				ready = false
				detail = fmt.Sprintf(
					"%s is %s with %d action(s) and %d blocker(s)",
					phase.Label,
					phase.Status,
					phase.Actions,
					phase.Blockers,
				)
			}
		}
		checks = append(checks, bootstrapverify.Check{
			ID: "phase." + string(phase.ID), Status: status, Detail: detail,
		})
	}
	return checks, ready, nil
}

func bootstrapLegacyCheck(loaded envconfig.Loaded) bootstrapverify.Check {
	home, err := os.UserHomeDir()
	if err != nil {
		return bootstrapverify.Check{
			ID: "legacy-independence", Status: bootstrapverify.StatusFailed,
			Detail: err.Error(),
		}
	}
	auditor, err := legacydeps.New(
		home,
		filepath.Join(home, "Documents", "env"),
		loaded.Root,
	)
	if err != nil {
		return bootstrapverify.Check{
			ID: "legacy-independence", Status: bootstrapverify.StatusFailed,
			Detail: err.Error(),
		}
	}
	report, err := auditor.Audit()
	if err != nil {
		return bootstrapverify.Check{
			ID: "legacy-independence", Status: bootstrapverify.StatusFailed,
			Detail: err.Error(),
		}
	}
	return bootstrapverify.Check{
		ID:     "legacy-independence",
		Status: boolCheckStatus(report.Ready),
		Detail: boolDetail(
			report.Ready,
			"no active configuration depends on ~/Documents/env",
			fmt.Sprintf(
				"%d active path(s) still depend on ~/Documents/env",
				report.Dependencies,
			),
		),
	}
}

func bootstrapGitCheck(
	ctx context.Context,
	loaded envconfig.Loaded,
) bootstrapverify.Check {
	output, err := (combinedOutputRunner{}).Run(
		ctx,
		"/usr/bin/env",
		"git",
		"-C",
		loaded.Root,
		"ls-remote",
		"origin",
		"HEAD",
	)
	passed := err == nil && strings.TrimSpace(string(output)) != ""
	detail := "private env-config Git access works without an SSH agent"
	if !passed {
		detail = commandFailureDetail("verify private env-config Git access", output, err)
	}
	return bootstrapverify.Check{
		ID: "private-config-git", Status: boolCheckStatus(passed), Detail: detail,
	}
}

func bootstrapShellCheck(
	ctx context.Context,
	loaded envconfig.Loaded,
) bootstrapverify.Check {
	home, err := os.UserHomeDir()
	if err != nil {
		return bootstrapverify.Check{
			ID: "clean-login-shell", Status: bootstrapverify.StatusFailed,
			Detail: err.Error(),
		}
	}
	user := os.Getenv("USER")
	if user == "" {
		user = filepath.Base(home)
	}
	const marker = "__ENVCTL_SHELL_OK__"
	const shellTest = `test -n "$ENV_CONFIG_HOME" &&
test "$ENV_CONFIG_HOME" = "$HOME/.local/share/envctl/repos/env-config" &&
typeset -f zinit >/dev/null &&
command -v fzf >/dev/null &&
command -v oh-my-posh >/dev/null &&
test -n "$PROMPT" &&
case "$ENV_CONFIG_HOME:$PATH" in *"$HOME/Documents/env"*) false;; *) true;; esac &&
printf __ENVCTL_SHELL_OK__`
	output, runErr := (combinedOutputRunner{}).Run(
		ctx,
		"/usr/bin/script",
		"-q",
		"/dev/null",
		"/usr/bin/env",
		"-i",
		"HOME="+home,
		"USER="+user,
		"LOGNAME="+user,
		"TERM=xterm-256color",
		"PATH="+filepath.Join(home, ".local", "bin")+
			":/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"/bin/zsh",
		"-lic",
		shellTest,
	)
	normalizedOutput := strings.ReplaceAll(string(output), "^D\b\b", "")
	passed := runErr == nil &&
		strings.TrimSpace(normalizedOutput) == marker
	detail := "a clean interactive login shell starts quietly with the managed prompt"
	if !passed {
		detail = commandFailureDetail("verify clean login shell", output, runErr)
	}
	return bootstrapverify.Check{
		ID: "clean-login-shell", Status: boolCheckStatus(passed), Detail: detail,
	}
}

func bootstrapCheckpointPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(
		home,
		".local",
		"state",
		"envctl",
		"bootstrap",
		"reboot-checkpoint.json",
	), nil
}

func boolCheckStatus(passed bool) bootstrapverify.CheckStatus {
	if passed {
		return bootstrapverify.StatusPassed
	}
	return bootstrapverify.StatusFailed
}

func boolDetail(condition bool, passed, failed string) string {
	if condition {
		return passed
	}
	return failed
}

func commandFailureDetail(action string, output []byte, err error) string {
	detail := strings.TrimSpace(string(output))
	if err == nil {
		err = errors.New("unexpected output")
	}
	if detail == "" {
		return fmt.Sprintf("%s: %v", action, err)
	}
	const maxDetail = 500
	if len(detail) > maxDetail {
		detail = detail[:maxDetail] + "..."
	}
	return fmt.Sprintf("%s: %v: %s", action, err, detail)
}

func printBootstrapChecks(
	writer io.Writer,
	checks []bootstrapverify.Check,
) {
	for _, check := range checks {
		symbol := "✓"
		switch check.Status {
		case bootstrapverify.StatusWarned:
			symbol = "!"
		case bootstrapverify.StatusFailed:
			symbol = "✗"
		}
		fmt.Fprintf(writer, "  %s %s: %s\n", symbol, check.ID, check.Detail)
	}
}

func runAppSettings(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	if len(args) == 0 || args[0] != "apply" {
		return errors.New(
			"usage: envctl app-settings apply --config DIR --machine ID " +
				"--local --json (--dry-run | --yes)",
		)
	}
	flags := flag.NewFlagSet("app-settings apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	machineID := flags.String("machine", "", "machine id from the native config")
	localMachine := flags.Bool(
		"local", false,
		"require this Mac's registered identity and execute locally",
	)
	dryRun := flags.Bool(
		"dry-run", false,
		"print the setting transaction without changing preferences",
	)
	yes := flags.Bool(
		"yes", false,
		"confirm the validated application-setting transaction",
	)
	asJSON := flags.Bool(
		"json", false,
		"print the application-setting transaction as JSON",
	)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("app-settings apply accepts no positional arguments")
	}
	if *configRoot == "" || *machineID == "" {
		return errors.New("--config and --machine are required")
	}
	if !*localMachine {
		return errors.New("application-setting apply currently requires --local")
	}
	if !*asJSON {
		return errors.New("application-setting apply currently requires --json")
	}
	if *dryRun == *yes {
		return errors.New("exactly one of --dry-run or --yes is required")
	}
	loaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return err
	}
	if err := forceLocalMachine(ctx, &loaded); err != nil {
		return err
	}
	if len(loaded.Desired.AppSettings) == 0 {
		return errors.New("machine has no desired application settings")
	}
	manager := appsetting.New(nil)
	plan := manager.Plan(ctx, loaded.Desired.AppSettings)
	mode := "dry-run"
	if *yes {
		mode = "apply"
	}
	response := appSettingsApplyResponse{
		Mode:         mode,
		MachineID:    loaded.Machine.ID,
		ConfigDigest: loaded.Digest,
		Plan:         plan,
	}
	if *dryRun {
		return encodeJSON(stdout, response)
	}
	if !plan.Ready {
		return fmt.Errorf(
			"refuse application-setting transaction with %d blocker(s)",
			len(plan.Blockers),
		)
	}
	reloaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return fmt.Errorf("reload configuration before app-setting apply: %w", err)
	}
	if err := forceLocalMachine(ctx, &reloaded); err != nil {
		return err
	}
	if reloaded.Digest != loaded.Digest {
		return errors.New(
			"configuration changed during app-setting planning; rerun app-settings apply",
		)
	}
	result, err := manager.Apply(ctx, reloaded.Desired.AppSettings)
	response.Result = &result
	if err != nil {
		return err
	}
	return encodeJSON(stdout, response)
}

func runLinks(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	if len(args) == 0 || args[0] != "apply" {
		return errors.New(
			"usage: envctl links apply --config DIR --machine ID " +
				"--local --json (--dry-run | --yes)",
		)
	}
	flags := flag.NewFlagSet("links apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	machineID := flags.String("machine", "", "machine id from the native config")
	localMachine := flags.Bool(
		"local", false,
		"require this Mac's registered identity and execute locally",
	)
	backupDirectory := flags.String(
		"backup-dir", "", "portable-link backup directory inside the home directory",
	)
	statePath := flags.String("state", "", "SQLite state database path")
	dryRun := flags.Bool("dry-run", false, "print the transaction without changing links")
	yes := flags.Bool("yes", false, "confirm the validated link transaction")
	asJSON := flags.Bool("json", false, "print the link transaction as JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *configRoot == "" || *machineID == "" {
		return errors.New("--config and --machine are required")
	}
	if !*localMachine {
		return errors.New("portable-link apply currently requires --local")
	}
	if !*asJSON {
		return errors.New("portable-link apply currently requires --json")
	}
	if *dryRun == *yes {
		return errors.New("exactly one of --dry-run or --yes is required")
	}

	loaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return err
	}
	if err := forceLocalMachine(ctx, &loaded); err != nil {
		return err
	}
	if len(loaded.Desired.Links) == 0 {
		return errors.New("machine has no desired portable links")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	resolvedBackup := *backupDirectory
	if resolvedBackup == "" {
		resolvedBackup = filepath.Join(
			home, ".local", "state", "envctl", "backups", "portable-links",
		)
	} else {
		resolvedBackup, err = expandHome(resolvedBackup)
		if err != nil {
			return err
		}
	}
	transaction, err := portablelink.NewTransaction(home, resolvedBackup)
	if err != nil {
		return err
	}
	plan, err := transaction.Plan(loaded.Desired.Links)
	if err != nil {
		return err
	}
	mode := "dry-run"
	if *yes {
		mode = "apply"
	}
	response := linkApplyResponse{
		Mode: mode, MachineID: loaded.Machine.ID,
		ConfigDigest: loaded.Digest, Plan: plan,
	}
	if *dryRun {
		return encodeJSON(stdout, response)
	}
	if !plan.Ready {
		return fmt.Errorf(
			"refuse portable-link transaction with %d blocker(s)",
			len(plan.Blockers),
		)
	}
	reloaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return fmt.Errorf("reload configuration before link apply: %w", err)
	}
	if err := forceLocalMachine(ctx, &reloaded); err != nil {
		return err
	}
	if reloaded.Digest != loaded.Digest {
		return errors.New("configuration changed during link planning; rerun links apply")
	}
	databasePath := *statePath
	if databasePath == "" {
		databasePath = loaded.Database
	}
	state, err := openState(databasePath)
	if err != nil {
		return err
	}
	defer state.Close()
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read hostname: %w", err)
	}
	machine := store.MachineInfo{
		ID: loaded.Machine.ID, Hostname: hostname,
	}
	beforeInventory := model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"portable-link"},
		Links:       portablelink.Collect(loaded.Desired.Links),
	}
	beforeSnapshot, err := state.RecordAudit(ctx, machine, beforeInventory)
	if err != nil {
		return err
	}
	record, err := state.RecordLinkPlan(
		ctx,
		loaded.Machine.ID,
		beforeSnapshot.ID,
		loaded.Digest,
		"links apply",
		false,
		plan,
	)
	if err != nil {
		return err
	}
	response.RunID = record.RunID
	response.PlanID = record.PlanID
	if err := state.BeginApply(ctx, record.RunID, record.PlanID); err != nil {
		return err
	}

	result, applyErr := transaction.Apply(
		ctx,
		plan,
		reloaded.Desired.Links,
		stateLinkJournal{state: state, planID: record.PlanID},
	)
	response.Result = &result
	afterInventory := model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"portable-link"},
		Links:       portablelink.Collect(reloaded.Desired.Links),
	}
	afterSnapshot, snapshotErr := state.RecordAudit(ctx, machine, afterInventory)
	if snapshotErr == nil {
		response.VerificationSnapshotID = afterSnapshot.ID
	}
	completionStatus := store.ActionStatusCompleted
	if applyErr != nil || !result.Verified || snapshotErr != nil {
		completionStatus = store.ActionStatusFailed
	}
	var completionErrors []error
	if applyErr != nil {
		completionErrors = append(completionErrors, applyErr)
		if err := state.SkipProposedActions(
			ctx, record.PlanID, applyErr.Error(),
		); err != nil {
			completionErrors = append(completionErrors, err)
		}
	}
	if snapshotErr != nil {
		completionErrors = append(
			completionErrors,
			fmt.Errorf("record link verification snapshot: %w", snapshotErr),
		)
	}
	verificationID := ""
	if snapshotErr == nil {
		verificationID = afterSnapshot.ID
	}
	if err := state.CompleteApply(
		ctx,
		record.RunID,
		record.PlanID,
		verificationID,
		completionStatus,
	); err != nil {
		completionErrors = append(completionErrors, err)
	}
	if err := encodeJSON(stdout, response); err != nil {
		return err
	}
	return errors.Join(completionErrors...)
}

type stateLinkJournal struct {
	state  *store.Store
	planID string
}

func (j stateLinkJournal) StartLink(
	ctx context.Context,
	action portablelink.LinkAction,
) error {
	return j.state.StartAction(ctx, j.planID, action.Sequence)
}

func (j stateLinkJournal) CompleteLink(
	ctx context.Context,
	action portablelink.LinkAction,
) error {
	if action.BackupPath != "" {
		if err := j.state.RecordActionBackup(
			ctx,
			j.planID,
			action.Sequence,
			action.Target,
			action.BackupPath,
			action.ExpectedLinkTarget,
		); err != nil {
			return err
		}
	}
	return j.state.FinishAction(
		ctx,
		j.planID,
		action.Sequence,
		store.ActionStatusCompleted,
		"",
	)
}

func (j stateLinkJournal) FailLink(
	ctx context.Context,
	action portablelink.LinkAction,
	reason string,
) error {
	return j.state.FinishAction(
		ctx,
		j.planID,
		action.Sequence,
		store.ActionStatusFailed,
		reason,
	)
}

func (j stateLinkJournal) RollBackLink(
	ctx context.Context,
	action portablelink.LinkAction,
	reason string,
) error {
	return j.state.RollBackAction(
		ctx,
		j.planID,
		action.Sequence,
		reason,
	)
}

type applyResponse struct {
	Mode                   string               `json:"mode"`
	MachineID              string               `json:"machine_id"`
	AccessType             string               `json:"access_type"`
	Manager                model.Manager        `json:"manager,omitempty"`
	ConfigDigest           string               `json:"config_digest"`
	Before                 model.Plan           `json:"before"`
	Execution              executor.Report      `json:"execution"`
	BlockedActions         []blockedAction      `json:"blocked_actions,omitempty"`
	DeferredActions        []model.Action       `json:"deferred_actions,omitempty"`
	MASPreflight           *mas.PreflightReport `json:"mas_preflight,omitempty"`
	Ready                  bool                 `json:"ready"`
	After                  *model.Plan          `json:"after,omitempty"`
	RunID                  string               `json:"run_id,omitempty"`
	PlanID                 string               `json:"plan_id,omitempty"`
	VerificationSnapshotID string               `json:"verification_snapshot_id,omitempty"`
	Verified               bool                 `json:"verified"`
}

type blockedAction struct {
	Action model.Action `json:"action"`
	Reason string       `json:"reason"`
}

func runApply(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	machineID := flags.String("machine", "", "machine id from the native config")
	managerName := flags.String(
		"manager", "", "limit apply to one supported manager: brew, mise, bun, custom, or mas",
	)
	localMachine := flags.Bool(
		"local", false,
		"require this Mac's registered identity and execute locally",
	)
	statePath := flags.String("state", "", "SQLite state database path")
	dryRun := flags.Bool("dry-run", false, "validate and print commands without changing state")
	yes := flags.Bool("yes", false, "confirm execution of the validated plan")
	asJSON := flags.Bool("json", false, "print the apply report as JSON")
	setupProgress := flags.Bool(
		"setup-progress",
		false,
		"stream concise installer progress for guided setup",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configRoot == "" || *machineID == "" {
		return errors.New("--config and --machine are required")
	}
	if !*asJSON {
		return errors.New("apply currently requires --json")
	}
	if *setupProgress && !*localMachine {
		return errors.New("--setup-progress requires --local")
	}
	if *dryRun == *yes {
		return errors.New("exactly one of --dry-run or --yes is required")
	}
	selectedManager, err := parseApplyManager(*managerName)
	if err != nil {
		return err
	}
	if *setupProgress {
		fmt.Fprintf(stderr, "\n==> %s\n", applyManagerLabel(selectedManager))
	}
	if selectedManager == model.ManagerMAS && *yes && !*localMachine {
		return errors.New("Mac App Store execution requires --local")
	}

	loaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return err
	}
	if *localMachine {
		if err := forceLocalMachine(ctx, &loaded); err != nil {
			return err
		}
	}
	beforeInventory, err := collectMachineInventory(ctx, loaded)
	if err != nil {
		return fmt.Errorf(
			"collect live inventory for %s machine %q: %w",
			loaded.Machine.Access.Type,
			loaded.Machine.ID, err,
		)
	}
	requiredCollector := collectorForApply(selectedManager)
	if err := requireCollector(beforeInventory, requiredCollector); err != nil {
		return fmt.Errorf(
			"refuse apply without current %s inventory: %w",
			requiredCollector, err,
		)
	}
	beforePlan := planInventory(
		loaded.Desired.Packages, loaded.Desired.Links, beforeInventory,
	)
	selectedActions, deferredActions := selectActions(
		beforePlan.Actions, selectedManager,
	)
	executableActions := append([]model.Action(nil), selectedActions...)
	var commands []executor.Command
	var blockedActions []blockedAction
	var masPreflight *mas.PreflightReport
	var masInstallations []mas.Installation
	if selectedManager == model.ManagerMAS {
		actionRunner, err := executionRunnerFor(loaded, nil)
		if err != nil {
			return err
		}
		preflight, err := mas.Preflight(
			ctx, selectedActions, masOutputAdapter{runner: actionRunner},
		)
		if err != nil {
			return err
		}
		masPreflight = &preflight
		var deferredMAS []mas.DeferredInstallation
		masInstallations, deferredMAS, err = mas.PlanInstallations(
			selectedActions,
			preflight,
		)
		if err != nil {
			return err
		}
		executableActions = make([]model.Action, 0, len(masInstallations))
		commands = make([]executor.Command, 0, len(masInstallations))
		for _, installation := range masInstallations {
			executableActions = append(
				executableActions,
				installation.Action,
			)
			commands = append(commands, installation.Command)
		}
		for _, deferred := range deferredMAS {
			blockedActions = append(blockedActions, blockedAction{
				Action: deferred.Action,
				Reason: deferred.Reason,
			})
		}
	} else {
		commands, blockedActions = classifyActions(selectedActions)
	}
	mode := "dry-run"
	if *yes {
		mode = "apply"
	}
	response := applyResponse{
		Mode:            mode,
		MachineID:       loaded.Machine.ID,
		AccessType:      loaded.Machine.Access.Type,
		Manager:         selectedManager,
		ConfigDigest:    loaded.Digest,
		Before:          beforePlan,
		Execution:       executor.Report{Commands: commands},
		BlockedActions:  blockedActions,
		DeferredActions: deferredActions,
		MASPreflight:    masPreflight,
		Ready:           len(blockedActions) == 0,
		Verified:        len(executableActions) == 0,
	}
	if *dryRun {
		return writeApplyResponse(
			stdout, stderr, response, *setupProgress,
		)
	}
	if len(blockedActions) > 0 && selectedManager != model.ManagerMAS {
		return fmt.Errorf(
			"refuse mixed or unsupported apply plan: %d action(s) are blocked",
			len(blockedActions),
		)
	}
	if len(commands) == 0 {
		return writeApplyResponse(
			stdout, stderr, response, *setupProgress,
		)
	}

	reloaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return fmt.Errorf("reload configuration before apply: %w", err)
	}
	if *localMachine {
		if err := forceLocalMachine(ctx, &reloaded); err != nil {
			return err
		}
	}
	if reloaded.Digest != loaded.Digest {
		return errors.New("configuration changed during planning; rerun apply")
	}
	var progressWriter io.Writer
	if *setupProgress {
		progressWriter = stderr
	}
	actionRunner, err := executionRunnerFor(reloaded, progressWriter)
	if err != nil {
		return err
	}

	databasePath := *statePath
	if databasePath == "" {
		databasePath = loaded.Database
	}
	state, err := openState(databasePath)
	if err != nil {
		return err
	}
	defer state.Close()
	hostname := reloaded.Machine.Access.Host
	if reloaded.Machine.Access.Type == "local" {
		hostname, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("read hostname: %w", err)
		}
	}
	machine := store.MachineInfo{ID: loaded.Machine.ID, Hostname: hostname}
	beforeSnapshot, err := state.RecordAudit(ctx, machine, beforeInventory)
	if err != nil {
		return err
	}
	recordedPlan := scopedPlan(beforePlan, executableActions, selectedManager)
	commandName := "apply"
	if selectedManager != "" {
		commandName += " --manager " + string(selectedManager)
	}
	record, err := state.RecordPlan(
		ctx, loaded.Machine.ID, beforeSnapshot.ID, loaded.Digest,
		commandName, false, recordedPlan,
	)
	if err != nil {
		return err
	}
	response.RunID = record.RunID
	response.PlanID = record.PlanID
	if err := state.BeginApply(ctx, record.RunID, record.PlanID); err != nil {
		return err
	}

	journal := stateActionJournal{state: state, planID: record.PlanID}
	var (
		report              executor.Report
		verificationActions []model.Action
		applyErr            error
	)
	if selectedManager == model.ManagerMAS {
		var executionBlocked []blockedAction
		report, verificationActions, executionBlocked, applyErr =
			applyMASInstallations(
				ctx,
				actionRunner,
				journal,
				masInstallations,
			)
		response.BlockedActions = append(
			response.BlockedActions,
			executionBlocked...,
		)
		response.Ready = len(response.BlockedActions) == 0
	} else {
		report, applyErr = executor.New(
			actionRunner,
			journal,
		).Apply(ctx, executableActions)
		verificationActions = executableActions
	}
	response.Execution = report

	var completionErrors []error
	afterInventory, afterCollectErr := collectMachineInventory(ctx, reloaded)
	var collectorErr, verificationErr, snapshotErr error
	if afterCollectErr != nil {
		completionErrors = append(completionErrors,
			fmt.Errorf("collect verification inventory: %w", afterCollectErr))
	} else {
		afterPlan := planInventory(
			reloaded.Desired.Packages, reloaded.Desired.Links, afterInventory,
		)
		response.After = &afterPlan
		collectorErr = requireCollector(afterInventory, requiredCollector)
		verified, err := verifyActions(verificationActions, afterPlan)
		verificationErr = err
		response.Verified = verified && collectorErr == nil && applyErr == nil

		afterSnapshot, err := state.RecordAudit(ctx, machine, afterInventory)
		snapshotErr = err
		if snapshotErr != nil {
			completionErrors = append(completionErrors,
				fmt.Errorf("record verification audit: %w", snapshotErr))
		} else {
			response.VerificationSnapshotID = afterSnapshot.ID
		}
	}
	status := store.ActionStatusCompleted
	if !response.Verified || afterCollectErr != nil || snapshotErr != nil {
		status = store.ActionStatusFailed
	}
	if err := state.CompleteApply(
		ctx, record.RunID, record.PlanID,
		response.VerificationSnapshotID, status,
	); err != nil {
		completionErrors = append(completionErrors, err)
	}
	if err := writeApplyResponse(
		stdout, stderr, response, *setupProgress,
	); err != nil {
		completionErrors = append(completionErrors, err)
	}
	if applyErr != nil {
		completionErrors = append(completionErrors, applyErr)
	}
	if collectorErr != nil {
		completionErrors = append(completionErrors,
			fmt.Errorf("post-apply %s audit: %w",
				requiredCollector, collectorErr))
	}
	if verificationErr != nil {
		completionErrors = append(completionErrors, verificationErr)
	}
	return errors.Join(completionErrors...)
}

type masOutputAdapter struct {
	runner executor.Runner
}

func (a masOutputAdapter) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	stdout, _, err := a.runner.Run(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	return []byte(stdout), nil
}

func applyMASInstallations(
	ctx context.Context,
	runner executor.Runner,
	journal executor.Journal,
	installations []mas.Installation,
) (
	executor.Report,
	[]model.Action,
	[]blockedAction,
	error,
) {
	report := executor.Report{
		Commands: make([]executor.Command, 0, len(installations)),
		Results:  make([]executor.Result, 0, len(installations)),
	}
	completed := make([]model.Action, 0, len(installations))
	var blocked []blockedAction
	var failures []error

	for _, installation := range installations {
		command := installation.Command
		report.Commands = append(report.Commands, command)
		if err := journal.StartAction(ctx, command.Sequence); err != nil {
			return report, completed, blocked, fmt.Errorf(
				"journal action %d start: %w",
				command.Sequence,
				err,
			)
		}

		stdout, stderr, runErr := runner.Run(
			ctx,
			command.Name,
			command.Args...,
		)
		result := executor.Result{
			Command: command,
			Stdout:  truncateApplyOutput(stdout, 4000),
			Stderr:  truncateApplyOutput(stderr, 4000),
		}
		if runErr == nil {
			result.Status = executor.StatusCompleted
			report.Results = append(report.Results, result)
			completed = append(completed, installation.Action)
			if err := journal.FinishAction(
				ctx,
				command.Sequence,
				executor.StatusCompleted,
				"",
			); err != nil {
				return report, completed, blocked, fmt.Errorf(
					"journal action %d completion: %w",
					command.Sequence,
					err,
				)
			}
			continue
		}

		reason := applyCommandError(runErr, stderr)
		status := executor.StatusFailed
		if installation.OwnedOnly {
			status = executor.StatusSkipped
			reason = "not installed as an already-owned app; purchase it " +
				"in the App Store or verify the signed-in Apple Account: " +
				reason
		} else {
			failures = append(failures, fmt.Errorf(
				"install Mac App Store app %s: %s",
				installation.Action.PackageID,
				reason,
			))
		}
		result.Status = status
		result.ErrorSummary = reason
		report.Results = append(report.Results, result)
		blocked = append(blocked, blockedAction{
			Action: installation.Action,
			Reason: reason,
		})
		if err := journal.FinishAction(
			ctx,
			command.Sequence,
			status,
			reason,
		); err != nil {
			return report, completed, blocked, fmt.Errorf(
				"journal action %d %s: %w",
				command.Sequence,
				status,
				err,
			)
		}
	}
	return report, completed, blocked, errors.Join(failures...)
}

func applyCommandError(runErr error, stderr string) string {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = runErr.Error()
	}
	return truncateApplyOutput(detail, 1000)
}

func truncateApplyOutput(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func parseApplyManager(value string) (model.Manager, error) {
	switch model.Manager(value) {
	case "":
		return "", nil
	case model.ManagerBrew, model.ManagerMise,
		model.ManagerBun, model.ManagerCustom, model.ManagerMAS:
		return model.Manager(value), nil
	default:
		return "", fmt.Errorf(
			"unsupported --manager %q; expected brew, mise, bun, custom, or mas", value,
		)
	}
}

func writeApplyResponse(
	stdout, stderr io.Writer,
	response applyResponse,
	setupProgress bool,
) error {
	if !setupProgress {
		return encodeJSON(stdout, response)
	}
	return writeSetupApplySummary(stderr, response)
}

func writeSetupApplySummary(
	writer io.Writer,
	response applyResponse,
) error {
	var summary strings.Builder
	label := applyManagerLabel(response.Manager)
	if response.Manager == model.ManagerMAS {
		completed := 0
		ownedOnly := 0
		for _, result := range response.Execution.Results {
			if result.Status == executor.StatusCompleted {
				completed++
			}
		}
		if response.MASPreflight != nil {
			for _, app := range response.MASPreflight.Apps {
				if app.RequiresOwnership && len(app.Blockers) == 0 {
					ownedOnly++
				}
			}
		}
		switch {
		case response.Mode == "dry-run":
			fmt.Fprintf(
				&summary,
				"\n==> %s: %d install(s) ready",
				label,
				len(response.Execution.Commands),
			)
			if ownedOnly > 0 {
				fmt.Fprintf(
					&summary,
					"; %d paid app(s) require existing ownership",
					ownedOnly,
				)
			}
			summary.WriteString("\n")
		case completed > 0:
			fmt.Fprintf(
				&summary,
				"\n==> %s: %d installed",
				label,
				completed,
			)
			if len(response.BlockedActions) > 0 {
				fmt.Fprintf(
					&summary,
					"; %d need manual action",
					len(response.BlockedActions),
				)
			}
			summary.WriteString("\n")
		case len(response.BlockedActions) == 0:
			fmt.Fprintf(&summary, "\n==> %s: already satisfied\n", label)
		default:
			fmt.Fprintf(
				&summary,
				"\n==> %s: %d app(s) need review\n",
				label,
				len(response.BlockedActions),
			)
		}
		for _, item := range response.BlockedActions {
			fmt.Fprintf(
				&summary,
				"    - %s: %s\n",
				masAppName(response.MASPreflight, item.Action),
				item.Reason,
			)
		}
		_, err := io.WriteString(writer, summary.String())
		return err
	}

	completed := 0
	for _, result := range response.Execution.Results {
		if result.Status == executor.StatusCompleted {
			completed++
		}
	}
	switch {
	case response.Verified:
		fmt.Fprintf(
			&summary,
			"\n==> %s: verified (%d install(s) completed)\n",
			label,
			completed,
		)
	case response.Mode == "dry-run":
		fmt.Fprintf(
			&summary,
			"\n==> %s: %d install(s) ready for review\n",
			label,
			len(response.Execution.Commands),
		)
	default:
		fmt.Fprintf(
			&summary,
			"\n==> %s: finished, but verification did not pass\n",
			label,
		)
	}
	_, err := io.WriteString(writer, summary.String())
	return err
}

func applyManagerLabel(manager model.Manager) string {
	switch manager {
	case model.ManagerBrew:
		return "Homebrew packages"
	case model.ManagerMise:
		return "Mise runtimes"
	case model.ManagerBun:
		return "Bun global tools"
	case model.ManagerCustom:
		return "Custom tools"
	case model.ManagerMAS:
		return "Mac App Store apps"
	default:
		return "Packages"
	}
}

func masAppName(
	preflight *mas.PreflightReport,
	action model.Action,
) string {
	if preflight != nil {
		for _, app := range preflight.Apps {
			if app.PackageID == action.PackageID && app.Name != "" {
				return app.Name
			}
		}
	}
	return action.PackageID
}

func collectorForApply(manager model.Manager) string {
	if manager == model.ManagerBun {
		return "bun"
	}
	if manager == model.ManagerMise {
		return "mise"
	}
	if manager == model.ManagerMAS {
		return "mas"
	}
	if manager == model.ManagerCustom {
		return "custom"
	}
	return "homebrew"
}

func selectActions(
	actions []model.Action,
	manager model.Manager,
) ([]model.Action, []model.Action) {
	if manager == "" {
		return append([]model.Action(nil), actions...), nil
	}
	var selected, deferred []model.Action
	for _, action := range actions {
		if action.Manager == manager {
			selected = append(selected, action)
		} else {
			deferred = append(deferred, action)
		}
	}
	return selected, deferred
}

func scopedPlan(
	plan model.Plan,
	actions []model.Action,
	manager model.Manager,
) model.Plan {
	scoped := plan
	scoped.Actions = append([]model.Action(nil), actions...)
	scoped.Summary.Actions = len(scoped.Actions)
	if manager != "" {
		scoped.Warnings = append(
			append([]string(nil), plan.Warnings...),
			fmt.Sprintf(
				"apply was explicitly scoped to the %s manager; other findings were not actionable in this run",
				manager,
			),
		)
	}
	return scoped
}

func executionRunnerFor(
	loaded envconfig.Loaded,
	progress io.Writer,
) (executor.Runner, error) {
	switch loaded.Machine.Access.Type {
	case "local":
		return executor.ExecRunner{Progress: progress}, nil
	case "ssh":
		runner, err := remoteexec.New(loaded.Machine.Access.Host)
		if err != nil {
			return nil, err
		}
		return runner, nil
	default:
		return nil, fmt.Errorf(
			"unsupported execution access type %q",
			loaded.Machine.Access.Type,
		)
	}
}

func forceLocalMachine(
	ctx context.Context,
	loaded *envconfig.Loaded,
) error {
	identity, err := onboard.Detect(ctx)
	if err != nil {
		return fmt.Errorf("verify local machine identity: %w", err)
	}
	machine, err := onboard.AsLocal(loaded.Machine, identity)
	if err != nil {
		return err
	}
	loaded.Machine = machine
	return nil
}

func collectMachineInventory(
	ctx context.Context,
	loaded envconfig.Loaded,
) (model.Inventory, error) {
	if loaded.Machine.Access.Type == "local" {
		return collectInventory(ctx, loaded.Desired.Links), nil
	}
	executable, err := os.Executable()
	if err != nil {
		return model.Inventory{}, fmt.Errorf("locate envctl executable: %w", err)
	}
	return fleetrefresh.New(
		executable, "", fleetrefresh.ExecRunner{},
	).Collect(ctx, fleetrefresh.Target{
		ID:         loaded.Machine.ID,
		AccessType: loaded.Machine.Access.Type,
		Host:       loaded.Machine.Access.Host,
		Links:      loaded.Desired.Links,
	})
}

func classifyActions(
	actions []model.Action,
) ([]executor.Command, []blockedAction) {
	commandPlanner := executor.New(nil, nil)
	var commands []executor.Command
	var blocked []blockedAction
	for _, action := range actions {
		planned, err := commandPlanner.Plan([]model.Action{action})
		if err != nil {
			blocked = append(blocked, blockedAction{
				Action: action,
				Reason: err.Error(),
			})
			continue
		}
		commands = append(commands, planned[0])
	}
	return commands, blocked
}

type stateActionJournal struct {
	state  *store.Store
	planID string
}

func (j stateActionJournal) StartAction(
	ctx context.Context,
	sequence int,
) error {
	return j.state.StartAction(ctx, j.planID, sequence)
}

func (j stateActionJournal) FinishAction(
	ctx context.Context,
	sequence int,
	status, errorSummary string,
) error {
	return j.state.FinishAction(
		ctx, j.planID, sequence, status, errorSummary,
	)
}

func (j stateActionJournal) SkipAction(
	ctx context.Context,
	sequence int,
	reason string,
) error {
	return j.state.SkipAction(ctx, j.planID, sequence, reason)
}

func planInventory(
	desired []model.PackageSpec,
	desiredLinks []model.LinkSpec,
	inventory model.Inventory,
) model.Plan {
	plan := planner.Build(
		desired, inventory.Packages, collectedManagers(inventory),
	)
	for _, collectorError := range inventory.Errors {
		plan.Warnings = append(
			plan.Warnings, inventoryWarning(collectorError),
		)
	}
	if len(desiredLinks) > 0 {
		summary, findings := planner.BuildLinks(
			desiredLinks,
			inventory.Links,
			hasCollector(inventory, "portable-link"),
		)
		plan.LinkSummary = &summary
		plan.LinkFindings = findings
	}
	return plan
}

func hasCollector(inventory model.Inventory, name string) bool {
	for _, collector := range inventory.Collectors {
		if collector == name {
			return true
		}
	}
	return false
}

func inventoryWarning(issue model.CollectorError) string {
	if strings.HasPrefix(issue.Collector, "custom.") {
		return fmt.Sprintf(
			"%s probe issue: %s", issue.Collector, issue.Message,
		)
	}
	if strings.HasPrefix(issue.Collector, "state-boundary.") {
		return fmt.Sprintf(
			"%s safety issue: %s", issue.Collector, issue.Message,
		)
	}
	return fmt.Sprintf(
		"%s collector failed: %s", issue.Collector, issue.Message,
	)
}

func requireCollector(inventory model.Inventory, collector string) error {
	for _, collected := range inventory.Collectors {
		if collected == collector {
			return nil
		}
	}
	for _, collectorError := range inventory.Errors {
		if collectorError.Collector == collector {
			return errors.New(collectorError.Message)
		}
	}
	return fmt.Errorf("%s collector did not report success", collector)
}

func verifyActions(actions []model.Action, after model.Plan) (bool, error) {
	if len(actions) == 0 {
		return true, nil
	}
	satisfied := make(map[string]bool)
	for _, finding := range after.Findings {
		if finding.Status == model.FindingSatisfied {
			satisfied[finding.PackageID] = true
		}
	}
	var missing []string
	for _, action := range actions {
		if !satisfied[action.PackageID] {
			missing = append(missing, action.PackageID)
		}
	}
	if len(missing) > 0 {
		return false, fmt.Errorf(
			"post-apply verification did not satisfy: %s",
			strings.Join(missing, ", "),
		)
	}
	return true, nil
}

func encodeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func runAudit(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "print the inventory as JSON")
	machineID := flags.String("machine", "", "machine id used in local history")
	statePath := flags.String("state", "", "SQLite state database path")
	noRecord := flags.Bool("no-record", false, "do not write the local audit history")
	encodedLinkSpecs := flags.String(
		"link-specs", "", "base64url-encoded portable link specifications",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*asJSON {
		return errors.New("the initial audit command currently requires --json")
	}

	linkSpecs, err := decodeLinkSpecs(*encodedLinkSpecs)
	if err != nil {
		return err
	}
	inventory := collectInventory(ctx, linkSpecs)
	if !*noRecord {
		resolvedMachineID, hostname, err := resolveMachineIdentity(*machineID)
		if err != nil {
			return err
		}
		state, err := openState(*statePath)
		if err != nil {
			return err
		}
		defer state.Close()
		snapshot, err := state.RecordAudit(ctx, store.MachineInfo{
			ID: resolvedMachineID, Hostname: hostname,
		}, inventory)
		if err != nil {
			return err
		}
		if _, err := state.RecordAuditRun(
			ctx, resolvedMachineID, snapshot.ID, false,
		); err != nil {
			return err
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(inventory)
}

func runImportLegacy(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("import-legacy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "path to the legacy apps-config.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("--input is required")
	}

	draft, err := legacy.LoadFile(*input)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(draft)
}

func runPlan(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	legacyPath := flags.String("legacy", "", "path to the legacy apps-config.json")
	configRoot := flags.String("config", "", "native env-config directory")
	machineIDFlag := flags.String("machine", "", "machine id from the native config")
	statePath := flags.String("state", "", "SQLite state database path")
	inventoryPath := flags.String("inventory", "", "saved inventory JSON instead of a live audit")
	noRecord := flags.Bool("no-record", false, "do not write local audit and plan history")
	asJSON := flags.Bool("json", false, "print the plan as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if (*legacyPath == "") == (*configRoot == "") {
		return errors.New("exactly one of --config or --legacy is required")
	}
	if !*asJSON {
		return errors.New("the initial plan command currently requires --json")
	}

	var (
		desired        []model.PackageSpec
		desiredLinks   []model.LinkSpec
		configDigest   string
		configDatabase string
		machineID      string
		hostname       string
		configWarnings []string
		legacyMode     bool
	)
	if *configRoot != "" {
		if *machineIDFlag == "" {
			return errors.New("--machine is required with --config")
		}
		loaded, err := envconfig.Load(*configRoot, *machineIDFlag)
		if err != nil {
			return err
		}
		desired = loaded.Desired.Packages
		desiredLinks = loaded.Desired.Links
		configDigest = loaded.Digest
		configDatabase = loaded.Database
		machineID = loaded.Machine.ID
		hostname, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("read hostname: %w", err)
		}
	} else {
		draft, err := legacy.LoadFile(*legacyPath)
		if err != nil {
			return err
		}
		desired = draft.Packages
		configWarnings = draft.Warnings
		configDigest, err = digestFile(*legacyPath)
		if err != nil {
			return err
		}
		machineID, hostname, err = resolveMachineIdentity(*machineIDFlag)
		if err != nil {
			return err
		}
		legacyMode = true
	}
	var inventory model.Inventory
	if *inventoryPath == "" {
		inventory = collectInventory(ctx, desiredLinks)
	} else {
		loadedInventory, err := loadInventory(*inventoryPath)
		if err != nil {
			return err
		}
		inventory = loadedInventory
	}
	managers := collectedManagers(inventory)
	var resolutionWarnings []string
	if legacyMode {
		desired, resolutionWarnings = resolveMissingHomebrew(ctx, desired, inventory.Packages)
	}
	plan := planner.Build(desired, inventory.Packages, managers)
	if len(desiredLinks) > 0 {
		linkSummary, linkFindings := planner.BuildLinks(
			desiredLinks,
			inventory.Links,
			hasCollector(inventory, "portable-link"),
		)
		plan.LinkSummary = &linkSummary
		plan.LinkFindings = linkFindings
	}
	plan.Warnings = append(configWarnings, plan.Warnings...)
	plan.Warnings = append(resolutionWarnings, plan.Warnings...)
	for _, collectorError := range inventory.Errors {
		plan.Warnings = append(
			plan.Warnings, inventoryWarning(collectorError),
		)
	}
	if !*noRecord {
		databasePath := *statePath
		if databasePath == "" {
			databasePath = configDatabase
		}
		state, err := openState(databasePath)
		if err != nil {
			return err
		}
		defer state.Close()
		snapshot, err := state.RecordAudit(ctx, store.MachineInfo{
			ID: machineID, Hostname: hostname,
		}, inventory)
		if err != nil {
			return err
		}
		if _, err := state.RecordPlan(
			ctx, machineID, snapshot.ID, configDigest, "plan", false, plan,
		); err != nil {
			return err
		}
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}

type configValidationResponse struct {
	Valid    bool     `json:"valid"`
	Machines []string `json:"machines"`
}

func runConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(
			"usage: envctl config (validate --config DIR --json | " +
				"resolve --config DIR --machine ID --json)",
		)
	}
	if args[0] == "validate" {
		return runConfigValidate(args[1:], stdout, stderr)
	}
	if args[0] != "resolve" {
		return fmt.Errorf("unknown config command %q", args[0])
	}
	flags := flag.NewFlagSet("config resolve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	machineID := flags.String("machine", "", "machine id to resolve")
	asJSON := flags.Bool("json", false, "print the resolved configuration as JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *configRoot == "" || *machineID == "" {
		return errors.New("--config and --machine are required")
	}
	if !*asJSON {
		return errors.New("the initial config resolve command currently requires --json")
	}
	loaded, err := envconfig.Load(*configRoot, *machineID)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(loaded)
}

func runConfigValidate(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("config validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	asJSON := flags.Bool("json", false, "print the validation result as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configRoot == "" {
		return errors.New("--config is required")
	}
	if !*asJSON {
		return errors.New("config validate currently requires --json")
	}
	machineIDs, err := envconfig.MachineIDs(*configRoot)
	if err != nil {
		return err
	}
	if len(machineIDs) == 0 {
		return errors.New("native configuration contains no machines")
	}
	for _, machineID := range machineIDs {
		if _, err := envconfig.Load(*configRoot, machineID); err != nil {
			return fmt.Errorf("validate machine %q: %w", machineID, err)
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(configValidationResponse{
		Valid: true, Machines: machineIDs,
	})
}

func runHistory(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("history", flag.ContinueOnError)
	flags.SetOutput(stderr)
	statePath := flags.String("state", "", "SQLite state database path")
	limit := flags.Int("limit", 20, "maximum number of runs")
	asJSON := flags.Bool("json", false, "print run history as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*asJSON {
		return errors.New("the initial history command currently requires --json")
	}
	state, err := openState(*statePath)
	if err != nil {
		return err
	}
	defer state.Close()
	history, err := state.History(ctx, *limit)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(history)
}

func runFleetTUI(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("tui", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	inventoryDirectory := flags.String(
		"inventory-dir", "", "directory containing MACHINE.json audit files",
	)
	statePath := flags.String("state", "", "SQLite state database path")
	refreshLocal := flags.Bool(
		"refresh", true, "refresh this Mac's inventory before opening",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	resolvedConfigRoot, err := resolveConfigRoot(*configRoot)
	if err != nil {
		return err
	}
	resolvedInventoryDirectory, err := resolveInventoryDirectory(*inventoryDirectory)
	if err != nil {
		return err
	}
	localMachineID, err := detectConfiguredLocalMachine(ctx, resolvedConfigRoot)
	if err != nil {
		return err
	}
	machineIDs, err := envconfig.MachineIDs(resolvedConfigRoot)
	if err != nil {
		return err
	}
	if len(machineIDs) == 0 {
		return errors.New("native configuration contains no machines")
	}

	var (
		machines       []fleetui.Machine
		configDatabase string
	)
	if *refreshLocal {
		loaded, err := envconfig.Load(resolvedConfigRoot, localMachineID)
		if err != nil {
			return err
		}
		inventory := collectInventory(ctx, loaded.Desired.Links)
		if err := writeInventory(
			filepath.Join(resolvedInventoryDirectory, localMachineID+".json"),
			inventory,
		); err != nil {
			return err
		}
	}
	refreshStatus, refreshStatusErr := fleetrefresh.LoadStatus(resolvedInventoryDirectory)
	refreshResults := make(map[string]fleetrefresh.Result)
	if refreshStatusErr != nil && !errors.Is(refreshStatusErr, os.ErrNotExist) {
		return fmt.Errorf("load fleet refresh status: %w", refreshStatusErr)
	}
	if refreshStatusErr == nil {
		for _, result := range refreshStatus.Results {
			refreshResults[result.MachineID] = result
		}
	}
	for _, machineID := range machineIDs {
		loaded, err := envconfig.Load(resolvedConfigRoot, machineID)
		if err != nil {
			return err
		}
		if configDatabase == "" {
			configDatabase = loaded.Database
		}
		inventory, err := loadInventory(
			filepath.Join(resolvedInventoryDirectory, machineID+".json"),
		)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("load %s inventory: %w", machineID, err)
			}
			inventory = model.Inventory{}
		}
		plan := planInventory(
			loaded.Desired.Packages, loaded.Desired.Links, inventory,
		)
		refreshResult := refreshResults[machineID]
		if machineID == localMachineID && *refreshLocal {
			refreshResult.Status = "ok"
		}
		machines = append(machines, fleetui.Machine{
			ID: machineID, Profiles: loaded.Desired.Profiles, Plan: plan,
			CollectedAt:      inventory.CollectedAt,
			RefreshStatus:    refreshResult.Status,
			RefreshError:     refreshResult.Error,
			RetainedLastGood: refreshResult.RetainedLastGood,
		})
	}

	databasePath := *statePath
	if databasePath == "" {
		databasePath = configDatabase
	}
	state, err := openState(databasePath)
	if err != nil {
		return err
	}
	defer state.Close()
	for _, machine := range machines {
		if err := state.EnsureMachine(ctx, store.MachineInfo{
			ID: machine.ID, Hostname: machine.ID,
		}); err != nil {
			return err
		}
	}
	storedDecisions, err := state.LatestDecisions(ctx, "")
	if err != nil {
		return err
	}
	decisions := make([]fleetui.Decision, 0, len(storedDecisions))
	for _, decision := range storedDecisions {
		decisions = append(decisions, fleetui.Decision{
			MachineID: decision.MachineID, InventoryKey: decision.InventoryKey,
			Value: decision.Value,
		})
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate envctl executable: %w", err)
	}
	localLoaded, err := envconfig.Load(resolvedConfigRoot, localMachineID)
	if err != nil {
		return err
	}
	profileName := preferredReconcileProfile(localLoaded.Desired.Profiles)
	model := fleetui.New(
		machines,
		decisions,
		stateDecisionWriter{ctx: ctx, state: state},
	).WithReconciler(fleetTUIReconciler{
		ctx: ctx, executable: executable,
		configRoot:         resolvedConfigRoot,
		inventoryDirectory: resolvedInventoryDirectory,
		statePath:          databasePath,
		profileName:        profileName,
		localMachineID:     localMachineID,
		state:              state,
	}).SelectMachine(localMachineID)
	return fleetui.Run(model)
}

type fleetTUIReconciler struct {
	ctx                context.Context
	executable         string
	configRoot         string
	inventoryDirectory string
	statePath          string
	profileName        string
	localMachineID     string
	state              *store.Store
}

func (r fleetTUIReconciler) Preview(
	machineID string,
) (fleetreconcile.Plan, error) {
	if machineID != r.localMachineID {
		return fleetreconcile.Plan{}, fmt.Errorf(
			"reconciliation is local-only; select %s", r.localMachineID,
		)
	}
	loaded, err := envconfig.Load(r.configRoot, machineID)
	if err != nil {
		return fleetreconcile.Plan{}, err
	}
	profile, err := fleetreconcile.ProfileByName(loaded.Profiles, r.profileName)
	if err != nil {
		return fleetreconcile.Plan{}, err
	}
	inventory, err := loadInventory(filepath.Join(
		r.inventoryDirectory, machineID+".json",
	))
	if err != nil {
		return fleetreconcile.Plan{}, err
	}
	decisions, err := r.state.LatestDecisions(r.ctx, machineID)
	if err != nil {
		return fleetreconcile.Plan{}, err
	}
	return fleetreconcile.Build(
		machineID, r.profileName, decisions, inventory,
		loaded.Catalog.Packages, profile,
	), nil
}

func (r fleetTUIReconciler) Command(machineID string) (*exec.Cmd, error) {
	if machineID != r.localMachineID {
		return nil, fmt.Errorf(
			"reconciliation is local-only; select %s", r.localMachineID,
		)
	}
	if r.executable == "" {
		return nil, errors.New("envctl executable is unavailable")
	}
	arguments := []string{
		"fleet", "reconcile",
		"--config", r.configRoot,
		"--inventory-dir", r.inventoryDirectory,
		"--machine", machineID,
		"--profile", r.profileName,
		"--local", "--yes", "--json",
	}
	if r.statePath != "" {
		arguments = append(arguments, "--state", r.statePath)
	}
	command := exec.CommandContext(r.ctx, r.executable, arguments...)
	command.Stdout = io.Discard
	return command, nil
}

func preferredReconcileProfile(profiles []string) string {
	for _, profile := range profiles {
		if profile == "shared" {
			return profile
		}
	}
	if len(profiles) > 0 {
		return profiles[len(profiles)-1]
	}
	return "shared"
}

func runFleet(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	if len(args) == 0 {
		return errors.New(
			"usage: envctl fleet (refresh | export-decisions | reconcile)",
		)
	}
	switch args[0] {
	case "refresh":
		return runFleetRefresh(ctx, args[1:], stdout, stderr)
	case "export-decisions":
		return runFleetExportDecisions(ctx, args[1:], stdout, stderr)
	case "reconcile":
		return runFleetReconcile(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown fleet command %q", args[0])
	}
}

type fleetReconcileResult struct {
	Sequence     int                       `json:"sequence"`
	InventoryKey string                    `json:"inventory_key"`
	Decision     string                    `json:"decision"`
	Status       string                    `json:"status"`
	Config       *envconfig.AdoptionResult `json:"config,omitempty"`
	Stdout       string                    `json:"stdout,omitempty"`
	Stderr       string                    `json:"stderr,omitempty"`
	Error        string                    `json:"error,omitempty"`
}

type fleetReconcileResponse struct {
	Mode                   string                 `json:"mode"`
	MachineID              string                 `json:"machine_id"`
	Profile                string                 `json:"profile"`
	Plan                   fleetreconcile.Plan    `json:"plan"`
	Results                []fleetReconcileResult `json:"results,omitempty"`
	RunID                  string                 `json:"run_id,omitempty"`
	PlanID                 string                 `json:"plan_id,omitempty"`
	VerificationSnapshotID string                 `json:"verification_snapshot_id,omitempty"`
	Verified               bool                   `json:"verified"`
}

func runFleetReconcile(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("fleet reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	inventoryDirectory := flags.String(
		"inventory-dir", "", "directory containing MACHINE.json audit files",
	)
	machineID := flags.String("machine", "", "machine whose reviewed extras to reconcile")
	profileName := flags.String(
		"profile", "shared", "profile that adopted packages should join",
	)
	statePath := flags.String("state", "", "SQLite state database path")
	localMachine := flags.Bool(
		"local", true, "verify this Mac's identity before local removals",
	)
	dryRun := flags.Bool(
		"dry-run", false, "print reviewed config edits and uninstall commands",
	)
	yes := flags.Bool(
		"yes", false, "apply the reviewed config edits and local removals",
	)
	asJSON := flags.Bool("json", true, "print reconciliation as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resolvedConfigRoot, err := resolveConfigRoot(*configRoot)
	if err != nil {
		return err
	}
	resolvedInventoryDirectory, err := resolveInventoryDirectory(*inventoryDirectory)
	if err != nil {
		return err
	}
	if *machineID == "" {
		*machineID, err = detectConfiguredLocalMachine(ctx, resolvedConfigRoot)
		if err != nil {
			return err
		}
	}
	if !*localMachine {
		return errors.New("fleet reconciliation currently requires --local")
	}
	if !*asJSON {
		return errors.New("fleet reconciliation currently requires --json")
	}
	if *dryRun == *yes {
		return errors.New("exactly one of --dry-run or --yes is required")
	}
	loaded, err := envconfig.Load(resolvedConfigRoot, *machineID)
	if err != nil {
		return err
	}
	if err := forceLocalMachine(ctx, &loaded); err != nil {
		return err
	}
	profile, err := fleetreconcile.ProfileByName(loaded.Profiles, *profileName)
	if err != nil {
		return err
	}
	inventory, err := loadInventory(filepath.Join(
		resolvedInventoryDirectory, *machineID+".json",
	))
	if err != nil {
		return fmt.Errorf("load %s inventory: %w", *machineID, err)
	}
	databasePath := *statePath
	if databasePath == "" {
		databasePath = loaded.Database
	}
	state, err := openState(databasePath)
	if err != nil {
		return err
	}
	defer state.Close()
	decisions, err := state.LatestDecisions(ctx, *machineID)
	if err != nil {
		return err
	}
	reconciliation := fleetreconcile.Build(
		*machineID, *profileName, decisions, inventory,
		loaded.Catalog.Packages, profile,
	)
	mode := "dry-run"
	if *yes {
		mode = "apply"
	}
	response := fleetReconcileResponse{
		Mode: mode, MachineID: *machineID, Profile: *profileName,
		Plan: reconciliation,
	}
	if *dryRun {
		return encodeJSON(stdout, response)
	}
	if !reconciliation.Ready {
		return fmt.Errorf(
			"refuse fleet reconciliation with %d blocker(s)",
			len(reconciliation.Blockers),
		)
	}
	planned := plannedReconcileActions(reconciliation.Actions)
	if len(planned) == 0 {
		response.Verified = true
		return encodeJSON(stdout, response)
	}
	for _, action := range planned {
		if action.Decision != "remove" {
			continue
		}
		present, err := packagePresent(ctx, action.Installed)
		if err != nil {
			return fmt.Errorf(
				"preflight removal %s: %w", action.InventoryKey, err,
			)
		}
		if !present {
			return fmt.Errorf(
				"preflight removal %s: package is absent from live inventory; refresh and review again",
				action.InventoryKey,
			)
		}
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read hostname: %w", err)
	}
	beforeSnapshot, err := state.RecordAudit(ctx, store.MachineInfo{
		ID: *machineID, Hostname: hostname,
	}, inventory)
	if err != nil {
		return err
	}
	journalPlan := reconciliationJournalPlan(planned)
	record, err := state.RecordPlan(
		ctx, *machineID, beforeSnapshot.ID, loaded.Digest,
		"fleet reconcile", true, journalPlan,
	)
	if err != nil {
		return err
	}
	response.RunID = record.RunID
	response.PlanID = record.PlanID
	if err := state.BeginApply(ctx, record.RunID, record.PlanID); err != nil {
		return err
	}

	runner := executor.ExecRunner{Progress: stderr}
	for index, action := range planned {
		if err := state.StartAction(ctx, record.PlanID, action.Sequence); err != nil {
			return err
		}
		result := fleetReconcileResult{
			Sequence: action.Sequence, InventoryKey: action.InventoryKey,
			Decision: action.Decision,
		}
		var actionErr error
		switch action.Decision {
		case "adopt":
			configResult, err := envconfig.AdoptPackages(
				resolvedConfigRoot, *profileName,
				[]envconfig.PackageAdoption{{
					ID: action.PackageID, Spec: *action.Spec,
				}},
			)
			if err != nil {
				actionErr = err
			} else {
				result.Config = &configResult
			}
		case "remove":
			fmt.Fprintf(
				stderr,
				"\n==> Uninstalling %s from %s (%s)\n",
				action.Installed.Package, *machineID, action.Installed.Manager,
			)
			result.Stdout, result.Stderr, actionErr = runner.Run(
				ctx, action.Command.Name, action.Command.Args...,
			)
			if actionErr == nil {
				actionErr = verifyPackageRemoved(ctx, action.Installed)
			}
		default:
			actionErr = fmt.Errorf("unsupported decision %q", action.Decision)
		}
		if actionErr != nil {
			result.Status = executor.StatusFailed
			result.Error = actionErr.Error()
			response.Results = append(response.Results, result)
			_ = state.FinishAction(
				ctx, record.PlanID, action.Sequence,
				store.ActionStatusFailed, actionErr.Error(),
			)
			for _, remaining := range planned[index+1:] {
				_ = state.SkipAction(
					ctx, record.PlanID, remaining.Sequence,
					"not attempted because an earlier reconciliation action failed",
				)
			}
			_ = state.CompleteApply(
				ctx, record.RunID, record.PlanID, "", store.ActionStatusFailed,
			)
			return fmt.Errorf(
				"reconcile action %d (%s): %w",
				action.Sequence, action.InventoryKey, actionErr,
			)
		}
		result.Status = executor.StatusCompleted
		response.Results = append(response.Results, result)
		if err := state.FinishAction(
			ctx, record.PlanID, action.Sequence,
			store.ActionStatusCompleted, "",
		); err != nil {
			return err
		}
		if _, err := state.RecordDecision(
			ctx, *machineID, action.InventoryKey, "clear", *profileName,
			"fleet reconciliation completed",
		); err != nil {
			return err
		}
	}

	reloaded, err := envconfig.Load(resolvedConfigRoot, *machineID)
	if err != nil {
		return fmt.Errorf("reload reconciled configuration: %w", err)
	}
	afterInventory := collectInventory(ctx, reloaded.Desired.Links)
	if err := writeInventory(
		filepath.Join(resolvedInventoryDirectory, *machineID+".json"),
		afterInventory,
	); err != nil {
		return fmt.Errorf("write verified inventory: %w", err)
	}
	verificationSnapshot, err := state.RecordAudit(
		ctx,
		store.MachineInfo{ID: *machineID, Hostname: hostname},
		afterInventory,
	)
	if err != nil {
		return err
	}
	response.VerificationSnapshotID = verificationSnapshot.ID
	response.Verified = true
	if err := state.CompleteApply(
		ctx, record.RunID, record.PlanID,
		verificationSnapshot.ID, store.ActionStatusCompleted,
	); err != nil {
		return err
	}
	return encodeJSON(stdout, response)
}

func plannedReconcileActions(
	actions []fleetreconcile.Action,
) []fleetreconcile.Action {
	var planned []fleetreconcile.Action
	for _, action := range actions {
		if action.Status == fleetreconcile.StatusPlanned {
			planned = append(planned, action)
		}
	}
	return planned
}

func reconciliationJournalPlan(
	actions []fleetreconcile.Action,
) model.Plan {
	plan := model.Plan{
		Summary: model.PlanSummary{Actions: len(actions)},
		Actions: make([]model.Action, 0, len(actions)),
	}
	for _, action := range actions {
		actionType := model.ActionAdopt
		risk := model.RiskLow
		reversible := true
		packageID := action.PackageID
		if action.Decision == "remove" {
			actionType = model.ActionRemove
			risk = model.RiskMedium
			reversible = false
			packageID = action.Installed.Package
		}
		plan.Actions = append(plan.Actions, model.Action{
			Sequence: action.Sequence, Type: actionType,
			PackageID: packageID,
			Manager:   action.Installed.Manager,
			Kind:      action.Installed.Kind,
			Source:    action.Installed.Source,
			Package:   action.Installed.Package,
			Version:   action.Installed.Version,
			Risk:      risk, Reversible: reversible,
			RequiresPrivilege: action.Installed.Manager == model.ManagerMAS,
			Reason:            action.Detail,
		})
	}
	return plan
}

func verifyPackageRemoved(
	ctx context.Context,
	item model.InstalledPackage,
) error {
	present, err := packagePresent(ctx, item)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf(
			"post-removal verification still finds %s",
			fleetreconcile.InventoryKey(item),
		)
	}
	return nil
}

func packagePresent(
	ctx context.Context,
	item model.InstalledPackage,
) (bool, error) {
	packages, err := collectManagerPackages(ctx, item.Manager)
	if err != nil {
		return false, fmt.Errorf(
			"collect %s inventory: %w", item.Manager, err,
		)
	}
	key := fleetreconcile.InventoryKey(item)
	for _, installed := range packages {
		if fleetreconcile.InventoryKey(installed) == key {
			return true, nil
		}
	}
	return false, nil
}

func collectManagerPackages(
	ctx context.Context,
	manager model.Manager,
) ([]model.InstalledPackage, error) {
	var (
		packages []model.InstalledPackage
		err      error
	)
	switch manager {
	case model.ManagerBrew:
		packages, err = homebrew.NewCollector(homebrew.ExecRunner{}).Collect(ctx)
	case model.ManagerMAS:
		packages, err = mas.NewCollector(mas.ExecRunner{}).Collect(ctx)
	case model.ManagerBun:
		var collector bun.Collector
		collector, err = bun.DefaultCollector()
		if err == nil {
			packages, err = collector.Collect(ctx)
		}
	case model.ManagerMise:
		packages, err = mise.NewCollector(mise.ExecRunner{}).Collect(ctx)
	default:
		return nil, fmt.Errorf("unsupported manager %q", manager)
	}
	if err != nil {
		return nil, err
	}
	return packages, nil
}

func runFleetExportDecisions(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("fleet export-decisions", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	statePath := flags.String("state", "", "SQLite state database path")
	outputPath := flags.String(
		"output", "reviews/fleet-decisions.yaml",
		"output path relative to the config root",
	)
	asJSON := flags.Bool("json", true, "print export result as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*asJSON {
		return errors.New("the initial decision export command currently requires --json")
	}
	resolvedConfigRoot, err := resolveConfigRoot(*configRoot)
	if err != nil {
		return err
	}
	machineIDs, err := envconfig.MachineIDs(resolvedConfigRoot)
	if err != nil {
		return err
	}
	if len(machineIDs) == 0 {
		return errors.New("native configuration contains no machines")
	}
	databasePath := *statePath
	if databasePath == "" {
		loaded, err := envconfig.Load(resolvedConfigRoot, machineIDs[0])
		if err != nil {
			return err
		}
		databasePath = loaded.Database
	}
	state, err := openState(databasePath)
	if err != nil {
		return err
	}
	defer state.Close()
	decisions, err := state.LatestDecisions(ctx, "")
	if err != nil {
		return err
	}
	knownMachines := make(map[string]bool)
	for _, machineID := range machineIDs {
		knownMachines[machineID] = true
	}
	result, err := decisionexport.Write(
		resolvedConfigRoot, *outputPath, decisions, knownMachines,
	)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runFleetRefresh(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("fleet refresh", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String("config", "", "native env-config directory")
	inventoryDirectory := flags.String(
		"inventory-dir", "", "directory for per-machine audit files",
	)
	selectedMachines := flags.String(
		"machines", "", "comma-separated machine ids; defaults to all",
	)
	asJSON := flags.Bool("json", true, "print refresh status as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*asJSON {
		return errors.New("the initial fleet refresh command currently requires --json")
	}
	resolvedConfigRoot, err := resolveConfigRoot(*configRoot)
	if err != nil {
		return err
	}
	resolvedInventoryDirectory, err := resolveInventoryDirectory(*inventoryDirectory)
	if err != nil {
		return err
	}
	machineIDs, err := envconfig.MachineIDs(resolvedConfigRoot)
	if err != nil {
		return err
	}
	if *selectedMachines != "" {
		machineIDs, err = selectMachineIDs(machineIDs, *selectedMachines)
		if err != nil {
			return err
		}
	}
	targets := make([]fleetrefresh.Target, 0, len(machineIDs))
	for _, machineID := range machineIDs {
		loaded, err := envconfig.Load(resolvedConfigRoot, machineID)
		if err != nil {
			return err
		}
		targets = append(targets, fleetrefresh.Target{
			ID: machineID, AccessType: loaded.Machine.Access.Type,
			Host: loaded.Machine.Access.Host, Links: loaded.Desired.Links,
		})
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate envctl executable: %w", err)
	}
	status, err := fleetrefresh.New(
		executable,
		resolvedInventoryDirectory,
		fleetrefresh.ExecRunner{},
	).Refresh(ctx, targets)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(status)
}

type stateDecisionWriter struct {
	ctx   context.Context
	state *store.Store
}

func (w stateDecisionWriter) SaveDecision(
	machineID, inventoryKey, value, profile string,
) error {
	_, err := w.state.RecordDecision(
		w.ctx, machineID, inventoryKey, value, profile, "fleet TUI",
	)
	return err
}

func collectInventory(
	ctx context.Context,
	linkSpecs []model.LinkSpec,
) model.Inventory {
	inventory := model.Inventory{CollectedAt: time.Now().UTC()}

	collect := func(
		name string,
		collector func(context.Context) ([]model.InstalledPackage, error),
	) {
		packages, err := collector(ctx)
		if err != nil {
			inventory.Errors = append(inventory.Errors, model.CollectorError{
				Collector: name,
				Message:   err.Error(),
			})
			return
		}
		inventory.Collectors = append(inventory.Collectors, name)
		inventory.Packages = append(inventory.Packages, packages...)
	}

	collect("homebrew", homebrew.NewCollector(homebrew.ExecRunner{}).Collect)
	collect("mas", mas.NewCollector(mas.ExecRunner{}).Collect)
	collect("mise", mise.NewCollector(mise.ExecRunner{}).Collect)
	bunCollector, err := bun.DefaultCollector()
	if err != nil {
		inventory.Errors = append(inventory.Errors, model.CollectorError{
			Collector: "bun",
			Message:   err.Error(),
		})
	} else {
		collect("bun", bunCollector.Collect)
	}
	customResult := customtool.NewCollector(customtool.ExecRunner{}).Collect(ctx)
	inventory.Collectors = append(inventory.Collectors, "custom")
	inventory.Packages = append(inventory.Packages, customResult.Packages...)
	for _, issue := range customResult.Issues {
		inventory.Errors = append(inventory.Errors, model.CollectorError{
			Collector: "custom." + issue.Tool,
			Message:   issue.Message,
		})
	}
	boundaryCollector, err := stateboundary.DefaultCollector()
	if err != nil {
		inventory.Errors = append(inventory.Errors, model.CollectorError{
			Collector: "state-boundary",
			Message:   err.Error(),
		})
	} else {
		inventory.Collectors = append(inventory.Collectors, "state-boundary")
		for _, issue := range boundaryCollector.Collect() {
			inventory.Errors = append(inventory.Errors, model.CollectorError{
				Collector: "state-boundary." + issue.ID,
				Message:   issue.Message,
			})
		}
	}
	if len(linkSpecs) > 0 {
		inventory.Collectors = append(inventory.Collectors, "portable-link")
		inventory.Links = portablelink.Collect(linkSpecs)
	}
	return inventory
}

func decodeLinkSpecs(encoded string) ([]model.LinkSpec, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > 128*1024 {
		return nil, errors.New("portable link specification payload is too large")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode portable link specifications: %w", err)
	}
	var specs []model.LinkSpec
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&specs); err != nil {
		return nil, fmt.Errorf("parse portable link specifications: %w", err)
	}
	if len(specs) > 256 {
		return nil, errors.New("too many portable link specifications")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	for _, spec := range specs {
		decodedDigest, digestErr := hex.DecodeString(spec.Digest)
		if spec.ID == "" ||
			(spec.Kind != model.LinkKindFile &&
				spec.Kind != model.LinkKindDirectory) ||
			!pathWithin(home, spec.Source) || !pathWithin(home, spec.Target) ||
			digestErr != nil || len(decodedDigest) != sha256.Size {
			return nil, fmt.Errorf("unsafe portable link specification %q", spec.ID)
		}
	}
	return specs, nil
}

func pathWithin(root, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func collectedManagers(inventory model.Inventory) []model.Manager {
	var managers []model.Manager
	for _, collector := range inventory.Collectors {
		switch collector {
		case "homebrew":
			managers = append(managers, model.ManagerBrew)
		case "mas":
			managers = append(managers, model.ManagerMAS)
		case "bun":
			managers = append(managers, model.ManagerBun)
		case "mise":
			managers = append(managers, model.ManagerMise)
		case "custom":
			managers = append(managers, model.ManagerCustom)
		}
	}
	return managers
}

func resolveMissingHomebrew(
	ctx context.Context,
	desired []model.PackageSpec,
	installed []model.InstalledPackage,
) ([]model.PackageSpec, []string) {
	resolved := append([]model.PackageSpec(nil), desired...)
	resolver := homebrew.NewResolver(homebrew.ExecRunner{})
	var warnings []string

	for index, wanted := range resolved {
		if wanted.Manager != model.ManagerBrew ||
			(wanted.Kind != "" && wanted.Kind != model.KindUnknown) ||
			hasInstalledPackage(installed, wanted.Manager, wanted.Package) {
			continue
		}
		item, err := resolver.Resolve(ctx, wanted)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		resolved[index] = item
	}
	return resolved, warnings
}

func hasInstalledPackage(
	installed []model.InstalledPackage,
	manager model.Manager,
	name string,
) bool {
	for _, item := range installed {
		if item.Manager == manager && item.Package == name {
			return true
		}
	}
	return false
}

func openState(path string) (*store.Store, error) {
	if path == "" {
		var err error
		path, err = defaultStatePath()
		if err != nil {
			return nil, err
		}
	}
	expanded, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	return store.Open(expanded)
}

func defaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "envctl", "state.db"), nil
}

func expandHome(path string) (string, error) {
	if path == "~" {
		return os.UserHomeDir()
	}
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}

func resolveMachineIdentity(configured string) (string, string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", "", fmt.Errorf("read hostname: %w", err)
	}
	if configured == "" {
		configured = hostname
	}
	return configured, hostname, nil
}

func digestFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read configuration for digest: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func selectMachineIDs(available []string, selection string) ([]string, error) {
	known := make(map[string]bool)
	for _, machineID := range available {
		known[machineID] = true
	}
	var selected []string
	seen := make(map[string]bool)
	for _, value := range strings.Split(selection, ",") {
		machineID := strings.TrimSpace(value)
		if machineID == "" {
			continue
		}
		if !known[machineID] {
			return nil, fmt.Errorf("unknown selected machine %q", machineID)
		}
		if !seen[machineID] {
			selected = append(selected, machineID)
			seen[machineID] = true
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("--machines selected no machines")
	}
	return selected, nil
}

func splitCommaSeparated(value string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			result = append(result, item)
			seen[item] = true
		}
	}
	return result
}

func loadInventory(path string) (model.Inventory, error) {
	file, err := os.Open(path)
	if err != nil {
		return model.Inventory{}, fmt.Errorf("open saved inventory: %w", err)
	}
	defer file.Close()

	var inventory model.Inventory
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return model.Inventory{}, fmt.Errorf("decode saved inventory: %w", err)
	}
	if inventory.CollectedAt.IsZero() {
		return model.Inventory{}, errors.New("saved inventory has no collection time")
	}
	return inventory, nil
}
