package api

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/redis/go-redis/v9"

	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
)

// Magic-code login ports plane.authentication.provider.credentials.magic_code.
// The Redis layout matches Django's, under the configured key prefix.
const (
	magicCodeTTL          = 600 * time.Second
	magicMaxVerifyAttempt = 5
)

type magicCodeData struct {
	CurrentAttempt int    `json:"current_attempt"`
	Email          string `json:"email"`
	Token          string `json:"token"`
}

// Atomic INCR with the TTL set only on first increment (Django's script).
var incrVerifyAttempts = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
    redis.call("EXPIRE", KEYS[1], tonumber(ARGV[1]))
end
return count`)

// magicProviderCheck is MagicCodeProvider.__init__'s configuration check.
func (a *API) magicProviderCheck(key string) error {
	if !a.cfg.Email.Configured() {
		return authErr("SMTP_NOT_CONFIGURED", kv{"email", key})
	}
	if !a.cfg.EnableMagicLinkLogin {
		return authErr("MAGIC_LINK_LOGIN_DISABLED", kv{"email", key})
	}
	return nil
}

// initiateMagicCode is MagicCodeProvider.initiate.
func (a *API) initiateMagicCode(ctx context.Context, email string) (key, token string, err error) {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "", "", err
	}
	token = strconv.FormatInt(n.Int64()+100000, 10)
	key = "magic_" + email
	rkey := a.cfg.RedisKeyPrefix + key

	value := magicCodeData{Email: email, Token: token}
	raw, err := a.rdb.Get(ctx, rkey).Result()
	switch {
	case err == nil:
		var prev magicCodeData
		if err := json.Unmarshal([]byte(raw), &prev, json.RejectUnknownMembers(false)); err != nil {
			return "", "", err
		}
		if prev.CurrentAttempt > 2 {
			exists, err := a.userExists(ctx, email)
			if err != nil {
				return "", "", err
			}
			if exists {
				return "", "", authErr("EMAIL_CODE_ATTEMPT_EXHAUSTED_SIGN_IN", kv{"email", email})
			}
			return "", "", authErr("EMAIL_CODE_ATTEMPT_EXHAUSTED_SIGN_UP", kv{"email", email})
		}
		value.CurrentAttempt = prev.CurrentAttempt + 1
	case !errors.Is(err, redis.Nil):
		return "", "", err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", "", err
	}
	if err := a.rdb.Set(ctx, rkey, b, magicCodeTTL).Err(); err != nil {
		return "", "", err
	}
	// Each new code gets a fresh verification budget.
	return key, token, a.rdb.Del(ctx, rkey+":verify_attempts").Err()
}

// verifyMagicCode is MagicCodeProvider.set_user_data: it returns the email
// the code was issued for, consuming the code on success.
func (a *API) verifyMagicCode(ctx context.Context, key, code string) (string, error) {
	rkey := a.cfg.RedisKeyPrefix + key
	attemptsKey := rkey + ":verify_attempts"
	email := strings.TrimPrefix(key, "magic_")

	raw, err := a.rdb.Get(ctx, rkey).Result()
	if errors.Is(err, redis.Nil) {
		exists, err := a.userExists(ctx, email)
		if err != nil {
			return "", err
		}
		return "", authErr(pick(exists, "EXPIRED_MAGIC_CODE_SIGN_IN", "EXPIRED_MAGIC_CODE_SIGN_UP"), kv{"email", email})
	}
	if err != nil {
		return "", err
	}
	var data magicCodeData
	if err := json.Unmarshal([]byte(raw), &data, json.RejectUnknownMembers(false)); err != nil {
		return "", err
	}
	if data.Token == code {
		return data.Email, a.rdb.Del(ctx, rkey, attemptsKey).Err()
	}

	exists, err := a.userExists(ctx, email)
	if err != nil {
		return "", err
	}
	ttl, err := a.rdb.TTL(ctx, rkey).Result()
	if err != nil {
		return "", err
	}
	secs := int64(ttl / time.Second)
	if secs <= 0 {
		secs = 1 // EXPIRE 0 would delete the counter and lift the cap
	}
	attempts, err := incrVerifyAttempts.Run(ctx, a.rdb, []string{attemptsKey}, secs).Int64()
	if err != nil {
		return "", err
	}
	if attempts >= magicMaxVerifyAttempt {
		if err := a.rdb.Del(ctx, rkey, attemptsKey).Err(); err != nil {
			return "", err
		}
		return "", authErr(pick(exists, "EMAIL_CODE_ATTEMPT_EXHAUSTED_SIGN_IN", "EMAIL_CODE_ATTEMPT_EXHAUSTED_SIGN_UP"), kv{"email", email})
	}
	return "", authErr(pick(exists, "INVALID_MAGIC_CODE_SIGN_IN", "INVALID_MAGIC_CODE_SIGN_UP"), kv{"email", email})
}

// magicGenerate ports authentication.views.app.magic.MagicGenerateEndpoint.
func (a *API) magicGenerate(c *httpx.Ctx) error {
	if err := a.drfCSRF(c); err != nil {
		return err
	}
	if c.User == nil {
		if err := a.throttle(c, "authentication", a.authRate); err != nil {
			return err
		}
	}
	form, err := c.Form()
	if err != nil {
		return err
	}
	email := normalizeEmail(form.Get("email"))
	if !auth.ValidEmail(email) {
		// Django's validate_email raises outside the view's except clause.
		return httpx.ErrDjangoServerError
	}
	ctx := c.Context()
	key, token, err := func() (string, string, error) {
		if err := a.magicProviderCheck(email); err != nil {
			return "", "", err
		}
		return a.initiateMagicCode(ctx, email)
	}()
	var ae *authError
	if errors.As(err, &ae) {
		return httpx.Body(http.StatusBadRequest, ae.body())
	}
	if err != nil {
		return err
	}
	if err := jobs.Enqueue(ctx, a.jobs, magicLinkEmail{Email: email, Key: key, Token: token}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"key": key})
}

// magicSignIn ports authentication.views.app.magic.MagicSignInEndpoint.
func (a *API) magicSignIn(c *httpx.Ctx) error {
	form, ok, err := a.formPost(c, true)
	if !ok {
		return err
	}
	ctx := c.Context()
	code := strings.TrimSpace(form.Get("code"))
	email := normalizeEmail(form.Get("email"))
	nextPath := form.Get("next_path")
	if code == "" || email == "" {
		return a.redirect(c, nextPath, authErr("MAGIC_SIGN_IN_EMAIL_CODE_REQUIRED").params())
	}
	if exists, err := a.userExists(ctx, email); err != nil {
		return err
	} else if !exists {
		return a.redirect(c, nextPath, authErr("USER_DOES_NOT_EXIST").params())
	}
	u, err := a.magicAuthenticate(c, "magic_"+email, code)
	var ae *authError
	if errors.As(err, &ae) {
		return a.redirect(c, nextPath, ae.params())
	}
	if err != nil {
		return err
	}
	if err := a.login(c, u); err != nil {
		return err
	}
	// Onboarded passwordless users land on "/"; everyone else on next_path
	// (or, effectively, the app root; see finishLogin).
	var autosetOnboarded bool
	if err := a.db.QueryRow(ctx, `
		SELECT u.is_password_autoset AND p.is_onboarded
		FROM users u JOIN profiles p ON p.user_id = u.id WHERE u.id = $1`, u.id,
	).Scan(&autosetOnboarded); err != nil {
		return err
	}
	if autosetOnboarded {
		return a.redirect(c, "/", nil)
	}
	return a.redirect(c, nextPath, nil)
}

// magicSignUp ports authentication.views.app.magic.MagicSignUpEndpoint.
func (a *API) magicSignUp(c *httpx.Ctx) error {
	form, ok, err := a.formPost(c, true)
	if !ok {
		return err
	}
	ctx := c.Context()
	code := strings.TrimSpace(form.Get("code"))
	email := normalizeEmail(form.Get("email"))
	nextPath := form.Get("next_path")
	if code == "" || email == "" {
		return a.redirect(c, nextPath, authErr("MAGIC_SIGN_UP_EMAIL_CODE_REQUIRED").params())
	}
	if exists, err := a.userExists(ctx, email); err != nil {
		return err
	} else if exists {
		return a.redirect(c, nextPath, authErr("USER_ALREADY_EXIST").params())
	}
	u, err := a.magicAuthenticate(c, "magic_"+email, code)
	return a.finishLogin(c, u, nextPath, err)
}

// magicAuthenticate is MagicCodeProvider.authenticate().
func (a *API) magicAuthenticate(c *httpx.Ctx, key, code string) (*loginUser, error) {
	if err := a.magicProviderCheck(key); err != nil {
		return nil, err
	}
	email, err := a.verifyMagicCode(c.Context(), key, code)
	if err != nil {
		return nil, err
	}
	return a.completeLoginOrSignup(c.Context(), c, email, credentials{autoset: true, medium: "magic-code"})
}
