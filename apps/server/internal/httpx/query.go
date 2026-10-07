package httpx

import (
	"strings"
	"unicode/utf8"
)

// ParseQuery parses a query string as Django's QueryDict does (urllib's
// parse_qsl with keep_blank_values): pairs split on "&" only, so values may
// hold ";"; "+" is a space; malformed %-escapes stay as they are and bytes
// that are not UTF-8 become U+FFFD.
func ParseQuery(raw string) map[string][]string {
	out := map[string][]string{}
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		k := pyUnquote(name)
		out[k] = append(out[k], pyUnquote(value))
	}
	return out
}

// pyUnquote is urllib.parse.unquote(s.replace("+", " ")).
func pyUnquote(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	if !strings.Contains(s, "%") {
		return s
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b = append(b, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// QueryValues is request.GET.
func (c *Ctx) QueryValues() map[string][]string {
	if c.query == nil {
		c.query = ParseQuery(c.R.URL.RawQuery)
	}
	return c.query
}

// HasQuery reports whether the query string names the parameter.
func (c *Ctx) HasQuery(name string) bool {
	_, ok := c.QueryValues()[name]
	return ok
}
