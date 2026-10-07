package drf

import (
	_ "embed"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
)

// Fixtures come from the reference stack; see contract/reference/gen_drf_fixtures.py.
//
//go:embed testdata/fixtures.json
var fixturesJSON []byte

type fixtures struct {
	DateTimes [][2]*string `json:"datetimes"`
	URLs      [][2]any     `json:"urls"`
	Floats    [][2]any     `json:"floats"`
	UUIDs     [][2]*string `json:"uuids"`
}

func loadFixtures(t *testing.T) fixtures {
	var f fixtures
	if err := json.Unmarshal(fixturesJSON, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseDateTimeMatchesPython(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range loadFixtures(t).DateTimes {
		in := *c[0]
		got, ok := ParseDateTime(in, kolkata)
		switch {
		case c[1] == nil && ok:
			t.Errorf("%q: accepted as %s, Python rejects", in, got.UTC())
		case c[1] != nil && !ok:
			t.Errorf("%q: rejected, Python gives %s", in, *c[1])
		case c[1] != nil:
			want, err := time.Parse("2006-01-02T15:04:05.999999-07:00", *c[1])
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) {
				t.Errorf("%q: got %s, want %s", in, got.UTC(), want.UTC())
			}
		}
	}
}

func TestValidURLMatchesDjango(t *testing.T) {
	for _, c := range loadFixtures(t).URLs {
		if got := ValidURL(c[0].(string)); got != c[1].(bool) {
			t.Errorf("ValidURL(%q) = %v, Django says %v", c[0], got, c[1])
		}
	}
}

func TestPyFloatRepr(t *testing.T) {
	for _, c := range loadFixtures(t).Floats {
		if got := PyFloatRepr(c[0].(float64)); got != c[1].(string) {
			t.Errorf("repr(%v) = %s, want %s", c[0], got, c[1])
		}
	}
}

func TestParseUUIDMatchesPython(t *testing.T) {
	for _, c := range loadFixtures(t).UUIDs {
		id, ok := ParseUUID(*c[0])
		switch {
		case c[1] == nil && ok:
			t.Errorf("%q accepted", *c[0])
		case c[1] != nil && (!ok || id.String() != *c[1]):
			t.Errorf("%q = %v %v, want %s", *c[0], id, ok, *c[1])
		}
	}
}

type pyCases struct {
	FloatParse [][2]*string `json:"float_parse"`
	IntParse   [][2]*string `json:"int_parse"`
	Upper      [][2]string  `json:"upper"`
}

func loadPyCases(t *testing.T) pyCases {
	var c pyCases
	if err := json.Unmarshal(fixturesJSON, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPyFloatStringMatchesPython(t *testing.T) {
	for _, c := range loadPyCases(t).FloatParse {
		f, ok := pyFloatString(*c[0])
		switch {
		case c[1] == nil && ok:
			t.Errorf("float(%q) accepted as %v", *c[0], f)
		case c[1] != nil && !ok:
			t.Errorf("float(%q) rejected, Python gives %s", *c[0], *c[1])
		case c[1] != nil && PyFloatRepr(f) != *c[1]:
			t.Errorf("float(%q) = %s, want %s", *c[0], PyFloatRepr(f), *c[1])
		}
	}
}

func TestPyIntStringMatchesPython(t *testing.T) {
	for _, c := range loadPyCases(t).IntParse {
		n, ok := PyIntString(*c[0])
		switch {
		case c[1] == nil && ok:
			t.Errorf("int(%q) accepted as %v", *c[0], n)
		case c[1] != nil && (!ok || n.String() != *c[1]):
			t.Errorf("int(%q) = %v %v, want %s", *c[0], n, ok, *c[1])
		}
	}
}

func TestPyUpperMatchesPython(t *testing.T) {
	for _, c := range loadPyCases(t).Upper {
		if got := PyUpper(c[0]); got != c[1] {
			t.Errorf("%q.upper() = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestSlugifyMatchesDjango(t *testing.T) {
	var c struct {
		Slugify [][2]string `json:"slugify"`
	}
	if err := json.Unmarshal(fixturesJSON, &c); err != nil {
		t.Fatal(err)
	}
	for _, f := range c.Slugify {
		if got := Slugify(f[0]); got != f[1] {
			t.Errorf("slugify(%q) = %q, want %q", f[0], got, f[1])
		}
	}
}

func TestParseDateMatchesDRF(t *testing.T) {
	var c struct {
		DateParse [][2]*string `json:"date_parse"`
		FormDate  [][2]*string `json:"form_date"`
	}
	if err := json.Unmarshal(fixturesJSON, &c); err != nil {
		t.Fatal(err)
	}
	for _, f := range c.FormDate {
		got, ok := ParseFormDate(*f[0])
		var s string
		if got != nil {
			s = got.Format(time.DateOnly)
		}
		if f[1] == nil && ok || f[1] != nil && (!ok || s != *f[1]) {
			t.Errorf("ParseFormDate(%q) = %q, %v, want %v", *f[0], s, ok, f[1])
		}
	}
	for _, f := range c.DateParse {
		got, ok := ParseDate(*f[0])
		switch {
		case f[1] == nil && ok:
			t.Errorf("ParseDate(%q) = %v, want error", *f[0], got)
		case f[1] != nil && (!ok || got.Format(time.DateOnly) != *f[1]):
			t.Errorf("ParseDate(%q) = %v, %v, want %s", *f[0], got, ok, *f[1])
		}
	}
}
