-- +goose Up
-- A person who uploaded or removed their photo themselves (PUT/DELETE
-- /v1/me/photo) chose it: the automatic copy of the Google or Telegram
-- picture never adds or replaces one after that (spec 21.5).
ALTER TABLE credentials ADD COLUMN photo_chosen boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE credentials DROP COLUMN photo_chosen;
