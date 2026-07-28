PRAGMA foreign_keys = ON;

CREATE TABLE machines (
    id TEXT PRIMARY KEY,
    hostname TEXT NOT NULL,
    hardware_class TEXT NOT NULL DEFAULT '',
    os_version TEXT NOT NULL DEFAULT '',
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
);

CREATE TABLE snapshots (
    id TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL REFERENCES machines(id),
    started_at TEXT NOT NULL,
    completed_at TEXT,
    collector_version TEXT NOT NULL
);

CREATE TABLE inventory_items (
    snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    manager TEXT NOT NULL,
    kind TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    item_key TEXT NOT NULL,
    installed_version TEXT NOT NULL DEFAULT '',
    location TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (snapshot_id, manager, kind, source, item_key)
);

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL REFERENCES machines(id),
    observed_snapshot_id TEXT REFERENCES snapshots(id),
    command TEXT NOT NULL,
    config_revision TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    completed_at TEXT,
    status TEXT NOT NULL,
    interactive INTEGER NOT NULL DEFAULT 0 CHECK (interactive IN (0, 1))
);

CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    observed_snapshot_id TEXT NOT NULL REFERENCES snapshots(id),
    desired_digest TEXT NOT NULL,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL,
    summary_json TEXT NOT NULL DEFAULT '{}',
    warnings_json TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE actions (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    action_type TEXT NOT NULL,
    item_key TEXT NOT NULL,
    risk TEXT NOT NULL,
    reversible INTEGER NOT NULL CHECK (reversible IN (0, 1)),
    requires_privilege INTEGER NOT NULL CHECK (requires_privilege IN (0, 1)),
    status TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    error_summary TEXT NOT NULL DEFAULT '',
    UNIQUE (plan_id, sequence)
);

CREATE TABLE backups (
    id TEXT PRIMARY KEY,
    action_id TEXT NOT NULL REFERENCES actions(id),
    original_path TEXT NOT NULL,
    backup_path TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE health_checks (
    snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    check_key TEXT NOT NULL,
    status TEXT NOT NULL,
    summary TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (snapshot_id, check_key)
);

CREATE TABLE plan_findings (
    plan_id TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    status TEXT NOT NULL,
    package_id TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL,
    desired_json TEXT NOT NULL DEFAULT 'null',
    installed_json TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (plan_id, sequence)
);

CREATE TABLE decisions (
    id TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL REFERENCES machines(id),
    inventory_key TEXT NOT NULL,
    decision TEXT NOT NULL,
    profile TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE INDEX inventory_items_lookup
    ON inventory_items(manager, kind, source, item_key);

CREATE INDEX runs_machine_started
    ON runs(machine_id, started_at DESC);
