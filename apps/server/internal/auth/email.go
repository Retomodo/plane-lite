package auth

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Port of Django 5.2's EmailValidator. Go's regexp has no lookarounds, so
// the domain part is checked label by label; labels can't contain dots, so
// this is equivalent to Django's single regex.
var (
	emailUserRe    = regexp.MustCompile(`(?i)^(?:[-!#$%&'*+/=?^_` + "`" + `{}|~0-9A-Z]+(?:\.[-!#$%&'*+/=?^_` + "`" + `{}|~0-9A-Z]+)*|"(?:[\x01-\x08\x0b\x0c\x0e-\x1f!#-\[\]-\x7f]|\\[\x01-\x09\x0b\x0c\x0e-\x7f])*")$`)
	emailLiteralRe = regexp.MustCompile(`(?i)^\[([A-F0-9:.]+)\]$`)
)

// ValidEmail reports whether Django's validate_email would accept s.
func ValidEmail(s string) bool {
	if s == "" || !strings.Contains(s, "@") || utf8.RuneCountInString(s) > 320 {
		return false
	}
	at := strings.LastIndex(s, "@")
	user, domain := s[:at], s[at+1:]
	if !emailUserRe.MatchString(user) {
		return false
	}
	if domain == "localhost" {
		return true
	}
	return validDomain(domain) || validLiteral(domain)
}

func validLiteral(domain string) bool {
	m := emailLiteralRe.FindStringSubmatch(domain)
	if m == nil {
		return false
	}
	addr, err := netip.ParseAddr(m[1])
	return err == nil && addr.Zone() == ""
}

func validDomain(domain string) bool {
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	if !validHostLabel([]rune(labels[0])) {
		return false
	}
	for _, l := range labels[1 : len(labels)-1] {
		if !validMiddleLabel([]rune(l)) {
			return false
		}
	}
	return validTLD([]rune(labels[len(labels)-1]))
}

// unicodeLetter matches Django's [a-z¡-￿] under re.IGNORECASE.
func unicodeLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= 0xA1 && r <= 0xFFFF)
}

func hostChar(r rune) bool { return unicodeLetter(r) || (r >= '0' && r <= '9') }

// hostname_re: [a-z¡-￿0-9](?:[a-z¡-￿0-9-]{0,61}[a-z¡-￿0-9])?
func validHostLabel(l []rune) bool {
	if len(l) == 0 || len(l) > 63 || !hostChar(l[0]) || !hostChar(l[len(l)-1]) {
		return false
	}
	for _, r := range l {
		if !hostChar(r) && r != '-' {
			return false
		}
	}
	return true
}

// domain_re label: (?!-)[a-z¡-￿0-9-]{1,63}(?<!-)
func validMiddleLabel(l []rune) bool {
	if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, r := range l {
		if !hostChar(r) && r != '-' {
			return false
		}
	}
	return true
}

// tld_no_fqdn_re: (?!-)(?:[a-z¡-￿-]{2,63}|xn--[a-z0-9]{1,59})(?<!-)
func validTLD(l []rune) bool {
	if len(l) < 2 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	letters := true
	for _, r := range l {
		if !unicodeLetter(r) && r != '-' {
			letters = false
			break
		}
	}
	if letters {
		return true
	}
	s := strings.ToLower(string(l))
	if !strings.HasPrefix(s, "xn--") || len(s) > 63 {
		return false
	}
	for _, r := range s[4:] {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return len(s) > 4
}
