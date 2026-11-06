-- +goose Up
-- Registered students (signed up themselves, spec 21) take only the practice
-- tests open to all: the course's first ten days. The others stay for SAT
-- MAKON's students (enrolled), who see every test as before. The importer
-- opens new tests DAY1 … DAY10 the same way (practice.OpenToAll).
ALTER TABLE practice_tests ADD COLUMN open_to_all boolean NOT NULL DEFAULT false;
UPDATE practice_tests SET open_to_all = true
WHERE upper(code) IN ('DAY1', 'DAY2', 'DAY3', 'DAY4', 'DAY5', 'DAY6', 'DAY7', 'DAY8', 'DAY9', 'DAY10');

-- +goose Down
ALTER TABLE practice_tests DROP COLUMN open_to_all;
