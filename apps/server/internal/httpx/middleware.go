package httpx

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
)

// SecurityHeaders adds the headers Django's SecurityMiddleware and
// XFrameOptionsMiddleware set on every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

var corsAllowHeaders = strings.Join([]string{
	"accept", "authorization", "content-type", "user-agent", "x-csrftoken", "x-requested-with", "X-API-Key",
}, ", ")

// CORS mirrors django-cors-headers with CORS_ALLOW_CREDENTIALS. An empty
// allowlist allows every origin, as Plane's settings do.
func CORS(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Add("Vary", "origin")
		origin := r.Header.Get("Origin")
		if origin == "" || (len(allowed) > 0 && !slices.Contains(allowed, origin)) {
			next.ServeHTTP(w, r)
			return
		}
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			h.Set("Access-Control-Allow-Methods", "DELETE, GET, OPTIONS, PATCH, POST, PUT")
			h.Set("Access-Control-Max-Age", "86400")
			h.Set("Content-Length", "0")
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Recover turns panics into Plane's generic 500 response.
func Recover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"Something went wrong please try again later"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
