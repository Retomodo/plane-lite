package drf

import (
	"net/http"
	"strings"

	"plane-lite/server/internal/httpx"
)

// SearchTerms is DRF 3.17's SearchFilter.get_search_terms: ?search= through
// a CharField (a null character fails the request), then
// search_smart_split. Quoted phrases stay whole; other tokens split on
// commas.
func SearchTerms(q string) ([]string, error) {
	if strings.ContainsRune(q, 0) {
		return nil, httpx.Body(http.StatusBadRequest, []string{"Null characters are not allowed."})
	}
	var terms []string
	for _, bit := range smartSplit(q) {
		bit = strings.Trim(bit, ",")
		if r := []rune(bit); len(r) > 0 && (r[0] == '"' || r[0] == '\'') && r[0] == r[len(r)-1] {
			// A quoted phrase: unescape_string_literal (a lone quote is "").
			quote := string(r[0])
			inner := ""
			if len(r) >= 2 {
				inner = string(r[1 : len(r)-1])
			}
			terms = append(terms, strings.ReplaceAll(strings.ReplaceAll(inner, `\`+quote, quote), `\\`, `\`))
			continue
		}
		for _, sub := range strings.Split(bit, ",") {
			if sub != "" {
				terms = append(terms, PyStrip(sub))
			}
		}
	}
	return terms, nil
}

// smartSplit is django.utils.text.smart_split.
func smartSplit(s string) []string {
	rs := []rune(s)
	n := len(rs)
	var out []string
	// quoted scans a quoted run starting at i: its end, or -1 if unclosed.
	quoted := func(i int) int {
		q := rs[i]
		for k := i + 1; k < n; k++ {
			switch rs[k] {
			case '\\':
				if k+1 < n {
					k++
				} else {
					return -1
				}
			case q:
				return k + 1
			}
		}
		return -1
	}
	for i := 0; i < n; {
		if pyIsSpace(rs[i]) {
			i++
			continue
		}
		// [^\s'"]* ( quoted [^\s'"]* )+  — else \S+
		k := i
		for k < n && !pyIsSpace(rs[k]) && rs[k] != '"' && rs[k] != '\'' {
			k++
		}
		end, groups := k, 0
		for end < n && (rs[end] == '"' || rs[end] == '\'') {
			e := quoted(end)
			if e < 0 {
				break
			}
			for e < n && !pyIsSpace(rs[e]) && rs[e] != '"' && rs[e] != '\'' {
				e++
			}
			end, groups = e, groups+1
		}
		if groups == 0 {
			end = i
			for end < n && !pyIsSpace(rs[end]) {
				end++
			}
		}
		out = append(out, string(rs[i:end]))
		i = end
	}
	return out
}
