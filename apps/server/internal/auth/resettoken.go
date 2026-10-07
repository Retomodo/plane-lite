package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"plane-lite/server/internal/drf"
)

// Port of django.contrib.auth.tokens.PasswordResetTokenGenerator. A token is
// "<base36 seconds since 2001-01-01>-<hmac>", where the HMAC covers the
// user's password hash and last login, so it dies once either changes.

const resetKeySalt = "django.contrib.auth.tokens.PasswordResetTokenGenerator"

var resetEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// ResetSubject is the user state a reset token is bound to.
type ResetSubject struct {
	ID           uuid.UUID
	PasswordHash string
	LastLogin    *time.Time
	Email        string
}

// MakeResetToken is PasswordResetTokenGenerator.make_token.
func MakeResetToken(secret string, u ResetSubject, now time.Time) string {
	return makeResetToken(secret, u, int64(now.Sub(resetEpoch)/time.Second))
}

// CheckResetToken is PasswordResetTokenGenerator.check_token.
func CheckResetToken(secret string, u ResetSubject, token string, now time.Time, timeout time.Duration) bool {
	tsB36, _, ok := strings.Cut(token, "-")
	if !ok {
		return false
	}
	ts, err := strconv.ParseInt(tsB36, 36, 64)
	if err != nil || ts < 0 {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(makeResetToken(secret, u, ts)), []byte(token)) != 1 {
		return false
	}
	return int64(now.Sub(resetEpoch)/time.Second)-ts <= int64(timeout/time.Second)
}

func makeResetToken(secret string, u ResetSubject, ts int64) string {
	login := ""
	if u.LastLogin != nil {
		// last_login.replace(microsecond=0, tzinfo=None), stored in UTC.
		login = u.LastLogin.UTC().Format("2006-01-02 15:04:05")
	}
	value := fmt.Sprintf("%s%s%s%d%s", u.ID, u.PasswordHash, login, ts, u.Email)
	key := sha256.Sum256([]byte(resetKeySalt + secret))
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(value))
	full := hex.EncodeToString(mac.Sum(nil))
	half := make([]byte, 0, len(full)/2)
	for i := 0; i < len(full); i += 2 { // hexdigest()[::2]
		half = append(half, full[i])
	}
	return strconv.FormatInt(ts, 36) + "-" + string(half)
}

// EncodeUID is urlsafe_base64_encode(smart_bytes(user.id)).
func EncodeUID(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id.String()))
}

// Errors from DecodeUID, mirroring which exception Django's view hits.
var (
	ErrUIDDecode  = errors.New("uid: not base64")       // ValueError -> INVALID_PASSWORD_TOKEN
	ErrUIDUnicode = errors.New("uid: not utf-8")        // DjangoUnicodeDecodeError -> EXPIRED_PASSWORD_TOKEN
	ErrUIDNotUUID = errors.New("uid: not a valid UUID") // ValidationError, uncaught -> HTTP 500
)

// DecodeUID is smart_str(urlsafe_base64_decode(uidb64)) followed by the
// UUIDField lookup's parsing, reproducing Python's lenient base64 decoder.
func DecodeUID(s string) (uuid.UUID, error) {
	var clean []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
			clean = append(clean, c)
		case c == '-':
			clean = append(clean, '+')
		case c == '_':
			clean = append(clean, '/')
		}
		// Anything else (including '=') is discarded, as non-strict
		// binascii.a2b_base64 does.
	}
	if len(clean)%4 == 1 {
		return uuid.Nil, ErrUIDDecode
	}
	raw, err := base64.RawStdEncoding.DecodeString(string(clean))
	if err != nil {
		return uuid.Nil, ErrUIDDecode
	}
	if !utf8.Valid(raw) {
		return uuid.Nil, ErrUIDUnicode
	}
	id, ok := drf.ParseUUID(string(raw))
	if !ok {
		return uuid.Nil, ErrUIDNotUUID
	}
	return id, nil
}
