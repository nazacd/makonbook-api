-- +goose Up
-- The group review posts steadily, one question an hour, and a question that
-- doesn't get enough votes within vote_hours closes and goes to the editors
-- ('expired'), so its place in the open limit frees up. (The 13:00 top-up,
-- which filled the open limit at once, is gone: it flooded the group, and
-- with every place taken by undecided posts the bot stopped posting.)
ALTER TABLE review_bot ADD COLUMN vote_hours smallint NOT NULL DEFAULT 48 CHECK (vote_hours > 0);
ALTER TABLE review_bot DROP COLUMN last_topup_on;
ALTER TABLE group_posts DROP CONSTRAINT group_posts_status_check;
ALTER TABLE group_posts ADD CONSTRAINT group_posts_status_check
    CHECK (status IN ('open', 'approved', 'denied', 'closed', 'expired'));

-- +goose Down
UPDATE group_posts SET status = 'closed' WHERE status = 'expired';
ALTER TABLE group_posts DROP CONSTRAINT group_posts_status_check;
ALTER TABLE group_posts ADD CONSTRAINT group_posts_status_check
    CHECK (status IN ('open', 'approved', 'denied', 'closed'));
ALTER TABLE review_bot ADD COLUMN last_topup_on date;
ALTER TABLE review_bot DROP COLUMN vote_hours;
