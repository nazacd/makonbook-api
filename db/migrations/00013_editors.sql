-- +goose Up
-- Editors of practice content (spec 9.4, D21): trusted teachers (or staff)
-- that super admins choose. Editing needs no branches, so someone may be an
-- editor only.
ALTER TABLE admin_access ADD COLUMN editor boolean NOT NULL DEFAULT false;
ALTER TABLE admin_access DROP CONSTRAINT admin_access_check;
ALTER TABLE admin_access ADD CONSTRAINT admin_access_check CHECK (super OR editor OR cardinality(office_ids) > 0);

-- +goose Down
DELETE FROM admin_access WHERE NOT super AND cardinality(office_ids) = 0;
ALTER TABLE admin_access DROP CONSTRAINT admin_access_check;
ALTER TABLE admin_access ADD CONSTRAINT admin_access_check CHECK (super OR cardinality(office_ids) > 0);
ALTER TABLE admin_access DROP COLUMN editor;
