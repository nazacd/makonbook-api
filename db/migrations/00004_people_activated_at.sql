-- +goose Up
-- Start of each person's current active period: set when first stored and
-- again whenever they are reactivated. Auth uses it to discard passwords and
-- sessions from an earlier period (spec 6.2: a returning student starts over
-- with the default password).
ALTER TABLE students ADD COLUMN activated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE teachers ADD COLUMN activated_at timestamptz NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE teachers DROP COLUMN activated_at;
ALTER TABLE students DROP COLUMN activated_at;
