package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/telegram"
)

// Telegram (spec 21): a third identity, signed in with Telegram's Login
// Widget (the web sends people to oauth.telegram.org and gets the signed
// data back), and a free way to prove a phone: the sign-in bot (its own bot,
// not the review bot's) asks for the person's own contact.

const (
	TelegramSignupTTL  = 30 * time.Minute // a Telegram account waiting for a phone
	TelegramCheckTTL   = 10 * time.Minute // a phone check through the bot
	telegramAuthMaxAge = 24 * time.Hour   // Login Widget data older than this is refused
	// TelegramWebhookPath is where Telegram posts the bot's updates.
	TelegramWebhookPath = "/v1/telegram/webhook"
)

var (
	ErrTelegramOff     = errors.New("Telegram isn't available") //nolint:staticcheck // a product name
	ErrInvalidTelegram = errors.New("the Telegram sign-in data isn't valid or is too old; try again")
	ErrTelegramTaken   = errors.New("this Telegram account is linked to another account")
	ErrBadWebhook      = errors.New("not from Telegram")
)

// TelegramConfig turns on Telegram.
type TelegramConfig struct {
	Token         string // the sign-in bot's, from @BotFather
	Username      string // the bot's username, without @
	WebhookSecret string // Telegram sends it with every update (setWebhook's secret_token)
	APIURL        string // the Bot API; Telegram's when empty (tests use their own)
}

type telegramBot struct {
	TelegramConfig
	id  int64
	bot *telegram.Bot
}

// WithTelegram turns on Telegram sign-in and phone checks.
func (s *Service) WithTelegram(c TelegramConfig) *Service {
	id, _ := strconv.ParseInt(strings.SplitN(c.Token, ":", 2)[0], 10, 64)
	b := telegram.New(c.Token)
	if c.APIURL != "" {
		b.WithBaseURL(c.APIURL)
	}
	s.telegram = &telegramBot{TelegramConfig: c, id: id, bot: b}
	return s
}

// TelegramBot is the sign-in bot's public id (the token's number) and
// username, for the Login Widget; ok is false when Telegram is off.
func (s *Service) TelegramBot() (id int64, username string, ok bool) {
	if s.telegram == nil {
		return 0, "", false
	}
	return s.telegram.id, s.telegram.Username, true
}

// TelegramAccount is a Telegram account as the Login Widget describes it.
type TelegramAccount struct {
	ID        int64
	Username  string
	FirstName string
	LastName  string
}

// checkTelegramAuth checks Login Widget data (Telegram's "Checking
// authorization"): the hash is HMAC-SHA256 of the other fields, sorted, as
// key=value lines, keyed with SHA-256 of the bot's token; auth_date within a
// day.
func (s *Service) checkTelegramAuth(raw json.RawMessage) (TelegramAccount, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return TelegramAccount{}, ErrInvalidTelegram
	}
	hash, _ := fields["hash"].(string)
	var lines []string
	for k, v := range fields {
		if k == "hash" || v == nil {
			continue
		}
		var val string
		switch v := v.(type) {
		case string:
			val = v
		case json.Number:
			val = v.String()
		case bool:
			val = strconv.FormatBool(v)
		default:
			return TelegramAccount{}, ErrInvalidTelegram
		}
		lines = append(lines, k+"="+val)
	}
	sort.Strings(lines)
	key := sha256.Sum256([]byte(s.telegram.Token))
	m := hmac.New(sha256.New, key[:])
	m.Write([]byte(strings.Join(lines, "\n")))
	want := hex.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(strings.ToLower(hash)), []byte(want)) {
		return TelegramAccount{}, ErrInvalidTelegram
	}
	num := func(k string) int64 {
		n, _ := fields[k].(json.Number)
		v, _ := n.Int64()
		return v
	}
	str := func(k string) string { v, _ := fields[k].(string); return strings.TrimSpace(v) }
	at := time.Unix(num("auth_date"), 0)
	if now := s.now(); now.Sub(at) > telegramAuthMaxAge || at.Sub(now) > 5*time.Minute {
		return TelegramAccount{}, ErrInvalidTelegram
	}
	acc := TelegramAccount{ID: num("id"), Username: str("username"), FirstName: str("first_name"), LastName: str("last_name")}
	if acc.ID == 0 {
		return TelegramAccount{}, ErrInvalidTelegram
	}
	return acc, nil
}

// TelegramResult is Telegram sign-in: a session when the account is linked
// to someone active, else a ticket to sign up with (once a phone is proved).
type TelegramResult struct {
	SignedIn     *LoginResult
	SignupTicket string
	Account      TelegramAccount
}

// TelegramSignIn checks Login Widget data and signs in, or answers a
// Telegram ticket for sign-up.
func (s *Service) TelegramSignIn(ctx context.Context, raw json.RawMessage, deviceID string) (TelegramResult, error) {
	if s.telegram == nil {
		return TelegramResult{}, ErrTelegramOff
	}
	acc, err := s.checkTelegramAuth(raw)
	if err != nil {
		return TelegramResult{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return TelegramResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	link, err := q.TelegramLinkByID(ctx, acc.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return TelegramResult{}, fmt.Errorf("telegram link: %w", err)
	}
	if err == nil {
		p, err := people.NewDirectory(tx).Get(ctx, link.PersonKind, link.PersonID)
		if err != nil && !errors.Is(err, people.ErrNotFound) {
			return TelegramResult{}, err
		}
		if err == nil && p.Active {
			if err := q.SetTelegramUsername(ctx, authdb.SetTelegramUsernameParams{TelegramID: acc.ID, Username: optText(acc.Username)}); err != nil {
				return TelegramResult{}, err
			}
			c, err := q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: p.Kind, PersonID: p.ID})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return TelegramResult{}, fmt.Errorf("get credential: %w", err)
			}
			_, custom := ownPassword(c.PasswordHash, c.PasswordChangedAt, p)
			res, err := s.openSession(ctx, q, p, deviceID, usesDefault(p, custom))
			if err != nil {
				return TelegramResult{}, err
			}
			return TelegramResult{SignedIn: &res, Account: acc}, tx.Commit(ctx)
		}
	}
	// Not linked, or linked to someone who has left: they sign up (or into
	// the account their phone has), and the link moves then (linkTelegram).
	signup, err := s.newTicket(ctx, q, ticketData{purpose: ticketTelegramSignup, telegram: &acc}, TelegramSignupTTL)
	if err != nil {
		return TelegramResult{}, err
	}
	return TelegramResult{SignupTicket: signup, Account: acc}, tx.Commit(ctx)
}

// linkTelegram links the Telegram account to p, as linkGoogle does Google's.
func linkTelegram(ctx context.Context, q *authdb.Queries, dir *people.Directory, p people.Person, acc TelegramAccount) error {
	link, err := q.TelegramLinkByID(ctx, acc.ID)
	switch {
	case err == nil && link.PersonKind == p.Kind && link.PersonID == p.ID:
		return q.SetTelegramUsername(ctx, authdb.SetTelegramUsernameParams{TelegramID: acc.ID, Username: optText(acc.Username)})
	case err == nil:
		other, err := dir.Get(ctx, link.PersonKind, link.PersonID)
		if err != nil && !errors.Is(err, people.ErrNotFound) {
			return err
		}
		if err == nil && other.Active {
			return ErrTelegramTaken
		}
		if err := q.DeleteTelegramLink(ctx, acc.ID); err != nil {
			return fmt.Errorf("unlink telegram: %w", err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("telegram link: %w", err)
	}
	if err := q.UnlinkTelegramPerson(ctx, authdb.UnlinkTelegramPersonParams{PersonKind: p.Kind, PersonID: p.ID}); err != nil {
		return fmt.Errorf("unlink telegram: %w", err)
	}
	if err := q.InsertTelegramLink(ctx, authdb.InsertTelegramLinkParams{TelegramID: acc.ID, PersonKind: p.Kind, PersonID: p.ID, Username: optText(acc.Username)}); err != nil {
		return fmt.Errorf("link telegram: %w", err)
	}
	return nil
}

// linkCheckedTelegram links the Telegram account that shared p's phone
// through the bot (telegramID; 0: none) to p, inside the caller's
// transaction, unless that would take it from someone else active or
// replace another Telegram account p has: then nothing (never a link stolen).
func linkCheckedTelegram(ctx context.Context, q *authdb.Queries, dir *people.Directory, p people.Person, telegramID int64) error {
	if telegramID == 0 {
		return nil
	}
	has, err := q.HasTelegramLink(ctx, authdb.HasTelegramLinkParams{PersonKind: p.Kind, PersonID: p.ID})
	if err != nil {
		return fmt.Errorf("telegram link: %w", err)
	}
	if link, err := q.TelegramLinkByID(ctx, telegramID); err == nil && link.PersonKind == p.Kind && link.PersonID == p.ID {
		return nil // already theirs
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("telegram link: %w", err)
	}
	if has {
		return nil // they have another Telegram account
	}
	if err := linkTelegram(ctx, q, dir, p, TelegramAccount{ID: telegramID}); err != nil && !errors.Is(err, ErrTelegramTaken) {
		return err
	}
	return nil
}

// TelegramCheck is a phone check through the bot, as StartTelegramCheck
// answers it.
type TelegramCheck struct {
	Nonce     string `json:"nonce"`
	Link      string `json:"link"` // https://t.me/<bot>?start=<nonce>
	ExpiresIn int    `json:"expires_in"`
}

// StartTelegramCheck starts proving a phone through the bot instead of an
// SMS code; for a sign-up it needs the identity, as VerifyPhone does.
// CodesPerNetwork an hour per network.
func (s *Service) StartTelegramCheck(ctx context.Context, purpose string, identity Identity, ip string) (TelegramCheck, error) {
	if s.telegram == nil {
		return TelegramCheck{}, ErrTelegramOff
	}
	now := s.now()
	var idHash []byte
	var idPurpose pgtype.Text
	if purpose == PurposeSignup {
		h, p, err := s.identityFor(ctx, identity, "", now)
		if err != nil {
			return TelegramCheck{}, err
		}
		idHash, idPurpose = h, pgtype.Text{String: p, Valid: true}
	}
	n, err := s.q.NetworkTelegramChecksSince(ctx, authdb.NetworkTelegramChecksSinceParams{Ip: ip, Since: timestamp(now.Add(-time.Hour))})
	if err != nil {
		return TelegramCheck{}, err
	}
	if n >= CodesPerNetwork {
		return TelegramCheck{}, &RateLimitedError{RetryAfter: time.Hour}
	}
	nonce, hash, err := newToken()
	if err != nil {
		return TelegramCheck{}, err
	}
	if err := s.q.InsertTelegramCheck(ctx, authdb.InsertTelegramCheckParams{
		NonceHash: hash, Purpose: purpose, Identity: idHash, IdentityPurpose: idPurpose, Ip: ip,
		CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(TelegramCheckTTL)),
	}); err != nil {
		return TelegramCheck{}, fmt.Errorf("store check: %w", err)
	}
	return TelegramCheck{Nonce: nonce, Link: "https://t.me/" + s.telegram.Username + "?start=" + nonce,
		ExpiresIn: int(TelegramCheckTTL.Seconds())}, nil
}

// TelegramCheckResult is a check's state; Ticket and Owner as VerifyPhone's.
// SignedIn: the identity was the same Telegram account that shared the
// phone, and the phone has an owner: signed straight into them.
type TelegramCheckResult struct {
	SignedIn *LoginResult
	Purpose  string
	Verified bool
	Phone    string
	Ticket   string
	Owner    *PhoneOwner
}

// CheckTelegram answers whether the bot has the phone yet; once it has, the
// phone ticket, once (ErrCodeExpired after that, and after expiry). A
// sign-up whose identity is the Telegram account that shared the phone, for
// a phone with an owner (registered or enrolled), signs straight into that
// owner instead: Telegram vouches for both, so it is the same person.
func (s *Service) CheckTelegram(ctx context.Context, nonce, deviceID string) (TelegramCheckResult, error) {
	if s.telegram == nil {
		return TelegramCheckResult{}, ErrTelegramOff
	}
	now := s.now()
	hash := hashToken(nonce)
	c, err := s.q.GetTelegramCheck(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return TelegramCheckResult{}, ErrCodeExpired
	}
	if err != nil {
		return TelegramCheckResult{}, err
	}
	if c.UsedAt.Valid || !c.ExpiresAt.Time.After(now) {
		return TelegramCheckResult{}, ErrCodeExpired
	}
	if !c.VerifiedAt.Valid {
		return TelegramCheckResult{}, nil
	}
	if c.Purpose == PurposeSignup {
		// The identity must still be live and not spent on another phone.
		it, err := s.q.PeekTicket(ctx, authdb.PeekTicketParams{TokenHash: c.Identity, Purpose: c.IdentityPurpose.String, Now: timestamp(now)})
		if errors.Is(err, pgx.ErrNoRows) {
			return TelegramCheckResult{}, ErrInvalidTicket
		}
		if err != nil {
			return TelegramCheckResult{}, err
		}
		if it.RevealedPhone.Valid && it.RevealedPhone.String != c.Phone.String {
			return TelegramCheckResult{}, ErrIdentitySpent
		}
		if c.IdentityPurpose.String == ticketTelegramSignup && it.TelegramID.Valid && c.TelegramUserID.Valid &&
			it.TelegramID.Int64 == c.TelegramUserID.Int64 {
			if owner, found, err := phoneOwner(ctx, s.people, c.Phone.String); err != nil {
				return TelegramCheckResult{}, err
			} else if found {
				return s.sameTelegramSignIn(ctx, hash, c.Identity, owner, c.TelegramUserID.Int64, deviceID)
			}
		}
	}
	if n, err := s.q.UseTelegramCheck(ctx, authdb.UseTelegramCheckParams{NonceHash: hash, Now: timestamp(now)}); err != nil {
		return TelegramCheckResult{}, err
	} else if n == 0 {
		return TelegramCheckResult{}, ErrCodeExpired
	}
	ticket, owner, err := s.phoneProved(ctx, c.Phone.String, c.Purpose, c.Identity, c.IdentityPurpose.String, c.TelegramUserID.Int64)
	if err != nil {
		return TelegramCheckResult{}, err
	}
	return TelegramCheckResult{Purpose: c.Purpose, Verified: true, Phone: c.Phone.String, Ticket: ticket, Owner: owner}, nil
}

// sameTelegramSignIn signs into p: the check and the identity ticket are
// spent, the Telegram account linked (if free).
func (s *Service) sameTelegramSignIn(ctx context.Context, checkHash, idHash []byte, p people.Person, telegramID int64, deviceID string) (TelegramCheckResult, error) {
	now := s.now()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return TelegramCheckResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	if n, err := q.UseTelegramCheck(ctx, authdb.UseTelegramCheckParams{NonceHash: checkHash, Now: timestamp(now)}); err != nil {
		return TelegramCheckResult{}, err
	} else if n == 0 {
		return TelegramCheckResult{}, ErrCodeExpired
	}
	if _, err := q.UseTicket(ctx, authdb.UseTicketParams{TokenHash: idHash, Purpose: ticketTelegramSignup, Now: timestamp(now)}); errors.Is(err, pgx.ErrNoRows) {
		return TelegramCheckResult{}, ErrInvalidTicket
	} else if err != nil {
		return TelegramCheckResult{}, fmt.Errorf("use ticket: %w", err)
	}
	if err := linkCheckedTelegram(ctx, q, people.NewDirectory(tx), p, telegramID); err != nil {
		return TelegramCheckResult{}, err
	}
	c, err := q.GetCredential(ctx, authdb.GetCredentialParams{PersonKind: p.Kind, PersonID: p.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return TelegramCheckResult{}, fmt.Errorf("get credential: %w", err)
	}
	_, custom := ownPassword(c.PasswordHash, c.PasswordChangedAt, p)
	res, err := s.openSession(ctx, q, p, deviceID, usesDefault(p, custom))
	if err != nil {
		return TelegramCheckResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TelegramCheckResult{}, err
	}
	return TelegramCheckResult{SignedIn: &res, Purpose: PurposeSignup}, nil
}

// The bot's replies.
const (
	tgAskContact = "To check your phone number for SATMAKON, tap \"Share my number\" below."
	tgNoCheck    = "This link has expired or was already used. Start again on SATMAKON."
	tgOpenFirst  = "Open the link from the SATMAKON site first, then share your number here."
	tgNotOwn     = "Please share your own number, with the \"Share my number\" button."
	tgNotUzbek   = "An Uzbek mobile number (+998) is needed."
	tgDone       = "Done — go back to SATMAKON."
	tgOther      = "This bot only checks phone numbers for SATMAKON. Open the link from the SATMAKON site."
)

// HandleTelegramUpdate is the bot's webhook (secret: the header Telegram
// sends, X-Telegram-Bot-Api-Secret-Token). Only private chats: "/start
// <nonce>" ties the check to the account and asks for the contact; the
// account's own contact proves the phone.
func (s *Service) HandleTelegramUpdate(ctx context.Context, secret string, u telegram.Update) error {
	t := s.telegram
	if t == nil {
		return ErrTelegramOff
	}
	if !s.telegramSecret(secret) {
		return ErrBadWebhook
	}
	m := u.Message
	if m == nil || m.From == nil || m.Chat.Type != "private" || m.From.IsBot {
		return nil
	}
	now := s.now()
	reply := func(text string, markup any) error {
		p := map[string]any{"chat_id": m.Chat.ID, "text": text}
		if markup != nil {
			p["reply_markup"] = markup
		}
		return t.bot.Call(ctx, "sendMessage", p, nil)
	}
	removeKeyboard := map[string]any{"remove_keyboard": true}

	if nonce, ok := strings.CutPrefix(strings.TrimSpace(m.Text), "/start "); ok {
		n, err := s.q.OpenTelegramCheck(ctx, authdb.OpenTelegramCheckParams{
			TelegramUserID: pgtype.Int8{Int64: m.From.ID, Valid: true}, NonceHash: hashToken(strings.TrimSpace(nonce)), Now: timestamp(now)})
		if err != nil {
			return err
		}
		if n == 0 {
			return reply(tgNoCheck, removeKeyboard)
		}
		return reply(tgAskContact, map[string]any{
			"keyboard":          [][]map[string]any{{{"text": "Share my number", "request_contact": true}}},
			"resize_keyboard":   true,
			"one_time_keyboard": true,
		})
	}
	if c := m.Contact; c != nil {
		if c.UserID != m.From.ID {
			return reply(tgNotOwn, nil)
		}
		raw := strings.TrimSpace(c.PhoneNumber)
		if !strings.HasPrefix(raw, "+") {
			raw = "+" + raw
		}
		phone, ok := uzbekPhone(raw)
		if !ok {
			return reply(tgNotUzbek, nil)
		}
		n, err := s.q.VerifyTelegramCheck(ctx, authdb.VerifyTelegramCheckParams{
			Phone: phone, Now: timestamp(now), TelegramUserID: pgtype.Int8{Int64: m.From.ID, Valid: true}})
		if err != nil {
			return err
		}
		if n == 0 {
			return reply(tgOpenFirst, removeKeyboard)
		}
		return reply(tgDone, removeKeyboard)
	}
	return reply(tgOther, nil)
}

func (s *Service) telegramOn() bool { return s.telegram != nil }

func (s *Service) telegramSecret(secret string) bool {
	t := s.telegram
	return t != nil && t.WebhookSecret != "" && hmac.Equal([]byte(secret), []byte(t.WebhookSecret))
}

// SetTelegramWebhook registers the bot's webhook: Telegram posts updates
// to publicURL + TelegramWebhookPath with the secret.
func (s *Service) SetTelegramWebhook(ctx context.Context, publicURL string) (string, error) {
	t := s.telegram
	if t == nil {
		return "", ErrTelegramOff
	}
	hook := strings.TrimRight(publicURL, "/") + TelegramWebhookPath
	err := t.bot.Call(ctx, "setWebhook", map[string]any{
		"url": hook, "secret_token": t.WebhookSecret, "allowed_updates": []string{"message"}, "drop_pending_updates": true,
	}, nil)
	return hook, err
}

// logTelegram records a webhook problem without failing Telegram's delivery.
func logTelegram(log *slog.Logger, err error) {
	if err != nil {
		log.Warn("telegram webhook", slog.Any("err", err))
	}
}

func optText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
