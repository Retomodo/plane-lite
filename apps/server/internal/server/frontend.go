package server

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// withFrontend puts the single-container front door around the API: /api/
// and /auth/ go to the API, /live/ to the collaboration server (apps/live) and
// everything else to the web app's static build, falling back to index.html
// for client-side routes. In the multi-service layout (or tests) both are
// unset and the API handles every path, as before.
func withFrontend(apiHandler http.Handler, staticDir, liveUpstream string) (http.Handler, error) {
	var live, static http.Handler
	if liveUpstream != "" {
		u, err := url.Parse(liveUpstream)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("LIVE_UPSTREAM: invalid URL %q", liveUpstream)
		}
		live = liveProxy(u)
	}
	if staticDir != "" {
		if _, err := os.Stat(filepath.Join(staticDir, "index.html")); err != nil {
			return nil, fmt.Errorf("STATIC_DIR: %w", err)
		}
		static = spaHandler(staticDir)
	}
	if live == nil && static == nil {
		return apiHandler, nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/auth/"):
			apiHandler.ServeHTTP(w, r)
		case live != nil && (p == "/live" || strings.HasPrefix(p, "/live/")):
			live.ServeHTTP(w, r)
		case static != nil:
			static.ServeHTTP(w, r)
		default:
			apiHandler.ServeHTTP(w, r)
		}
	}), nil
}

// liveProxy forwards to apps/live, websockets included. The path is kept
// (live serves under LIVE_BASE_PATH, /live by default) and the forwarding
// headers set by the outer reverse proxy are passed through untouched.
func liveProxy(u *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.URL.Path, pr.Out.URL.RawPath = pr.In.URL.Path, pr.In.URL.RawPath
			pr.Out.Host = pr.In.Host
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
				if v, ok := pr.In.Header[h]; ok {
					pr.Out.Header[h] = v
				}
			}
		},
	}
}

// spaHandler serves the React Router client build. Vite's hashed bundles
// under /assets/ are cached forever; everything else is revalidated so a
// deploy is picked up on the next load. Unknown paths outside /assets/ are
// client-side routes and get index.html.
func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(clean))); err == nil && !fi.IsDir() {
			if strings.HasPrefix(clean, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(clean, "/assets/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}
