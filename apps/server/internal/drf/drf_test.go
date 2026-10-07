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
