-- +goose Up
-- The teachers' Telegram group votes on the AI's fixes (spec 9.4). The bot
-- (makonbook review-bot) posts each question that isn't green as two
-- pictures, the bank's original and the AI's fix, with Approve and Deny.

-- The bot's settings and progress: one row.
CREATE TABLE review_bot (
    id               boolean PRIMARY KEY DEFAULT true CHECK (id),
    paused           boolean NOT NULL DEFAULT false,
    red_approvals    smallint NOT NULL DEFAULT 10 CHECK (red_approvals > 0),
    red_denials      smallint NOT NULL DEFAULT 5 CHECK (red_denials > 0),
    yellow_approvals smallint NOT NULL DEFAULT 5 CHECK (yellow_approvals > 0),
    yellow_denials   smallint NOT NULL DEFAULT 5 CHECK (yellow_denials > 0),
    max_open         smallint NOT NULL DEFAULT 20 CHECK (max_open > 0),
    update_offset    bigint NOT NULL DEFAULT 0, -- Telegram's getUpdates offset
    last_post_at     timestamptz,               -- the hourly post's last run
    last_topup_on    date                       -- the day 13:00's top-up ran
);
INSERT INTO review_bot DEFAULT VALUES;

-- A question posted to the group, at one version.
CREATE TABLE group_posts (
    id          bigserial PRIMARY KEY,
    question_id uuid NOT NULL REFERENCES questions ON DELETE CASCADE,
    version     integer NOT NULL,                  -- the version the pictures show
    test_code   text NOT NULL,                     -- the review it came from
    qid         text NOT NULL,
    risk        text NOT NULL CHECK (risk IN ('red', 'yellow')),
    caption     text NOT NULL,                     -- the vote message's head (Telegram HTML)
    chat_id     bigint NOT NULL,
    photo_ids   bigint[] NOT NULL,                 -- the pictures' messages
    vote_id     bigint NOT NULL,                   -- the message with the buttons
    status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'approved', 'denied', 'closed')),
    approvals   integer NOT NULL DEFAULT 0,
    denials     integer NOT NULL DEFAULT 0,
    posted_at   timestamptz NOT NULL DEFAULT now(),
    decided_at  timestamptz,
    CHECK ((status = 'open') = (decided_at IS NULL))
);
CREATE INDEX group_posts_question ON group_posts (question_id);
CREATE UNIQUE INDEX group_posts_one_open ON group_posts (question_id) WHERE status = 'open';

-- One vote per Telegram account and post; a teacher may change it until the decision.
CREATE TABLE group_votes (
    post_id  bigint NOT NULL REFERENCES group_posts ON DELETE CASCADE,
    user_id  bigint NOT NULL,
    name     text NOT NULL,
    approve  boolean NOT NULL,
    voted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (post_id, user_id)
);

-- Why a teacher denied, asked in a private chat with the bot.
CREATE TABLE group_deny_reasons (
    id         bigserial PRIMARY KEY,
    post_id    bigint NOT NULL REFERENCES group_posts ON DELETE CASCADE,
    user_id    bigint NOT NULL,
    name       text NOT NULL,
    reason     text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX group_deny_reasons_post ON group_deny_reasons (post_id);

-- A teacher the bot asked why, waiting for the answer.
CREATE TABLE group_reason_requests (
    user_id  bigint PRIMARY KEY,
    post_id  bigint NOT NULL REFERENCES group_posts ON DELETE CASCADE,
    asked_at timestamptz NOT NULL DEFAULT now()
);

-- The group's approval applies the fix as a version of its own.
ALTER TABLE question_versions DROP CONSTRAINT question_versions_source_check;
ALTER TABLE question_versions ADD CONSTRAINT question_versions_source_check
    CHECK (source IN ('import', 'ai_fix', 'group', 'editor'));

-- +goose Down
DELETE FROM question_versions WHERE source = 'group';
ALTER TABLE question_versions DROP CONSTRAINT question_versions_source_check;
ALTER TABLE question_versions ADD CONSTRAINT question_versions_source_check CHECK (source IN ('import', 'ai_fix', 'editor'));
DROP TABLE group_reason_requests;
DROP TABLE group_deny_reasons;
DROP TABLE group_votes;
DROP TABLE group_posts;
DROP TABLE review_bot;
