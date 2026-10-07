package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestRouter() *Router {
	rt := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)))
	rt.HandlePublic("/api/things/", Methods{"GET": func(c *Ctx) error { return c.JSON(200, map[string]any{"ok": true}) }})
	rt.Handle("/api/private/", Methods{"GET": func(c *Ctx) error { return c.NoContent() }})
	rt.HandlePublic("/api/items/{pk}/", Methods{"GET": func(c *Ctx) error {
		id, err := c.UUIDParam("pk")
		if err != nil {
			return err
		}
		return c.JSON(200, map[string]any{"id": id})
	}})
	return rt
}

func TestRouterDjangoConventions(t *testing.T) {
	rt := newTestRouter()
	cases := []struct {
		method, path string
		status       int
		body         string
		location     string
	}{
		{"GET", "/api/things/", 200, `{"ok":true}`, ""},
		{"GET", "/api/things", 301, "", "/api/things/"},
		{"GET", "/api/things?x=1", 301, "", "/api/things/?x=1"},
		{"GET", "/api/nope/", 404, `{"error":"Page not found."}`, ""},
		{"GET", "/api/nope", 404, `{"error":"Page not found."}`, ""},
		{"DELETE", "/api/things/", 405, `{"detail":"Method \"DELETE\" not allowed."}`, ""},
		{"GET", "/api/private/", 401, `{"detail":"Authentication credentials were not provided."}`, ""},
		{"GET", "/api/items/not-a-uuid/", 404, `{"error":"Page not found."}`, ""},
		{"GET", "/api/items/0b6d8f0e-1c51-4c55-9a4e-6f3f1d6c3d01/", 200, `{"id":"0b6d8f0e-1c51-4c55-9a4e-6f3f1d6c3d01"}`, ""},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.status {
			t.Errorf("%s %s: status %d, want %d", c.method, c.path, w.Code, c.status)
		}
		if c.body != "" && strings.TrimSpace(w.Body.String()) != c.body {
			t.Errorf("%s %s: body %s, want %s", c.method, c.path, w.Body.String(), c.body)
		}
		if loc := w.Header().Get("Location"); loc != c.location {
			t.Errorf("%s %s: location %q, want %q", c.method, c.path, loc, c.location)
		}
	}
}

func TestFormatDateTimeMatchesDRF(t *testing.T) {
	kolkata, _ := time.LoadLocation("Asia/Kolkata")
	ts := time.Date(2026, 10, 7, 18, 1, 26, 120_000_000, time.UTC)
	cases := []struct {
		t    time.Time
		loc  *time.Location
		want string
	}{
		{ts, time.UTC, "2026-10-07T18:01:26.120000Z"},
		{ts.Truncate(time.Second), time.UTC, "2026-10-07T18:01:26Z"},
		{ts, kolkata, "2026-10-07T23:31:26.120000+05:30"},
		{time.Date(2026, 1, 1, 0, 0, 0, 1000, time.UTC), time.UTC, "2026-01-01T00:00:00.000001Z"},
	}
	for _, c := range cases {
		if got := FormatDateTime(c.t, c.loc); got != c.want {
			t.Errorf("FormatDateTime(%v, %v) = %q, want %q", c.t, c.loc, got, c.want)
		}
	}
	b, err := Marshal(map[string]any{"at": ts, "none": (*time.Time)(nil)}, kolkata)
	if err != nil || string(b) != `{"at":"2026-10-07T23:31:26.120000+05:30","none":null}` {
		t.Errorf("Marshal = %s, %v", b, err)
	}
	_ = http.StatusOK
}
