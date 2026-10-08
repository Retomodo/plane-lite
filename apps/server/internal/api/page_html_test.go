package api

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestPageHTML checks the html.parser and BeautifulSoup ports against
// Python on testdata/page_html.json (contract/reference/gen_page_html_fixtures.py):
// strip_tags, find_all of the editor components and str(soup).
func TestPageHTML(t *testing.T) {
	raw, err := os.ReadFile("testdata/page_html.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		HTML          string                          `json:"html"`
		Stripped      *string                         `json:"stripped"`
		StrippedError string                          `json:"stripped_error"`
		Soup          *string                         `json:"soup"`
		SoupError     string                          `json:"soup_error"`
		Surrogate     bool                            `json:"surrogate"`
		Components    map[string][]map[string]*string `json:"components"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no fixtures")
	}
	for _, c := range cases {
		got, err := pageStripTags(c.HTML)
		switch {
		case c.StrippedError != "":
			if err == nil {
				t.Errorf("strip_tags(%q): want %s, got %v", c.HTML, c.StrippedError, got)
			}
		case err != nil:
			t.Errorf("strip_tags(%q): %v", c.HTML, err)
		case c.HTML == "":
			if got != nil {
				t.Errorf("strip_tags(\"\"): want None, got %q", *got)
			}
		case got == nil || *got != *c.Stripped:
			t.Errorf("strip_tags(%q):\n got %q\nwant %q", c.HTML, pageDeref(got), *c.Stripped)
		}

		soup, err := pageSoup(c.HTML)
		if c.SoupError != "" {
			if err == nil {
				t.Errorf("soup(%q): want %s", c.HTML, c.SoupError)
			}
			continue
		}
		if err != nil {
			t.Errorf("soup(%q): %v", c.HTML, err)
			continue
		}
		if s := soup.String(); s != *c.Soup {
			t.Errorf("str(soup(%q)):\n got %q\nwant %q", c.HTML, s, *c.Soup)
		}
		if soup.surrogate != c.Surrogate {
			t.Errorf("soup(%q): surrogate %v, want %v", c.HTML, soup.surrogate, c.Surrogate)
		}
		for name, want := range c.Components {
			got := []map[string]*string{}
			for _, tag := range soup.findAll(name) {
				m := map[string]*string{}
				for _, k := range []string{"id", "src", "entity_identifier", "entity_name"} {
					if v, ok := tag.get(k); ok {
						m[k] = &v
					} else {
						m[k] = nil
					}
				}
				got = append(got, m)
			}
			if !reflect.DeepEqual(got, want) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(want)
				t.Errorf("find_all(%q) in %q:\n got %s\nwant %s", name, c.HTML, g, w)
			}
		}
	}
}

func pageDeref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
