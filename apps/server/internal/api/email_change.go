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
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"

	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/throttle"
)

// emailVerificationRate is EmailVerificationThrottle (per user).
var emailVerificationRate = throttle.MustRate("3/hour")

const emailUpdateCodeTTL = 600 * time.Second

// emailUpdateCode is bgtasks.user_email_update_task.send_email_update_magic_code.
type emailUpdateCode struct {
	Email string `json:"email"`
	Token string `json:"token"`
}

func (emailUpdateCode) Kind() string                 { return "email_update_magic_code" }
func (emailUpdateCode) InsertOpts() river.InsertOpts { return emailInsertOpts }

// emailUpdated is bgtasks.user_email_update_task.send_email_update_confirmation.
type emailUpdated struct {
	Email string `json:"email"`
}

func (emailUpdated) Kind() string                 { return "email_update_confirmation" }
func (emailUpdated) InsertOpts() river.InsertOpts { return emailInsertOpts }

func (a *API) emailUpdateKey(userID uuid.UUID, email string) string {
	return a.cfg.RedisKeyPrefix + "magic_email_update_" + userID.String() + "_" + email
}

// validateNewEmail is UserEndpoint._validate_new_email.
func (a *API) validateNewEmail(ctx context.Context, userID uuid.UUID, email string) error {
	if email == "" {
		return httpx.Err(http.StatusBadRequest, "Email is required")
	}
	if !auth.ValidEmail(email) {
		return httpx.Err(http.StatusBadRequest, "Invalid email format")
	}
	var current *string
	var taken bool
	if err := a.db.QueryRow(ctx, `
		SELECT (SELECT email FROM users WHERE id = $1),
			EXISTS (SELECT 1 FROM users WHERE email = $2 AND id <> $1)`, userID, email,
	).Scan(&current, &taken); err != nil {
		return err
	}
	if current != nil && *current == email {
		return httpx.Err(http.StatusBadRequest, "New email must be different from current email")
	}
	if taken {
		return httpx.Err(http.StatusBadRequest, "An account with this email already exists")
	}
	return nil
}

// emailGenerateCode ports UserEndpoint.generate_email_verification_code.
func (a *API) emailGenerateCode(c *httpx.Ctx) error {
	ok, wait, err := a.limiter.Allow(c.Context(), "email_verification", c.User.ID.String(), emailVerificationRate)
	if err != nil {
		return err
	}
	if !ok {
		return a.rateLimited(c, wait)
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	raw, err := strData(data, "email")
	if err != nil {
		return err
	}
	email := strings.ToLower(drf.PyStrip(raw))
	ctx := c.Context()
	if err := a.validateNewEmail(ctx, c.User.ID, email); err != nil {
		return err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return err
	}
	token := strconv.FormatInt(n.Int64()+100000, 10)
	value, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return err
	}
	if err := a.rdb.Set(ctx, a.emailUpdateKey(c.User.ID, email), value, emailUpdateCodeTTL).Err(); err != nil {
		return err
	}
	if err := jobs.Enqueue(ctx, a.jobs, emailUpdateCode{Email: email, Token: token}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"message": "Verification code sent to email"})
}

// updateEmail ports UserEndpoint.update_email.
func (a *API) updateEmail(c *httpx.Ctx) error {
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	rawEmail, err := strData(data, "email")
	if err != nil {
		return err
	}
	rawCode, err := strData(data, "code")
	if err != nil {
		return err
	}
	email := strings.ToLower(drf.PyStrip(rawEmail))
	code := drf.PyStrip(rawCode)
	ctx := c.Context()
	if err := a.validateNewEmail(ctx, c.User.ID, email); err != nil {
		return err
	}
	if code == "" {
		return httpx.Err(http.StatusBadRequest, "Verification code is required")
	}
	key := a.emailUpdateKey(c.User.ID, email)
	cached, err := a.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) || (err == nil && cached == "") {
		return httpx.Err(http.StatusBadRequest, "Verification code has expired or is invalid")
	}
	if err != nil {
		return httpx.Err(http.StatusBadRequest, "Failed to verify code. Please try again.")
	}
	var stored struct {
		Token any `json:"token"`
	}
	if json.Unmarshal([]byte(cached), &stored) != nil {
		return httpx.Err(http.StatusBadRequest, "Failed to verify code. Please try again.")
	}
	if s, _ := stored.Token.(string); s != code {
		return httpx.Err(http.StatusBadRequest, "Invalid verification code")
	}
	// The address may have been taken since the code was sent.
	var taken bool
	var oldEmail *string
	if err := a.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM users WHERE email = $2 AND id <> $1), (SELECT email FROM users WHERE id = $1)`,
		c.User.ID, email).Scan(&taken, &oldEmail); err != nil {
		return err
	}
	if taken {
		return httpx.Err(http.StatusBadRequest, "An account with this email already exists")
	}
	if _, err := a.db.Exec(ctx, `UPDATE users SET email = $2, is_email_verified = false, `+userSaveSQL+` WHERE id = $1`,
		c.User.ID, email); err != nil {
		return err
	}
	if err := a.rdb.Del(ctx, key).Err(); err != nil {
		return err
	}
	if err := a.sessions.Logout(ctx, c.W, c.R); err != nil {
		return err
	}
	if err := jobs.Enqueue(ctx, a.jobs, emailUpdated{Email: email}); err != nil {
		return err
	}
	if oldEmail != nil {
		if err := jobs.Enqueue(ctx, a.jobs, emailUpdated{Email: *oldEmail}); err != nil {
			return err
		}
	}
	u, err := a.loadUserMe(ctx, c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, u)
}
