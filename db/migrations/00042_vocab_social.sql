-- +goose Up
-- Vocabulary, social (spec 11.4–11.6): the word games' results, decks students
-- make (kept private or shared with everyone, and moderated), and the
-- monthly leaderboard worked out from both.

-- A finished round of a word game, sent by the app (its id makes a repeat
-- harmless). points: what it adds to the leaderboard, worked out here when
-- it arrives (a day's games add at most 100).
CREATE TABLE vocab_game_results (
    id                uuid PRIMARY KEY,         -- the app's
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE,
    game              text NOT NULL CHECK (game IN ('pairs', 'unscramble', 'gap')),
    score             integer NOT NULL CHECK (score >= 0),
    total             integer NOT NULL CHECK (total > 0 AND score <= total),
    mistakes          integer NOT NULL DEFAULT 0 CHECK (mistakes >= 0),
    duration_ms       integer NOT NULL CHECK (duration_ms >= 0),
    missed            uuid[] NOT NULL DEFAULT '{}',   -- word ids to look at again
    played_at         timestamptz NOT NULL,
    points            integer NOT NULL CHECK (points >= 0),
    received_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vocab_game_results_student ON vocab_game_results (student_client_id, played_at DESC);
CREATE INDEX vocab_game_results_played ON vocab_game_results (played_at);
CREATE INDEX vocab_reviews_reviewed ON vocab_reviews (reviewed_at);

-- A deck a student made. Private until published; a published deck is in
-- the community for anyone to look inside and add. A teacher (or three
-- reports from different students) hides it from everyone but its owner.
CREATE TABLE vocab_user_decks (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_client_id  integer NOT NULL REFERENCES students ON DELETE CASCADE,
    title            text NOT NULL,
    description      text NOT NULL DEFAULT '',
    published_at     timestamptz,
    hidden_at        timestamptz,
    hidden_by        integer,                  -- the teacher; null: reports
    hidden_reason    text,
    deleted_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vocab_user_decks_owner ON vocab_user_decks (owner_client_id) WHERE deleted_at IS NULL;
CREATE INDEX vocab_user_decks_community ON vocab_user_decks (published_at DESC)
    WHERE published_at IS NOT NULL AND hidden_at IS NULL AND deleted_at IS NULL;

-- Its words. A word keeps its id while it stays in the deck; one taken out
-- is marked removed, never deleted, because reviews point to it.
CREATE TABLE vocab_user_words (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    deck_id        uuid NOT NULL REFERENCES vocab_user_decks ON DELETE CASCADE,
    position       integer NOT NULL,
    word           text NOT NULL,
    part_of_speech text,
    definition     text NOT NULL,
    example        text NOT NULL DEFAULT '',
    removed_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vocab_user_words_deck ON vocab_user_words (deck_id, position) WHERE removed_at IS NULL;

-- Community decks a student added to their own (followed: the owner's edits reach them).
CREATE TABLE vocab_deck_adds (
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE,
    deck_id           uuid NOT NULL REFERENCES vocab_user_decks ON DELETE CASCADE,
    added_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (student_client_id, deck_id)
);
CREATE INDEX vocab_deck_adds_deck ON vocab_deck_adds (deck_id);

-- A student's report of a community deck (one each).
CREATE TABLE vocab_deck_reports (
    deck_id           uuid NOT NULL REFERENCES vocab_user_decks ON DELETE CASCADE,
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE,
    reason            text NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    resolved_at       timestamptz,             -- a teacher kept the deck up
    PRIMARY KEY (deck_id, student_client_id)
);

-- Reviews may now be of a student's deck's words too.
ALTER TABLE vocab_reviews DROP CONSTRAINT vocab_reviews_word_id_fkey;

-- +goose Down
DELETE FROM vocab_reviews r WHERE NOT EXISTS (SELECT 1 FROM vocab_words w WHERE w.id = r.word_id);
ALTER TABLE vocab_reviews ADD CONSTRAINT vocab_reviews_word_id_fkey FOREIGN KEY (word_id) REFERENCES vocab_words;
DROP TABLE vocab_deck_reports;
DROP TABLE vocab_deck_adds;
DROP TABLE vocab_user_words;
DROP TABLE vocab_user_decks;
DROP INDEX vocab_reviews_reviewed;
DROP TABLE vocab_game_results;
