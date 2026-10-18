-- +goose Up
-- Images editors upload for questions (spec 9.4). The files are in
-- MakonBook's own R2 bucket (not SAT MAKON's); this records each one. A
-- question refers to one as ![description](r2:mb/<hash>.<ext>), like the
-- bank's images.
CREATE TABLE practice_images (
    key          text PRIMARY KEY,             -- mb/<sha-256 of the bytes>.<ext>: also the object key in the bucket
    content_type text NOT NULL CHECK (content_type IN ('image/png', 'image/jpeg', 'image/gif', 'image/webp')),
    bytes        integer NOT NULL,
    width        integer NOT NULL,
    height       integer NOT NULL,
    uploaded_by  text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE practice_images;
