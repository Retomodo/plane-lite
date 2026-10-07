package sanitize

import (
	"math/big"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// parseURL answers what ammonia asks of the url crate's Url::parse (the
// WHATWG URL parser, no base): relative when the input has no scheme
// (RelativeUrlWithoutBase); otherwise the lowercased scheme and whether
// parsing succeeded.
func parseURL(s string) (scheme string, relative, ok bool) {
	s = strings.TrimFunc(s, func(r rune) bool { return r <= ' ' })
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
	i := 0
	for i < len(s) && (isAlpha(s[i]) || i > 0 && (isDigit(s[i]) || s[i] == '+' || s[i] == '-' || s[i] == '.')) {
		i++
	}
	if i == 0 || i == len(s) || s[i] != ':' {
		return "", true, false
	}
	scheme = strings.ToLower(s[:i])
	rest := s[i+1:]
	switch scheme {
	case "http", "https", "ws", "wss", "ftp":
		return scheme, false, specialAuthorityOK(rest)
	case "file":
		return scheme, false, true
	}
	// Non-special: an authority only follows "//"; otherwise the path is
	// opaque and always parses.
	if strings.HasPrefix(rest, "//") {
		return scheme, false, opaqueAuthorityOK(rest[2:])
	}
	return scheme, false, true
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// specialAuthorityOK parses the authority of a special URL: leading slashes
// and backslashes are skipped, it ends at / \ ? #, may carry userinfo, and
// needs a valid, non-empty host and port.
func specialAuthorityOK(rest string) bool {
	rest = strings.TrimLeft(rest, `/\`)
	if end := strings.IndexAny(rest, `/\?#`); end >= 0 {
		rest = rest[:end]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		rest = rest[at+1:]
		if rest == "" {
			return false
		}
	}
	host, ok := splitPort(rest)
	if !ok || host == "" {
		return false
	}
	return hostOK(host, true)
}

func opaqueAuthorityOK(rest string) bool {
	if end := strings.IndexAny(rest, `/?#`); end >= 0 {
		rest = rest[:end]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		rest = rest[at+1:]
	}
	host, ok := splitPort(rest)
	if !ok {
		return false
	}
	if host == "" {
		return true
	}
	return hostOK(host, false)
}

// splitPort removes a ":port" suffix outside brackets, checking the port.
func splitPort(hostport string) (string, bool) {
	inBracket := false
	for i := 0; i < len(hostport); i++ {
		switch hostport[i] {
		case '[':
			inBracket = true
		case ']':
			inBracket = false
		case ':':
			if inBracket {
				continue
			}
			port := hostport[i+1:]
			for j := 0; j < len(port); j++ {
				if !isDigit(port[j]) {
					return "", false
				}
			}
			if len(strings.TrimLeft(port, "0")) > 5 {
				return "", false
			}
			n := 0
			for j := 0; j < len(port); j++ {
				n = n*10 + int(port[j]-'0')
			}
			if n > 65535 {
				return "", false
			}
			return hostport[:i], true
		}
	}
	return hostport, true
}

const forbiddenHost = "\x00\t\n\r #/:<>?@[\\]^|"

func hostOK(host string, special bool) bool {
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") {
			return false
		}
		a, err := netip.ParseAddr(host[1 : len(host)-1])
		return err == nil && a.Is6() && a.Zone() == ""
	}
	if !special {
		for _, r := range host {
			if strings.ContainsRune(forbiddenHost, r) {
				return false
			}
		}
		return !strings.Contains(host, "%") || validPercent(host)
	}
	decoded, err := url.PathUnescape(host)
	if err != nil {
		decoded = host // stray "%" survives decoding, then fails below
	}
	ascii, err := whatwgIDNA.ToASCII(decoded)
	if err != nil || ascii == "" {
		return false
	}
	for _, r := range ascii {
		if r < 0x20 || r == '%' || r == 0x7f || strings.ContainsRune(forbiddenHost, r) {
			return false
		}
	}
	if endsInNumber(ascii) {
		return ipv4OK(ascii)
	}
	return true
}

func validPercent(s string) bool {
	_, err := url.PathUnescape(s)
	return err == nil
}

// whatwgIDNA is UTS #46 with the URL standard's domain-to-ASCII flags.
var whatwgIDNA = idna.New(
	idna.MapForLookup(),
	idna.BidiRule(),
	idna.Transitional(false),
	idna.CheckHyphens(false),
	idna.CheckJoiners(false), // the idna crate skips ContextJ,
	idna.VerifyDNSLength(false),
	idna.StrictDomainName(false),
)

// endsInNumber is the URL standard's "ends in a number checker".
func endsInNumber(host string) bool {
	parts := strings.Split(host, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true
	}
	_, ok := ipv4Number(last)
	return ok
}

func ipv4Number(s string) (*big.Int, bool) {
	if s == "" {
		return nil, false
	}
	base := 10
	switch {
	case len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X"):
		s, base = s[2:], 16
	case len(s) >= 2 && s[0] == '0':
		s, base = s[1:], 8
	}
	if s == "" {
		return big.NewInt(0), true
	}
	n, ok := new(big.Int).SetString(s, base)
	return n, ok && n.Sign() >= 0
}

func ipv4OK(host string) bool {
	parts := strings.Split(host, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return false
	}
	nums := make([]*big.Int, len(parts))
	for i, p := range parts {
		n, ok := ipv4Number(p)
		if !ok {
			return false
		}
		nums[i] = n
	}
	limit := big.NewInt(255)
	for _, n := range nums[:len(nums)-1] {
		if n.Cmp(limit) > 0 {
			return false
		}
	}
	max := new(big.Int).Lsh(big.NewInt(1), uint(8*(5-len(nums))))
	return nums[len(nums)-1].Cmp(max) < 0
}
