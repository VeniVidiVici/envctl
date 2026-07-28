package portablelink

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeniVidiVici/envctl/internal/contentdigest"
	"github.com/VeniVidiVici/envctl/internal/model"
)

type fakeLinkJournal struct {
	started    []string
	completed  []string
	failed     []string
	rolledBack []string
}

func (j *fakeLinkJournal) StartLink(
	_ context.Context,
	action LinkAction,
) error {
	j.started = append(j.started, action.LinkID)
	return nil
}

func (j *fakeLinkJournal) CompleteLink(
	_ context.Context,
	action LinkAction,
) error {
	j.completed = append(j.completed, action.LinkID)
	return nil
}

func (j *fakeLinkJournal) FailLink(
	_ context.Context,
	action LinkAction,
	_ string,
) error {
	j.failed = append(j.failed, action.LinkID)
	return nil
}

func (j *fakeLinkJournal) RollBackLink(
	_ context.Context,
	action LinkAction,
	_ string,
) error {
	j.rolledBack = append(j.rolledBack, action.LinkID)
	return nil
}

func TestTransactionPlanBlocksOccupiedTargetBeforeAnyMutation(t *testing.T) {
	home := t.TempDir()
	source := writeSource(t, filepath.Join(home, "repo", "source"), "source")
	missing := filepath.Join(home, ".config", "missing")
	occupied := filepath.Join(home, ".config", "occupied")
	if err := os.MkdirAll(filepath.Dir(occupied), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(occupied, []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	transaction := newTestTransaction(t, home)
	plan, err := transaction.Plan([]model.LinkSpec{
		linkSpec("missing", source, missing),
		linkSpec("occupied", source, occupied),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ready || len(plan.Actions) != 1 || len(plan.Blockers) != 1 ||
		plan.Blockers[0].Status != model.LinkFindingOccupied {
		t.Fatalf("plan = %#v", plan)
	}
	result, err := transaction.Apply(context.Background(), plan, []model.LinkSpec{
		linkSpec("missing", source, missing),
		linkSpec("occupied", source, occupied),
	}, nil)
	if err == nil || result.Verified {
		t.Fatalf("apply = %#v, %v", result, err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing target was mutated: %v", err)
	}
}

func TestTransactionBacksUpWrongLinkCreatesMissingAndVerifies(t *testing.T) {
	home := t.TempDir()
	sourceOne := writeSource(t, filepath.Join(home, "repo", "one"), "one")
	sourceTwo := writeSource(t, filepath.Join(home, "repo", "two"), "two")
	oldSource := writeSource(t, filepath.Join(home, "legacy", "one"), "old")
	targetOne := filepath.Join(home, ".config", "app", "one")
	targetTwo := filepath.Join(home, ".config", "app", "two")
	if err := os.MkdirAll(filepath.Dir(targetOne), 0o700); err != nil {
		t.Fatal(err)
	}
	oldRelative, err := filepath.Rel(filepath.Dir(targetOne), oldSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldRelative, targetOne); err != nil {
		t.Fatal(err)
	}
	specs := []model.LinkSpec{
		linkSpec("one", sourceOne, targetOne),
		linkSpec("two", sourceTwo, targetTwo),
	}
	transaction := newTestTransaction(t, home)
	plan, err := transaction.Plan(specs)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || len(plan.Actions) != 2 ||
		plan.Actions[0].Type != ActionReplace ||
		plan.Actions[1].Type != ActionCreate {
		t.Fatalf("plan = %#v", plan)
	}
	result, err := transaction.Apply(context.Background(), plan, specs, nil)
	if err != nil {
		t.Fatalf("Apply() error = %v result = %#v", err, result)
	}
	if !result.Verified || result.RolledBack || len(result.Applied) != 2 {
		t.Fatalf("result = %#v", result)
	}
	for _, spec := range specs {
		observation := Collect([]model.LinkSpec{spec})[0]
		if observation.ResolvedTarget != spec.Source {
			t.Fatalf("observation = %#v", observation)
		}
	}
	backup := plan.Actions[0].BackupPath
	info, err := os.Lstat(backup)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("backup = %s, %v, %v", backup, info, err)
	}
	if got, err := os.Readlink(backup); err != nil || got != oldRelative {
		t.Fatalf("backup target = %q, %v", got, err)
	}
}

func TestTransactionReplacesLegacyDirectoryLink(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "env-config", "portable", "mise")
	legacy := filepath.Join(home, "legacy", "mise")
	for _, directory := range []string{source, legacy, filepath.Join(home, ".config")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(source, "config.toml"), []byte("[tools]\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".config", "mise")
	legacyRelative, err := filepath.Rel(filepath.Dir(target), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(legacyRelative, target); err != nil {
		t.Fatal(err)
	}
	digest, err := contentdigest.Directory(source)
	if err != nil {
		t.Fatal(err)
	}
	spec := model.LinkSpec{
		ID: "mise", Source: source, Target: target,
		Kind: model.LinkKindDirectory, Digest: digest,
	}
	transaction := newTestTransaction(t, home)
	plan, err := transaction.Plan([]model.LinkSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || len(plan.Actions) != 1 ||
		plan.Actions[0].Type != ActionReplace {
		t.Fatalf("plan = %#v", plan)
	}
	result, err := transaction.Apply(
		context.Background(), plan, []model.LinkSpec{spec}, nil,
	)
	if err != nil || !result.Verified {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	observation := Collect([]model.LinkSpec{spec})[0]
	if observation.ResolvedTarget != source {
		t.Fatalf("observation = %#v", observation)
	}
	backupInfo, err := os.Lstat(plan.Actions[0].BackupPath)
	if err != nil || backupInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("backup = %v, %v", backupInfo, err)
	}
}

func TestTransactionRollsBackEarlierLinksWhenTargetChanges(t *testing.T) {
	home := t.TempDir()
	source := writeSource(t, filepath.Join(home, "repo", "source"), "source")
	parent := filepath.Join(home, ".config", "app")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(parent, "first")
	second := filepath.Join(parent, "second")
	specs := []model.LinkSpec{
		linkSpec("first", source, first),
		linkSpec("second", source, second),
	}
	transaction := newTestTransaction(t, home)
	plan, err := transaction.Plan(specs)
	if err != nil {
		t.Fatal(err)
	}
	transaction.beforeAction = func(index int) {
		if index == 1 {
			if err := os.WriteFile(second, []byte("appeared"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	journal := &fakeLinkJournal{}
	result, err := transaction.Apply(context.Background(), plan, specs, journal)
	if err == nil || !result.RolledBack || result.Verified {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if _, err := os.Lstat(first); !os.IsNotExist(err) {
		t.Fatalf("first link was not rolled back: %v", err)
	}
	if raw, err := os.ReadFile(second); err != nil || string(raw) != "appeared" {
		t.Fatalf("external target was changed: %q, %v", raw, err)
	}
	if strings.Join(journal.started, ",") != "first" ||
		strings.Join(journal.completed, ",") != "first" ||
		strings.Join(journal.rolledBack, ",") != "first" ||
		len(journal.failed) != 0 {
		t.Fatalf("journal = %#v", journal)
	}
}

func TestTransactionRejectsSymlinkParent(t *testing.T) {
	home := t.TempDir()
	source := writeSource(t, filepath.Join(home, "repo", "source"), "source")
	realDirectory := filepath.Join(home, "real-config")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDirectory, filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	transaction := newTestTransaction(t, home)
	plan, err := transaction.Plan([]model.LinkSpec{
		linkSpec(
			"unsafe",
			source,
			filepath.Join(home, ".config", "app", "config"),
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ready || len(plan.Blockers) != 1 ||
		!strings.Contains(plan.Blockers[0].Detail, "not a real directory") {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestTransactionRefusesSourceChangeBeforeMutation(t *testing.T) {
	home := t.TempDir()
	source := writeSource(t, filepath.Join(home, "repo", "source"), "original")
	target := filepath.Join(home, ".config", "target")
	specs := []model.LinkSpec{linkSpec("source", source, target)}
	transaction := newTestTransaction(t, home)
	plan, err := transaction.Plan(specs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := transaction.Apply(context.Background(), plan, specs, nil)
	if err == nil || result.RolledBack {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("target was mutated: %v", err)
	}
}

func newTestTransaction(t *testing.T, home string) *Transaction {
	t.Helper()
	transaction, err := NewTransaction(
		home,
		filepath.Join(home, ".local", "state", "envctl", "backups"),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction.now = func() time.Time {
		return time.Date(2026, 7, 28, 18, 0, 0, 0, time.UTC)
	}
	return transaction
}

func writeSource(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func linkSpec(id, source, target string) model.LinkSpec {
	raw, err := os.ReadFile(source)
	if err != nil {
		panic(err)
	}
	return model.LinkSpec{
		ID: id, Source: source, Target: target, Kind: model.LinkKindFile,
		Digest: fmt.Sprintf("%x", sha256.Sum256(raw)),
	}
}
