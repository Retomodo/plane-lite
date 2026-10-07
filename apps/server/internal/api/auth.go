package api

import (
	"context"
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/throttle"
)

//go:embed templates/csrf_failure.html
var csrfFailureTemplate string

var djangoEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")

// csrfFailure renders plane.authentication.views.common.csrf_failure. Note
// Plane returns it with status 200.
func (a *API) csrfFailure(c *httpx.Ctx) error {
	body := strings.Replace(csrfFailureTemplate, "{{ root_url }}", djangoEscaper.Replace(a.baseHost(false)), 1)
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.WriteHeader(http.StatusOK)
	_, err := c.W.Write([]byte(body))
	return err
}

// redirect answers a Django auth form post with HttpResponseRedirect.
func (a *API) redirect(c *httpx.Ctx, nextPath string, params []kv) error {
	return c.Redirect(a.safeRedirectURL(a.baseHost(true), nextPath, params))
}

// formPost parses an auth form post and enforces Django's CSRF check and the
// throttle_auth_redirect decorator. ok=false means a response was written.
func (a *API) formPost(c *httpx.Ctx, throttled bool) (form url.Values, ok bool, err error) {
	form, err = c.Form()
	if err != nil {
		return nil, false, err
	}
	if a.csrf.Check(c.R, form) != nil {
		return nil, false, a.csrfFailure(c)
	}
	if throttled && c.User == nil {
		allowed, _, err := a.limiter.Allow(c.Context(), "authentication", throttle.Ident(c.R), a.authRate)
		if err != nil {
			return nil, false, err
		}
		if !allowed {
			return nil, false, a.redirect(c, form.Get("next_path"), authErr("RATE_LIMIT_EXCEEDED").params())
		}
	}
	return form, true, nil
}

// postValue is request.POST.get(key, False) rendered with str(): a missing
// key reads "False" in Plane's error payloads.
func postValue(form url.Values, key string) (string, bool) {
	if _, ok := form[key]; !ok {
		return "False", false
	}
	return form.Get(key), true
}

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// drfCSRF is DRF SessionAuthentication.enforce_csrf, which applies to the
// APIViews that keep DRF's default authentication (unlike Plane's own
// BaseAPIView): unsafe requests from logged-in users need the token header.
func (a *API) drfCSRF(c *httpx.Ctx) error {
	if c.User == nil {
		return nil
	}
	switch c.R.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return nil
	}
	if err := a.csrf.Check(c.R, nil); err != nil {
		return httpx.Detail(http.StatusForbidden, "CSRF Failed: "+err.Error())
	}
	return nil
}

// getCSRFToken ports authentication.views.common.CSRFTokenEndpoint.
func (a *API) getCSRFToken(c *httpx.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{"csrf_token": a.csrf.Token(c.W, c.R)})
}

// emailCheck ports authentication.views.app.check.EmailCheckEndpoint.
func (a *API) emailCheck(c *httpx.Ctx) error {
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
	email := form.Get("email")
	if email == "" {
		return httpx.Body(http.StatusBadRequest, authErr("EMAIL_REQUIRED").body())
	}
	email = normalizeEmail(email)
	if !auth.ValidEmail(email) {
		return httpx.Body(http.StatusBadRequest, authErr("INVALID_EMAIL").body())
	}
	magic := a.cfg.Email.Configured() && a.cfg.EnableMagicLinkLogin
	var autoset bool
	err = a.db.QueryRow(c.Context(), "SELECT is_password_autoset FROM users WHERE email = $1", email).Scan(&autoset)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return c.JSON(http.StatusOK, map[string]any{"existing": false, "status": pick(magic, "MAGIC_CODE", "CREDENTIAL")})
	case err != nil:
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"existing": true, "status": pick(autoset && magic, "MAGIC_CODE", "CREDENTIAL")})
}

func pick[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

// signUp ports authentication.views.app.email.SignUpAuthEndpoint.
func (a *API) signUp(c *httpx.Ctx) error {
	form, ok, err := a.formPost(c, true)
	if !ok {
		return err
	}
	ctx := c.Context()
	nextPath := form.Get("next_path")
	email, hasEmail := postValue(form, "email")
	password := form.Get("password")
	if !hasEmail || email == "" || password == "" {
		return a.redirect(c, nextPath, authErr("REQUIRED_EMAIL_PASSWORD_SIGN_UP", kv{"email", email}).params())
	}
	email = normalizeEmail(email)
	if !auth.ValidEmail(email) {
		return a.redirect(c, nextPath, authErr("INVALID_EMAIL_SIGN_UP", kv{"email", email}).params())
	}
	if exists, err := a.userExists(ctx, email); err != nil {
		return err
	} else if exists {
		return a.redirect(c, nextPath, authErr("USER_ALREADY_EXIST", kv{"email", email}).params())
	}
	if !a.cfg.EnableEmailPassword {
		return a.redirect(c, nextPath, authErr("EMAIL_PASSWORD_AUTHENTICATION_DISABLED").params())
	}
	u, err := a.completeLoginOrSignup(ctx, c, email, credentials{password: password, medium: "email"})
	return a.finishLogin(c, u, nextPath, err)
}

// signIn ports authentication.views.app.email.SignInAuthEndpoint.
func (a *API) signIn(c *httpx.Ctx) error {
	form, ok, err := a.formPost(c, true)
	if !ok {
		return err
	}
	ctx := c.Context()
	nextPath := form.Get("next_path")
	email, hasEmail := postValue(form, "email")
	password := form.Get("password")
	if !hasEmail || email == "" || password == "" {
		return a.redirect(c, nextPath, authErr("REQUIRED_EMAIL_PASSWORD_SIGN_IN", kv{"email", email}).params())
	}
	email = normalizeEmail(email)
	if !auth.ValidEmail(email) {
		return a.redirect(c, nextPath, authErr("INVALID_EMAIL_SIGN_IN", kv{"email", email}).params())
	}
	if exists, err := a.userExists(ctx, email); err != nil {
		return err
	} else if !exists {
		return a.redirect(c, nextPath, authErr("USER_DOES_NOT_EXIST", kv{"email", email}).params())
	}
	if !a.cfg.EnableEmailPassword {
		return a.redirect(c, nextPath, authErr("EMAIL_PASSWORD_AUTHENTICATION_DISABLED").params())
	}
	var hash string
	if err := a.db.QueryRow(ctx, "SELECT password FROM users WHERE email = $1", email).Scan(&hash); err != nil {
		return err
	}
	if !auth.CheckPassword(password, hash) {
		return a.redirect(c, nextPath, authErr("AUTHENTICATION_FAILED_SIGN_IN", kv{"email", email}).params())
	}
	u, err := a.completeLoginOrSignup(ctx, c, email, credentials{password: password, medium: "email"})
	return a.finishLogin(c, u, nextPath, err)
}

// finishLogin logs the user in and redirects to next_path (or the app
// root), or redirects back with the error.
func (a *API) finishLogin(c *httpx.Ctx, u *loginUser, nextPath string, err error) error {
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
	// get_redirection_path() only yields bare names ("onboarding", a slug...)
	// that validate_next_path rejects, so without next_path the redirect is
	// always the app root.
	return a.redirect(c, nextPath, nil)
}

// login is user_login(): session, device info, CSRF rotation. It also does
// the one side effect of get_redirection_path(): ensuring a profile exists.
func (a *API) login(c *httpx.Ctx, u *loginUser) error {
	device := auth.DeviceInfo{
		UserAgent: c.R.UserAgent(),
		IPAddress: auth.ClientIP(c.R),
		Domain:    a.baseHost(true),
	}
	if err := a.sessions.Login(c.Context(), c.W, c.R, u.id, u.passwordHash, device); err != nil {
		return err
	}
	a.csrf.Rotate(c.W)
	_, err := a.db.Exec(c.Context(), `
		INSERT INTO profiles (user_id, background_color) VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING`, u.id, randomColor())
	return err
}

// signOut ports authentication.views.app.signout.SignOutAuthEndpoint.
func (a *API) signOut(c *httpx.Ctx) error {
	_, ok, err := a.formPost(c, false)
	if !ok {
		return err
	}
	if c.User != nil {
		ctx := c.Context()
		if _, err := a.db.Exec(ctx, `
			UPDATE users SET last_logout_ip = $2, last_logout_time = now(), updated_at = now(),
				token = CASE WHEN token_updated_at IS NOT NULL THEN $3 ELSE token END,
				token_updated_at = CASE WHEN token_updated_at IS NOT NULL THEN now() END
			WHERE id = $1`, c.User.ID, auth.ClientIP(c.R), newUserToken()); err != nil {
			return err
		}
		if err := a.sessions.Logout(ctx, c.W, c.R); err != nil {
			return err
		}
	}
	return c.Redirect(a.baseHost(true))
}

func (a *API) userExists(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := a.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)", email).Scan(&exists)
	return exists, err
}

// newUserToken is User.save()'s token: two uuid4 hex strings.
func newUserToken() string {
	return strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
}

// randomColor is plane.utils.color.get_random_color: "#" + 6 hexdigits
// drawn from Python's string.hexdigits (mixed case).
func randomColor() string {
	return "#" + auth.RandomString(6, "0123456789abcdefABCDEF")
}
