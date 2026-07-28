package portablelink

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/planner"
)

type ActionType string

const (
	ActionCreate  ActionType = "link.create"
	ActionReplace ActionType = "link.replace"
)

type LinkAction struct {
	Sequence           int            `json:"sequence"`
	LinkID             string         `json:"link_id"`
	Type               ActionType     `json:"type"`
	Source             string         `json:"source"`
	Target             string         `json:"target"`
	LinkValue          string         `json:"link_value"`
	ExpectedDigest     string         `json:"expected_digest"`
	Kind               model.LinkKind `json:"kind"`
	ExpectedTargetType string         `json:"expected_target_type"`
	ExpectedLinkTarget string         `json:"expected_link_target,omitempty"`
	BackupPath         string         `json:"backup_path,omitempty"`
}

type LinkBlocker struct {
	LinkID string                  `json:"link_id"`
	Status model.LinkFindingStatus `json:"status"`
	Detail string                  `json:"detail"`
}

type TransactionPlan struct {
	RunID     string        `json:"run_id"`
	Ready     bool          `json:"ready"`
	Satisfied int           `json:"satisfied"`
	Actions   []LinkAction  `json:"actions"`
	Blockers  []LinkBlocker `json:"blockers,omitempty"`
}

type AppliedLink struct {
	LinkID     string `json:"link_id"`
	Target     string `json:"target"`
	BackupPath string `json:"backup_path,omitempty"`
}

type TransactionResult struct {
	Plan       TransactionPlan `json:"plan"`
	Applied    []AppliedLink   `json:"applied,omitempty"`
	Verified   bool            `json:"verified"`
	RolledBack bool            `json:"rolled_back"`
}

type Journal interface {
	StartLink(context.Context, LinkAction) error
	CompleteLink(context.Context, LinkAction) error
	FailLink(context.Context, LinkAction, string) error
	RollBackLink(context.Context, LinkAction, string) error
}

type Transaction struct {
	home         string
	backupRoot   string
	now          func() time.Time
	beforeAction func(int)
}

func NewTransaction(home, backupRoot string) (*Transaction, error) {
	absoluteHome, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	absoluteBackup, err := filepath.Abs(backupRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve backup directory: %w", err)
	}
	homeInfo, err := os.Lstat(absoluteHome)
	if err != nil {
		return nil, fmt.Errorf("inspect home directory: %w", err)
	}
	if !homeInfo.IsDir() || homeInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("home path must be a real directory")
	}
	if !pathWithin(absoluteHome, absoluteBackup) ||
		absoluteBackup == absoluteHome {
		return nil, errors.New("portable-link backup directory must be inside the home directory")
	}
	return &Transaction{
		home: absoluteHome, backupRoot: absoluteBackup, now: time.Now,
	}, nil
}

func (t *Transaction) Plan(specs []model.LinkSpec) (TransactionPlan, error) {
	runID, err := t.newRunID()
	if err != nil {
		return TransactionPlan{}, err
	}
	ordered := append([]model.LinkSpec(nil), specs...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})
	observed := Collect(ordered)
	_, findings := planner.BuildLinks(ordered, observed, true)
	plan := TransactionPlan{RunID: runID}
	for _, finding := range findings {
		spec := finding.Desired
		if err := t.validatePaths(spec); err != nil {
			plan.Blockers = append(plan.Blockers, LinkBlocker{
				LinkID: spec.ID,
				Status: model.LinkFindingNotChecked,
				Detail: err.Error(),
			})
			continue
		}
		switch finding.Status {
		case model.LinkFindingSatisfied:
			plan.Satisfied++
		case model.LinkFindingMissing, model.LinkFindingWrongTarget:
			linkValue, err := filepath.Rel(filepath.Dir(spec.Target), spec.Source)
			if err != nil {
				return TransactionPlan{}, fmt.Errorf(
					"calculate link %q target: %w", spec.ID, err,
				)
			}
			action := LinkAction{
				Sequence: len(plan.Actions) + 1,
				LinkID:   spec.ID, Source: spec.Source, Target: spec.Target,
				LinkValue: linkValue, ExpectedDigest: spec.Digest, Kind: spec.Kind,
			}
			if finding.Observed != nil {
				action.ExpectedTargetType = finding.Observed.TargetType
				action.ExpectedLinkTarget = finding.Observed.LinkTarget
			}
			if finding.Status == model.LinkFindingMissing {
				action.Type = ActionCreate
			} else {
				action.Type = ActionReplace
				relative, err := filepath.Rel(t.home, spec.Target)
				if err != nil {
					return TransactionPlan{}, err
				}
				action.BackupPath = filepath.Join(
					t.backupRoot, runID, relative,
				)
			}
			plan.Actions = append(plan.Actions, action)
		default:
			plan.Blockers = append(plan.Blockers, LinkBlocker{
				LinkID: spec.ID,
				Status: finding.Status,
				Detail: finding.Detail,
			})
		}
	}
	plan.Ready = len(plan.Blockers) == 0
	return plan, nil
}

func (t *Transaction) Apply(
	ctx context.Context,
	plan TransactionPlan,
	specs []model.LinkSpec,
	journal Journal,
) (TransactionResult, error) {
	result := TransactionResult{Plan: plan}
	if !plan.Ready {
		return result, errors.New("portable-link transaction has blockers")
	}
	if err := t.validatePlan(plan, specs); err != nil {
		return result, err
	}

	var (
		operations  []appliedOperation
		createdDirs []string
	)
	rollback := func(cause error) (TransactionResult, error) {
		rollbackErr := rollbackOperations(
			ctx, operations, createdDirs, journal, cause.Error(),
		)
		result.RolledBack = true
		if rollbackErr != nil {
			return result, errors.Join(cause, fmt.Errorf("rollback: %w", rollbackErr))
		}
		return result, cause
	}

	for index, action := range plan.Actions {
		if t.beforeAction != nil {
			t.beforeAction(index)
		}
		if err := validateActionPrecondition(action); err != nil {
			return rollback(err)
		}
		if journal != nil {
			if err := journal.StartLink(ctx, action); err != nil {
				return rollback(fmt.Errorf(
					"start link %q journal: %w", action.LinkID, err,
				))
			}
		}
		failAction := func(cause error) (TransactionResult, error) {
			if journal != nil {
				if err := journal.FailLink(ctx, action, cause.Error()); err != nil {
					cause = errors.Join(cause, fmt.Errorf(
						"fail link %q journal: %w", action.LinkID, err,
					))
				}
			}
			return rollback(cause)
		}
		directories, err := ensureDirectoryPath(t.home, filepath.Dir(action.Target))
		if err != nil {
			return failAction(fmt.Errorf(
				"prepare link %q target: %w", action.LinkID, err,
			))
		}
		createdDirs = appendUnique(createdDirs, directories...)

		operation := appliedOperation{action: action}
		if action.Type == ActionReplace {
			directories, err := ensureDirectoryPath(
				t.home, filepath.Dir(action.BackupPath),
			)
			if err != nil {
				return failAction(fmt.Errorf(
					"prepare link %q backup: %w", action.LinkID, err,
				))
			}
			createdDirs = appendUnique(createdDirs, directories...)
			if _, err := os.Lstat(action.BackupPath); !errors.Is(err, os.ErrNotExist) {
				if err == nil {
					return failAction(fmt.Errorf(
						"backup path already exists for link %q",
						action.LinkID,
					))
				}
				return failAction(fmt.Errorf(
					"inspect link %q backup: %w", action.LinkID, err,
				))
			}
			if err := os.Rename(action.Target, action.BackupPath); err != nil {
				return failAction(fmt.Errorf(
					"backup link %q: %w", action.LinkID, err,
				))
			}
			operation.backupMoved = true
		}
		operations = append(operations, operation)
		if err := os.Symlink(action.LinkValue, action.Target); err != nil {
			return failAction(fmt.Errorf(
				"create link %q: %w", action.LinkID, err,
			))
		}
		operations[len(operations)-1].linkCreated = true
		result.Applied = append(result.Applied, AppliedLink{
			LinkID: action.LinkID, Target: action.Target,
			BackupPath: action.BackupPath,
		})
		if journal != nil {
			if err := journal.CompleteLink(ctx, action); err != nil {
				return rollback(fmt.Errorf(
					"complete link %q journal: %w", action.LinkID, err,
				))
			}
		}
	}

	_, findings := planner.BuildLinks(specs, Collect(specs), true)
	for _, finding := range findings {
		if finding.Status != model.LinkFindingSatisfied {
			return rollback(fmt.Errorf(
				"verification failed for link %q: %s",
				finding.LinkID,
				finding.Detail,
			))
		}
	}
	result.Verified = true
	return result, nil
}

func (t *Transaction) validatePlan(
	plan TransactionPlan,
	specs []model.LinkSpec,
) error {
	if !safeRunID(plan.RunID) {
		return errors.New("portable-link plan has an unsafe run id")
	}
	if len(plan.Actions)+plan.Satisfied != len(specs) {
		return errors.New("portable-link plan does not cover every desired link")
	}
	byID := make(map[string]model.LinkSpec, len(specs))
	for _, spec := range specs {
		if err := t.validatePaths(spec); err != nil {
			return err
		}
		byID[spec.ID] = spec
	}
	actionByID := make(map[string]LinkAction, len(plan.Actions))
	for index, action := range plan.Actions {
		if action.Sequence != index+1 {
			return errors.New("portable-link action sequence is invalid")
		}
		if _, exists := actionByID[action.LinkID]; exists {
			return fmt.Errorf(
				"portable-link plan repeats link %q",
				action.LinkID,
			)
		}
		actionByID[action.LinkID] = action
		spec, ok := byID[action.LinkID]
		if !ok ||
			spec.Source != action.Source ||
			spec.Target != action.Target ||
			spec.Digest != action.ExpectedDigest ||
			spec.Kind != action.Kind {
			return fmt.Errorf(
				"portable-link action %q does not match desired state",
				action.LinkID,
			)
		}
		linkValue, err := filepath.Rel(filepath.Dir(spec.Target), spec.Source)
		if err != nil || linkValue != action.LinkValue {
			return fmt.Errorf(
				"portable-link action %q has an invalid link value",
				action.LinkID,
			)
		}
		expectedBackup := ""
		if action.Type == ActionReplace {
			relative, err := filepath.Rel(t.home, spec.Target)
			if err != nil {
				return err
			}
			expectedBackup = filepath.Join(t.backupRoot, plan.RunID, relative)
		}
		if action.BackupPath != expectedBackup {
			return fmt.Errorf(
				"portable-link action %q has an invalid backup path",
				action.LinkID,
			)
		}
		if err := validateActionPrecondition(action); err != nil {
			return err
		}
	}
	_, findings := planner.BuildLinks(specs, Collect(specs), true)
	satisfied := 0
	for _, finding := range findings {
		action, actionable := actionByID[finding.LinkID]
		switch finding.Status {
		case model.LinkFindingSatisfied:
			if actionable {
				return fmt.Errorf(
					"portable-link %q became satisfied after planning",
					finding.LinkID,
				)
			}
			satisfied++
		case model.LinkFindingMissing:
			if !actionable || action.Type != ActionCreate {
				return fmt.Errorf(
					"portable-link %q no longer matches its plan",
					finding.LinkID,
				)
			}
		case model.LinkFindingWrongTarget:
			if !actionable || action.Type != ActionReplace {
				return fmt.Errorf(
					"portable-link %q no longer matches its plan",
					finding.LinkID,
				)
			}
		default:
			return fmt.Errorf(
				"portable-link %q became blocked after planning: %s",
				finding.LinkID,
				finding.Detail,
			)
		}
	}
	if satisfied != plan.Satisfied {
		return errors.New("portable-link satisfied count changed after planning")
	}
	return nil
}

func (t *Transaction) validatePaths(spec model.LinkSpec) error {
	if spec.Kind != model.LinkKindFile &&
		spec.Kind != model.LinkKindDirectory {
		return fmt.Errorf(
			"link %q has unsupported portable kind %q", spec.ID, spec.Kind,
		)
	}
	if !pathWithin(t.home, spec.Target) || spec.Target == t.home {
		return fmt.Errorf("link %q target is outside the home directory", spec.ID)
	}
	if err := validateParentPath(t.home, filepath.Dir(spec.Target)); err != nil {
		return fmt.Errorf("link %q target parent: %w", spec.ID, err)
	}
	return nil
}

func (t *Transaction) newRunID() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate portable-link run id: %w", err)
	}
	return t.now().UTC().Format("20060102T150405.000000000Z") +
		"-" + hex.EncodeToString(random), nil
}

type appliedOperation struct {
	action      LinkAction
	backupMoved bool
	linkCreated bool
}

func validateActionPrecondition(action LinkAction) error {
	observation := Collect([]model.LinkSpec{{
		ID: action.LinkID, Source: action.Source, Target: action.Target,
		Kind: action.Kind,
	}})[0]
	expectedSourceType := "file"
	if action.Kind == model.LinkKindDirectory {
		expectedSourceType = "directory"
	}
	if observation.SourceType != expectedSourceType ||
		observation.SourceDigest != action.ExpectedDigest {
		return fmt.Errorf(
			"link %q source changed after planning",
			action.LinkID,
		)
	}
	switch action.Type {
	case ActionCreate:
		if observation.TargetType != "absent" {
			return fmt.Errorf(
				"link %q target appeared after planning",
				action.LinkID,
			)
		}
	case ActionReplace:
		if observation.TargetType != action.ExpectedTargetType ||
			observation.LinkTarget != action.ExpectedLinkTarget {
			return fmt.Errorf(
				"link %q target changed after planning",
				action.LinkID,
			)
		}
	default:
		return fmt.Errorf(
			"link %q has unsupported action %q",
			action.LinkID,
			action.Type,
		)
	}
	return nil
}

func validateParentPath(home, directory string) error {
	relative, err := filepath.Rel(home, directory)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("parent path escapes the home directory")
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
			return fmt.Errorf("inspect %s: %w", current, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("parent component is not a real directory: %s", current)
		}
	}
	return nil
}

func ensureDirectoryPath(home, directory string) ([]string, error) {
	if err := validateParentPath(home, directory); err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(home, directory)
	if err != nil {
		return nil, err
	}
	current := home
	var created []string
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return created, fmt.Errorf(
					"directory component changed while applying: %s",
					current,
				)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return created, err
		}
		if err := os.Mkdir(current, 0o700); err != nil {
			return created, err
		}
		created = append(created, current)
	}
	return created, nil
}

func rollbackOperations(
	ctx context.Context,
	operations []appliedOperation,
	createdDirectories []string,
	journal Journal,
	reason string,
) error {
	var rollbackErrors []error
	for index := len(operations) - 1; index >= 0; index-- {
		operation := operations[index]
		var operationErrors []error
		if operation.linkCreated {
			if err := os.Remove(operation.action.Target); err != nil &&
				!errors.Is(err, os.ErrNotExist) {
				operationErrors = append(operationErrors, fmt.Errorf(
					"remove new link %q: %w",
					operation.action.LinkID,
					err,
				))
			}
		}
		if operation.backupMoved {
			if err := os.Rename(
				operation.action.BackupPath,
				operation.action.Target,
			); err != nil {
				operationErrors = append(operationErrors, fmt.Errorf(
					"restore link %q: %w",
					operation.action.LinkID,
					err,
				))
			}
		}
		if len(operationErrors) == 0 && journal != nil {
			if err := journal.RollBackLink(
				ctx, operation.action, reason,
			); err != nil {
				operationErrors = append(operationErrors, fmt.Errorf(
					"record link %q rollback: %w",
					operation.action.LinkID,
					err,
				))
			}
		}
		rollbackErrors = append(rollbackErrors, operationErrors...)
	}
	for index := len(createdDirectories) - 1; index >= 0; index-- {
		if err := os.Remove(createdDirectories[index]); err != nil &&
			!errors.Is(err, os.ErrNotExist) &&
			!errors.Is(err, syscall.ENOTEMPTY) {
			rollbackErrors = append(rollbackErrors, fmt.Errorf(
				"remove created directory %s: %w",
				createdDirectories[index],
				err,
			))
		}
	}
	return errors.Join(rollbackErrors...)
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if !seen[value] {
			values = append(values, value)
			seen[value] = true
		}
	}
	return values
}

func safeRunID(value string) bool {
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

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
