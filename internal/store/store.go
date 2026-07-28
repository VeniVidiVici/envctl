package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/VeniVidiVici/envctl/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

type MachineInfo struct {
	ID            string `json:"id"`
	Hostname      string `json:"hostname"`
	HardwareClass string `json:"hardware_class,omitempty"`
	OSVersion     string `json:"os_version,omitempty"`
}

type SnapshotRecord struct {
	ID        string    `json:"id"`
	MachineID string    `json:"machine_id"`
	CreatedAt time.Time `json:"created_at"`
}

type PlanRecord struct {
	RunID     string    `json:"run_id"`
	PlanID    string    `json:"plan_id"`
	MachineID string    `json:"machine_id"`
	CreatedAt time.Time `json:"created_at"`
}

type HistoryEntry struct {
	RunID          string     `json:"run_id"`
	PlanID         string     `json:"plan_id,omitempty"`
	MachineID      string     `json:"machine_id"`
	SnapshotID     string     `json:"snapshot_id,omitempty"`
	Command        string     `json:"command"`
	ConfigRevision string     `json:"config_revision,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	Status         string     `json:"status"`
	ActionCount    int        `json:"action_count"`
}

type Decision struct {
	ID           string    `json:"id"`
	MachineID    string    `json:"machine_id"`
	InventoryKey string    `json:"inventory_key"`
	Value        string    `json:"value"`
	Profile      string    `json:"profile,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

const (
	ActionStatusProposed  = "proposed"
	ActionStatusRunning   = "running"
	ActionStatusCompleted = "completed"
	ActionStatusFailed    = "failed"
	ActionStatusSkipped   = "skipped"
)

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("state database path is empty")
	}
	if path != ":memory:" {
		directory := filepath.Dir(path)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create state directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, path: path}
	if err := store.prepare(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			db.Close()
			return nil, fmt.Errorf("secure state database permissions: %w", err)
		}
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) prepare(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to state database: %w", err)
	}
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure state database: %w", err)
		}
	}
	return s.migrate(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration journal: %w", err)
	}

	migrations, err := Migrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	entries, err := fs.ReadDir(migrations, ".")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var exists int
		err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM schema_migrations WHERE version = ?",
			entry.Name(),
		).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if exists > 0 {
			continue
		}
		sqlText, err := fs.ReadFile(migrations, entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		transaction, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err := transaction.ExecContext(ctx, string(sqlText)); err != nil {
			transaction.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
			entry.Name(), formatTime(time.Now()),
		); err != nil {
			transaction.Rollback()
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) RecordAudit(
	ctx context.Context,
	machine MachineInfo,
	inventory model.Inventory,
) (SnapshotRecord, error) {
	if machine.ID == "" {
		return SnapshotRecord{}, errors.New("machine id is required")
	}
	if machine.Hostname == "" {
		machine.Hostname = machine.ID
	}
	createdAt := inventory.CollectedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	snapshotID, err := newID()
	if err != nil {
		return SnapshotRecord{}, err
	}
	snapshot := SnapshotRecord{
		ID:        snapshotID,
		MachineID: machine.ID,
		CreatedAt: createdAt,
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SnapshotRecord{}, fmt.Errorf("begin audit record: %w", err)
	}
	defer transaction.Rollback()

	now := formatTime(createdAt)
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO machines(
			id, hostname, hardware_class, os_version, first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname = excluded.hostname,
			hardware_class = excluded.hardware_class,
			os_version = excluded.os_version,
			last_seen_at = excluded.last_seen_at
	`, machine.ID, machine.Hostname, machine.HardwareClass, machine.OSVersion, now, now); err != nil {
		return SnapshotRecord{}, fmt.Errorf("record machine: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO snapshots(
			id, machine_id, started_at, completed_at, collector_version
		) VALUES (?, ?, ?, ?, ?)
	`, snapshot.ID, machine.ID, now, now, "envctl-development"); err != nil {
		return SnapshotRecord{}, fmt.Errorf("record snapshot: %w", err)
	}

	for _, item := range inventory.Packages {
		metadata, err := json.Marshal(map[string]any{
			"requested":   item.Requested,
			"application": item.Application,
		})
		if err != nil {
			return SnapshotRecord{}, fmt.Errorf("encode inventory metadata: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO inventory_items(
				snapshot_id, manager, kind, source, item_key, installed_version,
				location, status, metadata_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, snapshot.ID, item.Manager, item.Kind, item.Source, item.Package,
			item.Version, item.Application, "installed", string(metadata)); err != nil {
			return SnapshotRecord{}, fmt.Errorf("record inventory item %s: %w", item.Package, err)
		}
	}
	for _, collectorError := range inventory.Errors {
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO health_checks(
				snapshot_id, check_key, status, summary, metadata_json
			) VALUES (?, ?, ?, ?, '{}')
		`, snapshot.ID, "collector."+collectorError.Collector, "error",
			collectorError.Message); err != nil {
			return SnapshotRecord{}, fmt.Errorf("record collector failure: %w", err)
		}
	}

	if err := transaction.Commit(); err != nil {
		return SnapshotRecord{}, fmt.Errorf("commit audit record: %w", err)
	}
	return snapshot, nil
}

func (s *Store) EnsureMachine(ctx context.Context, machine MachineInfo) error {
	if machine.ID == "" {
		return errors.New("machine id is required")
	}
	if machine.Hostname == "" {
		machine.Hostname = machine.ID
	}
	now := formatTime(time.Now())
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO machines(
			id, hostname, hardware_class, os_version, first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname = excluded.hostname,
			hardware_class = excluded.hardware_class,
			os_version = excluded.os_version,
			last_seen_at = excluded.last_seen_at
	`, machine.ID, machine.Hostname, machine.HardwareClass, machine.OSVersion, now, now); err != nil {
		return fmt.Errorf("ensure machine: %w", err)
	}
	return nil
}

func (s *Store) RecordPlan(
	ctx context.Context,
	machineID, snapshotID, configDigest, command string,
	interactive bool,
	plan model.Plan,
) (PlanRecord, error) {
	if machineID == "" || snapshotID == "" {
		return PlanRecord{}, errors.New("machine and snapshot ids are required")
	}
	now := time.Now().UTC()
	runID, err := newID()
	if err != nil {
		return PlanRecord{}, err
	}
	planID, err := newID()
	if err != nil {
		return PlanRecord{}, err
	}
	record := PlanRecord{
		RunID:     runID,
		PlanID:    planID,
		MachineID: machineID,
		CreatedAt: now,
	}
	summaryJSON, err := json.Marshal(plan.Summary)
	if err != nil {
		return PlanRecord{}, fmt.Errorf("encode plan summary: %w", err)
	}
	warningsJSON, err := json.Marshal(plan.Warnings)
	if err != nil {
		return PlanRecord{}, fmt.Errorf("encode plan warnings: %w", err)
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanRecord{}, fmt.Errorf("begin plan record: %w", err)
	}
	defer transaction.Rollback()

	timestamp := formatTime(now)
	interactiveValue := 0
	if interactive {
		interactiveValue = 1
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO runs(
			id, machine_id, observed_snapshot_id, command, config_revision,
			started_at, completed_at, status, interactive
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.RunID, machineID, snapshotID, command, configDigest, timestamp, timestamp,
		"planned", interactiveValue); err != nil {
		return PlanRecord{}, fmt.Errorf("record plan run: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO plans(
			id, run_id, observed_snapshot_id, desired_digest, created_at, status,
			summary_json, warnings_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, record.PlanID, record.RunID, snapshotID, configDigest, timestamp, "ready",
		string(summaryJSON), string(warningsJSON)); err != nil {
		return PlanRecord{}, fmt.Errorf("record plan: %w", err)
	}

	for index, finding := range plan.Findings {
		desiredJSON, err := json.Marshal(finding.Desired)
		if err != nil {
			return PlanRecord{}, fmt.Errorf("encode finding desired state: %w", err)
		}
		installedJSON, err := json.Marshal(finding.Installed)
		if err != nil {
			return PlanRecord{}, fmt.Errorf("encode finding installed state: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO plan_findings(
				plan_id, sequence, status, package_id, detail,
				desired_json, installed_json
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, record.PlanID, index+1, finding.Status, finding.PackageID,
			finding.Detail, string(desiredJSON), string(installedJSON)); err != nil {
			return PlanRecord{}, fmt.Errorf("record plan finding: %w", err)
		}
	}
	for _, action := range plan.Actions {
		actionID, err := newID()
		if err != nil {
			return PlanRecord{}, err
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO actions(
				id, plan_id, sequence, action_type, item_key, risk, reversible,
				requires_privilege, status, error_summary
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '')
		`, actionID, record.PlanID, action.Sequence, action.Type, action.Package,
			action.Risk, boolInt(action.Reversible), boolInt(action.RequiresPrivilege),
			"proposed"); err != nil {
			return PlanRecord{}, fmt.Errorf("record plan action: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return PlanRecord{}, fmt.Errorf("commit plan record: %w", err)
	}
	return record, nil
}

func (s *Store) BeginApply(ctx context.Context, runID, planID string) error {
	if runID == "" || planID == "" {
		return errors.New("run and plan ids are required")
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin apply transition: %w", err)
	}
	defer transaction.Rollback()

	result, err := transaction.ExecContext(ctx, `
		UPDATE runs
		SET status = 'applying', completed_at = NULL
		WHERE id = ? AND status = 'planned'
	`, runID)
	if err != nil {
		return fmt.Errorf("mark apply run active: %w", err)
	}
	if err := requireOneRow(result, "run is not in planned state"); err != nil {
		return err
	}
	result, err = transaction.ExecContext(ctx, `
		UPDATE plans
		SET status = 'applying'
		WHERE id = ? AND run_id = ? AND status = 'ready'
	`, planID, runID)
	if err != nil {
		return fmt.Errorf("mark plan active: %w", err)
	}
	if err := requireOneRow(result, "plan is not in ready state"); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit apply transition: %w", err)
	}
	return nil
}

func (s *Store) StartAction(ctx context.Context, planID string, sequence int) error {
	if planID == "" || sequence <= 0 {
		return errors.New("plan id and positive action sequence are required")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE actions
		SET status = ?, started_at = ?, completed_at = NULL, error_summary = ''
		WHERE plan_id = ? AND sequence = ? AND status = ?
	`, ActionStatusRunning, formatTime(time.Now()), planID, sequence,
		ActionStatusProposed)
	if err != nil {
		return fmt.Errorf("start action %d: %w", sequence, err)
	}
	if err := requireOneRow(result, "action is not in proposed state"); err != nil {
		return fmt.Errorf("start action %d: %w", sequence, err)
	}
	return nil
}

func (s *Store) FinishAction(
	ctx context.Context,
	planID string,
	sequence int,
	status, errorSummary string,
) error {
	if planID == "" || sequence <= 0 {
		return errors.New("plan id and positive action sequence are required")
	}
	switch status {
	case ActionStatusCompleted, ActionStatusFailed, ActionStatusSkipped:
	default:
		return fmt.Errorf("unsupported terminal action status %q", status)
	}
	if len(errorSummary) > 1000 {
		errorSummary = errorSummary[:1000]
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE actions
		SET status = ?, completed_at = ?, error_summary = ?
		WHERE plan_id = ? AND sequence = ? AND status = ?
	`, status, formatTime(time.Now()), errorSummary, planID, sequence,
		ActionStatusRunning)
	if err != nil {
		return fmt.Errorf("finish action %d: %w", sequence, err)
	}
	if err := requireOneRow(result, "action is not in running state"); err != nil {
		return fmt.Errorf("finish action %d: %w", sequence, err)
	}
	return nil
}

func (s *Store) SkipAction(
	ctx context.Context,
	planID string,
	sequence int,
	reason string,
) error {
	if planID == "" || sequence <= 0 {
		return errors.New("plan id and positive action sequence are required")
	}
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE actions
		SET status = ?, completed_at = ?, error_summary = ?
		WHERE plan_id = ? AND sequence = ? AND status = ?
	`, ActionStatusSkipped, formatTime(time.Now()), reason, planID, sequence,
		ActionStatusProposed)
	if err != nil {
		return fmt.Errorf("skip action %d: %w", sequence, err)
	}
	if err := requireOneRow(result, "action is not in proposed state"); err != nil {
		return fmt.Errorf("skip action %d: %w", sequence, err)
	}
	return nil
}

func (s *Store) CompleteApply(
	ctx context.Context,
	runID, planID, verificationSnapshotID, status string,
) error {
	if runID == "" || planID == "" {
		return errors.New("run and plan ids are required")
	}
	switch status {
	case ActionStatusCompleted, ActionStatusFailed:
	default:
		return fmt.Errorf("unsupported apply status %q", status)
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin apply completion: %w", err)
	}
	defer transaction.Rollback()

	result, err := transaction.ExecContext(ctx, `
		UPDATE plans
		SET status = ?, verification_snapshot_id = NULLIF(?, '')
		WHERE id = ? AND run_id = ? AND status = 'applying'
	`, status, verificationSnapshotID, planID, runID)
	if err != nil {
		return fmt.Errorf("complete plan: %w", err)
	}
	if err := requireOneRow(result, "plan is not in applying state"); err != nil {
		return err
	}
	result, err = transaction.ExecContext(ctx, `
		UPDATE runs
		SET status = ?, completed_at = ?
		WHERE id = ? AND status = 'applying'
	`, status, formatTime(time.Now()), runID)
	if err != nil {
		return fmt.Errorf("complete apply run: %w", err)
	}
	if err := requireOneRow(result, "run is not in applying state"); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit apply completion: %w", err)
	}
	return nil
}

func (s *Store) RecordAuditRun(
	ctx context.Context,
	machineID, snapshotID string,
	interactive bool,
) (string, error) {
	if machineID == "" || snapshotID == "" {
		return "", errors.New("machine and snapshot ids are required")
	}
	runID, err := newID()
	if err != nil {
		return "", err
	}
	interactiveValue := 0
	if interactive {
		interactiveValue = 1
	}
	timestamp := formatTime(time.Now())
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO runs(
			id, machine_id, observed_snapshot_id, command, config_revision,
			started_at, completed_at, status, interactive
		) VALUES (?, ?, ?, ?, '', ?, ?, ?, ?)
	`, runID, machineID, snapshotID, "audit", timestamp, timestamp,
		"completed", interactiveValue); err != nil {
		return "", fmt.Errorf("record audit run: %w", err)
	}
	return runID, nil
}

func (s *Store) History(ctx context.Context, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			r.id,
			COALESCE(p.id, ''),
			r.machine_id,
			COALESCE(r.observed_snapshot_id, ''),
			r.command,
			r.config_revision,
			r.started_at,
			r.completed_at,
			r.status,
			COUNT(a.id)
		FROM runs r
		LEFT JOIN plans p ON p.run_id = r.id
		LEFT JOIN actions a ON a.plan_id = p.id
		GROUP BY r.id, p.id
		ORDER BY r.started_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query run history: %w", err)
	}
	defer rows.Close()

	var history []HistoryEntry
	for rows.Next() {
		var entry HistoryEntry
		var startedAt string
		var completedAt sql.NullString
		if err := rows.Scan(
			&entry.RunID,
			&entry.PlanID,
			&entry.MachineID,
			&entry.SnapshotID,
			&entry.Command,
			&entry.ConfigRevision,
			&startedAt,
			&completedAt,
			&entry.Status,
			&entry.ActionCount,
		); err != nil {
			return nil, fmt.Errorf("scan run history: %w", err)
		}
		entry.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
		if err != nil {
			return nil, fmt.Errorf("parse run start time: %w", err)
		}
		if completedAt.Valid {
			value, err := time.Parse(time.RFC3339Nano, completedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse run completion time: %w", err)
			}
			entry.CompletedAt = &value
		}
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read run history: %w", err)
	}
	return history, nil
}

func (s *Store) RecordDecision(
	ctx context.Context,
	machineID, inventoryKey, value, profile, reason string,
) (Decision, error) {
	if machineID == "" || inventoryKey == "" {
		return Decision{}, errors.New("machine and inventory keys are required")
	}
	if !validDecision(value) {
		return Decision{}, fmt.Errorf("unsupported decision %q", value)
	}
	id, err := newID()
	if err != nil {
		return Decision{}, err
	}
	decision := Decision{
		ID:           id,
		MachineID:    machineID,
		InventoryKey: inventoryKey,
		Value:        value,
		Profile:      profile,
		Reason:       reason,
		CreatedAt:    time.Now().UTC(),
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO decisions(
			id, machine_id, inventory_key, decision, profile, reason, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, decision.ID, decision.MachineID, decision.InventoryKey, decision.Value,
		decision.Profile, decision.Reason, formatTime(decision.CreatedAt)); err != nil {
		return Decision{}, fmt.Errorf("record decision: %w", err)
	}
	return decision, nil
}

func (s *Store) LatestDecisions(ctx context.Context, machineID string) ([]Decision, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, machine_id, inventory_key, decision, profile, reason, created_at
		FROM (
			SELECT
				id,
				machine_id,
				inventory_key,
				decision,
				profile,
				reason,
				created_at,
				ROW_NUMBER() OVER (
					PARTITION BY machine_id, inventory_key
					ORDER BY created_at DESC, rowid DESC
				) AS decision_rank
			FROM decisions
			WHERE (? = '' OR machine_id = ?)
		)
		WHERE decision_rank = 1 AND decision != 'clear'
		ORDER BY machine_id, inventory_key
	`, machineID, machineID)
	if err != nil {
		return nil, fmt.Errorf("query latest decisions: %w", err)
	}
	defer rows.Close()

	decisions := make([]Decision, 0)
	for rows.Next() {
		var decision Decision
		var createdAt string
		if err := rows.Scan(
			&decision.ID,
			&decision.MachineID,
			&decision.InventoryKey,
			&decision.Value,
			&decision.Profile,
			&decision.Reason,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan decision: %w", err)
		}
		decision.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse decision time: %w", err)
		}
		decisions = append(decisions, decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read decisions: %w", err)
	}
	return decisions, nil
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate state id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func requireOneRow(result sql.Result, message string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect state transition: %w", err)
	}
	if count != 1 {
		return errors.New(message)
	}
	return nil
}

func validDecision(value string) bool {
	switch value {
	case "keep", "ignore", "adopt", "remove", "clear":
		return true
	default:
		return false
	}
}
