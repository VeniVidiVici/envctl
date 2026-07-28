package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VeniVidiVici/envctl/internal/model"
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
	if count != 2 {
		t.Fatalf("migration count = %d, want 2", count)
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
