-- name: CreateRun :one
INSERT INTO sync_runs (mode, status) VALUES (@mode, 'running')
RETURNING id;

-- name: FinishRun :exec
UPDATE sync_runs
SET status = @status, finished_at = now(), stats = @stats, error = @error
WHERE id = @id;

-- name: PreviousRunStatus :one
-- Status of the latest finished run before this one; used to alert only when
-- the outcome changes, not on every failing run.
SELECT status FROM sync_runs
WHERE id < @id AND status <> 'running'
ORDER BY id DESC
LIMIT 1;

-- name: LastSuccessfulFullRun :one
SELECT coalesce(max(started_at), '-infinity'::timestamptz)::timestamptz
FROM sync_runs
WHERE mode = 'full' AND status = 'succeeded';

-- name: GetCursor :one
SELECT value FROM sync_cursors WHERE name = @name;

-- name: SetCursor :exec
INSERT INTO sync_cursors (name, value) VALUES (@name, @value)
ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- name: TryLock :one
SELECT pg_try_advisory_lock(@key::bigint);

-- name: Unlock :one
SELECT pg_advisory_unlock(@key::bigint);
