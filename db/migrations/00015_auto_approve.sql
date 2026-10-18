-- +goose Up
-- A question its AI review rates green (the key confirmed by both blind
-- solves, a clean transcription, no issues) is approved on import. It is
-- marked so a person's approval stays apart: an auto-approved question
-- doesn't stop a test's reload, and a worse review of a repeat, or an
-- editor's change, takes the approval back.
ALTER TABLE questions ADD COLUMN auto_approved boolean NOT NULL DEFAULT false;
ALTER TABLE questions ADD CONSTRAINT questions_auto_approved_check CHECK (NOT auto_approved OR status = 'approved');

UPDATE questions SET status = 'approved', approved_at = now(), auto_approved = true
WHERE ai_risk = 'green' AND status = 'review' AND version = 1;

-- +goose Down
UPDATE questions SET status = 'review', approved_at = NULL WHERE auto_approved;
ALTER TABLE questions DROP COLUMN auto_approved;
