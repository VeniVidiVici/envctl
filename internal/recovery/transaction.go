package recovery

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type ActionType string

const (
	ActionRestore    ActionType = "recovery.restore"
	ActionRepairMode ActionType = "recovery.repair-mode"
)

type RecoveryAction struct {
	Sequence   int                         `json:"sequence"`
	RecoveryID string                      `json:"recovery_id"`
	Kind       model.RecoveryKind          `json:"kind"`
	Type       ActionType                  `json:"type"`
	Target     string                      `json:"target"`
	Expected   model.RecoveryFindingStatus `json:"expected_status"`
	BackupPath string                      `json:"backup_path,omitempty"`
}

type RecoveryBlocker struct {
	RecoveryID string                      `json:"recovery_id"`
	Status     model.RecoveryFindingStatus `json:"status"`
	Detail     string                      `json:"detail"`
}

type TransactionPlan struct {
	RunID     string            `json:"run_id"`
	Ready     bool              `json:"ready"`
	Satisfied int               `json:"satisfied"`
	Actions   []RecoveryAction  `json:"actions,omitempty"`
	Blockers  []RecoveryBlocker `json:"blockers,omitempty"`
}

type AppliedRecovery struct {
	RecoveryID string `json:"recovery_id"`
	Target     string `json:"target"`
	BackupPath string `json:"backup_path,omitempty"`
}

type TransactionResult struct {
	Plan       TransactionPlan   `json:"plan"`
	Applied    []AppliedRecovery `json:"applied,omitempty"`
	Verified   bool              `json:"verified"`
	RolledBack bool              `json:"rolled_back"`
}

type Journal interface {
	StartRecovery(context.Context, RecoveryAction) error
	RecordRecoveryBackup(
		context.Context,
		RecoveryAction,
		string,
		string,
	) error
	CompleteRecovery(context.Context, RecoveryAction) error
	FailRecovery(context.Context, RecoveryAction, string) error
	RollBackRecovery(context.Context, RecoveryAction, string) error
}

type Transaction struct {
	home          string
	backupRoot    string
	stagingRoot   string
	planner       *Planner
	now           func() time.Time
	beforeInstall func(int)
}

func NewTransaction(home, backupRoot, stagingRoot string) (*Transaction, error) {
	planner, err := NewPlanner(home)
	if err != nil {
		return nil, err
	}
	absoluteBackup, err := filepath.Abs(backupRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve recovery backup directory: %w", err)
	}
	absoluteStaging, err := filepath.Abs(stagingRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve recovery staging directory: %w", err)
	}
	for label, path := range map[string]string{
		"backup":  absoluteBackup,
		"staging": absoluteStaging,
	} {
		if !recoveryPathWithin(planner.home, path) || path == planner.home {
			return nil, fmt.Errorf(
				"recovery %s directory must be inside the home directory",
				label,
			)
		}
	}
	if recoveryPathWithin(absoluteBackup, absoluteStaging) ||
		recoveryPathWithin(absoluteStaging, absoluteBackup) {
		return nil, errors.New("recovery backup and staging directories must not overlap")
	}
	return &Transaction{
		home: planner.home, backupRoot: absoluteBackup,
		stagingRoot: absoluteStaging, planner: planner, now: time.Now,
	}, nil
}

func (t *Transaction) Plan(
	ctx context.Context,
	specs []model.RecoverySpec,
) (TransactionPlan, model.RecoveryPlan, error) {
	if err := t.validateSpecs(specs); err != nil {
		return TransactionPlan{}, model.RecoveryPlan{}, err
	}
	runID, err := t.newRunID()
	if err != nil {
		return TransactionPlan{}, model.RecoveryPlan{}, err
	}
	statusPlan := t.planner.Plan(ctx, specs)
	byID := make(map[string]model.RecoverySpec, len(specs))
	for _, spec := range specs {
		byID[spec.ID] = spec
	}
	plan := TransactionPlan{RunID: runID}
	for _, finding := range statusPlan.Findings {
		spec := byID[finding.RecoveryID]
		switch finding.Status {
		case model.RecoveryFindingSatisfied:
			plan.Satisfied++
		case model.RecoveryFindingMissing, model.RecoveryFindingDrifted:
			actionType := ActionRestore
			if spec.Kind == model.RecoveryKindGPGKeyring {
				info, err := os.Lstat(spec.Target)
				if errors.Is(err, os.ErrNotExist) {
					if finding.Status != model.RecoveryFindingMissing {
						plan.Blockers = append(plan.Blockers, RecoveryBlocker{
							RecoveryID: spec.ID, Status: finding.Status,
							Detail: "GPG keyring state changed while planning",
						})
						continue
					}
				} else if err != nil {
					plan.Blockers = append(plan.Blockers, RecoveryBlocker{
						RecoveryID: spec.ID, Status: model.RecoveryFindingBlocked,
						Detail: "GPG keyring could not be inspected",
					})
					continue
				} else if finding.Status == model.RecoveryFindingMissing {
					plan.Blockers = append(plan.Blockers, RecoveryBlocker{
						RecoveryID: spec.ID, Status: model.RecoveryFindingBlocked,
						Detail: "existing GPG keyring is missing the expected key and requires review",
					})
					continue
				} else {
					if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
						plan.Blockers = append(plan.Blockers, RecoveryBlocker{
							RecoveryID: spec.ID, Status: model.RecoveryFindingBlocked,
							Detail: "existing GPG keyring is not a real directory",
						})
						continue
					}
					actionType = ActionRepairMode
				}
			}
			action := RecoveryAction{
				Sequence: len(plan.Actions) + 1, RecoveryID: spec.ID,
				Kind: spec.Kind, Type: actionType, Target: spec.Target,
				Expected: finding.Status,
			}
			relative, err := filepath.Rel(t.home, spec.Target)
			if err != nil {
				return TransactionPlan{}, statusPlan, err
			}
			action.BackupPath = filepath.Join(
				t.backupRoot,
				runID,
				relative,
			)
			plan.Actions = append(plan.Actions, action)
		default:
			plan.Blockers = append(plan.Blockers, RecoveryBlocker{
				RecoveryID: finding.RecoveryID,
				Status:     finding.Status,
				Detail:     finding.Detail,
			})
		}
	}
	plan.Ready = len(plan.Blockers) == 0
	return plan, statusPlan, nil
}

func (t *Transaction) validateSpecs(specs []model.RecoverySpec) error {
	identifiers := make(map[string]bool, len(specs))
	targets := make(map[string]string, len(specs))
	for _, spec := range specs {
		if spec.ID == "" {
			return errors.New("recovery id is required")
		}
		if identifiers[spec.ID] {
			return fmt.Errorf("duplicate recovery id %q", spec.ID)
		}
		identifiers[spec.ID] = true
		absoluteTarget, err := filepath.Abs(spec.Target)
		if err != nil {
			return fmt.Errorf("resolve recovery target %q: %w", spec.ID, err)
		}
		if absoluteTarget != spec.Target ||
			absoluteTarget == t.home ||
			!recoveryPathWithin(t.home, absoluteTarget) {
			return fmt.Errorf(
				"recovery %q target must be an absolute path inside the home directory",
				spec.ID,
			)
		}
		if previous, exists := targets[absoluteTarget]; exists {
			return fmt.Errorf(
				"recoveries %q and %q share a target",
				previous,
				spec.ID,
			)
		}
		targets[absoluteTarget] = spec.ID
		if err := validateRecoveryParentPath(
			t.home,
			filepath.Dir(absoluteTarget),
		); err != nil {
			return fmt.Errorf(
				"recovery %q target parent is unsafe: %w",
				spec.ID,
				err,
			)
		}
	}
	return nil
}

func (t *Transaction) Apply(
	ctx context.Context,
	plan TransactionPlan,
	specs []model.RecoverySpec,
	journal Journal,
) (result TransactionResult, returnErr error) {
	result.Plan = plan
	if !plan.Ready {
		return result, errors.New("recovery transaction has blockers")
	}
	if err := t.validatePlan(ctx, plan, specs); err != nil {
		return result, err
	}
	stageRun := filepath.Join(t.stagingRoot, plan.RunID)
	if err := ensureRecoveryDirectoryPath(t.home, stageRun); err != nil {
		return result, fmt.Errorf("prepare recovery staging: %w", err)
	}
	if err := os.Chmod(stageRun, 0o700); err != nil {
		return result, fmt.Errorf("secure recovery staging: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(stageRun); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("remove recovery staging: %w", err),
			)
		}
	}()

	byID := make(map[string]model.RecoverySpec, len(specs))
	for _, spec := range specs {
		byID[spec.ID] = spec
	}
	staged := make(map[string]stagedRecovery, len(plan.Actions))
	var externalStaging []string
	defer func() {
		for _, path := range externalStaging {
			if err := os.RemoveAll(path); err != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("remove recovery tool staging: %w", err),
				)
			}
		}
	}()
	for _, action := range plan.Actions {
		payload, err := t.stage(ctx, action, byID[action.RecoveryID], stageRun)
		if err != nil {
			return result, fmt.Errorf(
				"stage recovery %q: %w",
				action.RecoveryID,
				err,
			)
		}
		staged[action.RecoveryID] = payload
		if payload.external {
			externalStaging = append(externalStaging, payload.path)
		}
	}

	var operations []recoveryOperation
	rollback := func(cause error) (TransactionResult, error) {
		rollbackErr := rollbackRecoveries(
			ctx,
			operations,
			journal,
			cause.Error(),
		)
		result.RolledBack = true
		if rollbackErr != nil {
			return result, errors.Join(
				cause,
				fmt.Errorf("rollback: %w", rollbackErr),
			)
		}
		return result, cause
	}

	for index, action := range plan.Actions {
		if t.beforeInstall != nil {
			t.beforeInstall(index)
		}
		if journal != nil {
			if err := journal.StartRecovery(ctx, action); err != nil {
				return rollback(fmt.Errorf(
					"start recovery %q journal: %w",
					action.RecoveryID,
					err,
				))
			}
		}
		operation, err := t.install(
			ctx,
			action,
			byID[action.RecoveryID],
			staged[action.RecoveryID],
			journal,
		)
		if err != nil {
			if operation.mutated {
				operations = append(operations, operation)
			}
			if journal != nil {
				if journalErr := journal.FailRecovery(
					ctx,
					action,
					err.Error(),
				); journalErr != nil {
					err = errors.Join(err, journalErr)
				}
			}
			return rollback(err)
		}
		operations = append(operations, operation)
		actualBackup := ""
		if len(operation.backups) > 0 {
			actualBackup = action.BackupPath
		}
		result.Applied = append(result.Applied, AppliedRecovery{
			RecoveryID: action.RecoveryID,
			Target:     action.Target,
			BackupPath: actualBackup,
		})
		if journal != nil {
			if err := journal.CompleteRecovery(ctx, action); err != nil {
				return rollback(fmt.Errorf(
					"complete recovery %q journal: %w",
					action.RecoveryID,
					err,
				))
			}
		}
	}

	verification := t.planner.Plan(ctx, specs)
	if !allRecoveriesSatisfied(verification) {
		return rollback(fmt.Errorf(
			"fresh recovery verification failed: %s",
			unsatisfiedRecoverySummary(verification),
		))
	}
	result.Verified = true
	return result, nil
}

type stagedRecovery struct {
	path     string
	external bool
}

func (t *Transaction) stage(
	ctx context.Context,
	action RecoveryAction,
	spec model.RecoverySpec,
	stageRun string,
) (stagedRecovery, error) {
	stagePath := filepath.Join(stageRun, spec.ID)
	switch action.Type {
	case ActionRepairMode:
		return stagedRecovery{}, nil
	case ActionRestore:
	default:
		return stagedRecovery{}, fmt.Errorf("unsupported recovery action %q", action.Type)
	}
	switch spec.Kind {
	case model.RecoveryKindSOPSFile:
		if err := t.stageSOPSFile(ctx, spec, stagePath); err != nil {
			return stagedRecovery{}, err
		}
	case model.RecoveryKindAgeArchive:
		if err := t.stageAgeArchive(ctx, spec, stagePath); err != nil {
			return stagedRecovery{}, err
		}
	case model.RecoveryKindGPGKeyring:
		shortPath, err := os.MkdirTemp(t.home, ".envctl-gpg-stage-*")
		if err != nil {
			return stagedRecovery{}, err
		}
		if err := os.Chmod(shortPath, 0o700); err != nil {
			_ = os.RemoveAll(shortPath)
			return stagedRecovery{}, err
		}
		stagePath = shortPath
		if err := t.stageGPGKeyring(ctx, spec, stagePath); err != nil {
			_ = os.RemoveAll(stagePath)
			return stagedRecovery{}, err
		}
		return stagedRecovery{path: stagePath, external: true}, nil
	default:
		return stagedRecovery{}, fmt.Errorf("unsupported recovery kind %q", spec.Kind)
	}
	return stagedRecovery{path: stagePath}, nil
}

func (t *Transaction) stageSOPSFile(
	ctx context.Context,
	spec model.RecoverySpec,
	stagePath string,
) error {
	sops, err := t.planner.lookPath("sops")
	if err != nil {
		return errors.New("sops is unavailable")
	}
	identity := filepath.Join(t.home, ".config", "sops", "age", "keys.txt")
	file, err := os.OpenFile(
		stagePath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return err
	}
	bounded := &boundedWriter{writer: file, limit: maxCommandOutputBytes}
	command := t.planner.command(
		ctx,
		sops,
		"decrypt",
		"--output-type",
		spec.Format,
		spec.Source,
	)
	command.Env = filteredEnvironment(
		os.Environ(),
		map[string]string{"SOPS_AGE_KEY_FILE": identity},
		"SOPS_AGE_KEY",
		"SOPS_AGE_KEY_CMD",
		"SOPS_AGE_KEY_FILE",
	)
	command.Stdout = bounded
	command.Stderr = io.Discard
	runErr := command.Run()
	syncErr := file.Sync()
	closeErr := file.Close()
	if runErr != nil || bounded.exceeded || syncErr != nil || closeErr != nil {
		return errors.Join(
			runErr,
			syncErr,
			closeErr,
			boolError(bounded.exceeded, "decrypted SOPS output exceeds size limit"),
		)
	}
	return os.Chmod(stagePath, 0o600)
}

func (t *Transaction) stageAgeArchive(
	ctx context.Context,
	spec model.RecoverySpec,
	stagePath string,
) error {
	if err := os.Mkdir(stagePath, 0o700); err != nil {
		return err
	}
	age, err := t.planner.lookPath("age")
	if err != nil {
		return errors.New("age is unavailable")
	}
	identity := filepath.Join(t.home, ".config", "sops", "age", "keys.txt")
	command := t.planner.command(
		ctx,
		age,
		"--decrypt",
		"--identity",
		identity,
		spec.Source,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return err
	}
	expected := make(map[string]bool, len(spec.Members))
	for _, member := range spec.Members {
		expected[member] = true
	}
	seen := make(map[string]bool, len(spec.Members))
	reader := tar.NewReader(io.LimitReader(stdout, maxArchiveBytes+1))
	var extractErr error
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			extractErr = err
			break
		}
		if appleDoubleMember(header, expected) {
			if _, err := io.CopyN(io.Discard, reader, header.Size); err != nil {
				extractErr = errors.New("truncated archive metadata member")
				break
			}
			continue
		}
		if !safeArchiveMember(header, expected) || seen[header.Name] {
			extractErr = fmt.Errorf("unsafe archive member %q", header.Name)
			break
		}
		destination := filepath.Join(stagePath, header.Name)
		file, err := os.OpenFile(
			destination,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if err != nil {
			extractErr = err
			break
		}
		written, copyErr := io.CopyN(file, reader, header.Size)
		syncErr := file.Sync()
		closeErr := file.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil ||
			written != header.Size {
			extractErr = errors.Join(copyErr, syncErr, closeErr)
			break
		}
		if err := os.Chmod(destination, 0o600); err != nil {
			extractErr = err
			break
		}
		seen[header.Name] = true
	}
	if extractErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return extractErr
	}
	if err := command.Wait(); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errors.New("archive is missing required members")
	}
	return nil
}

func (t *Transaction) stageGPGKeyring(
	ctx context.Context,
	spec model.RecoverySpec,
	stagePath string,
) (returnErr error) {
	info, err := os.Lstat(stagePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o700 {
		return errors.New("GPG staging home is unsafe")
	}
	gpg, err := t.planner.lookPath("gpg")
	if err != nil {
		return errors.New("gpg is unavailable")
	}
	gpgconf, err := t.planner.lookPath("gpgconf")
	if err != nil {
		return errors.New("gpgconf is unavailable")
	}
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := t.runDiscard(
			stopContext,
			gpgconf,
			"--homedir",
			stagePath,
			"--kill",
			"gpg-agent",
		); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("stop staged GPG agent: %w", err),
			)
		}
	}()
	age, err := t.planner.lookPath("age")
	if err != nil {
		return errors.New("age is unavailable")
	}
	if err := t.runDiscard(
		ctx,
		gpg,
		"--batch",
		"--homedir",
		stagePath,
		"--import",
		spec.Sources["public"],
	); err != nil {
		return fmt.Errorf("import public GPG recovery: %w", err)
	}
	identity := filepath.Join(t.home, ".config", "sops", "age", "keys.txt")
	if err := t.pipeAgeToGPG(
		ctx,
		age,
		gpg,
		identity,
		spec.Sources["private"],
		stagePath,
		"--import",
	); err != nil {
		return fmt.Errorf("import private GPG recovery: %w", err)
	}
	if err := t.pipeAgeToGPG(
		ctx,
		age,
		gpg,
		identity,
		spec.Sources["ownertrust"],
		stagePath,
		"--import-ownertrust",
	); err != nil {
		return fmt.Errorf("import GPG ownertrust recovery: %w", err)
	}
	fingerprint, err := t.planner.gpgSecretFingerprint(
		ctx,
		gpg,
		stagePath,
		spec.Fingerprint,
	)
	if err != nil || fingerprint != spec.Fingerprint {
		return errors.New("staged GPG keyring lacks the expected secret key")
	}
	return os.Chmod(stagePath, 0o700)
}

func (t *Transaction) pipeAgeToGPG(
	ctx context.Context,
	age, gpg, identity, source, home, operation string,
) error {
	decrypted, err := os.CreateTemp(home, ".envctl-gpg-input-*")
	if err != nil {
		return err
	}
	decryptedPath := decrypted.Name()
	defer func() {
		_ = os.Remove(decryptedPath)
	}()
	if err := os.Chmod(decryptedPath, 0o600); err != nil {
		_ = decrypted.Close()
		return err
	}
	bounded := &boundedWriter{
		writer: decrypted,
		limit:  maxCommandOutputBytes,
	}
	ageCommand := t.planner.command(
		ctx,
		age,
		"--decrypt",
		"--identity",
		identity,
		source,
	)
	ageCommand.Stdout = bounded
	ageCommand.Stderr = io.Discard
	runErr := ageCommand.Run()
	syncErr := decrypted.Sync()
	closeErr := decrypted.Close()
	if runErr != nil || bounded.exceeded || syncErr != nil || closeErr != nil {
		return errors.Join(
			runErr,
			syncErr,
			closeErr,
			boolError(
				bounded.exceeded,
				"decrypted GPG recovery input exceeds size limit",
			),
		)
	}
	input, err := os.Open(decryptedPath)
	if err != nil {
		return err
	}
	defer input.Close()
	gpgCommand := t.planner.command(
		ctx,
		gpg,
		"--batch",
		"--homedir",
		home,
		operation,
	)
	gpgCommand.Stdin = input
	gpgCommand.Stdout = io.Discard
	gpgCommand.Stderr = io.Discard
	return gpgCommand.Run()
}

func (t *Transaction) runDiscard(
	ctx context.Context,
	executable string,
	args ...string,
) error {
	command := t.planner.command(ctx, executable, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

type recoveryOperation struct {
	action           RecoveryAction
	installed        []string
	backups          []pathBackup
	createdTargetDir bool
	originalMode     os.FileMode
	modeChanged      bool
	mutated          bool
}

type pathBackup struct {
	original string
	backup   string
}

func (t *Transaction) install(
	ctx context.Context,
	action RecoveryAction,
	spec model.RecoverySpec,
	staged stagedRecovery,
	journal Journal,
) (recoveryOperation, error) {
	switch spec.Kind {
	case model.RecoveryKindSOPSFile:
		return t.installSOPSFile(ctx, action, spec, staged, journal)
	case model.RecoveryKindAgeArchive:
		return t.installAgeArchive(ctx, action, spec, staged, journal)
	case model.RecoveryKindGPGKeyring:
		return t.installGPGKeyring(action, spec, staged)
	default:
		return recoveryOperation{}, fmt.Errorf("unsupported recovery kind %q", spec.Kind)
	}
}

func (t *Transaction) installSOPSFile(
	ctx context.Context,
	action RecoveryAction,
	spec model.RecoverySpec,
	staged stagedRecovery,
	journal Journal,
) (recoveryOperation, error) {
	operation := recoveryOperation{action: action}
	if err := ensureRecoveryDirectoryPath(t.home, filepath.Dir(spec.Target)); err != nil {
		return operation, err
	}
	info, err := os.Lstat(spec.Target)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return operation, errors.New("credential target became unsafe")
		}
		if err := ensureRecoveryDirectoryPath(
			t.home,
			filepath.Dir(action.BackupPath),
		); err != nil {
			return operation, err
		}
		if err := os.Rename(spec.Target, action.BackupPath); err != nil {
			return operation, err
		}
		operation.backups = append(operation.backups, pathBackup{
			original: spec.Target,
			backup:   action.BackupPath,
		})
		operation.mutated = true
		if journal != nil {
			if err := journal.RecordRecoveryBackup(
				ctx,
				action,
				spec.Target,
				action.BackupPath,
			); err != nil {
				return operation, err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return operation, err
	}
	if err := os.Rename(staged.path, spec.Target); err != nil {
		return operation, err
	}
	operation.installed = append(operation.installed, spec.Target)
	operation.mutated = true
	return operation, nil
}

func (t *Transaction) installAgeArchive(
	ctx context.Context,
	action RecoveryAction,
	spec model.RecoverySpec,
	staged stagedRecovery,
	journal Journal,
) (recoveryOperation, error) {
	operation := recoveryOperation{action: action}
	info, err := os.Lstat(spec.Target)
	if errors.Is(err, os.ErrNotExist) {
		if err := ensureRecoveryDirectoryPath(
			t.home,
			filepath.Dir(spec.Target),
		); err != nil {
			return operation, err
		}
		if err := os.Mkdir(spec.Target, 0o700); err != nil {
			return operation, err
		}
		operation.createdTargetDir = true
		operation.mutated = true
	} else if err != nil {
		return operation, err
	} else {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return operation, errors.New("credential directory became unsafe")
		}
		operation.originalMode = info.Mode().Perm()
		if info.Mode().Perm() != 0o700 {
			if err := os.Chmod(spec.Target, 0o700); err != nil {
				return operation, err
			}
			operation.modeChanged = true
			operation.mutated = true
		}
	}
	for _, member := range spec.Members {
		stagedMember := filepath.Join(staged.path, member)
		targetMember := filepath.Join(spec.Target, member)
		targetInfo, err := os.Lstat(targetMember)
		if err == nil {
			if !targetInfo.Mode().IsRegular() ||
				targetInfo.Mode()&os.ModeSymlink != 0 {
				return operation, fmt.Errorf(
					"credential member became unsafe: %s",
					member,
				)
			}
			matches, err := filesMatch(stagedMember, targetMember)
			if err != nil {
				return operation, err
			}
			if matches && targetInfo.Mode().Perm() == 0o600 {
				continue
			}
			backup := filepath.Join(action.BackupPath, member)
			if err := ensureRecoveryDirectoryPath(
				t.home,
				filepath.Dir(backup),
			); err != nil {
				return operation, err
			}
			if err := os.Rename(targetMember, backup); err != nil {
				return operation, err
			}
			operation.backups = append(operation.backups, pathBackup{
				original: targetMember,
				backup:   backup,
			})
			operation.mutated = true
			if journal != nil {
				if err := journal.RecordRecoveryBackup(
					ctx,
					action,
					targetMember,
					backup,
				); err != nil {
					return operation, err
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return operation, err
		}
		if err := os.Rename(stagedMember, targetMember); err != nil {
			return operation, err
		}
		operation.installed = append(operation.installed, targetMember)
		operation.mutated = true
	}
	return operation, nil
}

func (t *Transaction) installGPGKeyring(
	action RecoveryAction,
	spec model.RecoverySpec,
	staged stagedRecovery,
) (recoveryOperation, error) {
	operation := recoveryOperation{action: action}
	if action.Type == ActionRepairMode {
		info, err := os.Lstat(spec.Target)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return operation, errors.New("GPG keyring became unsafe")
		}
		operation.originalMode = info.Mode().Perm()
		if err := os.Chmod(spec.Target, 0o700); err != nil {
			return operation, err
		}
		operation.modeChanged = true
		operation.mutated = true
		return operation, nil
	}
	if _, err := os.Lstat(spec.Target); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return operation, errors.New("GPG keyring appeared after planning")
		}
		return operation, err
	}
	if err := ensureRecoveryDirectoryPath(
		t.home,
		filepath.Dir(spec.Target),
	); err != nil {
		return operation, err
	}
	if err := os.Rename(staged.path, spec.Target); err != nil {
		return operation, err
	}
	operation.installed = append(operation.installed, spec.Target)
	operation.createdTargetDir = true
	operation.mutated = true
	return operation, nil
}

func (t *Transaction) validatePlan(
	ctx context.Context,
	plan TransactionPlan,
	specs []model.RecoverySpec,
) error {
	if !safeRecoveryRunID(plan.RunID) {
		return errors.New("recovery plan has an unsafe run id")
	}
	if len(plan.Actions)+plan.Satisfied != len(specs) {
		return errors.New("recovery plan does not cover every desired item")
	}
	freshPlan, _, err := t.Plan(ctx, specs)
	if err != nil {
		return err
	}
	if !freshPlan.Ready ||
		freshPlan.Satisfied != plan.Satisfied ||
		len(freshPlan.Actions) != len(plan.Actions) {
		return errors.New("recovery state changed after planning")
	}
	for index := range plan.Actions {
		expected := plan.Actions[index]
		fresh := freshPlan.Actions[index]
		if expected.Sequence != index+1 ||
			expected.RecoveryID != fresh.RecoveryID ||
			expected.Kind != fresh.Kind ||
			expected.Type != fresh.Type ||
			expected.Target != fresh.Target ||
			expected.Expected != fresh.Expected {
			return errors.New("recovery state changed after planning")
		}
		relative, err := filepath.Rel(t.home, expected.Target)
		if err != nil {
			return err
		}
		wantedBackup := filepath.Join(
			t.backupRoot,
			plan.RunID,
			relative,
		)
		if expected.BackupPath != wantedBackup {
			return fmt.Errorf(
				"recovery %q has an invalid backup path",
				expected.RecoveryID,
			)
		}
	}
	return nil
}

func rollbackRecoveries(
	ctx context.Context,
	operations []recoveryOperation,
	journal Journal,
	reason string,
) error {
	var rollbackErrors []error
	for index := len(operations) - 1; index >= 0; index-- {
		operation := operations[index]
		var operationErrors []error
		for installedIndex := len(operation.installed) - 1; installedIndex >= 0; installedIndex-- {
			path := operation.installed[installedIndex]
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				operationErrors = append(operationErrors, err)
				continue
			}
			if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				if err := os.RemoveAll(path); err != nil {
					operationErrors = append(operationErrors, err)
				}
			} else if err := os.Remove(path); err != nil {
				operationErrors = append(operationErrors, err)
			}
		}
		for backupIndex := len(operation.backups) - 1; backupIndex >= 0; backupIndex-- {
			backup := operation.backups[backupIndex]
			if err := os.Rename(backup.backup, backup.original); err != nil {
				operationErrors = append(operationErrors, err)
			}
		}
		if operation.modeChanged {
			if err := os.Chmod(
				operation.action.Target,
				operation.originalMode,
			); err != nil {
				operationErrors = append(operationErrors, err)
			}
		}
		if operation.createdTargetDir &&
			operation.action.Kind == model.RecoveryKindAgeArchive {
			if err := os.Remove(operation.action.Target); err != nil &&
				!errors.Is(err, os.ErrNotExist) &&
				!errors.Is(err, syscall.ENOTEMPTY) {
				operationErrors = append(operationErrors, err)
			}
		}
		if operation.mutated && len(operationErrors) == 0 && journal != nil {
			if err := journal.RollBackRecovery(
				ctx,
				operation.action,
				reason,
			); err != nil {
				operationErrors = append(operationErrors, err)
			}
		}
		rollbackErrors = append(rollbackErrors, operationErrors...)
	}
	return errors.Join(rollbackErrors...)
}

func allRecoveriesSatisfied(plan model.RecoveryPlan) bool {
	return plan.Summary.Satisfied == len(plan.Findings) &&
		plan.Summary.Missing == 0 &&
		plan.Summary.Drifted == 0 &&
		plan.Summary.Blocked == 0 &&
		plan.Summary.ToolMissing == 0 &&
		plan.Summary.SourceMissing == 0 &&
		plan.Summary.SourceUnsafe == 0
}

func unsatisfiedRecoverySummary(plan model.RecoveryPlan) string {
	var statuses []string
	for _, finding := range plan.Findings {
		if finding.Status == model.RecoveryFindingSatisfied {
			continue
		}
		statuses = append(
			statuses,
			finding.RecoveryID+"="+string(finding.Status),
		)
	}
	if len(statuses) == 0 {
		return "unknown verification failure"
	}
	return strings.Join(statuses, ", ")
}

func filesMatch(first, second string) (bool, error) {
	firstDigest, err := digestFile(first, maxArchiveMemberBytes)
	if err != nil {
		return false, err
	}
	secondDigest, err := digestFile(second, maxArchiveMemberBytes)
	if err != nil {
		return false, err
	}
	return firstDigest == secondDigest, nil
}

func appleDoubleMember(header *tar.Header, expected map[string]bool) bool {
	return strings.HasPrefix(header.Name, "._") &&
		expected[strings.TrimPrefix(header.Name, "._")] &&
		filepath.Base(header.Name) == header.Name &&
		(header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) &&
		header.Size >= 0 &&
		header.Size <= maxArchiveMemberBytes
}

func safeArchiveMember(header *tar.Header, expected map[string]bool) bool {
	return expected[header.Name] &&
		filepath.Base(header.Name) == header.Name &&
		(header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) &&
		header.Size >= 0 &&
		header.Size <= maxArchiveMemberBytes
}

func ensureRecoveryDirectoryPath(home, directory string) error {
	if err := validateRecoveryParentPath(home, directory); err != nil {
		return err
	}
	relative, err := filepath.Rel(home, directory)
	if err != nil {
		return err
	}
	current := home
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf(
					"directory component is not a real directory: %s",
					current,
				)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Mkdir(current, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func validateRecoveryParentPath(home, directory string) error {
	relative, err := filepath.Rel(home, directory)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("recovery path escapes the home directory")
	}
	current := home
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf(
				"parent component is not a real directory: %s",
				current,
			)
		}
	}
	return nil
}

func recoveryPathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeRecoveryRunID(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func (t *Transaction) newRunID() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate recovery run id: %w", err)
	}
	return t.now().UTC().Format("20060102T150405.000000000Z") +
		"-" + hex.EncodeToString(random), nil
}

func boolError(value bool, message string) error {
	if value {
		return errors.New(message)
	}
	return nil
}

func sortedRecoverySpecs(specs []model.RecoverySpec) []model.RecoverySpec {
	ordered := append([]model.RecoverySpec(nil), specs...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}
