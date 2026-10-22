-- +goose Up
-- Vocabulary (spec 11): SAT MAKON's word lists, studied with spaced
-- repetition (FSRS) on the phone. Every student sees every list. The phone
-- keeps an append-only log of reviews and uploads it; card states are worked
-- out from the log, so two phones never conflict.

-- Lists and words come from `makonbook vocab import`. A word keeps its id
-- across imports (found by list and word); one dropped from the file is
-- marked removed, never deleted, because reviews point to it.
CREATE TABLE vocab_lists (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text NOT NULL UNIQUE,           -- the file's "list", e.g. sat-1
    title      text NOT NULL,
    position   integer NOT NULL,               -- order in the file
    removed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE vocab_words (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    list_id        uuid NOT NULL REFERENCES vocab_lists,
    position       integer NOT NULL,
    word           text NOT NULL,
    part_of_speech text,
    definition     text NOT NULL,
    example        text NOT NULL,
    image          text,                       -- a file in MakonBook's bucket under v/img/
    removed_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX vocab_words_list_word ON vocab_words (list_id, lower(word));

-- Images the words use, copied into MakonBook's bucket (named by their bytes).
CREATE TABLE vocab_images (
    file         text PRIMARY KEY,             -- <sha-256>.<ext>
    content_type text NOT NULL,
    bytes        integer NOT NULL,
    width        integer NOT NULL,
    height       integer NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- The deck's version: a hash of every list and word as students get them,
-- set by each import, so a phone downloads the deck again only when it changed.
CREATE TABLE vocab_deck (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    version   text NOT NULL,
    built_at  timestamptz NOT NULL DEFAULT now()
);

-- Every review, as the phone made it: the rating and the card's state after
-- it (the phone's FSRS worked it out). seq orders what the server received,
-- for phones catching up.
CREATE TABLE vocab_reviews (
    seq               bigserial PRIMARY KEY,
    id                uuid NOT NULL UNIQUE,    -- the phone's
    student_client_id integer NOT NULL REFERENCES students,
    word_id           uuid NOT NULL REFERENCES vocab_words,
    rating            smallint NOT NULL CHECK (rating BETWEEN 1 AND 4),  -- again, hard, good, easy
    reviewed_at       timestamptz NOT NULL,
    duration_ms       integer NOT NULL CHECK (duration_ms >= 0),
    state             smallint NOT NULL CHECK (state BETWEEN 0 AND 3),   -- after: new, learning, review, relearning
    due               timestamptz NOT NULL,
    stability         real NOT NULL,
    difficulty        real NOT NULL,
    scheduled_days    integer NOT NULL,
    received_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vocab_reviews_student ON vocab_reviews (student_client_id, seq);
CREATE INDEX vocab_reviews_student_word ON vocab_reviews (student_client_id, word_id, reviewed_at DESC);

-- +goose Down
DROP TABLE vocab_reviews;
DROP TABLE vocab_deck;
DROP TABLE vocab_images;
DROP TABLE vocab_words;
DROP TABLE vocab_lists;
