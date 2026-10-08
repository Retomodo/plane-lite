package contract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

// Step is one recorded request/response pair, normalized.
type Step struct {
	Actor       string   `json:"actor"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Request     any      `json:"request,omitempty"`
	Status      int      `json:"status"`
	Location    string   `json:"location,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	Cookies     []string `json:"cookies,omitempty"`
	Body        any      `json:"body,omitzero"` // omitzero keeps [] and {}
}

type golden struct {
	Steps []Step `json:"steps"`
}

// Scenario drives one golden file.
type Scenario struct {
	t     *testing.T
	name  string
	tgt   *target
	ids   *idMap
	steps []Step
}

// Run executes fn against the current target and records or verifies its
// golden file, testdata/golden/<name>.json.
func Run(t *testing.T, name string, fn func(s *Scenario)) {
	t.Helper()
	tgt := current(t)
	ctx := context.Background()
	if err := tgt.reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	s := &Scenario{t: t, name: name, tgt: tgt, ids: newIDMap()}
	fn(s)
	s.finish()
}

func (s *Scenario) goldenPath() string {
	return filepath.Join("testdata", "golden", s.name+".json")
}

func (s *Scenario) finish() {
	t := s.t
	t.Helper()
	if s.tgt.env.record {
		b, err := json.Marshal(golden{Steps: s.steps}, json.Deterministic(true), jsontext.WithIndent("  "))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(s.goldenPath()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.goldenPath(), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d steps to %s", len(s.steps), s.goldenPath())
		return
	}

	raw, err := os.ReadFile(s.goldenPath())
	if err != nil {
		t.Fatalf("no golden for %q (record it with CONTRACT_RECORD=1): %v", s.name, err)
	}
	var want golden
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	// Round-trip actual steps through JSON so both sides have the same types.
	gotRaw, err := json.Marshal(golden{Steps: s.steps})
	if err != nil {
		t.Fatal(err)
	}
	var got golden
	if err := json.Unmarshal(gotRaw, &got); err != nil {
		t.Fatal(err)
	}
	if len(want.Steps) != len(got.Steps) {
		t.Errorf("want %d steps, got %d", len(want.Steps), len(got.Steps))
	}
	for i := range min(len(want.Steps), len(got.Steps)) {
		w, g := want.Steps[i], got.Steps[i]
		var diffs []string
		if w.Method != g.Method || w.Path != g.Path || w.Actor != g.Actor {
			diffs = append(diffs, fmt.Sprintf("request: want %s %s %s, got %s %s %s", w.Actor, w.Method, w.Path, g.Actor, g.Method, g.Path))
		}
		if w.Status != g.Status {
			diffs = append(diffs, fmt.Sprintf("status: want %d, got %d", w.Status, g.Status))
		}
		if w.Location != g.Location {
			diffs = append(diffs, fmt.Sprintf("location: want %q, got %q", w.Location, g.Location))
		}
		if w.ContentType != g.ContentType {
			diffs = append(diffs, fmt.Sprintf("content-type: want %q, got %q", w.ContentType, g.ContentType))
		}
		if strings.Join(w.Cookies, "\n") != strings.Join(g.Cookies, "\n") {
			diffs = append(diffs, fmt.Sprintf("cookies: want %q, got %q", w.Cookies, g.Cookies))
		}
		var bodyDiffs []string
		diff("body", w.Body, g.Body, &bodyDiffs)
		diffs = append(diffs, bodyDiffs...)
		if len(diffs) > 0 {
			t.Errorf("step %d (%s %s %s):\n  %s", i, w.Actor, w.Method, w.Path, strings.Join(diffs, "\n  "))
		}
	}
}

// Alias makes value normalize to placeholder wherever it appears (paths,
// bodies, redirects), for opaque values like reset tokens.
func (s *Scenario) Alias(value, placeholder string) {
	if value != "" {
		s.ids.aliases = append(s.ids.aliases, [2]string{value, placeholder})
	}
}

// AliasToday makes today's UTC date (Django's timezone.now().date())
// normalize to "<today>", for values like archived_at that depend on the
// day the scenario runs.
func (s *Scenario) AliasToday() {
	s.Alias(time.Now().UTC().Format(time.DateOnly), "<today>")
}

// Client is one actor with its own cookie jar (a browser session).
type Client struct {
	s    *Scenario
	name string
	ip   string // sent as X-Forwarded-For; rate limits are per client IP
	http *http.Client
}

// Client returns a new anonymous actor.
func (s *Scenario) Client(name string) *Client {
	return s.ClientFrom(name, "")
}

// ClientFrom returns an actor that appears to come from ip (via the
// X-Forwarded-For header a reverse proxy would set).
func (s *Scenario) ClientFrom(name, ip string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{s: s, name: name, ip: ip, http: &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Opt adjusts how a step is normalized.
type Opt func(*normalizer)

// Exact keeps the values at paths verbatim, skipping datetime/ID
// normalization, for fields whose exact value is the point of the step.
func Exact(paths ...string) Opt {
	return func(n *normalizer) {
		for _, p := range paths {
			n.exact[bodyPath(p)] = true
		}
	}
}

// NoCSRF omits the X-CSRFToken header the client otherwise sends on unsafe
// requests, to exercise CSRF rejection.
func NoCSRF() Opt { return func(n *normalizer) { n.noCSRF = true } }

// Mask replaces the values at the given dotted body paths (e.g.
// "csrf_token", "results[].token") with "<masked>". A "*.name" path masks
// every value whose path ends in ".name".
func Mask(paths ...string) Opt {
	return func(n *normalizer) {
		for _, p := range paths {
			n.masks[bodyPath(p)] = true
		}
	}
}

// SortBy orders the array at path by an object key before comparing, for
// endpoints whose ordering is unspecified.
func SortBy(path, key string) Opt {
	return func(n *normalizer) { n.sorts[bodyPath(path)] = key }
}

// Unordered sorts the scalar arrays at paths after normalization, for
// array_agg(DISTINCT uuid) columns whose order follows the random ids. A
// "*.name" path matches every array whose path ends in ".name".
func Unordered(paths ...string) Opt {
	return func(n *normalizer) {
		for _, p := range paths {
			n.unordered[bodyPath(p)] = true
		}
	}
}

// bodyPath roots an option path at the response body; "[]..." addresses the
// elements of a top-level array.
func bodyPath(p string) string {
	if p == "" || strings.HasPrefix(p, "[") {
		return "body" + p
	}
	return "body." + p
}

// Response is the raw response, for scenario code to read values from.
type Response struct {
	Status int
	Header http.Header
	Raw    []byte
	Data   any
}

// Get reads a value at a dotted path, e.g. "results.0.id".
func (r *Response) Get(path string) any {
	var cur any = r.Data
	for _, part := range strings.Split(path, ".") {
		switch x := cur.(type) {
		case map[string]any:
			cur = x[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i >= len(x) {
				return nil
			}
			cur = x[i]
		default:
			return nil
		}
	}
	return cur
}

func (r *Response) String(path string) string {
	if v, ok := r.Get(path).(string); ok {
		return v
	}
	return ""
}

func (c *Client) Get(path string, opts ...Opt) *Response {
	return c.Do(http.MethodGet, path, nil, opts...)
}

func (c *Client) Post(path string, body any, opts ...Opt) *Response {
	return c.Do(http.MethodPost, path, body, opts...)
}

func (c *Client) Patch(path string, body any, opts ...Opt) *Response {
	return c.Do(http.MethodPatch, path, body, opts...)
}

func (c *Client) Put(path string, body any, opts ...Opt) *Response {
	return c.Do(http.MethodPut, path, body, opts...)
}

func (c *Client) Delete(path string, opts ...Opt) *Response {
	return c.Do(http.MethodDelete, path, nil, opts...)
}

// PostForm submits an HTML form, as the web app's auth pages do.
func (c *Client) PostForm(path string, form url.Values, opts ...Opt) *Response {
	return c.Do(http.MethodPost, path, form, opts...)
}

// Do sends a request (body: nil, url.Values for a form, or any JSON value)
// and records it as a step.
func (c *Client) Do(method, path string, body any, opts ...Opt) *Response {
	t := c.s.t
	t.Helper()

	var (
		reader      io.Reader
		contentType string
		reqRecord   any
	)
	switch b := body.(type) {
	case nil:
	case url.Values:
		reader = strings.NewReader(b.Encode())
		contentType = "application/x-www-form-urlencoded"
		m := map[string]any{}
		for k, v := range b {
			m[k] = strings.Join(v, ",")
		}
		reqRecord = m
	default:
		raw, err := json.Marshal(b, json.Deterministic(true)) // sorted keys: bodies are reproducible
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
		contentType = "application/json"
		_ = json.Unmarshal(raw, &reqRecord)
	}

	req, err := http.NewRequest(method, c.s.tgt.baseURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.ip != "" {
		req.Header.Set("X-Forwarded-For", c.ip)
	}
	n := &normalizer{ids: c.s.ids, masks: map[string]bool{}, exact: map[string]bool{}, sorts: map[string]string{}, unordered: map[string]bool{}}
	for _, o := range opts {
		o(n)
	}
	if csrf := c.cookie("csrftoken"); csrf != "" && method != http.MethodGet && !n.noCSRF {
		req.Header.Set("X-CSRFToken", csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	r := &Response{Status: resp.StatusCode, Header: resp.Header, Raw: raw}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") && len(raw) > 0 {
		if err := json.Unmarshal(raw, &r.Data); err != nil {
			t.Fatalf("%s %s: invalid JSON response: %v\n%s", method, path, err, raw)
		}
	}

	step := Step{
		Actor:       c.name,
		Method:      method,
		Path:        c.s.ids.replace(path),
		Status:      resp.StatusCode,
		ContentType: strings.TrimSpace(strings.Split(ct, ";")[0]),
		Cookies:     normalizeCookies(resp),
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		step.Location = c.s.ids.replace(strings.Replace(loc, c.s.tgt.baseURL, "<base>", 1))
	}
	if reqRecord != nil {
		step.Request = (&normalizer{ids: c.s.ids, masks: map[string]bool{"csrfmiddlewaretoken": true}}).value("", reqRecord)
	}
	switch {
	case r.Data != nil:
		step.Body = n.value("body", r.Data)
	case len(raw) > 0 && strings.HasPrefix(ct, "text/"):
		step.Body = n.str(string(raw))
	case len(raw) > 0:
		step.Body = fmt.Sprintf("<%d bytes>", len(raw))
	}
	c.s.steps = append(c.s.steps, step)
	return r
}

func (c *Client) cookie(name string) string {
	u, _ := url.Parse(c.s.tgt.baseURL)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

// normalizeCookies renders Set-Cookie headers without their values.
func normalizeCookies(resp *http.Response) []string {
	var out []string
	for _, ck := range resp.Cookies() {
		parts := []string{ck.Name}
		if ck.Value == "" || ck.MaxAge < 0 {
			parts = append(parts, "value=<deleted>")
		} else {
			parts = append(parts, "value=<set>")
		}
		if ck.Path != "" {
			parts = append(parts, "Path="+ck.Path)
		}
		if ck.Domain != "" {
			parts = append(parts, "Domain="+ck.Domain)
		}
		if ck.MaxAge > 0 {
			parts = append(parts, "Max-Age="+strconv.Itoa(ck.MaxAge))
		}
		if ck.HttpOnly {
			parts = append(parts, "HttpOnly")
		}
		if ck.Secure {
			parts = append(parts, "Secure")
		}
		switch ck.SameSite {
		case http.SameSiteLaxMode:
			parts = append(parts, "SameSite=Lax")
		case http.SameSiteStrictMode:
			parts = append(parts, "SameSite=Strict")
		case http.SameSiteNoneMode:
			parts = append(parts, "SameSite=None")
		}
		out = append(out, strings.Join(parts, "; "))
	}
	sort.Strings(out)
	return out
}
