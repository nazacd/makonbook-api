-- +goose Up
-- Mako's room (spec 28): what a student placed in each slot of the room, and
-- the unlocked items they have already been shown. Cosmetic only. The items
-- and what unlocks them live in code (internal/gamification/room.go); a
-- stored item that is unknown or no longer unlocked reads as the slot's
-- default.
CREATE TABLE room_layouts (
    student_client_id integer PRIMARY KEY REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    slots             jsonb NOT NULL DEFAULT '{}',   -- slot -> item code
    seen              text[] NOT NULL DEFAULT '{}',  -- item codes shown as new already
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE room_layouts;
