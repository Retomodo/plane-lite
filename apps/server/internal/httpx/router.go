package httpx

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
)

type HandlerFunc func(*Ctx) error

// Methods maps HTTP methods to handlers for one URL pattern, mirroring a
// Django view's get/post/patch/... methods.
type Methods map[string]HandlerFunc

// Router registers Django-style URL patterns (with trailing slashes and
// {name} wildcards) on a ServeMux and adapts Plane's error conventions.
type Router struct {
	mux *http.ServeMux
	log *slog.Logger
}

func NewRouter(log *slog.Logger) *Router {
	rt := &Router{mux: http.NewServeMux(), log: log}
	rt.mux.HandleFunc("/", rt.notFound)
	return rt
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Every Plane route ends in a slash. Handle slash-less paths here so the
	// mux's own 307 redirect never fires; Django answers them with a 301.
	if !strings.HasSuffix(r.URL.Path, "/") {
		rt.notFound(w, r)
		return
	}
	rt.mux.ServeHTTP(w, r)
}

// Handle registers an endpoint that requires an authenticated user (DRF's
// IsAuthenticated default).
func (rt *Router) Handle(pattern string, m Methods) { rt.register(pattern, m, false) }

// HandlePublic registers an endpoint that allows anonymous access.
func (rt *Router) HandlePublic(pattern string, m Methods) { rt.register(pattern, m, true) }

func (rt *Router) register(pattern string, m Methods, public bool) {
	if !strings.HasSuffix(pattern, "/") {
		panic("httpx: pattern must end with a slash: " + pattern)
	}
	allowed := make([]string, 0, len(m)+2)
	for method := range m {
		allowed = append(allowed, method)
	}
	if _, ok := m[http.MethodGet]; ok {
		allowed = append(allowed, http.MethodHead)
	}
	allowed = append(allowed, http.MethodOptions)
	slices.Sort(allowed)
	allow := strings.Join(allowed, ", ")

	rt.mux.HandleFunc(pattern+"{$}", func(w http.ResponseWriter, r *http.Request) {
		c := &Ctx{W: w, R: r, User: PrincipalFrom(r.Context())}
		if !public && c.User == nil {
			rt.writeError(c, ErrNotAuthenticated)
			return
		}
		method := r.Method
		if method == http.MethodHead {
			method = http.MethodGet
		}
		h, ok := m[method]
		if !ok {
			w.Header().Set("Allow", allow)
			rt.writeError(c, Detail(http.StatusMethodNotAllowed, `Method "`+r.Method+`" not allowed.`))
			return
		}
		if err := h(c); err != nil {
			rt.writeError(c, err)
		}
	})
}

func (rt *Router) writeError(c *Ctx, err error) {
	status, body := toResponse(err)
	if status >= 500 && err != ErrDjangoServerError {
		rt.log.Error("request failed", "method", c.R.Method, "path", c.R.URL.Path, "err", err)
	}
	if h, ok := body.(htmlBody); ok {
		c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
		c.W.WriteHeader(status)
		_, _ = c.W.Write([]byte(h))
		return
	}
	if werr := c.JSON(status, body); werr != nil {
		rt.log.Error("write error response", "err", werr)
	}
}

// notFound mirrors Django's CommonMiddleware (APPEND_SLASH redirects) and
// Plane's JSON 404 handler.
func (rt *Router) notFound(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/") {
		probe := r.Clone(r.Context())
		probe.URL.Path += "/"
		if _, pattern := rt.mux.Handler(probe); pattern != "/" && pattern != "" {
			target := probe.URL.Path
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			// HttpResponsePermanentRedirect: empty body.
			w.Header().Set("Location", target)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
	}
	rt.writeError(&Ctx{W: w, R: r}, ErrPageNotFound)
}
