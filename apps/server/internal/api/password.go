package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
)

// passwordResetTimeout is Plane's PASSWORD_RESET_TIMEOUT.
const passwordResetTimeout = time.Hour

// forgotPasswordEmail is bgtasks.forgot_password_task.forgot_password.
type forgotPasswordEmail struct {
	FirstName   string `json:"first_name"`
	Email       string `json:"email"`
	UIDB64      string `json:"uidb64"`
	Token       string `json:"token"`
	CurrentSite string `json:"current_site"`
}

func (forgotPasswordEmail) Kind() string                 { return "forgot_password" }
func (forgotPasswordEmail) InsertOpts() river.InsertOpts { return emailInsertOpts }

func (a *API) registerPasswordJobs() {
	jobs.Register(a.jobs, func(ctx context.Context, j forgotPasswordEmail) error {
		link := j.CurrentSite + "/accounts/reset-password/?uidb64=" + j.UIDB64 + "&token=" + j.Token + "&email=" + j.Email
		return a.mailer.SendTemplate(ctx, j.Email, "A new password to your Plane account has been requested",
			"auth/forgot_password.html", map[string]string{"first_name": j.FirstName, "forgot_password_url": link, "email": j.Email})
	})
}

// forgotPassword ports authentication.views.app.password_management.ForgotPasswordEndpoint.
func (a *API) forgotPassword(c *httpx.Ctx) error {
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
	if !a.cfg.Email.Configured() {
		return httpx.Body(http.StatusBadRequest, authErr("SMTP_NOT_CONFIGURED").body())
	}
	// No normalization: Django validates and looks up the email as given.
	email := form.Get("email")
	if !auth.ValidEmail(email) {
		return httpx.Body(http.StatusBadRequest, authErr("INVALID_EMAIL").body())
	}
	ctx := c.Context()
	var (
		sub       auth.ResetSubject
		firstName string
	)
	err = a.db.QueryRow(ctx, `SELECT id, password, last_login, email, first_name FROM users WHERE email = $1`, email).
		Scan(&sub.ID, &sub.PasswordHash, &sub.LastLogin, &sub.Email, &firstName)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Body(http.StatusBadRequest, authErr("USER_DOES_NOT_EXIST").body())
	}
	if err != nil {
		return err
	}
	if err := jobs.Enqueue(ctx, a.jobs, forgotPasswordEmail{
		FirstName:   firstName,
		Email:       sub.Email,
		UIDB64:      auth.EncodeUID(sub.ID),
		Token:       auth.MakeResetToken(a.cfg.SecretKey, sub, time.Now()),
		CurrentSite: a.baseHost(true),
	}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"message": "Check your email to reset your password"})
}

// resetPassword ports authentication.views.app.password_management.ResetPasswordEndpoint.
func (a *API) resetPassword(c *httpx.Ctx) error {
	form, ok, err := a.formPost(c, false)
	if !ok {
		return err
	}
	fail := func(message string) error {
		return c.Redirect(a.resetRedirect("accounts/reset-password", authErr(message).params()))
	}
	id, err := auth.DecodeUID(c.Param("uidb64"))
	switch {
	case errors.Is(err, auth.ErrUIDNotUUID):
		return httpx.ErrDjangoServerError // UUIDField raises ValidationError, uncaught
	case err != nil:
		// DjangoUnicodeDecodeError subclasses ValueError, so both decode
		// failures hit the INVALID_PASSWORD_TOKEN branch.
		return fail("INVALID_PASSWORD_TOKEN")
	}
	ctx := c.Context()
	sub := auth.ResetSubject{ID: id}
	var email *string
	err = a.db.QueryRow(ctx, `SELECT password, last_login, email FROM users WHERE id = $1`, id).
		Scan(&sub.PasswordHash, &sub.LastLogin, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail("INVALID_PASSWORD_TOKEN")
	}
	if err != nil {
		return err
	}
	if email != nil {
		sub.Email = *email
	}
	if !auth.CheckResetToken(a.cfg.SecretKey, sub, c.Param("token"), time.Now(), passwordResetTimeout) {
		return fail("INVALID_PASSWORD_TOKEN")
	}
	password := form.Get("password")
	if password == "" {
		return fail("INVALID_PASSWORD")
	}
	if !auth.PasswordStrong(password) {
		return fail("PASSWORD_TOO_WEAK")
	}
	if _, err := a.setUserPassword(ctx, id, password); err != nil {
		return err
	}
	return c.Redirect(a.resetRedirect("sign-in", []kv{{"success", "True"}}))
}

// resetRedirect is urljoin(base_host(is_app=True), path + "?" + urlencode(params)).
func (a *API) resetRedirect(path string, params []kv) string {
	enc := make([]string, len(params))
	for i, p := range params {
		enc[i] = url.QueryEscape(p.k) + "=" + url.QueryEscape(p.v)
	}
	rel := path + "?" + strings.Join(enc, "&")
	base, err := url.Parse(a.baseHost(true))
	if err != nil {
		return rel
	}
	if base.Path == "" {
		base.Path = "/"
	}
	ref, err := url.Parse(rel)
	if err != nil {
		return rel
	}
	return base.ResolveReference(ref).String()
}

// changePassword ports authentication.views.common.ChangePasswordEndpoint.
func (a *API) changePassword(c *httpx.Ctx) error {
	if err := a.drfCSRF(c); err != nil {
		return err
	}
	form, err := c.Form()
	if err != nil {
		return err
	}
	ctx := c.Context()
	var (
		hash    string
		autoset bool
	)
	if err := a.db.QueryRow(ctx, `SELECT password, is_password_autoset FROM users WHERE id = $1`, c.User.ID).
		Scan(&hash, &autoset); err != nil {
		return err
	}
	oldPassword := form.Get("old_password")
	if !autoset && oldPassword == "" {
		return httpx.Body(http.StatusBadRequest, authErr("MISSING_PASSWORD", kv{"error", "Old password is missing"}).body())
	}
	newPassword := form.Get("new_password")
	if newPassword == "" {
		return httpx.Body(http.StatusBadRequest, authErr("MISSING_PASSWORD", kv{"error", "Old or new password is missing"}).body())
	}
	if !autoset && !auth.CheckPassword(oldPassword, hash) {
		return httpx.Body(http.StatusBadRequest, authErr("INCORRECT_OLD_PASSWORD", kv{"error", "Old password is not correct"}).body())
	}
	if !auth.PasswordStrong(newPassword) {
		return httpx.Body(http.StatusBadRequest, authErr("PASSWORD_TOO_WEAK").body())
	}
	newHash, err := a.setUserPassword(ctx, c.User.ID, newPassword)
	if err != nil {
		return err
	}
	if err := a.login(c, &loginUser{id: c.User.ID, passwordHash: newHash}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"message": "Password updated successfully"})
}

// setPassword ports authentication.views.common.SetUserPasswordEndpoint.
func (a *API) setPassword(c *httpx.Ctx) error {
	if err := a.drfCSRF(c); err != nil {
		return err
	}
	form, err := c.Form()
	if err != nil {
		return err
	}
	ctx := c.Context()
	var autoset bool
	if err := a.db.QueryRow(ctx, `SELECT is_password_autoset FROM users WHERE id = $1`, c.User.ID).Scan(&autoset); err != nil {
		return err
	}
	if !autoset {
		return httpx.Body(http.StatusBadRequest, authErr("PASSWORD_ALREADY_SET",
			kv{"error", "Your password is already set please change your password from profile"}).body())
	}
	password := form.Get("password")
	if password == "" || !auth.PasswordStrong(password) {
		return httpx.Body(http.StatusBadRequest, authErr("INVALID_PASSWORD").body())
	}
	newHash, err := a.setUserPassword(ctx, c.User.ID, password)
	if err != nil {
		return err
	}
	if err := a.login(c, &loginUser{id: c.User.ID, passwordHash: newHash}); err != nil {
		return err
	}
	u, err := a.loadUserFull(ctx, c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, u)
}

// setUserPassword is set_password() + is_password_autoset=False + save().
// User.save() also rotates the API token once one has been issued.
func (a *API) setUserPassword(ctx context.Context, id uuid.UUID, password string) (string, error) {
	hash := auth.HashPassword(password)
	_, err := a.db.Exec(ctx, `
		UPDATE users SET password = $2, is_password_autoset = false, updated_at = now(),
			token = CASE WHEN token_updated_at IS NOT NULL THEN $3 ELSE token END,
			token_updated_at = CASE WHEN token_updated_at IS NOT NULL THEN now() END
		WHERE id = $1`, id, hash, newUserToken())
	return hash, err
}
