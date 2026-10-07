package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"plane-lite/server/internal/config"
	"plane-lite/server/internal/httpx"
)

const sessionKeyChars = "abcdefghijklmnopqrstuvwxyz0123456789"

// Sessions stores login sessions in Plane's `sessions` table, which keeps
// device_info and user_id alongside the payload (the "Sessions" settings page
// reads them).
type Sessions struct {
	pool *pgxpool.Pool
	cfg  *config.Config
	log  *slog.Logger
}

func NewSessions(pool *pgxpool.Pool, cfg *config.Config, log *slog.Logger) *Sessions {
	return &Sessions{pool: pool, cfg: cfg, log: log}
}

type sessionData struct {
	UserID     string     `json:"_auth_user_id"`
	UserHash   string     `json:"_auth_user_hash"`
	DeviceInfo DeviceInfo `json:"device_info"`
}

type DeviceInfo struct {
	UserAgent string `json:"user_agent"`
	IPAddress string `json:"ip_address"`
	Domain    string `json:"domain"`
}

// Middleware attaches the session's user to the request context. A cookie
// that no longer maps to a live session is cleared, as Django does.
func (s *Sessions) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(s.cfg.SessionCookieName)
		if err != nil || ck.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		p, err := s.load(r.Context(), ck.Value)
		switch {
		case err == nil:
			r = r.WithContext(httpx.WithPrincipal(r.Context(), p))
		case errors.Is(err, errNoSession):
			s.clearCookie(w)
		default:
			s.log.Error("load session", "err", err)
		}
		next.ServeHTTP(w, r)
	})
}

var errNoSession = errors.New("no session")

func (s *Sessions) load(ctx context.Context, key string) (*httpx.Principal, error) {
	var (
		raw      string
		id       uuid.UUID
		email    *string
		active   bool
		password string
		tz       string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT s.session_data, u.id, u.email, u.is_active, u.password, u.user_timezone
		FROM sessions s JOIN users u ON u.id::text = s.user_id
		WHERE s.session_key = $1 AND s.expire_date > now()`, key,
	).Scan(&raw, &id, &email, &active, &password, &tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoSession
	}
	if err != nil {
		return nil, err
	}
	var data sessionData
	if err := json.Unmarshal([]byte(raw), &data, json.RejectUnknownMembers(false)); err != nil {
		return nil, errNoSession
	}
	// Django invalidates sessions when the password changes (session auth hash).
	if !hmac.Equal([]byte(data.UserHash), []byte(s.authHash(password))) || !active {
		return nil, errNoSession
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	p := &httpx.Principal{ID: id, Timezone: loc}
	if email != nil {
		p.Email = *email
	}
	return p, nil
}

// Login creates a session for the user, sets the cookie, and records
// last_login (Django's login() + Plane's user_login()).
func (s *Sessions) Login(ctx context.Context, w http.ResponseWriter, r *http.Request, userID uuid.UUID, passwordHash string, device DeviceInfo) error {
	// Replace any session the browser already has (Django cycles the key).
	if ck, err := r.Cookie(s.cfg.SessionCookieName); err == nil && ck.Value != "" {
		if _, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE session_key = $1", ck.Value); err != nil {
			return err
		}
	}
	data, err := json.Marshal(sessionData{
		UserID:     userID.String(),
		UserHash:   s.authHash(passwordHash),
		DeviceInfo: device,
	})
	if err != nil {
		return err
	}
	deviceJSON, err := json.Marshal(device)
	if err != nil {
		return err
	}
	key := RandomString(128, sessionKeyChars)
	age := time.Duration(s.cfg.SessionCookieAge) * time.Second
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (session_key, session_data, expire_date, device_info, user_id)
		VALUES ($1, $2, now() + make_interval(secs => $3), $4, $5)`,
		key, string(data), s.cfg.SessionCookieAge, deviceJSON, userID.String(),
	); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, "UPDATE users SET last_login = now() WHERE id = $1", userID); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.SessionCookieName,
		Value:    key,
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		MaxAge:   s.cfg.SessionCookieAge,
		Expires:  time.Now().Add(age).UTC(),
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Logout deletes the request's session and clears the cookie.
func (s *Sessions) Logout(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	ck, err := r.Cookie(s.cfg.SessionCookieName)
	if err != nil {
		return nil
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE session_key = $1", ck.Value); err != nil {
		return err
	}
	s.clearCookie(w)
	return nil
}

func (s *Sessions) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.SessionCookieName,
		Value:    "",
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
		SameSite: http.SameSiteLaxMode,
	})
}

// authHash mirrors AbstractBaseUser.get_session_auth_hash.
func (s *Sessions) authHash(passwordHash string) string {
	key := sha256.Sum256([]byte("django.contrib.auth.models.AbstractBaseUser.get_session_auth_hash" + s.cfg.SecretKey))
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(passwordHash))
	return hex.EncodeToString(mac.Sum(nil))
}

// ClientIP mirrors Plane's get_client_ip: first X-Forwarded-For entry, else
// the peer address.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.Split(xff, ",")[0]
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
