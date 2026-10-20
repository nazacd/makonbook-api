package telegram

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBot(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/botTOKEN/")
		switch method {
		case "sendMediaGroup", "editMessageMedia":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			if method == "editMessageMedia" {
				f, _, err := r.FormFile("p")
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(f)
				got = append(got, method+" "+r.FormValue("chat_id")+" "+r.FormValue("message_id")+" "+r.FormValue("media")+" "+string(b))
				_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7,"chat":{"id":-100}}}`)
				return
			}
			f, _, err := r.FormFile("p1")
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(f)
			got = append(got, method+" "+r.FormValue("chat_id")+" "+r.FormValue("media")+" "+string(b))
			_, _ = io.WriteString(w, `{"ok":true,"result":[{"message_id":7,"chat":{"id":-100}},{"message_id":8,"chat":{"id":-100}}]}`)
		case "sendMessage":
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			j, _ := json.Marshal(p)
			got = append(got, method+" "+string(j))
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":9,"chat":{"id":-100}}}`)
		case "editMessageText":
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`)
		case "answerCallbackQuery":
			_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`)
		default:
			t.Errorf("unexpected %s", method)
		}
	}))
	defer srv.Close()
	b := New("TOKEN")
	b.base = srv.URL

	msgs, err := b.SendPhotos(t.Context(), -100, []Photo{{PNG: []byte("one"), Caption: "Original"}, {PNG: []byte("two"), Caption: "Fix"}})
	if err != nil || len(msgs) != 2 || msgs[0].MessageID != 7 {
		t.Fatalf("album: %+v %v", msgs, err)
	}
	if !strings.Contains(got[0], `"media":"attach://p1"`) || !strings.HasSuffix(got[0], " two") || !strings.HasPrefix(got[0], "sendMediaGroup -100 ") {
		t.Errorf("album request: %s", got[0])
	}
	m, err := b.SendMessage(t.Context(), -100, "<b>vote</b>", 7, Keyboard{{{Text: "Yes", Data: "a:1"}}})
	if err != nil || m.MessageID != 9 ||
		!strings.Contains(got[1], `"inline_keyboard":[[{"callback_data":"a:1","text":"Yes"}]]`) || !strings.Contains(got[1], `"reply_parameters":{"allow_sending_without_reply":true,"message_id":7}`) {
		t.Errorf("message: %v %s", err, got[1])
	}
	if err := b.EditMessage(t.Context(), -100, 9, "same", nil); !NotModified(err) {
		t.Errorf("not modified: %v", err)
	}
	if err := b.EditPhoto(t.Context(), -100, 7, Photo{PNG: []byte("new"), Caption: "Original"}); err != nil ||
		got[2] != `editMessageMedia -100 7 {"caption":"Original","media":"attach://p","type":"photo"} new` {
		t.Errorf("edit photo: %v %s", err, got[2])
	}
	var e *Error
	if err := b.AnswerCallback(t.Context(), "q", "hi", ""); !errors.As(err, &e) || e.RetryAfter != 3*time.Second {
		t.Errorf("rate limit: %v", err)
	}
	if (User{FirstName: "Aziza", LastName: "K", Username: "aziza"}).Name() != "Aziza K (@aziza)" || (User{Username: "x"}).Name() != "@x" {
		t.Error("names")
	}
}
