package mail

import (
	_ "embed"
	"encoding/json"
	"strings"
	"testing"
)

// testdata/issue_updates.json comes from contract/reference/gen_issue_updates_fixtures.py:
// Django's render_to_string of the digest template.
//
//go:embed testdata/issue_updates.json
var issueUpdatesFixtures []byte

// djFromJSON turns JSON numbers into ints, as the Go callers pass them.
func djFromJSON(v any) any {
	switch x := v.(type) {
	case float64:
		return int(x)
	case map[string]any:
		for k, e := range x {
			x[k] = djFromJSON(e)
		}
	case []any:
		for i, e := range x {
			x[i] = djFromJSON(e)
		}
	}
	return v
}

func TestRenderDjangoIssueUpdates(t *testing.T) {
	var cases []struct {
		Context map[string]any `json:"context"`
		HTML    string         `json:"html"`
	}
	if err := json.Unmarshal(issueUpdatesFixtures, &cases); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		got, err := RenderDjango("notifications/issue-updates.html", djFromJSON(c.Context).(map[string]any))
		if err != nil {
			t.Fatal(err)
		}
		if got != c.HTML {
			gl, wl := strings.Split(got, "\n"), strings.Split(c.HTML, "\n")
			for j := range max(len(gl), len(wl)) {
				var g, w string
				if j < len(gl) {
					g = gl[j]
				}
				if j < len(wl) {
					w = wl[j]
				}
				if g != w {
					t.Errorf("case %d line %d:\n got %q\nwant %q", i, j+1, g, w)
					break
				}
			}
		}
	}
}

func TestDjangoConditions(t *testing.T) {
	ctx := map[string]any{"n": 2, "s": "x", "l": []any{"a", "b"}, "none": nil}
	for cond, want := range map[string]bool{
		"n == 2":                  true,
		"n > 1 and s == 'x'":      true,
		"missing > 1":             false,
		"not missing":             true,
		"l|length > 1 or none":    true,
		"none == missing":         true,
		"s != \"x\" or not l":     false,
		"l.1 == 'b'":              true,
		"l|last == 'b' and n < 3": true,
	} {
		c, err := djParseCond(cond)
		if err != nil {
			t.Fatalf("%s: %v", cond, err)
		}
		if got := djTruthy(c([]map[string]any{ctx})); got != want {
			t.Errorf("%s: got %v, want %v", cond, got, want)
		}
	}
}
