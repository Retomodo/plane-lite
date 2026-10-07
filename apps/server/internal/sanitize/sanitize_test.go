package sanitize

import (
	_ "embed"
	"encoding/json"
	"testing"
)

//go:embed testdata/nh3.json
var nh3JSON []byte

func TestHTMLMatchesNH3(t *testing.T) {
	var cases [][3]*string
	if err := json.Unmarshal(nh3JSON, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		clean, text, err := HTML(*c[0])
		if err != nil {
			t.Errorf("HTML(%q): %v", *c[0], err)
			continue
		}
		if c[1] != nil && clean != *c[1] {
			t.Errorf("HTML(%q)\n got  %q\n want %q", *c[0], clean, *c[1])
		}
		if c[2] != nil && text != *c[2] {
			t.Errorf("text(%q)\n got  %q\n want %q", *c[0], text, *c[2])
		}
	}
}
