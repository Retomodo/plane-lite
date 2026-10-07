package drf

import (
	"regexp"
	"strconv"
	"time"
)

// ParseDateTime is django.utils.dateparse.parse_datetime as DRF's
// DateTimeField applies it: CPython 3.12's datetime.fromisoformat (the C
// implementation), falling back to Django's regex. A naive result is
// interpreted in loc, as DRF's enforce_timezone does with the active
// timezone. ok is false where DRF would report "Datetime has wrong format".
func ParseDateTime(s string, loc *time.Location) (t time.Time, ok bool) {
	if p, err := fromISOFormat(s); err == nil {
		return p.at(loc)
	} else if !errInvalidISO(err) {
		return time.Time{}, false
	}
	p, matched := djangoDateTime(s)
	if !matched {
		return time.Time{}, false
	}
	return p.at(loc)
}

type isoParts struct {
	year, month, day, hour, minute, second, usec int
	aware                                        bool
	offsetUsec                                   int64 // east of UTC
}

func (p isoParts) at(loc *time.Location) (time.Time, bool) {
	if p.year < 1 || p.year > 9999 || p.month < 1 || p.month > 12 || p.day < 1 ||
		p.day > daysIn(p.year, p.month) || p.hour > 23 || p.minute > 59 || p.second > 59 || p.usec > 999999 {
		return time.Time{}, false
	}
	if !p.aware {
		return time.Date(p.year, time.Month(p.month), p.day, p.hour, p.minute, p.second, p.usec*1000, loc), true
	}
	if p.offsetUsec <= -86400e6 || p.offsetUsec >= 86400e6 {
		return time.Time{}, false // timezone() rejects |offset| >= 24h
	}
	wall := time.Date(p.year, time.Month(p.month), p.day, p.hour, p.minute, p.second, p.usec*1000, time.UTC)
	return wall.Add(-time.Duration(p.offsetUsec) * time.Microsecond), true
}

func daysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

type isoError struct{}

func (isoError) Error() string { return "Invalid isoformat string" }

func errInvalidISO(err error) bool { _, ok := err.(isoError); return ok }

// fromISOFormat ports datetime_fromisoformat from Modules/_datetimemodule.c
// (3.12). It works on UTF-8 bytes, as the C code does; at() applies the
// range checks the datetime constructor would.
func fromISOFormat(s string) (isoParts, error) {
	var p isoParts
	b := []byte(s)
	n := len(b)
	if n < 7 {
		return p, isoError{}
	}
	at := func(i int) byte { // the C code reads the NUL terminator
		if i < 0 || i >= n {
			return 0
		}
		return b[i]
	}
	sep := isoSeparator(at, n)
	if sep < 0 {
		return p, isoError{}
	}
	if !isoDate(at, sep, &p) {
		return p, isoError{}
	}
	if n > sep {
		pos := sep + 1
		// Skip a multi-byte separator character.
		if c := at(sep); c >= 0x80 {
			switch c & 0xf0 {
			case 0xe0:
				pos += 2
			case 0xf0:
				pos += 3
			default:
				pos++
			}
		}
		if !isoTime(at, pos, n, &p) {
			return p, isoError{}
		}
	}
	// Values the datetime constructor rejects raise ValueError too, which
	// sends parse_datetime on to its regex.
	if _, ok := p.at(time.UTC); !ok {
		return p, isoError{}
	}
	return p, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isoSeparator(at func(int) byte, n int) int {
	if n == 7 {
		return 7
	}
	if at(4) == '-' {
		if at(5) == 'W' {
			if n < 8 {
				return -1
			}
			if n > 8 && at(8) == '-' {
				if n == 9 {
					return -1
				}
				if n > 10 && isDigit(at(10)) {
					return 8
				}
				return 10
			}
			return 8
		}
		return 10
	}
	if at(4) == 'W' {
		idx := 7
		for ; idx < n; idx++ {
			if !isDigit(at(idx)) {
				break
			}
		}
		if idx < 9 {
			return idx
		}
		if idx%2 == 0 {
			return 7
		}
		return 8
	}
	return 8
}

// digits is parse_digits: exactly count ASCII digits.
func digits(at func(int) byte, pos, count int) (int, int, bool) {
	v := 0
	for i := 0; i < count; i++ {
		c := at(pos + i)
		if !isDigit(c) {
			return 0, pos, false
		}
		v = v*10 + int(c-'0')
	}
	return v, pos + count, true
}

func isoDate(at func(int) byte, length int, p *isoParts) bool {
	var ok bool
	pos := 0
	if p.year, pos, ok = digits(at, pos, 4); !ok {
		return false
	}
	sep := at(pos) == '-'
	if sep {
		pos++
	}
	if at(pos) == 'W' {
		pos++
		var week, day int
		if week, pos, ok = digits(at, pos, 2); !ok {
			return false
		}
		day = 1
		if pos < length {
			if sep {
				if at(pos) != '-' {
					return false
				}
				pos++
			}
			if day, _, ok = digits(at, pos, 1); !ok {
				return false
			}
		}
		y, m, d, ok := isoWeekToDate(p.year, week, day)
		if !ok {
			return false
		}
		p.year, p.month, p.day = y, m, d
		return true
	}
	if p.month, pos, ok = digits(at, pos, 2); !ok {
		return false
	}
	if sep {
		if at(pos) != '-' {
			return false
		}
		pos++
	}
	p.day, _, ok = digits(at, pos, 2)
	return ok
}

func isoWeekToDate(year, week, day int) (int, int, int, bool) {
	if year < 1 || year > 9999 || day < 1 || day > 7 {
		return 0, 0, 0, false
	}
	if week < 1 || week > 53 {
		return 0, 0, 0, false
	}
	if week == 53 {
		jan1 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Weekday()
		leap := daysIn(year, 2) == 29
		if !(jan1 == time.Thursday || (jan1 == time.Wednesday && leap)) {
			return 0, 0, 0, false
		}
	}
	// Monday of ISO week 1 is the Monday on or before January 4th.
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
	d := monday.AddDate(0, 0, (week-1)*7+day-1)
	return d.Year(), int(d.Month()), d.Day(), true
}

// hhmmssff is parse_hh_mm_ss_ff over [pos, end). It returns rv: 0 at the
// end of the string, 1 if anything follows, negative on error.
func hhmmssff(at func(int) byte, pos, end int, vals *[4]int) int {
	*vals = [4]int{}
	hasSep := true
	var ok bool
	for i := 0; i < 3; i++ {
		if vals[i], pos, ok = digits(at, pos, 2); !ok {
			return -3
		}
		c := at(pos)
		pos++
		if i == 0 {
			hasSep = c == ':'
		}
		if pos >= end {
			if c != 0 {
				return 1
			}
			return 0
		}
		if hasSep && c == ':' {
			continue
		} else if c == '.' || c == ',' {
			break
		} else if !hasSep {
			pos--
		} else {
			return -4
		}
	}
	remains := end - pos
	if remains <= 0 {
		return -3 // CPython rejects an empty fraction ("10:00:00.")
	}
	toParse := min(remains, 6)
	if vals[3], pos, ok = digits(at, pos, toParse); !ok {
		return -3
	}
	correction := [...]int{100000, 10000, 1000, 100, 10}
	if toParse < 6 {
		vals[3] *= correction[toParse-1]
	}
	for isDigit(at(pos)) {
		pos++
	}
	if at(pos) != 0 {
		return 1
	}
	return 0
}

func isoTime(at func(int) byte, pos, n int, p *isoParts) bool {
	tz := pos
	for {
		c := at(tz)
		if c == 'Z' || c == '+' || c == '-' {
			break
		}
		tz++
		if tz >= n {
			break
		}
	}
	var t [4]int
	rv := hhmmssff(at, pos, tz, &t)
	p.hour, p.minute, p.second, p.usec = t[0], t[1], t[2], t[3]
	if rv < 0 {
		return false
	}
	if tz >= n {
		return rv != 1
	}
	if at(tz) == 'Z' {
		p.aware = true
		return at(tz+1) == 0
	}
	sign := int64(1)
	if at(tz) == '-' {
		sign = -1
	}
	var o [4]int
	if hhmmssff(at, tz+1, n, &o) != 0 {
		return false
	}
	p.aware = true
	p.offsetUsec = sign * ((int64(o[0])*3600+int64(o[1])*60+int64(o[2]))*1e6 + int64(o[3]))
	return true
}

// Django's datetime_re, with Python's Unicode \s spelled out.
var djangoDateTimeRe = regexp.MustCompile(`^([0-9]{4})-([0-9]{1,2})-([0-9]{1,2})[T ]([0-9]{1,2}):([0-9]{1,2})` +
	`(?::([0-9]{1,2})(?:[.,]([0-9]{1,6})[0-9]{0,6})?)?` +
	`[\t\n\v\f\r \x1c-\x1f\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]*` +
	`(Z|[+-][0-9]{2}(?::?[0-9]{2})?)?\n?$`)

func djangoDateTime(s string) (isoParts, bool) {
	m := djangoDateTimeRe.FindStringSubmatch(s)
	if m == nil {
		return isoParts{}, false
	}
	atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
	p := isoParts{year: atoi(m[1]), month: atoi(m[2]), day: atoi(m[3]), hour: atoi(m[4]), minute: atoi(m[5]), second: atoi(m[6])}
	if m[7] != "" {
		us := m[7]
		for len(us) < 6 {
			us += "0"
		}
		p.usec = atoi(us)
	}
	switch tz := m[8]; {
	case tz == "Z":
		p.aware = true
	case tz != "":
		mins := 0
		if len(tz) > 3 {
			mins = atoi(tz[len(tz)-2:])
		}
		off := 60*atoi(tz[1:3]) + mins
		if tz[0] == '-' {
			off = -off
		}
		p.aware = true
		p.offsetUsec = int64(off) * 60e6
	}
	return p, true
}
