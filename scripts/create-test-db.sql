-- Runs once when the local Docker database is first created.
-- Integration tests connect here and create a throwaway database per test,
-- so the role needs CREATEDB.
ALTER ROLE makonbook CREATEDB;
CREATE DATABASE makonbook_test OWNER makonbook;
