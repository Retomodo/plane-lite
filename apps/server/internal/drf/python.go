package drf

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
)

// Python value semantics for JSON-decoded data: str(), repr(), str.strip()
// and uuid.UUID(), so coercions and error messages read as they do in DRF.

func isIntLiteral(s string) bool { return !strings.ContainsAny(s, ".eE") }

// PyStr is Python's str() of the decoded JSON value.
func PyStr(v Value) string {
	switch v.Kind() {
	case '"':
		return v.Str()
	default:
		return pyRepr(v.raw)
	}
}

func pyRepr(raw jsontext.Value) string {
	switch raw.Kind() {
	case 'n':
		return "None"
	case 't':
		return "True"
	case 'f':
		return "False"
	case '"':
		var s string
		_ = json.Unmarshal(raw, &s)
		return pyStrRepr(s)
	case '0':
		s := string(raw)
		if isIntLiteral(s) {
			n, ok := new(big.Int).SetString(s, 10)
			if ok {
				return n.String()
			}
			return s
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "inf"
		}
		return PyFloatRepr(f)
	case '[':
		var elems []jsontext.Value
		_ = json.Unmarshal(raw, &elems)
		parts := make([]string, len(elems))
		for i, e := range elems {
			parts[i] = pyRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case '{':
		dec := jsontext.NewDecoder(strings.NewReader(string(raw)), jsontext.AllowDuplicateNames(true))
		_, _ = dec.ReadToken()
		var parts []string
		for dec.PeekKind() != '}' {
			k, err := dec.ReadToken()
			if err != nil {
				break
			}
			val, err := dec.ReadValue()
			if err != nil {
				break
			}
			parts = append(parts, pyStrRepr(k.String())+": "+pyRepr(val))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return string(raw)
}

// pyStrRepr is repr(str) for printable text.
func pyStrRepr(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = `"`
	}
	var b strings.Builder
	b.WriteString(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case string(r) == quote:
			b.WriteString(`\` + quote)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\x` + strconv.FormatInt(int64(r)|0x100, 16)[1:])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(quote)
	return b.String()
}

// PyFloatRepr is repr(float): shortest round-trip digits, positional for
// exponents in [-4, 16), scientific with a two-digit exponent otherwise.
func PyFloatRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	neg := strings.HasPrefix(mant, "-")
	digits := strings.Replace(strings.TrimPrefix(mant, "-"), ".", "", 1)
	sign := ""
	if neg {
		sign = "-"
	}
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := "+"
		if exp < 0 {
			es, exp = "-", -exp
		}
		ed := strconv.Itoa(exp)
		if len(ed) < 2 {
			ed = "0" + ed
		}
		return sign + m + "e" + es + ed
	}
	var s string
	switch {
	case exp < 0:
		s = "0." + strings.Repeat("0", -exp-1) + digits
	case exp+1 >= len(digits):
		s = digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
	default:
		s = digits[:exp+1] + "." + digits[exp+1:]
	}
	return sign + s
}

// PyStrip is str.strip() with no arguments.
func PyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// ParseUUID is uuid.UUID(hex=s): "urn:" and "uuid:" removed anywhere,
// surrounding braces stripped, hyphens ignored, exactly 32 hex digits.
func ParseUUID(s string) (uuid.UUID, bool) {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "urn:", ""), "uuid:", "")
	s = strings.ReplaceAll(strings.Trim(s, "{}"), "-", "")
	if len(s) != 32 {
		return uuid.Nil, false
	}
	n, ok := new(big.Int).SetString(s, 16)
	if !ok || n.Sign() < 0 {
		return uuid.Nil, false
	}
	return uuidFromInt(n)
}

var maxUUID = new(big.Int).Lsh(big.NewInt(1), 128)

// uuidFromInt is uuid.UUID(int=n).
func uuidFromInt(n *big.Int) (uuid.UUID, bool) {
	if n.Sign() < 0 || n.Cmp(maxUUID) >= 0 {
		return uuid.Nil, false
	}
	var id uuid.UUID
	n.FillBytes(id[:])
	return id, true
}
