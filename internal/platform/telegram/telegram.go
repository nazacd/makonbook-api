// Package telegram is the part of the Telegram Bot API the review bot uses:
// reading updates (long polling), posting pictures and messages with
// buttons, editing them, and answering button presses.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Bot talks to the Bot API as one bot.
type Bot struct {
	token string
	base  string
	http  *http.Client
}

// New is a bot with its token (from @BotFather).
func New(token string) *Bot {
	return &Bot{token: token, base: "https://api.telegram.org", http: &http.Client{Timeout: 60 * time.Second}}
}

// User is a Telegram account.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

// Name is how the account shows: its name, and @username when it has one.
func (u User) Name() string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.Username != "" {
		if name == "" {
			return "@" + u.Username
		}
		name += " (@" + u.Username + ")"
	}
	return name
}

// Chat is a private chat, a group or a channel.
type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"` // private | group | supergroup | channel
	Title string `json:"title"`
}

// Message is a message, as far as the bot reads it.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

// CallbackQuery is a press on one of the bot's buttons.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// ChatMemberUpdated says the bot was added to or removed from a chat.
type ChatMemberUpdated struct {
	Chat          Chat `json:"chat"`
	From          User `json:"from"`
	NewChatMember struct {
		Status string `json:"status"`
	} `json:"new_chat_member"`
}

// Update is one thing that happened.
type Update struct {
	UpdateID      int64              `json:"update_id"`
	Message       *Message           `json:"message"`
	CallbackQuery *CallbackQuery     `json:"callback_query"`
	MyChatMember  *ChatMemberUpdated `json:"my_chat_member"`
}

// Button is an inline button that sends Data back to the bot.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// Keyboard is rows of buttons under a message; nil removes them.
type Keyboard [][]Button

// Error is an answer from Telegram that isn't ok.
type Error struct {
	Code        int
	Description string
	RetryAfter  time.Duration // set on 429: wait that long
}

func (e *Error) Error() string { return fmt.Sprintf("telegram: %d %s", e.Code, e.Description) }

// NotModified: an edit that changes nothing, which Telegram refuses; harmless.
func NotModified(err error) bool {
	var e *Error
	return errors.As(err, &e) && strings.Contains(e.Description, "message is not modified")
}

// Me is the bot's own account (its username makes links to its chat).
func (b *Bot) Me(ctx context.Context) (User, error) {
	var u User
	return u, b.call(ctx, "getMe", struct{}{}, &u)
}

// Updates waits up to wait for what happened after offset (the last
// update's id + 1).
func (b *Bot) Updates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error) {
	var u []Update
	err := b.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": int(wait.Seconds()),
		"allowed_updates": []string{"message", "callback_query", "my_chat_member"},
	}, &u)
	return u, err
}

// Photo is a picture to send, with its caption.
type Photo struct {
	PNG     []byte
	Caption string
}

// SendPhotos posts pictures to a chat: one as a photo, several as an album.
// It returns their messages.
func (b *Bot) SendPhotos(ctx context.Context, chatID int64, photos []Photo) ([]Message, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("chat_id", fmt.Sprint(chatID))
	method := "sendPhoto"
	if len(photos) == 1 {
		_ = w.WriteField("caption", photos[0].Caption)
		if err := attach(w, "photo", photos[0].PNG); err != nil {
			return nil, err
		}
	} else {
		method = "sendMediaGroup"
		media := make([]map[string]string, len(photos))
		for i, p := range photos {
			name := fmt.Sprintf("p%d", i)
			media[i] = map[string]string{"type": "photo", "media": "attach://" + name, "caption": p.Caption}
			if err := attach(w, name, p.PNG); err != nil {
				return nil, err
			}
		}
		j, _ := json.Marshal(media)
		_ = w.WriteField("media", string(j))
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	raw, err := b.post(ctx, method, w.FormDataContentType(), &body)
	if err != nil {
		return nil, err
	}
	if method == "sendPhoto" {
		var m Message
		return []Message{m}, json.Unmarshal(raw, &m)
	}
	var ms []Message
	return ms, json.Unmarshal(raw, &ms)
}

func attach(w *multipart.Writer, name string, png []byte) error {
	part, err := w.CreateFormFile(name, name+".png")
	if err != nil {
		return err
	}
	_, err = part.Write(png)
	return err
}

// SendMessage posts text (Telegram's HTML) to a chat, as a reply to replyTo
// when it isn't 0, with buttons when kb isn't nil.
func (b *Bot) SendMessage(ctx context.Context, chatID int64, text string, replyTo int64, kb Keyboard) (Message, error) {
	p := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]bool{"is_disabled": true}}
	if replyTo != 0 {
		p["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	if kb != nil {
		p["reply_markup"] = map[string]any{"inline_keyboard": kb}
	}
	var m Message
	return m, b.call(ctx, "sendMessage", p, &m)
}

// EditMessage replaces a message's text and buttons (nil kb: no buttons).
func (b *Bot) EditMessage(ctx context.Context, chatID, messageID int64, text string, kb Keyboard) error {
	if kb == nil {
		kb = Keyboard{}
	}
	return b.call(ctx, "editMessageText", map[string]any{
		"chat_id": chatID, "message_id": messageID, "text": text, "parse_mode": "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
		"reply_markup":         map[string]any{"inline_keyboard": kb},
	}, nil)
}

// AnswerCallback answers a button press: a short notice only the presser
// sees, or a t.me link to the bot's chat to open.
func (b *Bot) AnswerCallback(ctx context.Context, id, text, link string) error {
	p := map[string]any{"callback_query_id": id}
	if text != "" {
		p["text"] = text
	}
	if link != "" {
		p["url"] = link
	}
	return b.call(ctx, "answerCallbackQuery", p, nil)
}

func (b *Bot) call(ctx context.Context, method string, params, out any) error {
	j, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err := b.post(ctx, method, "application/json", bytes.NewReader(j))
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// post sends a request and returns the answer's result.
func (b *Bot) post(ctx context.Context, method, contentType string, body io.Reader) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/bot"+b.token+"/"+method, body)
	if err != nil {
		return nil, errors.New("telegram: build request")
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := b.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL holds the token
		}
		return nil, fmt.Errorf("telegram %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&env); err != nil {
		return nil, fmt.Errorf("telegram %s: status %d, unreadable answer", method, resp.StatusCode)
	}
	if !env.OK {
		return nil, &Error{Code: env.ErrorCode, Description: env.Description, RetryAfter: time.Duration(env.Parameters.RetryAfter) * time.Second}
	}
	return env.Result, nil
}
