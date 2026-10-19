-- +goose Up
-- A green question whose AI review proposed a fix gets the fix applied as
-- its own version (spec 9.2), so the history shows what the AI changed and
-- an editor can undo it.
ALTER TABLE question_versions DROP CONSTRAINT question_versions_source_check;
ALTER TABLE question_versions ADD CONSTRAINT question_versions_source_check CHECK (source IN ('import', 'ai_fix', 'editor'));

-- +goose Down
DELETE FROM question_versions WHERE source = 'ai_fix';
ALTER TABLE question_versions DROP CONSTRAINT question_versions_source_check;
ALTER TABLE question_versions ADD CONSTRAINT question_versions_source_check CHECK (source IN ('import', 'editor'));
