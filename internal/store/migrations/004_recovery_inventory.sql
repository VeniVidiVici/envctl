CREATE TABLE recovery_inventory_items (
    snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    recovery_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    target_path TEXT NOT NULL,
    status TEXT NOT NULL,
    detail TEXT NOT NULL,
    PRIMARY KEY (snapshot_id, recovery_id)
);
