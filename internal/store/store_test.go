package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/portablelink"
	"github.com/VeniVidiVici/envctl/internal/recovery"
)

func TestRecordsAuditPlanAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.db")
	state, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer state.Close()

	requested := true
	inventory := model.Inventory{
		CollectedAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		Collectors:  []string{"homebrew"},
		Packages: []model.InstalledPackage{{
			Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "homebrew/core", Package: "example", Version: "1.0",
			Requested: &requested,
		}},
	}
	snapshot, err := state.RecordAudit(context.Background(), MachineInfo{
		ID: "example-machine", Hostname: "example-machine",
	}, inventory)
	if err != nil {
		t.Fatalf("RecordAudit() error = %v", err)
	}
	if _, err := state.RecordAuditRun(
		context.Background(), "example-machine", snapshot.ID, false,
	); err != nil {
		t.Fatalf("RecordAuditRun() error = %v", err)
	}

	plan := model.Plan{
		Summary: model.PlanSummary{Missing: 1, Actions: 1},
		Findings: []model.Finding{{
			Status: model.FindingMissing, PackageID: "missing",
			Detail: "desired package is not installed",
		}},
		Actions: []model.Action{{
			Sequence: 1, Type: model.ActionInstall, PackageID: "missing",
			Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "homebrew/core", Package: "missing", Risk: model.RiskLow,
		}},
	}
	record, err := state.RecordPlan(
		context.Background(),
		"example-machine",
		snapshot.ID,
		"example-digest",
		"plan",
		false,
		plan,
	)
	if err != nil {
		t.Fatalf("RecordPlan() error = %v", err)
	}
	if record.PlanID == "" || record.RunID == "" {
		t.Fatalf("plan record = %#v", record)
	}

	history, err := state.History(context.Background(), 10)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(history) != 2 || history[0].ActionCount != 1 {
		t.Fatalf("history = %#v", history)
	}
	if history[0].ConfigRevision != "example-digest" {
		t.Fatalf("history config revision = %q", history[0].ConfigRevision)
	}
	if history[1].Command != "audit" || history[1].SnapshotID != snapshot.ID {
		t.Fatalf("audit history = %#v", history[1])
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestOpenAppliesMigrationsIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer second.Close()

	var count int
	if err := second.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("migration count = %d, want 4", count)
	}
}

func TestRecordsCredentialRecoveryLifecycleWithoutSecretDigests(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	machine := MachineInfo{ID: "example-machine", Hostname: "example-machine"}
	before, err := state.RecordAudit(ctx, machine, model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"credential-recovery"},
		Recoveries: []model.RecoveryFinding{{
			RecoveryID: "aws", Kind: model.RecoveryKindSOPSFile,
			Target: "/home/.aws/credentials",
			Status: model.RecoveryFindingDrifted,
			Detail: "credential target differs from the encrypted desired source",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := recovery.TransactionPlan{
		RunID: "recovery-run", Ready: true,
		Actions: []recovery.RecoveryAction{{
			Sequence: 1, RecoveryID: "aws",
			Kind:       model.RecoveryKindSOPSFile,
			Type:       recovery.ActionRestore,
			Target:     "/home/.aws/credentials",
			BackupPath: "/home/.local/state/envctl/backups/recovery/run/.aws/credentials",
		}},
	}
	record, err := state.RecordRecoveryPlan(
		ctx,
		machine.ID,
		before.ID,
		"digest",
		"recovery apply",
		false,
		plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.BeginApply(ctx, record.RunID, record.PlanID); err != nil {
		t.Fatal(err)
	}
	if err := state.StartAction(ctx, record.PlanID, 1); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordRecoveryBackup(
		ctx,
		record.PlanID,
		1,
		"/home/.aws/credentials",
		"/home/.local/state/envctl/backups/recovery/run/.aws/credentials",
	); err != nil {
		t.Fatal(err)
	}
	if err := state.FinishAction(
		ctx,
		record.PlanID,
		1,
		ActionStatusCompleted,
		"",
	); err != nil {
		t.Fatal(err)
	}
	after, err := state.RecordAudit(ctx, machine, model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"credential-recovery"},
		Recoveries: []model.RecoveryFinding{{
			RecoveryID: "aws", Kind: model.RecoveryKindSOPSFile,
			Target: "/home/.aws/credentials",
			Status: model.RecoveryFindingSatisfied,
			Detail: "credential target matches the decryptable desired source",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CompleteApply(
		ctx,
		record.RunID,
		record.PlanID,
		after.ID,
		ActionStatusCompleted,
	); err != nil {
		t.Fatal(err)
	}
	var recoveryRows, backupRows int
	var digest string
	if err := state.db.QueryRow(
		"SELECT COUNT(*) FROM recovery_inventory_items",
	).Scan(&recoveryRows); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRow(
		"SELECT COUNT(*) FROM backups",
	).Scan(&backupRows); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRow(
		"SELECT content_digest FROM backups LIMIT 1",
	).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if recoveryRows != 2 || backupRows != 1 ||
		digest != "secret-digest-intentionally-not-recorded" {
		t.Fatalf(
			"recovery rows = %d backup rows = %d digest = %q",
			recoveryRows,
			backupRows,
			digest,
		)
	}
}

func TestRecordsApplyLifecycleAndVerification(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer state.Close()
	ctx := context.Background()
	machine := MachineInfo{ID: "example-machine", Hostname: "example-machine"}
	before, err := state.RecordAudit(ctx, machine, model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"homebrew"},
	})
	if err != nil {
		t.Fatalf("RecordAudit(before) error = %v", err)
	}
	plan := model.Plan{
		Summary: model.PlanSummary{Missing: 1, Actions: 1},
		Actions: []model.Action{{
			Sequence: 1, Type: model.ActionInstall, PackageID: "example",
			Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "homebrew/core", Package: "example", Risk: model.RiskLow,
		}},
	}
	record, err := state.RecordPlan(
		ctx, machine.ID, before.ID, "digest", "apply", false, plan,
	)
	if err != nil {
		t.Fatalf("RecordPlan() error = %v", err)
	}
	if err := state.BeginApply(ctx, record.RunID, record.PlanID); err != nil {
		t.Fatalf("BeginApply() error = %v", err)
	}
	if err := state.StartAction(ctx, record.PlanID, 1); err != nil {
		t.Fatalf("StartAction() error = %v", err)
	}
	if err := state.FinishAction(
		ctx, record.PlanID, 1, ActionStatusCompleted, "",
	); err != nil {
		t.Fatalf("FinishAction() error = %v", err)
	}
	after, err := state.RecordAudit(ctx, machine, model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"homebrew"},
		Packages: []model.InstalledPackage{{
			Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "homebrew/core", Package: "example",
		}},
	})
	if err != nil {
		t.Fatalf("RecordAudit(after) error = %v", err)
	}
	if err := state.CompleteApply(
		ctx, record.RunID, record.PlanID, after.ID, ActionStatusCompleted,
	); err != nil {
		t.Fatalf("CompleteApply() error = %v", err)
	}

	var runStatus, planStatus, actionStatus, verificationID string
	if err := state.db.QueryRow(`
		SELECT r.status, p.status, a.status, p.verification_snapshot_id
		FROM runs r
		JOIN plans p ON p.run_id = r.id
		JOIN actions a ON a.plan_id = p.id
		WHERE r.id = ?
	`, record.RunID).Scan(
		&runStatus, &planStatus, &actionStatus, &verificationID,
	); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" || planStatus != "completed" ||
		actionStatus != "completed" || verificationID != after.ID {
		t.Fatalf(
			"states = run %q plan %q action %q verification %q",
			runStatus, planStatus, actionStatus, verificationID,
		)
	}
}

func TestRecordsPortableLinkLifecycleAndBackup(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	machine := MachineInfo{ID: "example-machine", Hostname: "example-machine"}
	before, err := state.RecordAudit(ctx, machine, model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"portable-link"},
		Links: []model.LinkObservation{{
			ID: "shell", Source: "/repo/.zshrc", Target: "/home/.zshrc",
			SourceType: "file", SourceDigest: "digest",
			TargetType: "symlink", LinkTarget: "/legacy/.zshrc",
			ResolvedTarget: "/legacy/.zshrc",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := portablelink.TransactionPlan{
		RunID: "link-run", Ready: true,
		Actions: []portablelink.LinkAction{{
			Sequence: 1, LinkID: "shell", Type: portablelink.ActionReplace,
			Source: "/repo/.zshrc", Target: "/home/.zshrc",
			BackupPath: "/home/.local/state/envctl/backups/run/.zshrc",
		}},
	}
	record, err := state.RecordLinkPlan(
		ctx, machine.ID, before.ID, "digest", "links apply", false, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.BeginApply(ctx, record.RunID, record.PlanID); err != nil {
		t.Fatal(err)
	}
	if err := state.StartAction(ctx, record.PlanID, 1); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordActionBackup(
		ctx, record.PlanID, 1, "/home/.zshrc",
		"/home/.local/state/envctl/backups/run/.zshrc",
		"/legacy/.zshrc",
	); err != nil {
		t.Fatal(err)
	}
	if err := state.FinishAction(
		ctx, record.PlanID, 1, ActionStatusCompleted, "",
	); err != nil {
		t.Fatal(err)
	}
	after, err := state.RecordAudit(ctx, machine, model.Inventory{
		CollectedAt: time.Now().UTC(),
		Collectors:  []string{"portable-link"},
		Links: []model.LinkObservation{{
			ID: "shell", Source: "/repo/.zshrc", Target: "/home/.zshrc",
			SourceType: "file", SourceDigest: "digest",
			TargetType: "symlink", LinkTarget: "../repo/.zshrc",
			ResolvedTarget: "/repo/.zshrc",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CompleteApply(
		ctx, record.RunID, record.PlanID, after.ID, ActionStatusCompleted,
	); err != nil {
		t.Fatal(err)
	}
	var linkRows, backupRows int
	if err := state.db.QueryRow(
		"SELECT COUNT(*) FROM link_inventory_items",
	).Scan(&linkRows); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRow(
		"SELECT COUNT(*) FROM backups",
	).Scan(&backupRows); err != nil {
		t.Fatal(err)
	}
	if linkRows != 2 || backupRows != 1 {
		t.Fatalf("link rows = %d, backup rows = %d", linkRows, backupRows)
	}
}

func TestPortableLinkRollbackRemovesStaleBackupRecord(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	machine := MachineInfo{ID: "example-machine", Hostname: "example-machine"}
	snapshot, err := state.RecordAudit(ctx, machine, model.Inventory{
		CollectedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := portablelink.TransactionPlan{
		RunID: "link-run", Ready: true,
		Actions: []portablelink.LinkAction{{
			Sequence: 1, LinkID: "shell", Type: portablelink.ActionReplace,
		}},
	}
	record, err := state.RecordLinkPlan(
		ctx, machine.ID, snapshot.ID, "digest", "links apply", false, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.BeginApply(ctx, record.RunID, record.PlanID); err != nil {
		t.Fatal(err)
	}
	if err := state.StartAction(ctx, record.PlanID, 1); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordActionBackup(
		ctx, record.PlanID, 1, "/home/.zshrc", "/backup/.zshrc", "legacy",
	); err != nil {
		t.Fatal(err)
	}
	if err := state.FinishAction(
		ctx, record.PlanID, 1, ActionStatusCompleted, "",
	); err != nil {
		t.Fatal(err)
	}
	if err := state.RollBackAction(ctx, record.PlanID, 1, "later action failed"); err != nil {
		t.Fatal(err)
	}
	var status string
	var backups int
	if err := state.db.QueryRow(
		"SELECT status FROM actions WHERE plan_id = ?",
		record.PlanID,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRow("SELECT COUNT(*) FROM backups").Scan(&backups); err != nil {
		t.Fatal(err)
	}
	if status != ActionStatusRolledBack || backups != 0 {
		t.Fatalf("status = %q, backups = %d", status, backups)
	}
}

func TestApplyLifecycleRejectsInvalidTransitions(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer state.Close()
	if err := state.BeginApply(context.Background(), "missing", "missing"); err == nil {
		t.Fatal("BeginApply() error = nil, want invalid transition")
	}
	if err := state.FinishAction(
		context.Background(), "missing", 1, "unknown", "",
	); err == nil {
		t.Fatal("FinishAction() error = nil, want invalid status")
	}
}

func TestRecordsLatestDecisionWithoutLosingHistory(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer state.Close()
	ctx := context.Background()
	if err := state.EnsureMachine(ctx, MachineInfo{
		ID: "example-machine", Hostname: "example-machine",
	}); err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if _, err := state.RecordDecision(
		ctx, "example-machine", "brew|formula|homebrew/core|example",
		"keep", "", "",
	); err != nil {
		t.Fatalf("RecordDecision(keep) error = %v", err)
	}
	if _, err := state.RecordDecision(
		ctx, "example-machine", "brew|formula|homebrew/core|example",
		"adopt", "shared", "",
	); err != nil {
		t.Fatalf("RecordDecision(adopt) error = %v", err)
	}
	decisions, err := state.LatestDecisions(ctx, "example-machine")
	if err != nil {
		t.Fatalf("LatestDecisions() error = %v", err)
	}
	if len(decisions) != 1 || decisions[0].Value != "adopt" ||
		decisions[0].Profile != "shared" {
		t.Fatalf("latest decisions = %#v", decisions)
	}

	var historyCount int
	if err := state.db.QueryRow("SELECT COUNT(*) FROM decisions").Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if historyCount != 2 {
		t.Fatalf("decision history count = %d, want 2", historyCount)
	}

	if _, err := state.RecordDecision(
		ctx, "example-machine", "brew|formula|homebrew/core|example",
		"clear", "", "",
	); err != nil {
		t.Fatalf("RecordDecision(clear) error = %v", err)
	}
	decisions, err = state.LatestDecisions(ctx, "example-machine")
	if err != nil {
		t.Fatalf("LatestDecisions() after clear error = %v", err)
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions after clear = %#v", decisions)
	}
}
