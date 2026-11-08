package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/auth/authdb"
	"github.com/nazacd/makonbook-api/internal/people"
)

// Telegram sign-in (spec 21.3): Telegram's OpenID Connect flow, run by the
// API (authorization code with PKCE, scope "openid profile phone"). Telegram
// shares the person's verified phone when they allow it. Two purposes:
// signin (anyone) and verify (a signed-in person adding their phone).

// Telegram's endpoints (oauth.telegram.org/.well-known/openid-configuration).
//
//nolint:gosec // G101: addresses, not credentials
const (
	TelegramAuthURL  = "https://oauth.telegram.org/auth"
	TelegramTokenURL = "https://oauth.telegram.org/token"
	TelegramJWKSURL  = "https://oauth.telegram.org/.well-known/jwks.json"
	telegramIssuer   = "https://oauth.telegram.org"
)

const (
	// TelegramCallbackPath is where Telegram sends people back, on WebOrigin.
	TelegramCallbackPath = "/v1/auth/telegram/callback"
	telegramWebPath      = "/auth/telegram"
	telegramStateTTL     = 10 * time.Minute
	telegramTicketTTL    = 5 * time.Minute
)

// Telegram flow purposes.
const (
	TelegramSignIn = "signin"
	TelegramVerify = "verify"
)

// Reasons the callback sends the web instead of a ticket (?error=…).
const (
	TelegramDenied = "telegram_denied"
	TelegramFailed = "telegram_failed"
)

var ErrTelegramOff = errors.New("sign-in with Telegram isn't available")

// TelegramConfig turns on Telegram sign-in.
type TelegramConfig struct {
	ClientID, ClientSecret string // from @BotFather → the bot → Login Widget
	WebOrigin              string // its /v1/ is this API
	// Telegram's endpoints unless set (tests use their own).
	AuthURL, TokenURL, JWKSURL string
	Client                     *http.Client // a client with a 15 s timeout when nil
}

type telegramOIDC struct {
	TelegramConfig
	keys *jwks
}

// WithTelegram turns on Telegram sign-in.
func (s *Service) WithTelegram(c TelegramConfig) *Service {
	if c.AuthURL == "" {
		c.AuthURL = TelegramAuthURL
	}
	if c.TokenURL == "" {
		c.TokenURL = TelegramTokenURL
	}
	if c.JWKSURL == "" {
		c.JWKSURL = TelegramJWKSURL
	}
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 15 * time.Second}
	}
	s.telegram = &telegramOIDC{TelegramConfig: c, keys: &jwks{url: c.JWKSURL, client: c.Client}}
	return s
}

// TelegramEnabled: Telegram sign-in is configured.
func (s *Service) TelegramEnabled() bool { return s.telegram != nil }

// TelegramAccount is a Telegram account as its ID token describes it.
type TelegramAccount struct {
	ID        int64 // Telegram's user id (the claim "id")
	Username  string
	FirstName string
	LastName  string
	Phone     string // E.164, when they shared it
	PhotoURL  string
}

// TelegramStart begins Telegram's flow: the address to send the browser to.
// verify needs the signed-in person (pr), whom the flow is tied to. The state
// and the PKCE verifier stay on the server for 10 minutes.
func (s *Service) TelegramStart(ctx context.Context, purpose string, pr *Principal, returnPath string) (string, error) {
	t := s.telegram
	if t == nil {
		return "", ErrTelegramOff
	}
	state, err := randomString()
	if err != nil {
		return "", err
	}
	verifier, err := randomString()
	if err != nil {
		return "", err
	}
	now := s.now()
	p := authdb.InsertOAuthStateParams{StateHash: hashToken(state), Purpose: purpose, Verifier: verifier,
		ReturnPath: safeReturn(returnPath), CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(telegramStateTTL))}
	if purpose == TelegramVerify {
		p.PersonKind = pgtype.Text{String: pr.Kind, Valid: true}
		p.PersonID = pgtype.Int4{Int32: pr.ID, Valid: true}
	}
	if err := s.q.InsertOAuthState(ctx, p); err != nil {
		return "", fmt.Errorf("store state: %w", err)
	}
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id":             {t.ClientID},
		"redirect_uri":          {t.WebOrigin + TelegramCallbackPath},
		"response_type":         {"code"},
		"scope":                 {"openid profile phone"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	return t.AuthURL + "?" + q.Encode(), nil
}

// TelegramCallback finishes the round trip at Telegram and answers where to
// send the browser: the web's /auth/telegram with ?ticket=…&purpose=…&return=…
// or ?error=…; reason explains a failure, for the log.
func (s *Service) TelegramCallback(ctx context.Context, query url.Values) (location string, reason error) {
	t := s.telegram
	fail := func(code string, err error) (string, error) {
		return t.WebOrigin + telegramWebPath + "?error=" + code, err
	}
	now := s.now()
	st, err := s.q.UseOAuthState(ctx, authdb.UseOAuthStateParams{StateHash: hashToken(query.Get("state")), Now: timestamp(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(TelegramFailed, errors.New("unknown, used or expired state"))
	}
	if err != nil {
		return fail(TelegramFailed, err)
	}
	if e := query.Get("error"); e != "" {
		if e == "access_denied" {
			return fail(TelegramDenied, nil)
		}
		return fail(TelegramFailed, fmt.Errorf("telegram answered %q", e))
	}
	code := query.Get("code")
	if code == "" {
		return fail(TelegramFailed, errors.New("no code"))
	}
	acc, err := s.telegramExchange(ctx, code, st.Verifier)
	if err != nil {
		return fail(TelegramFailed, err)
	}
	td := ticketData{purpose: ticketTelegramSignin, telegram: &acc}
	if st.Purpose == TelegramVerify {
		td.purpose, td.personKind, td.personID = ticketTelegramVerify, st.PersonKind.String, st.PersonID.Int32
	}
	ticket, err := s.newTicket(ctx, s.q, td, telegramTicketTTL)
	if err != nil {
		return fail(TelegramFailed, err)
	}
	q := url.Values{"ticket": {ticket}, "purpose": {st.Purpose}, "return": {st.ReturnPath}}
	return t.WebOrigin + telegramWebPath + "?" + q.Encode(), nil
}

// telegramExchange trades the code for Telegram's ID token (client secret by
// Basic auth, PKCE verifier) and checks it: signature (JWKS), issuer,
// audience, expiry.
func (s *Service) telegramExchange(ctx context.Context, code, verifier string) (TelegramAccount, error) {
	t := s.telegram
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {t.WebOrigin + TelegramCallbackPath},
		"client_id":     {t.ClientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return TelegramAccount{}, err
	}
	req.SetBasicAuth(t.ClientID, t.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := t.Client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return TelegramAccount{}, fmt.Errorf("telegram token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return TelegramAccount{}, fmt.Errorf("telegram token: %w", err)
	}
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || resp.StatusCode != http.StatusOK || tok.IDToken == "" {
		return TelegramAccount{}, fmt.Errorf("telegram token: status %d %s", resp.StatusCode, tok.Error)
	}
	payload, err := t.keys.verify(ctx, tok.IDToken)
	if err != nil {
		return TelegramAccount{}, err
	}
	return s.telegramClaims(payload)
}

func (s *Service) telegramClaims(payload []byte) (TelegramAccount, error) {
	var c struct {
		Iss                 string          `json:"iss"`
		Aud                 json.RawMessage `json:"aud"`
		Exp                 json.Number     `json:"exp"`
		ID                  json.Number     `json:"id"`
		Name                string          `json:"name"`
		GivenName           string          `json:"given_name"`
		FamilyName          string          `json:"family_name"`
		PreferredUsername   string          `json:"preferred_username"`
		Picture             string          `json:"picture"`
		PhoneNumber         string          `json:"phone_number"`
		PhoneNumberVerified *bool           `json:"phone_number_verified"`
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&c); err != nil {
		return TelegramAccount{}, fmt.Errorf("id token: %w", err)
	}
	if c.Iss != telegramIssuer {
		return TelegramAccount{}, fmt.Errorf("id token: issuer %q", c.Iss)
	}
	var aud json.Number
	if json.Unmarshal(c.Aud, &aud) != nil || aud.String() != s.telegram.ClientID {
		var str string
		if json.Unmarshal(c.Aud, &str) != nil || str != s.telegram.ClientID {
			return TelegramAccount{}, errors.New("id token: not for this client")
		}
	}
	if exp, err := c.Exp.Int64(); err != nil || s.now().Unix() >= exp {
		return TelegramAccount{}, errors.New("id token: expired")
	}
	id, err := c.ID.Int64()
	if err != nil || id == 0 {
		return TelegramAccount{}, errors.New("id token: no user id (scope profile)")
	}
	first, last := strings.TrimSpace(c.GivenName), strings.TrimSpace(c.FamilyName)
	if first == "" && last == "" {
		first, last, _ = strings.Cut(strings.TrimSpace(c.Name), " ")
	}
	acc := TelegramAccount{ID: id, Username: c.PreferredUsername, FirstName: first, LastName: strings.TrimSpace(last)}
	if strings.HasPrefix(c.Picture, "https://") || strings.HasPrefix(c.Picture, "http://") { // copyPhoto takes https only
		acc.PhotoURL = c.Picture
	}
	if c.PhoneNumber != "" && (c.PhoneNumberVerified == nil || *c.PhoneNumberVerified) {
		raw := strings.TrimSpace(c.PhoneNumber)
		if !strings.HasPrefix(raw, "+") {
			raw = "+" + raw
		}
		if phone, ok := people.NormalizePhone(raw); ok {
			acc.Phone = phone
		}
	}
	return acc, nil
}

// TelegramTicket signs in with a sign-in ticket from the callback:
//   - the Telegram account is linked to someone active: them (and the shared
//     phone becomes theirs if they have none and nobody else has it);
//   - else the shared phone is someone active's: Telegram is linked to them
//     (if they have no Telegram yet) and they are signed in;
//   - else a registered student is made: names, the phone if shared, the
//     photo, Telegram linked.
func (s *Service) TelegramTicket(ctx context.Context, ticket, deviceID string) (LoginResult, error) {
	if s.telegram == nil {
		return LoginResult{}, ErrTelegramOff
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := authdb.New(tx)
	t, err := s.useTicket(ctx, q, ticket, ticketTelegramSignin, s.now())
	if err != nil {
		return LoginResult{}, err
	}
	acc := *t.telegram
	dir := people.NewDirectory(tx)

	var p people.Person
	found := false
	if link, err := q.TelegramLinkByID(ctx, acc.ID); err == nil {
		if lp, err := dir.Get(ctx, link.PersonKind, link.PersonID); err == nil && lp.Active {
			p, found = lp, true
			if acc.Phone != "" && lp.LoginPhone == "" && lp.Registered {
				if _, owned, err := phoneOwner(ctx, dir, acc.Phone); err != nil {
					return LoginResult{}, err
				} else if !owned {
					if err := people.SetSelfPhone(ctx, tx, lp.ID, acc.Phone); err != nil {
						return LoginResult{}, err
					}
					if p, err = dir.Get(ctx, lp.Kind, lp.ID); err != nil {
						return LoginResult{}, err
					}
				}
			}
		} else if err != nil && !errors.Is(err, people.ErrNotFound) {
			return LoginResult{}, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, fmt.Errorf("telegram link: %w", err)
	}
	if !found && acc.Phone != "" {
		o, owned, err := phoneOwner(ctx, dir, acc.Phone)
		if err != nil {
			return LoginResult{}, err
		}
		if owned {
			p, found = o, true
		}
	}
	if !found {
		first, last := personNames(acc.FirstName, acc.LastName, acc.Username)
		id, err := people.CreateSelfStudent(ctx, tx, first, last, acc.Phone)
		if err != nil {
			return LoginResult{}, err
		}
		if p, err = dir.Get(ctx, people.KindStudent, id); err != nil {
			return LoginResult{}, err
		}
	}
	if err := linkTelegramIfFree(ctx, q, dir, p, acc); err != nil {
		return LoginResult{}, err
	}
	res, err := s.signInPerson(ctx, q, p, deviceID)
	if err != nil {
		return LoginResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LoginResult{}, err
	}
	s.ensurePhoto(ctx, p, acc.PhotoURL)
	return res, nil
}

// MyPhoneTelegram adds the phone Telegram shared (a verify ticket of the
// same person) to a signed-in person without one (applyPhone).
func (s *Service) MyPhoneTelegram(ctx context.Context, pr Principal, ticket, deviceID string) (LoginResult, error) {
	if s.telegram == nil {
		return LoginResult{}, ErrTelegramOff
	}
	u, err := s.phoneless(ctx, pr)
	if err != nil {
		return LoginResult{}, err
	}
	now := s.now()
	pt, err := s.q.PeekTicket(ctx, authdb.PeekTicketParams{TokenHash: hashToken(ticket), Purpose: ticketTelegramVerify, Now: timestamp(now)})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (pt.PersonKind.String != pr.Kind || pt.PersonID.Int32 != pr.ID)) {
		return LoginResult{}, ErrInvalidTicket
	}
	if err != nil {
		return LoginResult{}, err
	}
	if !pt.SharedPhone.Valid {
		return LoginResult{}, ErrNoTelegramPhone
	}
	return s.applyPhone(ctx, u, pt.SharedPhone.String, func(q *authdb.Queries) (*TelegramAccount, error) {
		t, err := s.useTicket(ctx, q, ticket, ticketTelegramVerify, now)
		if err != nil {
			return nil, err
		}
		return t.telegram, nil
	}, deviceID)
}

// linkTelegramIfFree links the Telegram account to p unless it is linked to
// someone else active or p has another one (never a link taken or
// replaced); a link to someone who left moves.
func linkTelegramIfFree(ctx context.Context, q *authdb.Queries, dir personGetter, p people.Person, acc TelegramAccount) error {
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
			return nil
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("telegram link: %w", err)
	}
	if has, err := q.HasTelegramLink(ctx, authdb.HasTelegramLinkParams{PersonKind: p.Kind, PersonID: p.ID}); err != nil {
		return fmt.Errorf("telegram link: %w", err)
	} else if has {
		return nil
	}
	if err := q.DeleteTelegramLink(ctx, acc.ID); err != nil {
		return fmt.Errorf("unlink telegram: %w", err)
	}
	if err := q.InsertTelegramLink(ctx, authdb.InsertTelegramLinkParams{TelegramID: acc.ID, PersonKind: p.Kind, PersonID: p.ID, Username: optText(acc.Username)}); err != nil {
		return fmt.Errorf("link telegram: %w", err)
	}
	return nil
}
