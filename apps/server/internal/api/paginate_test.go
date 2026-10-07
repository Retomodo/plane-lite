package api

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"plane-lite/server/internal/httpx"
)

func TestParseOffsetPage(t *testing.T) {
	for _, c := range []struct {
		query         string
		limit, offset int64
		err           any // nil, errViewCrash, or the 400 body
	}{
		{query: "", limit: 1000},
		{query: "per_page=10&cursor=10:2:0", limit: 10, offset: 20},
		{query: "per_page=%201_0%20&cursor=10:1:0", limit: 10, offset: 10},
		{query: "per_page=10&cursor=10.0:1:1", limit: 10, offset: 10}, // 10.0 == 10
		{query: "per_page=x", err: "Invalid per_page parameter."},
		{query: "per_page=5000", err: "Invalid per_page value. Cannot exceed 1000."},
		{query: "cursor=nope", err: "Invalid cursor parameter."},
		{query: "cursor=1:2", err: "Invalid cursor parameter."},
		{query: "cursor=1.x:0:0", err: "Invalid cursor parameter."},
		{query: "per_page=10&cursor=10:-1:0", err: "Error in parsing"},
		{query: "per_page=0", err: errViewCrash},
		{query: "per_page=10&cursor=5:1:1", err: errViewCrash},
	} {
		c := c
		ctx := &httpx.Ctx{R: httptest.NewRequest("GET", "/x/?"+c.query, nil)}
		p, err := parseOffsetPage(ctx)
		switch want := c.err.(type) {
		case nil:
			if err != nil {
				t.Errorf("%s: %v", c.query, err)
			} else if p.limit != c.limit || p.offset != c.offset {
				t.Errorf("%s: limit %d offset %d, want %d %d", c.query, p.limit, p.offset, c.limit, c.offset)
			}
		case string:
			var he *httpx.Error
			if !errors.As(err, &he) || he.Status != 400 || he.Body.(map[string]any)["detail"] != want {
				t.Errorf("%s: got %v, want 400 %q", c.query, err, want)
			}
		default:
			if !errors.Is(err, errViewCrash) {
				t.Errorf("%s: got %v, want a crash", c.query, err)
			}
		}
	}
}

func TestOffsetPageResponse(t *testing.T) {
	ctx := &httpx.Ctx{R: httptest.NewRequest("GET", "/x/?per_page=10&cursor=10:0:0", nil)}
	p, err := parseOffsetPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := p.response(nil, 11, 25)
	if r.NextCursor != "10:1:0" || r.PrevCursor != "10:-1:1" || !r.NextPageResults || r.PrevPageResults ||
		r.Count != 10 || r.TotalPages != 3 || r.TotalCount != 25 || r.TotalResults != 25 {
		t.Errorf("unexpected page: %+v", r)
	}
}

func TestSanitizeOrderBy(t *testing.T) {
	allowed := []string{"created_at", "name"}
	for in, want := range map[string]struct {
		field string
		desc  bool
	}{
		"": {"created_at", true}, "name": {"name", false}, "-name": {"name", true},
		"--name": {"created_at", true}, "email": {"created_at", true}, "-": {"created_at", true},
	} {
		if f, d := sanitizeOrderBy(in, allowed, "-created_at"); f != want.field || d != want.desc {
			t.Errorf("sanitizeOrderBy(%q) = %s %v", in, f, d)
		}
	}
}

func TestPyDateTimeStr(t *testing.T) {
	at := time.Date(2026, 10, 7, 9, 5, 3, 0, time.UTC)
	if got := pyDateTimeStr(at); got != "2026-10-07 09:05:03+00:00" {
		t.Errorf("got %s", got)
	}
	if got := pyDateTimeStr(at.Add(1500 * time.Microsecond)); got != "2026-10-07 09:05:03.001500+00:00" {
		t.Errorf("got %s", got)
	}
}
