// Package auth implements Django-compatible authentication primitives:
// password hashes, sessions, CSRF tokens and email validation.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/trustelem/zxcvbn"
)

// Django 5.2's PBKDF2PasswordHasher settings. Hashes stay interchangeable
// with Django, so users imported from an existing Plane keep their passwords.
const (
	pbkdf2Algorithm  = "pbkdf2_sha256"
	pbkdf2Iterations = 1_000_000
	saltLength       = 22 // 128 bits of entropy over [a-zA-Z0-9]
)

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// HashPassword returns a Django-format "pbkdf2_sha256$iter$salt$hash".
func HashPassword(password string) string {
	salt := RandomString(saltLength, alnum)
	return encodePBKDF2(password, salt, pbkdf2Iterations)
}

func encodePBKDF2(password, salt string, iterations int) string {
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), iterations, sha256.Size)
	if err != nil {
		panic(err) // only fails on invalid parameters
	}
	return fmt.Sprintf("%s$%d$%s$%s", pbkdf2Algorithm, iterations, salt, base64.StdEncoding.EncodeToString(key))
}

// CheckPassword verifies password against a Django-format hash.
func CheckPassword(password, encoded string) bool {
	parts := strings.SplitN(encoded, "$", 4)
	if len(parts) != 4 || parts[0] != pbkdf2Algorithm {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	want := encodePBKDF2(password, parts[2], iterations)
	return subtle.ConstantTimeCompare([]byte(want), []byte(encoded)) == 1
}

// UnusablePassword mirrors Django's set_unusable_password() marker.
func UnusablePassword() string { return "!" + RandomString(40, alnum) }

// PasswordStrong applies Plane's rule: a zxcvbn score of at least 3.
func PasswordStrong(password string) bool {
	return zxcvbn.PasswordStrength(password, nil).Score >= 3
}

// RandomString draws n characters from alphabet using crypto/rand.
func RandomString(n int, alphabet string) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[randIntn(len(alphabet))]
	}
	return string(b)
}

func randIntn(n int) int {
	// Rejection sampling over a byte to avoid modulo bias.
	limit := 256 - 256%n
	var buf [1]byte
	for {
		if _, err := rand.Read(buf[:]); err != nil {
			panic(err)
		}
		if int(buf[0]) < limit {
			return int(buf[0]) % n
		}
	}
}

// NeedsRehash reports whether a valid hash uses weaker settings than
// HashPassword would (Django's must_update).
func NeedsRehash(encoded string) bool {
	parts := strings.SplitN(encoded, "$", 4)
	if len(parts) != 4 || parts[0] != pbkdf2Algorithm {
		return false
	}
	n, err := strconv.Atoi(parts[1])
	return err == nil && n < pbkdf2Iterations
}
