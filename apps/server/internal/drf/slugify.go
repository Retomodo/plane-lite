package drf

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Slugify is django.utils.text.slugify (allow_unicode=False): NFKD, drop
// non-ASCII, lowercase, keep word characters, whitespace and hyphens, then
// collapse runs of hyphens/whitespace into one hyphen and trim "-" and "_".
func Slugify(s string) string {
	var kept strings.Builder
	for _, r := range norm.NFKD.String(s) {
		if r > unicode.MaxASCII {
			continue
		}
		r = unicode.ToLower(r)
		if isWordASCII(r) || isSpaceASCII(r) || r == '-' {
			kept.WriteRune(r)
		}
	}
	var out strings.Builder
	sep := false
	for _, r := range kept.String() {
		if r == '-' || isSpaceASCII(r) {
			sep = true
			continue
		}
		if sep {
			out.WriteByte('-')
			sep = false
		}
		out.WriteRune(r)
	}
	if sep {
		out.WriteByte('-')
	}
	return strings.Trim(out.String(), "-_")
}

func isWordASCII(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// isSpaceASCII is Python's \s restricted to ASCII (it includes \x1c-\x1f).
func isSpaceASCII(r rune) bool {
	return r == ' ' || r >= '\t' && r <= '\r' || r >= 0x1c && r <= 0x1f
}
