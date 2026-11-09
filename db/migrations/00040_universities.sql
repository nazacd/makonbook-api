-- +goose Up
-- Universities (spec 24): the dream-university globe in onboarding and the
-- university section. Imported from the private makonbook-universities
-- repository with `makonbook universities import`, never committed here.
-- QS's rank is kept as the data gives it; the logos are in MakonBook's R2
-- bucket (logo_key), served by the API.
CREATE TABLE universities (
    id           text PRIMARY KEY CHECK (id ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name         text NOT NULL,
    short_name   text,
    country      text NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    city         text,
    lat          double precision CHECK (lat BETWEEN -90 AND 90),
    lng          double precision CHECK (lng BETWEEN -180 AND 180),
    qs_rank      integer CHECK (qs_rank > 0),
    qs_display   text,
    list_reason  text NOT NULL CHECK (list_reason IN ('qs', 'uzbekistan', 'roadmap')),
    sat          jsonb,     -- admissions.sat of the record: policy, percentiles, minimum
    details      jsonb,     -- the whole record (spec 24.1), null while the university is only listed
    logo_key     text,      -- in the uploads bucket: universities/logos/<sha256>.<ext>
    logo_type    text CHECK (logo_type IN ('image/svg+xml', 'image/png')),
    logo_sha256  text,
    listed       boolean NOT NULL DEFAULT true,  -- false: gone from the data; kept for students who chose it
    position     integer NOT NULL DEFAULT 0,     -- the order of data/list.jsonl
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK ((lat IS NULL) = (lng IS NULL)),
    CHECK ((logo_key IS NULL) = (logo_type IS NULL) AND (logo_key IS NULL) = (logo_sha256 IS NULL))
);

CREATE TABLE university_countries (
    code        text PRIMARY KEY CHECK (code ~ '^[A-Z]{2}$'),
    details     jsonb NOT NULL,  -- the record of data/countries.jsonl (spec 24.1)
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- A student's dream university (spec 22.3, 24.3).
ALTER TABLE student_goals ADD COLUMN university_id text REFERENCES universities ON DELETE SET NULL ON UPDATE CASCADE;

-- +goose Down
ALTER TABLE student_goals DROP COLUMN university_id;
DROP TABLE university_countries;
DROP TABLE universities;
