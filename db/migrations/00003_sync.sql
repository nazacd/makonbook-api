-- +goose Up
-- Bookkeeping for the HolliHop sync (spec 6).

CREATE TABLE sync_runs (
    id          bigserial PRIMARY KEY,
    mode        text NOT NULL CHECK (mode IN ('full', 'incremental')),
    status      text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'blocked')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    stats       jsonb NOT NULL DEFAULT '{}',
    error       text NOT NULL DEFAULT ''
);
CREATE INDEX sync_runs_started_at_idx ON sync_runs (started_at DESC);

-- Named cursors, e.g. the HolliHop "Now" value for incremental student pulls.
CREATE TABLE sync_cursors (
    name       text PRIMARY KEY,
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE sync_cursors;
DROP TABLE sync_runs;
