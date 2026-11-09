-- +goose Up
-- Logos of the roadmaps' university stops (spec 22.7), used with the
-- universities' permission. They are trademarks, so they are never
-- committed: `makonbook curriculum import` loads them from logos/ next to
-- the curriculum files.
CREATE TABLE university_logos (
    slug         text PRIMARY KEY,                 -- roadmap.Slug of the university's name
    content_type text NOT NULL CHECK (content_type IN ('image/svg+xml', 'image/png', 'image/webp')),
    body         bytea NOT NULL CHECK (octet_length(body) <= 1048576),
    sha256       text NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE university_logos;
