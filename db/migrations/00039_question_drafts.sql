-- +goose Up
-- Questions written outside the AI review (`makonbook practice add`: a
-- teacher's, or drafts for teachers to check) start as a version of their own
-- kind, so the editor's history says where they came from. They wait in
-- review until an editor approves them.
ALTER TABLE question_versions DROP CONSTRAINT question_versions_source_check;
ALTER TABLE question_versions ADD CONSTRAINT question_versions_source_check
    CHECK (source IN ('import', 'ai_fix', 'group', 'editor', 'draft'));

-- +goose Down
-- A draft's first version is relabelled as an editor's, so its question keeps a history.
UPDATE question_versions SET source = 'editor' WHERE source = 'draft';
ALTER TABLE question_versions DROP CONSTRAINT question_versions_source_check;
ALTER TABLE question_versions ADD CONSTRAINT question_versions_source_check
    CHECK (source IN ('import', 'ai_fix', 'group', 'editor'));
