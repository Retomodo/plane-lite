package api

import (
	"bytes"
	"context"
	"math"
	"strings"
	"unicode"

	"github.com/go-json-experiment/json"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
)

// Shared pieces of the search views (app/views/search/), which build Q()
// objects of icontains lookups and answer .values() dicts.

// searchSQL collects the positional arguments of one search query.
type searchSQL struct{ args []any }

func (s *searchSQL) arg(v any) string {
	s.args = append(s.args, v)
	return "$" + itoa(len(s.args))
}

// contains is Q(field__icontains=q) OR'ed over cols, as the views build
// it: UPPER(col::text) LIKE UPPER('%q%') with LIKE's wildcards escaped.
func (s *searchSQL) contains(q string, cols ...string) string {
	pattern := s.arg("%" + likeEscape(q) + "%")
	parts := make([]string, len(cols))
	for i, col := range cols {
		parts[i] = "UPPER(" + col + "::text) LIKE UPPER(" + pattern + ")"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// issueQuery is the views' issue search Q(): name or the project identifier
// containing q, or the sequence id equal to a whole number in q. The
// search_issues util (wholeNumbers false) instead matches the sequence id
// as text when q is longer than 20 characters.
func (s *searchSQL) issueQuery(q string, wholeNumbers bool) string {
	pattern := s.arg("%" + likeEscape(q) + "%")
	like := func(col string) string { return "UPPER(" + col + "::text) LIKE UPPER(" + pattern + ")" }
	parts := []string{like("i.name")}
	if wholeNumbers || len([]rune(q)) <= 20 {
		for _, n := range searchSequenceIDs(q) {
			parts = append(parts, "i.sequence_id = "+s.arg(n))
		}
	} else {
		parts = append(parts, like("i.sequence_id"))
	}
	parts = append(parts, like("p.identifier"))
	return "(" + strings.Join(parts, " OR ") + ")"
}

// searchSequenceIDs ports re.findall(r"\b\d+\b", query) and the integer
// lookup that follows: runs of Unicode decimal digits standing alone as
// words, as int(). Values outside the integer column's range match nothing
// (Django drops the lookup), so they are left out.
func searchSequenceIDs(q string) []int64 {
	// Python's \w for str patterns: alphanumerics and the underscore.
	word := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }
	rs := []rune(q)
	var out []int64
	for i := 0; i < len(rs); {
		if !unicode.IsDigit(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) && unicode.IsDigit(rs[j]) {
			j++
		}
		if (i == 0 || !word(rs[i-1])) && (j == len(rs) || !word(rs[j])) {
			if n, ok := drf.PyIntString(string(rs[i:j])); ok && n.IsInt64() && n.Int64() <= math.MaxInt32 {
				out = append(out, n.Int64())
			}
		}
		i = j
	}
	return out
}

// searchRows runs a query and collects its rows into T by position.
func searchRows[T any](ctx context.Context, a *API, sql string, args []any) ([]T, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[T])
	if out == nil {
		out = []T{}
	}
	return out, err
}

// searchBuckets is the views' response_data dict: one list per entity
// type, in the order the types were first asked for.
type searchBuckets struct {
	keys []string
	vals map[string]any
}

func (b *searchBuckets) set(key string, v any) {
	if b.vals == nil {
		b.vals = map[string]any{}
	}
	if _, ok := b.vals[key]; !ok {
		b.keys = append(b.keys, key)
	}
	b.vals[key] = v
}

func (b searchBuckets) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range b.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(b.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
