package drf

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
)

// Django's URLValidator regex needs lookarounds, hence regexp2. Python's
// \Z (absolute end) is .NET's \z.
var urlRe = regexp2.MustCompile(
	`^(?:[a-z0-9.+-]*)://`+
		`(?:[^\s:@/]+(?::[^\s:@/]*)?@)?`+
		`(?:(?:0|25[0-5]|2[0-4][0-9]|1[0-9]?[0-9]?|[1-9][0-9]?)(?:\.(?:0|25[0-5]|2[0-4][0-9]|1[0-9]?[0-9]?|[1-9][0-9]?)){3}`+
		`|\[[0-9a-f:.]+\]`+
		`|([a-z¡-￿0-9](?:[a-z¡-￿0-9-]{0,61}[a-z¡-￿0-9])?`+
		`(?:\.(?!-)[a-z¡-￿0-9-]{1,63}(?<!-))*`+
		`\.(?!-)(?:[a-z¡-￿-]{2,63}|xn--[a-z0-9]{1,59})(?<!-)\.?|localhost))`+
		`(?::[0-9]{1,5})?`+
		`(?:[/?#][^\s]*)?`+
		`\z`,
	regexp2.IgnoreCase|regexp2.Unicode)

var bracketHostRe = regexp.MustCompile(`^\[(.+)\](?::[0-9]{1,5})?$`)

var urlSchemes = []string{"http", "https", "ftp", "ftps"}

// ValidURL is django.core.validators.URLValidator() (Django 5.2).
func ValidURL(value string) bool {
	if utf8.RuneCountInString(value) > 2048 || strings.ContainsAny(value, "\t\r\n") {
		return false
	}
	scheme, _, _ := strings.Cut(value, "://")
	scheme = strings.ToLower(scheme)
	ok := false
	for _, s := range urlSchemes {
		ok = ok || s == scheme
	}
	if !ok {
		return false
	}
	netloc, ok := urlsplitNetloc(value)
	if !ok {
		return false
	}
	if m, _ := urlRe.MatchString(value); !m {
		return false
	}
	if hm := bracketHostRe.FindStringSubmatch(netloc); hm != nil && !validIPv6(hm[1]) {
		return false
	}
	host := urlHostname(netloc)
	return host != "" && utf8.RuneCountInString(host) <= 253
}

// urlsplitNetloc is the netloc of urllib.parse.urlsplit(), with its
// ValueErrors for unbalanced or invalid bracketed hosts.
func urlsplitNetloc(value string) (string, bool) {
	rest := value
	if i := strings.IndexByte(rest, ':'); i > 0 {
		rest = rest[i+1:]
	}
	if !strings.HasPrefix(rest, "//") {
		return "", true
	}
	rest = rest[2:]
	end := len(rest)
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		end = i
	}
	netloc := rest[:end]
	open, cls := strings.Contains(netloc, "["), strings.Contains(netloc, "]")
	if open != cls {
		return "", false
	}
	if open {
		_, after, _ := strings.Cut(netloc, "[")
		host, _, _ := strings.Cut(after, "]")
		if !strings.HasPrefix(host, "v") {
			// ipaddress.ip_address() must accept it (v4 or v6).
			if _, err := netip.ParseAddr(host); err != nil {
				return "", false
			}
		}
	}
	return netloc, true
}

// urlHostname is SplitResult.hostname (lower-cased; "" for None).
func urlHostname(netloc string) string {
	if i := strings.LastIndexByte(netloc, '@'); i >= 0 {
		netloc = netloc[i+1:]
	}
	var host string
	if _, after, ok := strings.Cut(netloc, "["); ok {
		host, _, _ = strings.Cut(after, "]")
	} else {
		host, _, _ = strings.Cut(netloc, ":")
	}
	if host == "" {
		return ""
	}
	h, zone, pct := strings.Cut(host, "%")
	if pct {
		return strings.ToLower(h) + "%" + zone
	}
	return strings.ToLower(host)
}

// validIPv6 is django.utils.ipv6.is_valid_ipv6_address.
func validIPv6(s string) bool {
	if utf8.RuneCountInString(s) > 39 {
		return false
	}
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is6()
}
