-- +goose Up
-- Each group's actual lesson dates, mirrored from HolliHop (GetEdUnits with
-- queryDays) for a window around today. Unlike the weekly schedule, these
-- reflect holidays, cancellations and one-off extra lessons as the office
-- enters them. Attendance can only be taken on a lesson day (spec 8.1).
CREATE TABLE lesson_days (
    group_id          integer NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    date              date NOT NULL,
    minutes           integer NOT NULL,
    schedule_item_ids integer[] NOT NULL,   -- the schedule items falling on this day
    PRIMARY KEY (group_id, date)
);
CREATE INDEX lesson_days_date_idx ON lesson_days (date);

-- +goose Down
DROP TABLE lesson_days;
