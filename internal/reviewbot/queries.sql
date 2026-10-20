-- The teachers' Telegram group review (spec 9.4).

-- name: Settings :one
SELECT * FROM review_bot;

-- name: SaveOffset :exec
UPDATE review_bot SET update_offset = @update_offset;

-- name: MarkHourlyPost :exec
UPDATE review_bot SET last_post_at = @at;

-- name: MarkTopUp :exec
UPDATE review_bot SET last_topup_on = @day;

-- name: OpenCount :one
SELECT count(*)::int FROM group_posts WHERE status = 'open';

-- Questions for the group, in test order (so tests open one by one): yellow
-- or red, not approved, as the AI review left them (an editor who has worked
-- on one finishes it), with a fix, and never posted. Each comes with the
-- review of its first test.
-- name: Candidates :many
SELECT q.id, q.version, r.code AS test, r.qid
FROM questions q
CROSS JOIN LATERAL (
    SELECT t.code, ar.qid, ar.record
    FROM question_ai_reviews ar
    JOIN practice_tests t ON t.id = ar.test_id
    WHERE ar.question_id = q.id
    ORDER BY length(t.code), t.code, ar.qid
    LIMIT 1
) r
WHERE q.status = 'review'
  AND q.ai_risk IN ('red', 'yellow')
  AND jsonb_typeof(r.record -> 'fix') = 'object'
  AND NOT EXISTS (SELECT 1 FROM question_versions v WHERE v.question_id = q.id AND v.source = 'editor')
  AND NOT EXISTS (SELECT 1 FROM group_posts p WHERE p.question_id = q.id)
ORDER BY length(r.code), r.code, r.qid
LIMIT @lim;

-- name: InsertPost :one
INSERT INTO group_posts (question_id, version, test_code, qid, risk, caption, chat_id, photo_ids, vote_id, picture_version)
VALUES (@question_id, @version, @test_code, @qid, @risk, @caption, @chat_id, @photo_ids, @vote_id, @picture_version)
RETURNING id;

-- name: Post :one
SELECT * FROM group_posts WHERE id = @id;

-- Open posts whose question changed in the panel meanwhile: approved, or a
-- new version.
-- name: OvertakenPosts :many
SELECT p.*
FROM group_posts p
JOIN questions q ON q.id = p.question_id
WHERE p.status = 'open' AND (q.status = 'approved' OR q.version <> p.version);

-- Open posts drawn with another version of the picture page.
-- name: StalePictures :many
SELECT * FROM group_posts WHERE status = 'open' AND picture_version <> @picture_version ORDER BY id;

-- name: SetPictureVersion :exec
UPDATE group_posts SET picture_version = @picture_version WHERE id = @id;

-- name: Vote :exec
INSERT INTO group_votes (post_id, user_id, name, approve)
VALUES (@post_id, @user_id, @name, @approve)
ON CONFLICT (post_id, user_id) DO UPDATE SET approve = excluded.approve, name = excluded.name, voted_at = now();

-- name: Tally :one
UPDATE group_posts p
SET approvals = (SELECT count(*) FROM group_votes v WHERE v.post_id = p.id AND v.approve),
    denials   = (SELECT count(*) FROM group_votes v WHERE v.post_id = p.id AND NOT v.approve)
WHERE p.id = @id
RETURNING approvals, denials;

-- name: Decide :execrows
UPDATE group_posts SET status = @status, decided_at = now() WHERE id = @id AND status = 'open';

-- name: UserVote :one
SELECT approve FROM group_votes WHERE post_id = @post_id AND user_id = @user_id;

-- name: AskReason :exec
INSERT INTO group_reason_requests (user_id, post_id) VALUES (@user_id, @post_id)
ON CONFLICT (user_id) DO UPDATE SET post_id = excluded.post_id, asked_at = now();

-- name: PendingReason :one
SELECT post_id FROM group_reason_requests WHERE user_id = @user_id;

-- name: DropReasonRequest :exec
DELETE FROM group_reason_requests WHERE user_id = @user_id;

-- name: AddReason :exec
INSERT INTO group_deny_reasons (post_id, user_id, name, reason) VALUES (@post_id, @user_id, @name, @reason);
