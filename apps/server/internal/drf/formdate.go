package drf

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// formDateFormats are the en-us DATE_INPUT_FORMATS a django.forms.DateField
// tries in order, compiled the way Python's _strptime does: whitespace in a
// format matches any run of whitespace and matching ignores case.
var formDateFormats = func() []*regexp.Regexp {
	months := func(names []string) string {
		return "(" + strings.Join(names, "|") + ")"
	}
	abbr := months([]string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"})
	full := months([]string{"september", "february", "november", "december", "january", "october", "august",
		"march", "april", "june", "july", "may"})
	parts := map[byte]string{
		'Y': `(?P<Y>[0-9]{4})`,
		'm': `(?P<m>1[0-2]|0[1-9]|[1-9])`,
		'd': `(?P<d>3[01]|[12][0-9]|0[1-9]|[1-9]| [1-9])`,
		'y': `(?P<y>[0-9]{2})`,
		'b': strings.Replace(abbr, "(", "(?P<b>", 1),
		'B': strings.Replace(full, "(", "(?P<B>", 1),
	}
	var out []*regexp.Regexp
	for _, f := range []string{"%Y-%m-%d", "%m/%d/%Y", "%m/%d/%y", "%b %d %Y", "%b %d, %Y", "%d %b %Y", "%d %b, %Y",
		"%B %d %Y", "%B %d, %Y", "%d %B %Y", "%d %B, %Y"} {
		var re strings.Builder
		re.WriteString(`(?i)\A`)
		for i := 0; i < len(f); i++ {
			switch {
			case f[i] == '%':
				i++
				re.WriteString(parts[f[i]])
			case f[i] == ' ':
				re.WriteString(`\s+`)
			default:
				re.WriteString(regexp.QuoteMeta(string(f[i])))
			}
		}
		re.WriteString(`\z`)
		out = append(out, regexp.MustCompile(re.String()))
	}
	return out
}()

var monthNumbers = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8,
	"sep": 9, "oct": 10, "nov": 11, "dec": 12}

// ParseFormDate is django.forms.DateField.clean for a non-required field:
// empty input is a nil date with ok true; ok false means "Enter a valid
// date.".
func ParseFormDate(s string) (*time.Time, bool) {
	s = PyStrip(s)
	if s == "" {
		return nil, true
	}
	for _, re := range formDateFormats {
		m := re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		var year, month, day int
		for i, name := range re.SubexpNames() {
			v := m[i]
			switch name {
			case "Y":
				year, _ = strconv.Atoi(v)
			case "y":
				year, _ = strconv.Atoi(v)
				if year < 69 {
					year += 2000
				} else {
					year += 1900
				}
			case "m":
				month, _ = strconv.Atoi(v)
			case "d":
				day, _ = strconv.Atoi(strings.TrimSpace(v))
			case "b", "B":
				month = monthNumbers[strings.ToLower(v)[:3]]
			}
		}
		if year < 1 || day > daysIn(year, month) {
			continue // strptime raises ValueError; the next format is tried
		}
		t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		return &t, true
	}
	return nil, false
}

// StrptimeYMD is datetime.strptime(s, "%Y-%m-%d").date().
func StrptimeYMD(s string) (time.Time, bool) {
	m := formDateFormats[0].FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(strings.TrimSpace(m[3]))
	if year < 1 || day > daysIn(year, month) {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}
