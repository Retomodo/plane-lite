package httpx

import (
	"bytes"
	"fmt"
	"strconv"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

// FormatDateTime renders t the way Django REST framework does: Python's
// isoformat() in loc (the requesting user's timezone), microseconds only when
// non-zero, and a "Z" suffix instead of "+00:00".
func FormatDateTime(t time.Time, loc *time.Location) string {
	if loc != nil {
		t = t.In(loc)
	}
	b := make([]byte, 0, 32)
	b = t.AppendFormat(b, "2006-01-02T15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		b = append(b, '.')
		s := strconv.Itoa(us)
		for range 6 - len(s) {
			b = append(b, '0')
		}
		b = append(b, s...)
	}
	if _, off := t.Zone(); off == 0 {
		b = append(b, 'Z')
	} else {
		b = t.AppendFormat(b, "-07:00")
	}
	return string(b)
}

// Date is a calendar date column (Django DateField), rendered "YYYY-MM-DD".
type Date struct {
	time.Time
}

func (d Date) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Format(time.DateOnly) + `"`), nil
}

func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// Marshal encodes v as a JSON response body. time.Time values (and pointers
// to them) are rendered in loc, so handlers can scan timestamps straight
// into response structs without per-field conversion.
func Marshal(v any, loc *time.Location) ([]byte, error) {
	if loc == nil {
		loc = time.UTC
	}
	var buf bytes.Buffer
	err := json.MarshalWrite(&buf, v,
		json.WithMarshalers(json.MarshalToFunc(func(enc *jsontext.Encoder, t time.Time) error {
			return enc.WriteToken(jsontext.String(FormatDateTime(t, loc)))
		})),
		json.Deterministic(true),
	)
	if err != nil {
		return nil, fmt.Errorf("httpx: marshal response: %w", err)
	}
	return buf.Bytes(), nil
}
