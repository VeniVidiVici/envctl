CREATE TABLE link_inventory_items (
    snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    link_id TEXT NOT NULL,
    source_path TEXT NOT NULL,
    target_path TEXT NOT NULL,
    source_type TEXT NOT NULL,
    source_digest TEXT NOT NULL DEFAULT '',
    target_type TEXT NOT NULL,
    link_target TEXT NOT NULL DEFAULT '',
    resolved_target TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (snapshot_id, link_id)
);
