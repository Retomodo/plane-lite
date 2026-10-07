package auth

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"

	"plane-lite/server/internal/config"
)

// Django's CSRF scheme: the cookie holds a 32-char secret; forms carry a
// 64-char token (random mask + secret enciphered with it), so the token
// changes on every page while the secret stays put.
const (
	CSRFCookieName   = "csrftoken"
	csrfSecretLength = 32
	csrfCookieAge    = 31449600 // one year, Django's CSRF_COOKIE_AGE
)

type CSRF struct {
	cfg *config.Config
}

func NewCSRF(cfg *config.Config) *CSRF { return &CSRF{cfg: cfg} }

// Token implements django.middleware.csrf.get_token: reuse the cookie's
// secret (or mint one), refresh the cookie, and return a masked token.
func (c *CSRF) Token(w http.ResponseWriter, r *http.Request) string {
	secret := c.secret(r)
	if secret == "" {
		secret = RandomString(csrfSecretLength, alnum)
	}
	c.setCookie(w, secret)
	return mask(secret)
}

// Rotate implements rotate_token, which Django's login() calls.
func (c *CSRF) Rotate(w http.ResponseWriter) {
	c.setCookie(w, RandomString(csrfSecretLength, alnum))
}

// Check runs CsrfViewMiddleware.process_view for an unsafe request whose
// form has already been parsed. It returns false when the request must be
// rejected.
func (c *CSRF) Check(r *http.Request, form url.Values) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		if !c.originAllowed(r, origin) {
			return false
		}
	} else if isSecure(r) && !c.refererAllowed(r) {
		return false
	}
	secret := c.secret(r)
	if secret == "" {
		return false
	}
	token := form.Get("csrfmiddlewaretoken")
	if token == "" {
		token = r.Header.Get("X-CSRFToken")
	}
	if !validTokenFormat(token) {
		return false
	}
	if len(token) == 2*csrfSecretLength {
		token = unmask(token)
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1
}

func (c *CSRF) secret(r *http.Request) string {
	ck, err := r.Cookie(CSRFCookieName)
	if err != nil {
		return ""
	}
	v := ck.Value
	// Older Django versions stored a masked 64-char token in the cookie.
	if len(v) == 2*csrfSecretLength && validTokenFormat(v) {
		v = unmask(v)
	}
	if len(v) != csrfSecretLength || !validTokenFormat(v) {
		return ""
	}
	return v
}

func (c *CSRF) setCookie(w http.ResponseWriter, secret string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    secret,
		Path:     "/",
		Domain:   c.cfg.CookieDomain,
		MaxAge:   csrfCookieAge,
		Expires:  time.Now().Add(csrfCookieAge * time.Second).UTC(),
		HttpOnly: true,
		Secure:   c.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (c *CSRF) originAllowed(r *http.Request, origin string) bool {
	scheme := "http"
	if isSecure(r) {
		scheme = "https"
	}
	if origin == scheme+"://"+r.Host {
		return true
	}
	for _, o := range c.cfg.CORSAllowedOrigins { // Plane sets CSRF_TRUSTED_ORIGINS to these
		if origin == o {
			return true
		}
	}
	return false
}

func (c *CSRF) refererAllowed(r *http.Request) bool {
	ref, err := url.Parse(r.Referer())
	if err != nil || ref.Scheme != "https" || ref.Host == "" {
		return false
	}
	if ref.Host == r.Host {
		return true
	}
	for _, o := range c.cfg.CORSAllowedOrigins {
		if u, err := url.Parse(o); err == nil && u.Host == ref.Host {
			return true
		}
	}
	return false
}

// isSecure honours SECURE_PROXY_SSL_HEADER = (X-Forwarded-Proto, https).
func isSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func validTokenFormat(t string) bool {
	if len(t) != csrfSecretLength && len(t) != 2*csrfSecretLength {
		return false
	}
	for i := 0; i < len(t); i++ {
		if strings.IndexByte(alnum, t[i]) < 0 {
			return false
		}
	}
	return true
}

func mask(secret string) string {
	m := RandomString(csrfSecretLength, alnum)
	out := []byte(m)
	n := len(alnum)
	for i := 0; i < csrfSecretLength; i++ {
		x := strings.IndexByte(alnum, secret[i])
		y := strings.IndexByte(alnum, m[i])
		out = append(out, alnum[(x+y)%n])
	}
	return string(out)
}

func unmask(token string) string {
	m, cipher := token[:csrfSecretLength], token[csrfSecretLength:]
	out := make([]byte, csrfSecretLength)
	n := len(alnum)
	for i := 0; i < csrfSecretLength; i++ {
		x := strings.IndexByte(alnum, cipher[i])
		y := strings.IndexByte(alnum, m[i])
		out[i] = alnum[((x-y)%n+n)%n]
	}
	return string(out)
}
