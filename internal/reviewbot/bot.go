// Package reviewbot is the teachers' Telegram group review (spec 9.4): it
// posts each yellow or red question as two pictures, the bank's original and
// the AI's fix, with Approve and Deny; counts one vote per Telegram account;
// applies the fix and approves when enough teachers approve, or sends the
// question to the editors when enough deny, asking each who denied why in a
// private chat. It posts one question an hour from 08:00 to 23:00, and at
// 13:00 enough to fill the open limit.
package reviewbot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/platform/telegram"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/reviewbot/reviewbotdb"
)

// Telegram is what the bot needs of the Bot API (internal/platform/telegram).
type Telegram interface {
	Me(ctx context.Context) (telegram.User, error)
	Updates(ctx context.Context, offset int64, wait time.Duration) ([]telegram.Update, error)
	SendPhotos(ctx context.Context, chatID int64, photos []telegram.Photo) ([]telegram.Message, error)
	EditPhoto(ctx context.Context, chatID, messageID int64, photo telegram.Photo) error
	SendMessage(ctx context.Context, chatID int64, text string, replyTo int64, kb telegram.Keyboard) (telegram.Message, error)
	EditMessage(ctx context.Context, chatID, messageID int64, text string, kb telegram.Keyboard) error
	AnswerCallback(ctx context.Context, id, text, link string) error
}

// Practice is what the bot needs of the question bank (internal/practice).
type Practice interface {
	GroupReady(ctx context.Context, id, test string) (practice.GroupPost, error)
	Picture(ctx context.Context, id, kind, test string, edit *practice.Edit) ([]byte, string, error)
	PictureVersion() string
	ApproveByGroup(ctx context.Context, id string, version int32, test, approvedBy string) error
}

// Bot runs the group review in one group.
type Bot struct {
	pool     *pgxpool.Pool
	tg       Telegram
	practice Practice
	chatID   int64 // the teachers' group; 0: not set yet (the bot only says where it is)
	loc      *time.Location
	log      *slog.Logger
	username string // the bot's, for links to its private chat
	pause    time.Duration
}

// New is a bot for the group chatID, scheduled in loc (the school's time).
func New(pool *pgxpool.Pool, tg Telegram, p Practice, chatID int64, loc *time.Location, log *slog.Logger) *Bot {
	return &Bot{pool: pool, tg: tg, practice: p, chatID: chatID, loc: loc, log: log, pause: 4 * time.Second}
}

// Posting hours, Tashkent time: one question an hour from 08:00 to 23:00;
// at 13:00 (lunch) as many as fill the open limit.
const (
	firstHour = 8
	lastHour  = 23
	topUpHour = 13
)

// Run reads Telegram until ctx ends, and keeps the schedule.
func (b *Bot) Run(ctx context.Context) error {
	me, err := b.tg.Me(ctx)
	if err != nil {
		return fmt.Errorf("the bot's token: %w", err)
	}
	b.username = me.Username
	if b.chatID == 0 {
		b.log.Warn("TELEGRAM_REVIEW_CHAT_ID not set: add the bot to the teachers' group; its ID shows here")
	}
	b.log.Info("review bot started", "bot", "@"+me.Username, "group", b.chatID)
	if err := b.Redraw(ctx); err != nil && ctx.Err() == nil {
		b.log.Warn("review bot: redraw", "err", err)
	}
	var lastTick time.Time
	for ctx.Err() == nil {
		if err := b.poll(ctx); err != nil && ctx.Err() == nil {
			b.log.Warn("review bot: updates", "err", err)
			sleep(ctx, 5*time.Second)
		}
		if time.Since(lastTick) >= 30*time.Second {
			lastTick = time.Now()
			if err := b.Tick(ctx, lastTick); err != nil && ctx.Err() == nil {
				b.log.Warn("review bot: schedule", "err", err)
			}
		}
	}
	return nil
}

// poll handles what happened since the last poll, waiting up to 25 s.
func (b *Bot) poll(ctx context.Context) error {
	q := reviewbotdb.New(b.pool)
	s, err := q.Settings(ctx)
	if err != nil {
		return err
	}
	updates, err := b.tg.Updates(ctx, s.UpdateOffset, 25*time.Second)
	if err != nil {
		return err
	}
	for _, u := range updates {
		if err := b.Handle(ctx, u); err != nil {
			b.log.Warn("review bot: update", "update", u.UpdateID, "err", err)
		}
		if err := q.SaveOffset(ctx, u.UpdateID+1); err != nil {
			return err
		}
	}
	return nil
}

// Tick keeps the schedule at now: closes posts the panel overtook, then
// posts what this hour calls for.
func (b *Bot) Tick(ctx context.Context, now time.Time) error {
	q := reviewbotdb.New(b.pool)
	overtaken, err := q.OvertakenPosts(ctx)
	if err != nil {
		return err
	}
	for _, p := range overtaken {
		if n, err := q.Decide(ctx, reviewbotdb.DecideParams{ID: p.ID, Status: "closed"}); err != nil || n == 0 {
			continue
		}
		b.edit(ctx, p, "🛈 Closed: the question was changed or decided in the admin panel.")
	}

	s, err := q.Settings(ctx)
	if err != nil || s.Paused || b.chatID == 0 {
		return err
	}
	local := now.In(b.loc)
	if local.Hour() < firstHour || local.Hour() > lastHour {
		return nil
	}
	n, err := q.OpenCount(ctx)
	if err != nil {
		return err
	}
	open, most := int(n), int(s.MaxOpen)
	hour := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, b.loc)
	if !s.LastPostAt.Valid || s.LastPostAt.Time.Before(hour) {
		if err := q.MarkHourlyPost(ctx, pgtype.Timestamptz{Time: now, Valid: true}); err != nil {
			return err
		}
		if open < most {
			open += b.postNext(ctx, 1)
		}
	}
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if local.Hour() == topUpHour && (!s.LastTopupOn.Valid || s.LastTopupOn.Time.Before(today)) {
		if err := q.MarkTopUp(ctx, pgtype.Date{Time: today, Valid: true}); err != nil {
			return err
		}
		if open < most {
			b.postNext(ctx, most-open)
		}
	}
	return nil
}

// postNext posts up to n questions, the next in test order that are ready;
// it returns how many it posted.
func (b *Bot) postNext(ctx context.Context, n int) int {
	cands, err := reviewbotdb.New(b.pool).Candidates(ctx, int32(n+20)) //nolint:gosec // small
	if err != nil {
		b.log.Warn("review bot: candidates", "err", err)
		return 0
	}
	posted := 0
	for _, c := range cands {
		if posted == n || ctx.Err() != nil {
			break
		}
		if posted > 0 {
			sleep(ctx, b.pause) // Telegram allows about 20 messages a minute in a group
		}
		if err := b.Post(ctx, c); err != nil {
			b.log.Warn("review bot: not posted", "test", c.Test, "qid", c.Qid, "err", err)
			continue
		}
		posted++
	}
	return posted
}

// Post puts one question in the group: its pictures, then the vote under them.
func (b *Bot) Post(ctx context.Context, c reviewbotdb.CandidatesRow) error {
	id := uuidString(c.ID)
	info, err := b.practice.GroupReady(ctx, id, c.Test)
	if err != nil {
		return err
	}
	original, _, err := b.practice.Picture(ctx, id, "original", c.Test, nil)
	if err != nil {
		return fmt.Errorf("original picture: %w", err)
	}
	photos := []telegram.Photo{{PNG: original, Caption: pictureCaption["original"]}}
	if info.Changes {
		fixed, _, err := b.practice.Picture(ctx, id, "fix", c.Test, nil)
		if err != nil {
			return fmt.Errorf("fix picture: %w", err)
		}
		photos = append(photos, telegram.Photo{PNG: fixed, Caption: pictureCaption["fix"]})
	}
	msgs, err := retry(ctx, func() ([]telegram.Message, error) { return b.tg.SendPhotos(ctx, b.chatID, photos) })
	if err != nil {
		return err
	}
	caption := captionOf(info)
	s, err := reviewbotdb.New(b.pool).Settings(ctx)
	if err != nil {
		return err
	}
	approvals, denials := thresholds(s, info.Risk)
	vote, err := retry(ctx, func() (telegram.Message, error) {
		return b.tg.SendMessage(ctx, b.chatID, voteText(caption, approvals, denials, 0), msgs[0].MessageID, nil)
	})
	if err != nil {
		return err
	}
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.MessageID
	}
	postID, err := reviewbotdb.New(b.pool).InsertPost(ctx, reviewbotdb.InsertPostParams{
		QuestionID: c.ID, Version: c.Version, TestCode: c.Test, Qid: c.Qid, Risk: info.Risk,
		Caption: caption, ChatID: b.chatID, PhotoIds: ids, VoteID: vote.MessageID,
		PictureVersion: b.practice.PictureVersion(),
	})
	if err != nil {
		return err
	}
	// The buttons carry the post's id, known only now, so they come last.
	if err := b.tg.EditMessage(ctx, b.chatID, vote.MessageID, voteText(caption, approvals, denials, 0), keyboard(postID)); err != nil {
		return err
	}
	b.log.Info("review bot: posted", "test", c.Test, "qid", c.Qid, "post", postID)
	return nil
}

// A post's pictures, in the order they're sent: the fix only when the AI
// changed something.
var (
	pictureKinds   = []string{"original", "fix"}
	pictureCaption = map[string]string{"original": "Original (the bank's)", "fix": "AI fix (changes highlighted)"}
)

// Redraw draws again the pictures of open posts drawn with another version
// of the picture page (an update changed how questions look), and replaces
// them in the group in place.
func (b *Bot) Redraw(ctx context.Context) error {
	version := b.practice.PictureVersion()
	if version == "" || b.chatID == 0 {
		return nil
	}
	q := reviewbotdb.New(b.pool)
	posts, err := q.StalePictures(ctx, version)
	if err != nil {
		return err
	}
	redrawn := 0
	for _, p := range posts {
		if len(p.PhotoIds) > len(pictureKinds) {
			continue
		}
		id := uuidString(p.QuestionID)
		var failed error
		for i, msg := range p.PhotoIds {
			kind := pictureKinds[i]
			png, _, err := b.practice.Picture(ctx, id, kind, p.TestCode, nil)
			if err != nil {
				failed = fmt.Errorf("%s picture: %w", kind, err)
				break
			}
			sleep(ctx, b.pause) // edits count toward the group's limit like messages
			_, err = retry(ctx, func() (struct{}, error) {
				return struct{}{}, b.tg.EditPhoto(ctx, p.ChatID, msg, telegram.Photo{PNG: png, Caption: pictureCaption[kind]})
			})
			if err != nil && !telegram.NotModified(err) {
				failed = err
				break
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if failed != nil {
			b.log.Warn("review bot: redraw", "post", p.ID, "err", failed)
			continue
		}
		if err := q.SetPictureVersion(ctx, reviewbotdb.SetPictureVersionParams{ID: p.ID, PictureVersion: version}); err != nil {
			return err
		}
		redrawn++
	}
	if redrawn > 0 {
		b.log.Info("review bot: redrew open posts", "posts", redrawn)
	}
	return nil
}

// Handle acts on one update: a vote, or a private message.
func (b *Bot) Handle(ctx context.Context, u telegram.Update) error {
	switch {
	case u.CallbackQuery != nil:
		return b.vote(ctx, u.CallbackQuery)
	case u.MyChatMember != nil:
		c := u.MyChatMember.Chat
		b.log.Info("review bot: added to or removed from a chat", "chat", c.ID, "title", c.Title, "status", u.MyChatMember.NewChatMember.Status)
	case u.Message != nil && u.Message.Chat.Type == "private" && u.Message.From != nil:
		return b.private(ctx, u.Message)
	case u.Message != nil && b.chatID == 0 && u.Message.Chat.Type != "private":
		b.log.Info("review bot: a group's ID (set TELEGRAM_REVIEW_CHAT_ID to it)", "chat", u.Message.Chat.ID, "title", u.Message.Chat.Title)
	}
	return nil
}

// vote counts a press on Approve or Deny, and decides when it's enough.
func (b *Bot) vote(ctx context.Context, cb *telegram.CallbackQuery) error {
	kind, idText, ok := strings.Cut(cb.Data, ":")
	postID, err := strconv.ParseInt(idText, 10, 64)
	if !ok || err != nil || (kind != "a" && kind != "d") || cb.Message == nil || cb.Message.Chat.ID != b.chatID || cb.From.IsBot {
		return b.tg.AnswerCallback(ctx, cb.ID, "", "")
	}
	q := reviewbotdb.New(b.pool)
	p, err := q.Post(ctx, postID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && p.Status != "open" {
		return b.tg.AnswerCallback(ctx, cb.ID, "This vote is closed.", "")
	}
	if err != nil {
		return err
	}
	approve := kind == "a"
	if err := q.Vote(ctx, reviewbotdb.VoteParams{PostID: postID, UserID: cb.From.ID, Name: cb.From.Name(), Approve: approve}); err != nil {
		return err
	}
	if approve {
		if err := b.tg.AnswerCallback(ctx, cb.ID, "Your vote: Approve ✅. You can change it until the decision.", ""); err != nil {
			b.log.Warn("review bot: answer", "err", err)
		}
	} else if err := b.tg.AnswerCallback(ctx, cb.ID, "", fmt.Sprintf("https://t.me/%s?start=deny_%d", b.username, postID)); err != nil {
		b.log.Warn("review bot: answer", "err", err)
	}
	return b.count(ctx, p)
}

// count tallies a post's votes and decides when one side has enough.
func (b *Bot) count(ctx context.Context, p reviewbotdb.GroupPost) error {
	q := reviewbotdb.New(b.pool)
	t, err := q.Tally(ctx, p.ID)
	if err != nil {
		return err
	}
	s, err := q.Settings(ctx)
	if err != nil {
		return err
	}
	approvals, denials := thresholds(s, p.Risk)
	switch {
	case int(t.Approvals) >= approvals:
		return b.approve(ctx, p, t.Approvals, t.Denials)
	case int(t.Denials) >= denials:
		if n, err := q.Decide(ctx, reviewbotdb.DecideParams{ID: p.ID, Status: "denied"}); err != nil || n == 0 {
			return err
		}
		b.edit(ctx, p, fmt.Sprintf("❌ <b>Denied by the group</b>: %d for, %d against. Sent to the editors.", t.Approvals, t.Denials))
		b.log.Info("review bot: denied", "post", p.ID, "for", t.Approvals, "against", t.Denials)
	default:
		err := b.tg.EditMessage(ctx, p.ChatID, p.VoteID, voteText(p.Caption, approvals, denials, int(t.Approvals+t.Denials)), keyboard(p.ID))
		if err != nil && !telegram.NotModified(err) {
			b.log.Warn("review bot: vote count", "err", err)
		}
	}
	return nil
}

// approve applies the fix and approves the question for the group.
func (b *Bot) approve(ctx context.Context, p reviewbotdb.GroupPost, yes, no int32) error {
	q := reviewbotdb.New(b.pool)
	by := fmt.Sprintf("%s (%d for, %d against)", practice.GroupReviewer, yes, no)
	err := b.practice.ApproveByGroup(ctx, uuidString(p.QuestionID), p.Version, p.TestCode, by)
	status, text := "approved", fmt.Sprintf("✅ <b>Approved by the group</b>: %d for, %d against. The fix is applied.", yes, no)
	var invalid *practice.InvalidError
	switch {
	case errors.Is(err, practice.ErrConflict):
		status, text = "closed", "🛈 Approved by the group, but the question was changed in the admin panel meanwhile; the editors decide."
	case errors.As(err, &invalid):
		status, text = "closed", "⚠️ Approved by the group, but the fixed question doesn't pass the checks; sent to the editors."
		b.log.Warn("review bot: approved fix invalid", "post", p.ID, "fields", invalid.Fields)
	case err != nil:
		return err // try again at the next vote
	}
	if n, err := q.Decide(ctx, reviewbotdb.DecideParams{ID: p.ID, Status: status}); err != nil || n == 0 {
		return err
	}
	b.edit(ctx, p, text)
	b.log.Info("review bot: decided", "post", p.ID, "status", status, "for", yes, "against", no)
	return nil
}

// private is a teacher's message to the bot: the deny link's /start, a
// reason, or anything else.
func (b *Bot) private(ctx context.Context, m *telegram.Message) error {
	q := reviewbotdb.New(b.pool)
	from := *m.From
	text := strings.TrimSpace(m.Text)
	reply := func(t string) error {
		_, err := b.tg.SendMessage(ctx, m.Chat.ID, t, 0, nil)
		return err
	}
	if arg, ok := strings.CutPrefix(text, "/start deny_"); ok {
		postID, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return reply("This link is broken.")
		}
		p, err := q.Post(ctx, postID)
		if errors.Is(err, pgx.ErrNoRows) {
			return reply("That question isn't in the review any more.")
		}
		if err != nil {
			return err
		}
		approve, err := q.UserVote(ctx, reviewbotdb.UserVoteParams{PostID: postID, UserID: from.ID})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && approve {
			return reply("You haven't denied that question.")
		}
		if err != nil {
			return err
		}
		if err := q.AskReason(ctx, reviewbotdb.AskReasonParams{UserID: from.ID, PostID: postID}); err != nil {
			return err
		}
		return reply(fmt.Sprintf("Your vote: Deny ❌ on %s.\n\nWhy? Write it in a few words for the editors (for example, \"the restored word is wrong\"). Send /skip to leave it.", firstLine(p.Caption)))
	}
	postID, err := q.PendingReason(ctx, from.ID)
	pending := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	switch {
	case pending && text == "/skip":
		if err := q.DropReasonRequest(ctx, from.ID); err != nil {
			return err
		}
		return reply("OK, no reason. Thank you for voting.")
	case pending && text != "" && !strings.HasPrefix(text, "/"):
		if utf8.RuneCountInString(text) > 1000 {
			text = string([]rune(text)[:1000])
		}
		if err := q.AddReason(ctx, reviewbotdb.AddReasonParams{PostID: postID, UserID: from.ID, Name: from.Name(), Reason: text}); err != nil {
			return err
		}
		if err := q.DropReasonRequest(ctx, from.ID); err != nil {
			return err
		}
		return reply("Thank you. The editors will see it.")
	}
	return reply("I post SAT MAKON's practice questions to the teachers' group: the bank's original and the AI's fix. Vote there with Approve or Deny. When you deny, I ask you here why.")
}

// edit replaces a post's vote message with its outcome, without buttons.
func (b *Bot) edit(ctx context.Context, p reviewbotdb.GroupPost, outcome string) {
	if err := b.tg.EditMessage(ctx, p.ChatID, p.VoteID, p.Caption+"\n\n"+outcome, nil); err != nil && !telegram.NotModified(err) {
		b.log.Warn("review bot: edit", "post", p.ID, "err", err)
	}
}

var kindLabel = map[string]string{
	"corrected": "Corrected", "restored": "Restored missing text", "invented": "Made up by the AI", "redrawn": "Figure redrawn",
}

// captionOf is the head of a post's vote message (Telegram HTML).
func captionOf(info practice.GroupPost) string {
	risk := "🟡 Yellow"
	if info.Risk == "red" {
		risk = "🔴 Red"
	}
	kinds := make([]string, 0, len(info.Kinds))
	for _, k := range info.Kinds {
		if l, ok := kindLabel[k]; ok {
			kinds = append(kinds, l)
		}
	}
	fix := "no change needed, approve as is?"
	if info.Changes && len(kinds) > 0 {
		fix = strings.Join(kinds, " · ")
	}
	text := fmt.Sprintf("<b>%s</b>\n%s · AI fix: %s", html.EscapeString(info.Place), risk, html.EscapeString(fix))
	if note := strings.TrimSpace(info.Note); note != "" {
		text += "\n<i>" + html.EscapeString(note) + "</i>"
	}
	return text
}

// voteText is the open vote's message: the totals stay hidden, only how
// many have voted shows.
func voteText(caption string, approvals, denials, votes int) string {
	return fmt.Sprintf("%s\n\nApproved at %d ✅ or denied at %d ❌. Votes so far: %d.", caption, approvals, denials, votes)
}

func keyboard(postID int64) telegram.Keyboard {
	return telegram.Keyboard{{
		{Text: "✅ Approve", Data: fmt.Sprintf("a:%d", postID)},
		{Text: "❌ Deny", Data: fmt.Sprintf("d:%d", postID)},
	}}
}

func thresholds(s reviewbotdb.ReviewBot, risk string) (approvals, denials int) {
	if risk == "red" {
		return int(s.RedApprovals), int(s.RedDenials)
	}
	return int(s.YellowApprovals), int(s.YellowDenials)
}

// firstLine is the caption's first line as plain text (the question's place).
func firstLine(caption string) string {
	line, _, _ := strings.Cut(caption, "\n")
	return html.UnescapeString(strings.NewReplacer("<b>", "", "</b>", "").Replace(line))
}

// retry calls f again once after Telegram's "too many requests" wait.
func retry[T any](ctx context.Context, f func() (T, error)) (T, error) {
	v, err := f()
	var e *telegram.Error
	if errors.As(err, &e) && e.RetryAfter > 0 {
		sleep(ctx, e.RetryAfter)
		return f()
	}
	return v, err
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func uuidString(u pgtype.UUID) string {
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
