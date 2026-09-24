-- name: LockMigrations :exec
SELECT pg_advisory_xact_lock(731242001);

-- name: EnsureSchemaMigrations :exec
CREATE TABLE IF NOT EXISTS schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

-- name: MigrationApplied :one
SELECT EXISTS (
    SELECT 1 FROM schema_migrations WHERE version = sqlc.arg(version)
);

-- name: RecordMigration :exec
INSERT INTO schema_migrations(version)
VALUES (sqlc.arg(version))
ON CONFLICT DO NOTHING;
