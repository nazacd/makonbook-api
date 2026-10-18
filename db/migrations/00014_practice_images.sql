-- +goose Up
-- Images editors upload for questions (spec 9.4). Kept in the database: they
-- are few and small, and so come with every backup. A question refers to one
-- as ![description](r2:mb/<hash>.<ext>), like the bank's R2 images.
CREATE TABLE practice_images (
    key          text PRIMARY KEY,             -- mb/<sha-256 of the bytes>.<ext>: the same file is stored once
    content_type text NOT NULL CHECK (content_type IN ('image/png', 'image/jpeg', 'image/gif', 'image/webp')),
    data         bytea NOT NULL,
    width        integer NOT NULL,
    height       integer NOT NULL,
    uploaded_by  text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE practice_images;
