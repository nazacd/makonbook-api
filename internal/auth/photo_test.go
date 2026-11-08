package auth

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nazacd/makonbook-api/internal/platform/ratelimit"
)

func encoded(t *testing.T, format string, size int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for x := range size {
		img.Set(x, x, color.RGBA{R: 200, A: 255})
	}
	var b bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	case "png":
		err = png.Encode(&b, img)
	case "gif":
		err = gif.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// putPhoto sends a raw body to PUT /v1/me/photo.
func (a accounts) putPhoto(token string, data []byte, contentType string) response {
	a.t.Helper()
	req, _ := http.NewRequestWithContext(a.t.Context(), http.MethodPut, a.srv.URL+"/v1/me/photo", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := response{status: resp.StatusCode, header: resp.Header}
	_ = json.NewDecoder(resp.Body).Decode(&out.body)
	return out
}

func (a accounts) fetchPhoto(path string) (int, []byte) {
	a.t.Helper()
	req, _ := http.NewRequestWithContext(a.t.Context(), http.MethodGet, a.srv.URL+path, nil)
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestOwnPhoto(t *testing.T) {
	a := setupGoogle(t)
	a.svc.photos.allowHTTP = true
	// No phone yet: a photo all the same.
	token, _ := a.emailSignup("g-5@gmail.com", "Photo")

	jpg := encoded(t, "jpeg", 512)
	r := a.putPhoto(token, jpg, "image/jpeg")
	first, _ := r.body["photo_url"].(string)
	if r.status != http.StatusOK || !strings.HasPrefix(first, "/v1/photos/") || r.body["phone"] != nil {
		t.Fatalf("upload: %d %v", r.status, r.body)
	}
	if code, b := a.fetchPhoto(first); code != http.StatusOK || !bytes.Equal(b, jpg) {
		t.Errorf("served: %d %d bytes", code, len(b))
	}
	// A new one: a new address; the old one is gone.
	r = a.putPhoto(token, encoded(t, "png", 300), "image/png")
	second, _ := r.body["photo_url"].(string)
	if r.status != http.StatusOK || second == first {
		t.Fatalf("second upload: %d %v", r.status, r.body)
	}
	if code, _ := a.fetchPhoto(first); code != http.StatusNotFound {
		t.Errorf("old photo still served: %d", code)
	}

	// What the bytes are counts, not the header; it must decode.
	for name, c := range map[string]struct {
		data   []byte
		header string
		status int
		code   string
	}{
		"gif":        {encoded(t, "gif", 64), "image/jpeg", http.StatusUnprocessableEntity, CodeInvalidPhoto},
		"text":       {[]byte("hello, not an image"), "image/png", http.StatusUnprocessableEntity, CodeInvalidPhoto},
		"broken jpg": {append(jpg[:20:20], bytes.Repeat([]byte{0}, 100)...), "image/jpeg", http.StatusUnprocessableEntity, CodeInvalidPhoto},
		"too large":  {append(jpg, bytes.Repeat([]byte{0}, 512<<10)...), "image/jpeg", http.StatusRequestEntityTooLarge, CodePhotoTooLarge},
	} {
		if r := a.putPhoto(token, c.data, c.header); r.status != c.status || r.code() != c.code {
			t.Errorf("%s: %d %v", name, r.status, r.body)
		}
	}

	// Removed: none, and Google's picture doesn't come back.
	r = a.do("DELETE", "/v1/me/photo", token, nil)
	if r.status != http.StatusOK || r.body["photo_url"] != nil {
		t.Fatalf("remove: %d %v", r.status, r.body)
	}
	if code, _ := a.fetchPhoto(second); code != http.StatusNotFound {
		t.Errorf("removed photo still served: %d", code)
	}
	claims := a.claims("g-5")
	claims["picture"] = photoServer(t).URL + "/me.png"
	if r := a.trade(a.roundTrip("/", claims).Get("ticket")); r.body["user"].(map[string]any)["photo_url"] != nil {
		t.Errorf("the automatic copy came back: %v", r.body)
	}

	// Someone still on the default password can set one too.
	enrolled, _ := a.mustLogin("+998902001234", "1234")
	if r := a.putPhoto(enrolled, jpg, "image/jpeg"); r.status != http.StatusOK || r.body["photo_url"] == nil {
		t.Errorf("enrolled on the default password: %d %v", r.status, r.body)
	}

	// A few uploads an hour.
	a.svc.photoLimit = ratelimit.New(2, photoUploadWindow)
	for range 2 {
		a.putPhoto(token, jpg, "image/jpeg")
	}
	if r := a.putPhoto(token, jpg, "image/jpeg"); r.status != http.StatusTooManyRequests || r.code() != "rate_limited" {
		t.Errorf("rate limit: %d %v", r.status, r.body)
	}
}

// A photo the person uploaded goes with them into the account they merge into.
func TestOwnPhotoMoves(t *testing.T) {
	a := setupAccounts(t, nil)
	token, _ := a.emailSignup("mover@example.com", "Mover")
	jpg := encoded(t, "jpeg", 128)
	if r := a.putPhoto(token, jpg, "image/jpeg"); r.status != http.StatusOK {
		t.Fatal(r.status)
	}
	r := a.addPhone(token, "+998902001234")
	u, _ := r.body["user"].(map[string]any)
	if r.status != http.StatusOK || u["id"] != float64(1) || u["photo_url"] == nil {
		t.Fatalf("merge: %d %v", r.status, r.body)
	}
	if code, b := a.fetchPhoto(u["photo_url"].(string)); code != http.StatusOK || !bytes.Equal(b, jpg) {
		t.Errorf("moved photo: %d", code)
	}
}
