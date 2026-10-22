-- +goose Up
-- A decided post's last line ("Approved by the group…", "Closed: not enough
-- votes…") is kept, with whether Telegram took the edit. An edit Telegram
-- refused (its rate limit, a network error) is tried again at the bot's next
-- check, so a closed post can't keep showing open buttons.
ALTER TABLE group_posts ADD COLUMN outcome text;
ALTER TABLE group_posts ADD COLUMN outcome_shown boolean NOT NULL DEFAULT true;

-- Posts that closed for lack of votes before this: some of their edits were
-- refused (20 closed at once). Show the outcome on each again; a message that
-- already says so is left as is.
UPDATE group_posts
SET outcome = format('⏱ Closed: not enough votes (%s for, %s against). An editor will decide in the admin panel.', approvals, denials),
    outcome_shown = false
WHERE status = 'expired';

-- +goose Down
ALTER TABLE group_posts DROP COLUMN outcome_shown;
ALTER TABLE group_posts DROP COLUMN outcome;
