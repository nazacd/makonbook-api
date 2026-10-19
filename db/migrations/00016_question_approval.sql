-- +goose Up
-- Who approved a question in the editor (spec 9.4), until the Telegram
-- reviewers come. NULL while it isn't approved, or when the AI review did.
ALTER TABLE questions ADD COLUMN approved_by text;
ALTER TABLE questions ADD CONSTRAINT questions_approved_by_check CHECK (approved_by IS NULL OR (status = 'approved' AND NOT auto_approved));

-- +goose Down
ALTER TABLE questions DROP COLUMN approved_by;
