-- +goose Up
-- The version of the picture page (web/admin's src/card) each post was drawn
-- with. When an update changes the page, the bot draws its open posts again
-- and replaces their photos in place.
ALTER TABLE group_posts ADD COLUMN picture_version text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE group_posts DROP COLUMN picture_version;
