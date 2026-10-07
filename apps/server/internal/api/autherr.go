package api

import (
	"net/url"
	"strconv"
	"strings"
)

// Plane's AUTHENTICATION_ERROR_CODES (plane/authentication/adapter/error.py).
var authErrorCodes = map[string]int{
	"INSTANCE_NOT_CONFIGURED":                5000,
	"INVALID_EMAIL":                          5005,
	"EMAIL_REQUIRED":                         5010,
	"SIGNUP_DISABLED":                        5015,
	"MAGIC_LINK_LOGIN_DISABLED":              5016,
	"BOT_USER_LOGIN_FORBIDDEN":               5017,
	"PASSWORD_LOGIN_DISABLED":                5018,
	"USER_ACCOUNT_DEACTIVATED":               5019,
	"INVALID_PASSWORD":                       5020,
	"PASSWORD_TOO_WEAK":                      5021,
	"SMTP_NOT_CONFIGURED":                    5025,
	"USER_ALREADY_EXIST":                     5030,
	"AUTHENTICATION_FAILED_SIGN_UP":          5035,
	"REQUIRED_EMAIL_PASSWORD_SIGN_UP":        5040,
	"INVALID_EMAIL_SIGN_UP":                  5045,
	"INVALID_EMAIL_MAGIC_SIGN_UP":            5050,
	"MAGIC_SIGN_UP_EMAIL_CODE_REQUIRED":      5055,
	"EMAIL_PASSWORD_AUTHENTICATION_DISABLED": 5056,
	"USER_DOES_NOT_EXIST":                    5060,
	"AUTHENTICATION_FAILED_SIGN_IN":          5065,
	"REQUIRED_EMAIL_PASSWORD_SIGN_IN":        5070,
	"INVALID_EMAIL_SIGN_IN":                  5075,
	"INVALID_EMAIL_MAGIC_SIGN_IN":            5080,
	"MAGIC_SIGN_IN_EMAIL_CODE_REQUIRED":      5085,
	"INVALID_MAGIC_CODE_SIGN_IN":             5090,
	"INVALID_MAGIC_CODE_SIGN_UP":             5092,
	"EXPIRED_MAGIC_CODE_SIGN_IN":             5095,
	"EXPIRED_MAGIC_CODE_SIGN_UP":             5097,
	"EMAIL_CODE_ATTEMPT_EXHAUSTED_SIGN_IN":   5100,
	"EMAIL_CODE_ATTEMPT_EXHAUSTED_SIGN_UP":   5102,
	"INVALID_PASSWORD_TOKEN":                 5125,
	"EXPIRED_PASSWORD_TOKEN":                 5130,
	"INCORRECT_OLD_PASSWORD":                 5135,
	"MISSING_PASSWORD":                       5138,
	"INVALID_NEW_PASSWORD":                   5140,
	"PASSWORD_ALREADY_SET":                   5145,
	"RATE_LIMIT_EXCEEDED":                    5900,
	"AUTHENTICATION_FAILED":                  5999,
}

// kv is one ordered key/value pair; Python dicts keep insertion order and
// Plane's redirect query strings depend on it.
type kv struct{ k, v string }

// authError is Plane's AuthenticationException.
type authError struct {
	message string
	payload []kv
}

func (e *authError) Error() string { return e.message }

func authErr(message string, payload ...kv) *authError {
	if _, ok := authErrorCodes[message]; !ok {
		panic("unknown auth error " + message)
	}
	return &authError{message: message, payload: payload}
}

// params is get_error_dict() as ordered query parameters.
func (e *authError) params() []kv {
	out := []kv{
		{"error_code", strconv.Itoa(authErrorCodes[e.message])},
		{"error_message", e.message},
	}
	return append(out, e.payload...)
}

// body is get_error_dict() as a JSON object.
func (e *authError) body() map[string]any {
	m := map[string]any{"error_code": authErrorCodes[e.message], "error_message": e.message}
	for _, p := range e.payload {
		m[p.k] = p.v
	}
	return m
}

// baseHost mirrors plane.authentication.utils.host.base_host.
func (a *API) baseHost(isApp bool) string {
	if isApp && a.cfg.AppBaseURL != "" {
		return a.cfg.AppBaseURL
	}
	if a.cfg.WebURL != "" {
		return a.cfg.WebURL
	}
	return a.cfg.AppBaseURL
}

// safeRedirectURL mirrors plane.utils.path_validator.get_safe_redirect_url.
func (a *API) safeRedirectURL(baseURL, nextPath string, params []kv) string {
	validated := validateNextPath(nextPath)
	baseURL = strings.TrimRight(baseURL, "/")
	var parts []string
	encoded := ""
	if validated != "" {
		parts = append(parts, "next_path="+validated)
	}
	if len(params) > 0 {
		enc := make([]string, len(params))
		for i, p := range params {
			enc[i] = url.QueryEscape(p.k) + "=" + url.QueryEscape(p.v)
		}
		encoded = strings.Join(enc, "&")
		parts = append(parts, encoded)
	}
	u := baseURL
	if len(parts) > 0 {
		u = baseURL + "/?" + strings.Join(parts, "&")
	}
	if a.hostAllowed(u) {
		return u
	}
	if encoded != "" {
		return baseURL + "?" + encoded
	}
	return baseURL
}

func (a *API) hostAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	for _, s := range []string{a.cfg.WebURL, a.cfg.AppBaseURL, a.cfg.AdminBaseURL, a.cfg.SpaceBaseURL} {
		if s == "" {
			continue
		}
		if h, err := url.Parse(s); err == nil && h.Host == u.Host {
			return true
		}
	}
	return false
}

var suspiciousPatterns = []string{
	"javascript:", "data:", "vbscript:", "file:", "ftp:", "%2e%2e", "%2f%2f", "%5c%5c",
	"<script", "<iframe", "<object", "<embed", "<form", "onload=", "onerror=", "onclick=",
}

// validateNextPath mirrors plane.utils.path_validator.validate_next_path.
func validateNextPath(p string) string {
	if p == "" || len([]rune(p)) > 500 {
		return ""
	}
	p = strings.NewReplacer(`\`, "", "\t", "", "\r", "", "\n", "").Replace(p)
	scheme, netloc, path := pyURLSplit(p)
	if scheme != "" || netloc != "" {
		p = path
	}
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, "..") {
		return ""
	}
	lower := strings.ToLower(p)
	for _, s := range suspiciousPatterns {
		if strings.Contains(lower, s) {
			return ""
		}
	}
	return p
}

// pyURLSplit reproduces the parts of Python's urllib.parse.urlsplit that
// validate_next_path relies on.
func pyURLSplit(s string) (scheme, netloc, path string) {
	s = strings.TrimLeft(s, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f ")
	if i := strings.IndexByte(s, ':'); i > 0 && isASCIILetter(s[0]) {
		valid := true
		for j := 0; j < i; j++ {
			c := s[j]
			if !(isASCIILetter(c) || (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.') {
				valid = false
				break
			}
		}
		if valid {
			scheme, s = strings.ToLower(s[:i]), s[i+1:]
		}
	}
	if strings.HasPrefix(s, "//") {
		rest := s[2:]
		end := strings.IndexAny(rest, "/?#")
		if end < 0 {
			end = len(rest)
		}
		netloc, s = rest[:end], rest[end:]
	}
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	return scheme, netloc, s
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
