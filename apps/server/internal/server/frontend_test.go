package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func frontendFixture(t *testing.T) (http.Handler, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<html>app</html>")
	writeFile(t, filepath.Join(dir, "assets", "app-abc123.js"), "console.log(1)")
	writeFile(t, filepath.Join(dir, "favicon", "favicon.ico"), "ico")

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			rw.Flush()
			line, _ := rw.ReadString('\n')
			rw.WriteString("echo:" + line)
			rw.Flush()
			return
		}
		io.WriteString(w, "live "+r.URL.Path+" xff="+r.Header.Get("X-Forwarded-For")+" proto="+r.Header.Get("X-Forwarded-Proto"))
	}))
	t.Cleanup(live.Close)

	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "api "+r.URL.Path) })
	h, err := withFrontend(api, dir, live.URL)
	if err != nil {
		t.Fatal(err)
	}
	return h, live
}

func TestFrontendRouting(t *testing.T) {
	h, _ := frontendFixture(t)
	cases := []struct {
		method, path   string
		status         int
		body, cacheCtl string
	}{
		{"GET", "/api/users/me/", 200, "api /api/users/me/", ""},
		{"POST", "/auth/sign-in/", 200, "api /auth/sign-in/", ""},
		{"GET", "/live/collaboration", 200, "live /live/collaboration xff=203.0.113.9 proto=https", ""},
		{"GET", "/", 200, "<html>app</html>", "no-cache"},
		{"GET", "/acme/projects/123/issues", 200, "<html>app</html>", "no-cache"},
		{"GET", "/assets/app-abc123.js", 200, "console.log(1)", "public, max-age=31536000, immutable"},
		{"GET", "/assets/missing.js", 404, "404 page not found\n", ""},
		{"GET", "/favicon/favicon.ico", 200, "ico", "no-cache"},
		{"GET", "/../../etc/passwd", 400, "invalid URL path\n", ""},
		{"POST", "/acme/", 405, "method not allowed\n", ""},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Body.String() != tc.body {
			t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.path, w.Code, w.Body.String(), tc.status, tc.body)
		}
		if got := w.Header().Get("Cache-Control"); got != tc.cacheCtl {
			t.Errorf("%s %s Cache-Control = %q, want %q", tc.method, tc.path, got, tc.cacheCtl)
		}
	}
}

func TestFrontendLiveWebsocket(t *testing.T) {
	h, _ := frontendFixture(t)
	front := httptest.NewServer(h)
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET /live/collaboration HTTP/1.1\r\nHost: plane.test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("status line %q, err %v", status, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	io.WriteString(conn, "hello\n")
	got, err := br.ReadString('\n')
	if err != nil || got != "echo:hello\n" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestFrontendDisabled(t *testing.T) {
	api := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h, err := withFrontend(api, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.(http.HandlerFunc); !ok {
		t.Fatal("expected the API handler itself when the frontend is off")
	}
	if _, err := withFrontend(api, t.TempDir(), ""); err == nil {
		t.Fatal("expected an error for a STATIC_DIR without index.html")
	}
	if _, err := withFrontend(api, "", "localhost:3100"); err == nil {
		t.Fatal("expected an error for a LIVE_UPSTREAM without a scheme")
	}
}
