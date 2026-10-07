package httpx

import (
	"reflect"
	"testing"
)

// The expectations are urllib.parse.parse_qsl(..., keep_blank_values=True).
func TestParseQuery(t *testing.T) {
	got := ParseQuery("a=1&a=2;3&b&&c=x+y%20z&d=%zz%4&e=%e2%82&=v&f==")
	want := map[string][]string{
		"a": {"1", "2;3"}, "b": {""}, "c": {"x y z"}, "d": {"%zz%4"}, "e": {"\uFFFD"}, "": {"v"}, "f": {"="},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseQuery = %q, want %q", got, want)
	}
}
