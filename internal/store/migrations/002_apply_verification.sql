ALTER TABLE plans
ADD COLUMN verification_snapshot_id TEXT REFERENCES snapshots(id);
