package api

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
	_ "time/tzdata"
)

// TestTimezoneList checks timezoneList against TimezoneEndpoint's output at
// fixed instants (testdata/timezones.json, recorded from the reference with
// datetime.now() patched), on both sides of the northern DST change. The
// contract golden can't pin offsets or order: they follow the date.
func TestTimezoneList(t *testing.T) {
	raw, err := os.ReadFile("testdata/timezones.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]struct {
		Timezones []timezoneEntry `json:"timezones"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("no fixtures")
	}
	for stamp, w := range want {
		now, err := time.Parse("2006-01-02T15:04:05", stamp)
		if err != nil {
			t.Fatal(err)
		}
		got := timezoneList(now)
		if !reflect.DeepEqual(got, w.Timezones) {
			for i := range min(len(got), len(w.Timezones)) {
				if got[i] != w.Timezones[i] {
					t.Errorf("%s [%d]: got %+v, want %+v", stamp, i, got[i], w.Timezones[i])
				}
			}
			if len(got) != len(w.Timezones) {
				t.Errorf("%s: got %d zones, want %d", stamp, len(got), len(w.Timezones))
			}
		}
	}
}
