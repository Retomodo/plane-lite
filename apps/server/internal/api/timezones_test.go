package api

import (
	"testing"
	"time"
	_ "time/tzdata"
)

// The session middleware renders datetimes in the user's timezone, so
// every choice must load.
func TestUserTimezonesLoad(t *testing.T) {
	for _, name := range userTimezones {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestContainsURL(t *testing.T) {
	for in, want := range map[string]bool{
		"Alice": false, "John.Doe": true, "see www.evil.com": true, "http://x": true,
		"10.0.0.1": true, "a.b": false, "Mr. Smith": false, "O'Brien-Smith": false,
	} {
		if got := containsURL(in); got != want {
			t.Errorf("containsURL(%q) = %v", in, got)
		}
	}
}
