// Package config loads server settings from the environment. Variable names
// follow the Django backend's so existing .env files keep working.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr  string
	Debug bool

	// SecretKey signs CSRF tokens and password-reset tokens.
	SecretKey string

	DatabaseURL string
	// DatabaseMaxConns caps the pool; managed Postgres plans often have low
	// connection limits.
	DatabaseMaxConns int32

	RedisURL string
	// RedisKeyPrefix namespaces every key so a shared Redis instance can be
	// used safely.
	RedisKeyPrefix string

	WebURL       string
	AppBaseURL   string
	AdminBaseURL string
	SpaceBaseURL string
	LiveBaseURL  string

	CORSAllowedOrigins []string
	CookieDomain       string

	SessionCookieName       string
	SessionCookieAge        int
	SessionSaveEveryRequest bool

	EnableSignup             bool
	EnableEmailPassword      bool
	EnableMagicLinkLogin     bool
	DisableWorkspaceCreation bool

	// AuthenticationRateLimit throttles sign-in/up and email checks per IP
	// (DRF rate syntax).
	AuthenticationRateLimit string

	Email Email

	FileSizeLimit        int64
	AppVersion           string
	InstanceChangelogURL string
}

type Email struct {
	Host     string
	Port     int
	User     string
	Password string
	UseTLS   bool
	UseSSL   bool
	From     string
}

// Configured reports whether outgoing email is set up.
func (e Email) Configured() bool { return e.Host != "" }

// SecureCookies mirrors Django's `secure_origins`: cookies are Secure unless
// any allowed origin is plain http (or none are configured).
func (c *Config) SecureCookies() bool {
	if len(c.CORSAllowedOrigins) == 0 {
		return false
	}
	for _, o := range c.CORSAllowedOrigins {
		if strings.Contains(o, "http:") {
			return false
		}
	}
	return true
}

func Load() (*Config, error) {
	c := &Config{
		Addr:             ":" + get("PORT", "8000"),
		Debug:            getBool("DEBUG", false),
		SecretKey:        os.Getenv("SECRET_KEY"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		DatabaseMaxConns: int32(getInt("DATABASE_MAX_CONNS", 10)),
		RedisURL:         os.Getenv("REDIS_URL"),
		RedisKeyPrefix:   get("REDIS_KEY_PREFIX", "plane:"),

		WebURL:       os.Getenv("WEB_URL"),
		AppBaseURL:   os.Getenv("APP_BASE_URL"),
		AdminBaseURL: os.Getenv("ADMIN_BASE_URL"),
		SpaceBaseURL: os.Getenv("SPACE_BASE_URL"),
		LiveBaseURL:  os.Getenv("LIVE_BASE_URL"),

		CORSAllowedOrigins: splitList(os.Getenv("CORS_ALLOWED_ORIGINS")),
		CookieDomain:       os.Getenv("COOKIE_DOMAIN"),

		SessionCookieName:       get("SESSION_COOKIE_NAME", "session-id"),
		SessionCookieAge:        getInt("SESSION_COOKIE_AGE", 604800),
		SessionSaveEveryRequest: getBool("SESSION_SAVE_EVERY_REQUEST", false),

		// Django defaults ENABLE_SIGNUP to "0" for the instance config the web
		// app reads but "1" for the sign-up check itself; one value here,
		// defaulting to allowed. See DEVIATIONS.md.
		EnableSignup:             getBool("ENABLE_SIGNUP", true),
		EnableEmailPassword:      getBool("ENABLE_EMAIL_PASSWORD", true),
		EnableMagicLinkLogin:     getBool("ENABLE_MAGIC_LINK_LOGIN", true),
		DisableWorkspaceCreation: getBool("DISABLE_WORKSPACE_CREATION", false),
		AuthenticationRateLimit:  get("AUTHENTICATION_RATE_LIMIT", "10/minute"),

		Email: Email{
			Host:     os.Getenv("EMAIL_HOST"),
			Port:     getInt("EMAIL_PORT", 587),
			User:     os.Getenv("EMAIL_HOST_USER"),
			Password: os.Getenv("EMAIL_HOST_PASSWORD"),
			UseTLS:   getBool("EMAIL_USE_TLS", true),
			UseSSL:   getBool("EMAIL_USE_SSL", false),
			From:     get("EMAIL_FROM", "Team Plane <team@mailer.plane.so>"),
		},

		FileSizeLimit:        int64(getInt("FILE_SIZE_LIMIT", 5242880)),
		AppVersion:           get("APP_VERSION", "v1.4.2"),
		InstanceChangelogURL: get("INSTANCE_CHANGELOG_URL", "https://sites.plane.so/pages/691ef037bcfe416a902e48cb55f59891/"),
	}

	var errs []error
	if c.SecretKey == "" {
		errs = append(errs, errors.New("SECRET_KEY is required"))
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if c.RedisURL == "" {
		errs = append(errs, errors.New("REDIS_URL is required"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

func get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// getBool accepts Django-style "1"/"0" as well as true/false.
func getBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func getInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
