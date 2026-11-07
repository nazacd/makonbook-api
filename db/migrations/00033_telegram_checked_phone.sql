-- +goose Up
-- A phone ticket from a Telegram bot check remembers the Telegram account
-- that shared the number, so sign-up and reset can link it (spec 21.5).
ALTER TABLE auth_tickets ADD COLUMN checked_telegram_id bigint;

-- +goose Down
ALTER TABLE auth_tickets DROP COLUMN checked_telegram_id;
