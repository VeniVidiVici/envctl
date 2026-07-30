package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
)

const (
	defaultEnvConfigBranch = "main"
	defaultEnvConfigRepo   = "git@github.com:VeniVidiVici/env-config.git"
)

var safeSyncTarget = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type configSyncTarget struct {
	MachineID string `json:"machine_id"`
	Host      string `json:"host,omitempty"`
	Status    string `json:"status"`
	Revision  string `json:"revision,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type configSyncReport struct {
	Mode            string             `json:"mode"`
	ConfigRoot      string             `json:"config_root"`
	LocalMachine    string             `json:"local_machine"`
	Branch          string             `json:"branch"`
	ChangedFiles    []string           `json:"changed_files,omitempty"`
	Committed       bool               `json:"committed"`
	Pushed          bool               `json:"pushed"`
	LocalApplied    bool               `json:"local_applied"`
	Revision        string             `json:"revision,omitempty"`
	RemoteAhead     int                `json:"remote_ahead,omitempty"`
	LocalAhead      int                `json:"local_ahead,omitempty"`
	Targets         []configSyncTarget `json:"targets,omitempty"`
	PendingMachines int                `json:"pending_machines"`
}

type configSyncStatus struct {
	Path      string
	Code      string
	Untracked bool
}

func runConfigSync(
	ctx context.Context,
	args []string,
	input io.Reader,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configRoot := flags.String(
		"config", "", "env-config checkout; normally discovered automatically",
	)
	message := flags.String(
		"message", "", "commit message; defaults to the detected machine name",
	)
	dryRun := flags.Bool("dry-run", false, "inspect without committing, pushing, or applying")
	yes := flags.Bool("yes", false, "publish reviewed tracked changes without prompting")
	asJSON := flags.Bool("json", false, "print a compact machine-readable report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("sync does not accept positional arguments")
	}
	if *dryRun && *yes {
		return errors.New("--dry-run and --yes cannot be used together")
	}
	if strings.ContainsAny(*message, "\r\n") {
		return errors.New("sync commit message must be a single line")
	}

	root, err := resolveConfigRoot(*configRoot)
	if err != nil {
		return err
	}
	if err := requireEnvConfigGitCheckout(root); err != nil {
		return err
	}
	localMachine, err := detectConfiguredLocalMachine(ctx, root)
	if err != nil {
		return err
	}
	if err := validateEnvConfig(root); err != nil {
		return err
	}
	branch, err := trimmedGitOutput(ctx, root, "branch", "--show-current")
	if err != nil {
		return err
	}
	if branch != defaultEnvConfigBranch {
		return fmt.Errorf(
			"env-config is on branch %q, not %q",
			branch,
			defaultEnvConfigBranch,
		)
	}
	origin, err := trimmedGitOutput(ctx, root, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if !trustedEnvConfigOrigin(origin) {
		return fmt.Errorf("refusing to sync untrusted env-config origin %q", origin)
	}

	report := configSyncReport{
		Mode: "sync", ConfigRoot: root, LocalMachine: localMachine,
		Branch: branch,
	}
	if *dryRun {
		report.Mode = "dry-run"
	} else {
		fmt.Fprintf(stdout, "Checking env-config from %s\n", localMachine)
	}
	if err := executeGit(
		ctx, root, io.Discard, stderr,
		"fetch", "--quiet", "origin", defaultEnvConfigBranch,
	); err != nil {
		return fmt.Errorf("fetch env-config: %w", err)
	}
	report.LocalAhead, report.RemoteAhead, err = configSyncAheadBehind(ctx, root)
	if err != nil {
		return err
	}
	if report.LocalAhead > 0 && report.RemoteAhead > 0 {
		return fmt.Errorf(
			"env-config history has diverged (%d local, %d remote); reconcile it before syncing",
			report.LocalAhead,
			report.RemoteAhead,
		)
	}

	statuses, err := configSyncStatuses(ctx, root)
	if err != nil {
		return err
	}
	if untracked := configSyncUntracked(statuses); len(untracked) > 0 {
		return fmt.Errorf(
			"env-config has untracked files; review and git add or remove them first: %s",
			strings.Join(untracked, ", "),
		)
	}
	matchesIncoming := false
	if report.RemoteAhead > 0 {
		if len(statuses) > 0 {
			matchesRemote, matchErr := configSyncWorktreeMatchesRemote(ctx, root)
			if matchErr != nil {
				return matchErr
			}
			if !matchesRemote {
				return errors.New(
					"env-config has both local edits and incoming commits; commit or discard the edits before syncing",
				)
			}
			if *dryRun {
				matchesIncoming = true
			} else if err := fastForwardMatchingConfigWorktree(ctx, root); err != nil {
				return err
			} else {
				report.RemoteAhead = 0
			}
		}
		if !*dryRun && len(statuses) == 0 {
			if err := executeGit(
				ctx, root, stdout, stderr,
				"merge", "--ff-only", "origin/"+defaultEnvConfigBranch,
			); err != nil {
				return fmt.Errorf("fast-forward env-config: %w", err)
			}
			report.RemoteAhead = 0
		}
	}

	statuses, err = configSyncStatuses(ctx, root)
	if err != nil {
		return err
	}
	if matchesIncoming {
		statuses = nil
	}
	report.ChangedFiles = configSyncChangedPaths(statuses)
	if err := configSyncDiffCheck(ctx, root); err != nil {
		return err
	}
	if err := validateEnvConfig(root); err != nil {
		return err
	}

	needsPublish := len(report.ChangedFiles) > 0 || report.LocalAhead > 0
	if len(report.ChangedFiles) > 0 && !*asJSON {
		fmt.Fprintln(stdout, "\nTracked changes ready to publish:")
		for _, path := range report.ChangedFiles {
			fmt.Fprintf(stdout, "  %s\n", path)
		}
		stat, statErr := gitOutput(ctx, root, "diff", "--stat", "HEAD")
		if statErr == nil && strings.TrimSpace(stat) != "" {
			fmt.Fprintf(stdout, "\n%s", stat)
		}
	}
	if *dryRun {
		revision, revisionErr := trimmedGitOutput(ctx, root, "rev-parse", "--short", "HEAD")
		if revisionErr != nil {
			return revisionErr
		}
		report.Revision = revision
		return writeConfigSyncReport(stdout, report, *asJSON)
	}
	if needsPublish && !*yes {
		if *asJSON {
			return errors.New("sync with publishable changes requires --yes when using --json")
		}
		confirmed, confirmErr := confirmConfigSync(input, stdout)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			return errors.New("sync cancelled; no changes were published")
		}
	}

	if len(report.ChangedFiles) > 0 {
		if err := executeGit(ctx, root, stdout, stderr, "add", "-u"); err != nil {
			return err
		}
		commitMessage := strings.TrimSpace(*message)
		if commitMessage == "" {
			commitMessage = "Sync env-config from " + localMachine
		}
		if err := executeGit(
			ctx, root, stdout, stderr, "commit", "-m", commitMessage,
		); err != nil {
			return fmt.Errorf("commit env-config: %w", err)
		}
		report.Committed = true
		report.LocalAhead++
	}
	if report.LocalAhead > 0 {
		if err := executeGit(
			ctx, root, stdout, stderr,
			"push", "origin", defaultEnvConfigBranch,
		); err != nil {
			return fmt.Errorf("push env-config: %w", err)
		}
		report.Pushed = true
	}
	report.Revision, err = trimmedGitOutput(ctx, root, "rev-parse", "--short", "HEAD")
	if err != nil {
		return err
	}

	if err := applyLocalConfigLinks(ctx, root, localMachine, stderr); err != nil {
		return fmt.Errorf("apply portable links locally: %w", err)
	}
	report.LocalApplied = true
	report.Targets, report.PendingMachines, err = syncRemoteConfigTargets(
		ctx, root, localMachine,
	)
	if err != nil {
		return err
	}
	return writeConfigSyncReport(stdout, report, *asJSON)
}

func requireEnvConfigGitCheckout(root string) error {
	info, err := os.Lstat(filepath.Join(root, ".git"))
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return fmt.Errorf("env-config is not a Git checkout: %s", root)
	}
	return nil
}

func validateEnvConfig(root string) error {
	machineIDs, err := envconfig.MachineIDs(root)
	if err != nil {
		return fmt.Errorf("validate env-config: %w", err)
	}
	if len(machineIDs) == 0 {
		return errors.New("validate env-config: no configured machines")
	}
	for _, machineID := range machineIDs {
		if _, err := envconfig.Load(root, machineID); err != nil {
			return fmt.Errorf("validate env-config machine %q: %w", machineID, err)
		}
	}
	return nil
}

func trustedEnvConfigOrigin(origin string) bool {
	switch origin {
	case defaultEnvConfigRepo,
		"https://github.com/VeniVidiVici/env-config.git",
		"ssh://git@github.com/VeniVidiVici/env-config.git":
		return true
	default:
		return false
	}
}

func trimmedGitOutput(
	ctx context.Context,
	root string,
	args ...string,
) (string, error) {
	output, err := gitOutput(ctx, root, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func configSyncAheadBehind(ctx context.Context, root string) (int, int, error) {
	output, err := trimmedGitOutput(
		ctx, root, "rev-list", "--left-right", "--count",
		"HEAD...origin/"+defaultEnvConfigBranch,
	)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(output)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected Git ahead/behind result %q", output)
	}
	local, localErr := strconv.Atoi(fields[0])
	remote, remoteErr := strconv.Atoi(fields[1])
	if localErr != nil || remoteErr != nil {
		return 0, 0, fmt.Errorf("unexpected Git ahead/behind result %q", output)
	}
	return local, remote, nil
}

func configSyncStatuses(ctx context.Context, root string) ([]configSyncStatus, error) {
	output, err := gitOutput(
		ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all",
	)
	if err != nil {
		return nil, err
	}
	return parseConfigSyncStatus(output)
}

func parseConfigSyncStatus(output string) ([]configSyncStatus, error) {
	records := strings.Split(output, "\x00")
	statuses := make([]configSyncStatus, 0, len(records))
	for index := 0; index < len(records); index++ {
		record := records[index]
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("unexpected Git status record %q", record)
		}
		status := configSyncStatus{
			Code: record[:2], Path: record[3:],
			Untracked: record[:2] == "??",
		}
		if record[0] == 'R' || record[0] == 'C' {
			index++
			if index >= len(records) || records[index] == "" {
				return nil, fmt.Errorf("Git status rename has no source for %q", status.Path)
			}
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func configSyncChangedPaths(statuses []configSyncStatus) []string {
	paths := make([]string, 0, len(statuses))
	for _, status := range statuses {
		if !status.Untracked {
			paths = append(paths, status.Path)
		}
	}
	return paths
}

func configSyncUntracked(statuses []configSyncStatus) []string {
	var paths []string
	for _, status := range statuses {
		if status.Untracked {
			paths = append(paths, status.Path)
		}
	}
	return paths
}

func configSyncDiffCheck(ctx context.Context, root string) error {
	for _, args := range [][]string{
		{"diff", "--check"},
		{"diff", "--cached", "--check"},
	} {
		if _, err := gitOutput(ctx, root, args...); err != nil {
			return fmt.Errorf("env-config contains invalid whitespace: %w", err)
		}
	}
	return nil
}

func configSyncWorktreeMatchesRemote(
	ctx context.Context,
	root string,
) (bool, error) {
	ancestor := exec.CommandContext(
		ctx,
		"git", "-C", root,
		"merge-base", "--is-ancestor",
		"HEAD", "origin/"+defaultEnvConfigBranch,
	)
	if err := ancestor.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("check env-config ancestry: %w", err)
	}
	diff := exec.CommandContext(
		ctx,
		"git", "-C", root,
		"diff", "--quiet", "origin/"+defaultEnvConfigBranch, "--",
	)
	if err := diff.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("compare env-config with incoming commit: %w", err)
	}
	return true, nil
}

func fastForwardMatchingConfigWorktree(ctx context.Context, root string) error {
	current, err := trimmedGitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if err := executeGit(
		ctx, root, io.Discard, io.Discard,
		"update-ref", "HEAD", "origin/"+defaultEnvConfigBranch, current,
	); err != nil {
		return fmt.Errorf("advance matching env-config worktree: %w", err)
	}
	if err := executeGit(
		ctx, root, io.Discard, io.Discard, "read-tree", "HEAD",
	); err != nil {
		return fmt.Errorf("refresh matching env-config index: %w", err)
	}
	return nil
}

func confirmConfigSync(input io.Reader, output io.Writer) (bool, error) {
	fmt.Fprint(output, "\nPublish these changes and sync reachable Macs? [y/N] ")
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read sync confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func applyLocalConfigLinks(
	ctx context.Context,
	root, machineID string,
	stderr io.Writer,
) error {
	return runLinks(
		ctx,
		[]string{
			"apply",
			"--config", root,
			"--machine", machineID,
			"--local",
			"--yes",
			"--json",
		},
		io.Discard,
		stderr,
	)
}

func syncRemoteConfigTargets(
	ctx context.Context,
	root, localMachine string,
) ([]configSyncTarget, int, error) {
	machineIDs, err := envconfig.MachineIDs(root)
	if err != nil {
		return nil, 0, err
	}
	var targets []configSyncTarget
	pending := 0
	for _, machineID := range machineIDs {
		if machineID == localMachine {
			continue
		}
		loaded, loadErr := envconfig.Load(root, machineID)
		if loadErr != nil {
			return nil, 0, loadErr
		}
		target := configSyncTarget{
			MachineID: machineID,
			Host:      loaded.Machine.Access.Host,
		}
		if loaded.Machine.Access.Type != "ssh" {
			target.Status = "pending"
			target.Detail = "machine has no SSH sync access"
			pending++
			targets = append(targets, target)
			continue
		}
		revision, syncErr := runRemoteConfigSync(
			ctx,
			loaded.Machine.Access.Host,
			machineID,
		)
		if syncErr != nil {
			target.Status = "pending"
			target.Detail = compactSyncError(syncErr)
			pending++
		} else {
			target.Status = "synced"
			target.Revision = revision
		}
		targets = append(targets, target)
	}
	return targets, pending, nil
}

const remoteConfigSyncScript = `set -eu
machine_id=$1
config_root=$HOME/.local/share/envctl/repos/env-config
envctl_bin=$HOME/.local/bin/envctl
git -C "$config_root" diff --check
git -C "$config_root" fetch --quiet origin main
if test -n "$(git -C "$config_root" status --porcelain)" &&
	git -C "$config_root" merge-base --is-ancestor HEAD origin/main &&
	git -C "$config_root" diff --quiet origin/main -- &&
	! git -C "$config_root" status --porcelain | grep -q '^??'; then
	current_revision=$(git -C "$config_root" rev-parse HEAD)
	git -C "$config_root" update-ref HEAD origin/main "$current_revision"
	git -C "$config_root" read-tree HEAD
else
	git -C "$config_root" merge --ff-only --quiet origin/main
fi
"$envctl_bin" config validate --config "$config_root" --json >/dev/null
"$envctl_bin" links apply --config "$config_root" --machine "$machine_id" --local --yes --json >/dev/null
git -C "$config_root" rev-parse --short HEAD
`

func runRemoteConfigSync(
	ctx context.Context,
	host, machineID string,
) (string, error) {
	if !safeSyncTarget.MatchString(host) || !safeSyncTarget.MatchString(machineID) {
		return "", errors.New("unsafe remote sync target")
	}
	command := exec.CommandContext(
		ctx,
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ConnectTimeout=8",
		host,
		"sh", "-s", "--", machineID,
	)
	command.Stdin = strings.NewReader(remoteConfigSyncScript)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", errors.New(detail)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func compactSyncError(err error) string {
	detail := strings.TrimSpace(err.Error())
	if newline := strings.IndexByte(detail, '\n'); newline >= 0 {
		detail = detail[:newline]
	}
	if len(detail) > 180 {
		detail = detail[:177] + "..."
	}
	return detail
}

func writeConfigSyncReport(
	output io.Writer,
	report configSyncReport,
	asJSON bool,
) error {
	if asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if report.Mode == "dry-run" {
		fmt.Fprintln(output, "\nDry run complete; nothing was changed.")
		if report.RemoteAhead > 0 {
			fmt.Fprintf(output, "  %d incoming commit(s) would be applied.\n", report.RemoteAhead)
		}
		if len(report.ChangedFiles) > 0 {
			fmt.Fprintf(output, "  %d tracked file(s) would be published.\n", len(report.ChangedFiles))
		}
		return nil
	}
	fmt.Fprintf(output, "\nEnv config synced at %s.\n", report.Revision)
	if report.Committed {
		fmt.Fprintf(output, "  Published %d tracked file(s).\n", len(report.ChangedFiles))
	} else if report.Pushed {
		fmt.Fprintln(output, "  Published existing local commit(s).")
	} else {
		fmt.Fprintln(output, "  No local config changes needed publishing.")
	}
	fmt.Fprintln(output, "  This Mac: portable links applied.")
	for _, target := range report.Targets {
		if target.Status == "synced" {
			fmt.Fprintf(output, "  %-14s synced (%s)\n", target.MachineID, target.Revision)
		} else {
			fmt.Fprintf(output, "  %-14s pending: %s\n", target.MachineID, target.Detail)
		}
	}
	if report.PendingMachines > 0 {
		fmt.Fprintf(
			output,
			"\n%d offline or locally changed Mac(s) can catch up with `envctl sync` later.\n",
			report.PendingMachines,
		)
	}
	return nil
}
