package auth

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"plane-lite/server/internal/config"
)

// Fixtures below were produced by the Django reference (Django 5.2,
// zxcvbn 4.4.28) so these tests pin byte-level compatibility.

func TestCheckPasswordAcceptsDjangoHashes(t *testing.T) {
	cases := []struct{ password, hash string }{
		{"correct-Horse-battery-staple-91", "pbkdf2_sha256$1000000$bi1LRDcO6dDtMT3DKBEBFv$pHX/zwBQwYdlvqVX2J8H/eOinBBC+dTk/8gKG1dtzG0="},
		{"hunter2", "pbkdf2_sha256$600000$abcdefghijklmnopqrstuv$nsFDQ93Pq4tYO0KnnIGXG7tPR4nUdZgqty3nPH8F6oQ="},
	}
	for _, c := range cases {
		if !CheckPassword(c.password, c.hash) {
			t.Errorf("CheckPassword(%q, django hash) = false", c.password)
		}
		if CheckPassword(c.password+"x", c.hash) {
			t.Errorf("CheckPassword accepted a wrong password for %q", c.password)
		}
	}
	if !NeedsRehash(cases[1].hash) || NeedsRehash(cases[0].hash) {
		t.Error("NeedsRehash should flag only the 600k-iteration hash")
	}
}

func TestHashPasswordRoundTrip(t *testing.T) {
	h := HashPassword("s3cr3t-pl4n3")
	if !strings.HasPrefix(h, "pbkdf2_sha256$1000000$") || len(strings.Split(h, "$")[2]) != 22 {
		t.Fatalf("unexpected hash format %q", h)
	}
	if !CheckPassword("s3cr3t-pl4n3", h) || CheckPassword("nope", h) {
		t.Fatal("round trip failed")
	}
	if CheckPassword("", UnusablePassword()) {
		t.Fatal("unusable password must never match")
	}
}

func TestValidEmailMatchesDjango(t *testing.T) {
	cases := map[string]bool{
		"a@b.co": true, "alice@example.com": true, "alice@localhost": true, "a@b": false, "a@b.c": false,
		"a@-b.com": false, "a@b-.com": false, "a@b.c0m": false, "a@xn--p1ai.xn--p1ai": true,
		"a@xn--80ak6aa92e.com": true, `"quoted local"@example.com`: false, "a..b@example.com": false,
		".a@example.com": false, "a@[127.0.0.1]": true, "a@[::1]": true, "a@[IPv6:::1]": false,
		"a@[300.1.1.1]": false, "a@exa_mple.com": false, "a@münchen.de": true, "a@xn--mnchen-3ya.de": true,
		"a+tag@sub.domain.example.org": true, "a@123.com": true, "a@example.123": false,
		"a@example.c-m": true, "a@example.-cm": false, "a@a.b.c.d.e.fg": true, "üser@example.com": false,
		"a b@example.com": false, "a@" + strings.Repeat("x", 63) + ".com": true,
		"a@" + strings.Repeat("x", 64) + ".com": false, "a@example.com.": false, "@example.com": false,
		"a@": false, "a@@b.com": false, "A@EXAMPLE.COM": true,
	}
	for email, want := range cases {
		if got := ValidEmail(email); got != want {
			t.Errorf("ValidEmail(%q) = %v, Django says %v", email, got, want)
		}
	}
}

func TestPasswordStrengthMatchesZxcvbnPython(t *testing.T) {
	scores := map[string]int{
		"password1": 0, "correct-Horse-battery-staple-91": 4, "Tr0ub4dor&3": 4, "hunter2": 1, "qwertyuiop": 0, "P@ssw0rd!": 1, "plane1234": 1, "abcdefgh12": 1, "alice@example.com": 4, "letmein!2024": 2, "zxcvbnm,./": 1, "correcthorsebatterystaple": 4, "Summer2026!": 2, "ilovemycat99": 4, "MyPlaneP4ss": 3, "aaaaaaaaaaaaaaaa": 0, "1q2w3e4r5t6y": 1, "ji32k7au4a83": 4, "D0g!!!!!!!": 1, "s3cr3t-pl4n3": 3,
	}
	for pw, score := range scores {
		if got := PasswordStrong(pw); got != (score >= 3) {
			t.Errorf("PasswordStrong(%q) = %v, Python zxcvbn score is %d", pw, got, score)
		}
	}
}

func TestCSRFMaskRoundTrip(t *testing.T) {
	c := NewCSRF(&config.Config{})
	w := httptest.NewRecorder()
	token := c.Token(w, httptest.NewRequest("GET", "/", nil))
	cookie := w.Result().Cookies()[0]
	if len(token) != 64 || len(cookie.Value) != 32 || unmask(token) != cookie.Value {
		t.Fatalf("token %q does not unmask to cookie secret %q", token, cookie.Value)
	}

	post := func(form url.Values, origin string) bool {
		r := httptest.NewRequest("POST", "http://api.example.com/auth/sign-in/", strings.NewReader(form.Encode()))
		r.AddCookie(cookie)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return c.Check(r, form) == nil
	}
	if !post(url.Values{"csrfmiddlewaretoken": {token}}, "") {
		t.Error("valid masked token rejected")
	}
	if !post(url.Values{"csrfmiddlewaretoken": {cookie.Value}}, "") {
		t.Error("valid unmasked secret rejected")
	}
	if post(url.Values{"csrfmiddlewaretoken": {mask(RandomString(32, alnum))}}, "") {
		t.Error("token for another secret accepted")
	}
	if post(url.Values{"csrfmiddlewaretoken": {token}}, "http://evil.example.com") {
		t.Error("cross-origin post accepted")
	}
	if !post(url.Values{"csrfmiddlewaretoken": {token}}, "http://api.example.com") {
		t.Error("same-origin post rejected")
	}
}

func TestResetTokenMatchesDjango(t *testing.T) {
	const secret = "reference-secret-key-not-for-production-use-0123456789"
	login := time.Date(2026, 10, 7, 18, 1, 26, 123456000, time.UTC)
	u := ResetSubject{
		ID:           uuid.MustParse("0b6d8f0e-1c51-4c55-9a4e-6f3f1d6c3d01"),
		PasswordHash: "pbkdf2_sha256$1000000$abc$def=",
		LastLogin:    &login,
		Email:        "alice@example.com",
	}
	now := time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)
	if got := MakeResetToken(secret, u, now); got != "dg4ie0-6d227fe51d3998101fd8edf98334646e" {
		t.Errorf("token = %s", got)
	}
	noLogin := u
	noLogin.LastLogin = nil
	if got := MakeResetToken(secret, noLogin, now); got != "dg4ie0-64abe3b7c7931508abfc9397cf6417b0" {
		t.Errorf("token without last_login = %s", got)
	}
	tok := MakeResetToken(secret, u, now)
	if !CheckResetToken(secret, u, tok, now.Add(time.Hour), time.Hour) {
		t.Error("token rejected within timeout")
	}
	if CheckResetToken(secret, u, tok, now.Add(time.Hour+time.Second), time.Hour) {
		t.Error("expired token accepted")
	}
	changed := u
	changed.PasswordHash += "x"
	if CheckResetToken(secret, changed, tok, now, time.Hour) {
		t.Error("token survived a password change")
	}

	if got := EncodeUID(u.ID); got != "MGI2ZDhmMGUtMWM1MS00YzU1LTlhNGUtNmYzZjFkNmMzZDAx" {
		t.Errorf("uid = %s", got)
	}
	for in, want := range map[string]error{
		"MGI2ZDhmMGUtMWM1MS00YzU1LTlhNGUtNmYzZjFkNmMzZDAx": nil,
		"!!!":      ErrUIDNotUUID, // decodes to "" in Python
		"bm9ib2R5": ErrUIDNotUUID, // "nobody"
		"Zm9vY":    ErrUIDDecode,
		"_w":       ErrUIDUnicode, // 0xff
	} {
		if _, err := DecodeUID(in); err != want {
			t.Errorf("DecodeUID(%q) err = %v, want %v", in, err, want)
		}
	}
}
