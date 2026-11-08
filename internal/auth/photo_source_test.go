package auth

import "testing"

// Only the host and the first path segment are logged: never a username or a signed token.
func TestPhotoSource(t *testing.T) {
	for in, want := range map[string]string{
		"https://t.me/i/userpic/320/someone.jpg":        "t.me/i",
		"https://cdn4.telesco.pe/file/AbCdEf.jpg":       "cdn4.telesco.pe/file",
		"https://lh3.googleusercontent.com/a/xyz=s96-c": "lh3.googleusercontent.com/a",
		"::":     "unparsable",
		"no-url": "unparsable",
	} {
		if got := photoSource(in); got != want {
			t.Errorf("photoSource(%q) = %q, want %q", in, got, want)
		}
	}
}
