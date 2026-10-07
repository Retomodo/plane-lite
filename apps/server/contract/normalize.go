package contract

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var (
	uuidRe     = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	dateTimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{6})?(Z|[+-]\d{2}:\d{2})$`)
	// uuid4().hex values (usernames, tokens).
	hex32Re = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// idMap assigns stable placeholders to UUIDs in order of first appearance,
// so references between responses stay checkable while values differ
// between runs and implementations.
type idMap struct {
	ids     map[string]string
	aliases [][2]string
}

func newIDMap() *idMap { return &idMap{ids: map[string]string{}} }

func (m *idMap) replace(s string) string {
	for _, a := range m.aliases {
		s = strings.ReplaceAll(s, a[0], a[1])
	}
	return uuidRe.ReplaceAllStringFunc(s, func(id string) string {
		p, ok := m.ids[id]
		if !ok {
			p = fmt.Sprintf("<uuid:%d>", len(m.ids)+1)
			m.ids[id] = p
		}
		return p
	})
}

// normalizer rewrites volatile values in decoded JSON. Object keys are
// visited in sorted order so placeholder numbering is deterministic.
type normalizer struct {
	ids   *idMap
	masks map[string]bool // dotted paths whose values are replaced by "<masked>"
	exact map[string]bool // dotted paths kept verbatim
	sorts map[string]string
	// unordered marks scalar arrays sorted after normalization.
	unordered map[string]bool
	// noCSRF is a request option carried here for convenience.
	noCSRF bool
}

func (n *normalizer) value(path string, v any) any {
	if n.isMasked(path) && v != nil {
		return "<masked>"
	}
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			// Keys keyed by id (e.g. {project_id: role}) get placeholders too;
			// options still address them by their raw path.
			out[n.ids.replace(k)] = n.value(join(path, k), x[k])
		}
		return out
	case []any:
		if key, ok := n.sorts[path]; ok {
			x = slices.Clone(x)
			slices.SortStableFunc(x, func(a, b any) int {
				return strings.Compare(sortKey(a, key), sortKey(b, key))
			})
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = n.value(path+"[]", e)
		}
		if n.isUnordered(path) {
			slices.SortStableFunc(out, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
		}
		return out
	case string:
		if n.exact[path] {
			return x
		}
		return n.str(x)
	default:
		return v
	}
}

// isMasked reports whether the value at path was masked, directly or by a
// "*.name" suffix pattern.
func (n *normalizer) isMasked(path string) bool {
	if n.masks[path] {
		return true
	}
	for p := range n.masks {
		if rest, ok := strings.CutPrefix(p, "body.*"); ok && strings.HasSuffix(path, rest) {
			return true
		}
	}
	return false
}

// isUnordered reports whether the array at path was marked Unordered,
// directly or by a "*.name" suffix pattern.
func (n *normalizer) isUnordered(path string) bool {
	if n.unordered[path] {
		return true
	}
	for p := range n.unordered {
		if rest, ok := strings.CutPrefix(p, "body.*"); ok && strings.HasSuffix(path, rest) {
			return true
		}
	}
	return false
}

func (n *normalizer) str(s string) string {
	if m := dateTimeRe.FindStringSubmatch(s); m != nil {
		// Keep the offset: Plane renders times in the user's timezone.
		return "<datetime " + m[2] + ">"
	}
	if hex32Re.MatchString(s) {
		return "<hex32>"
	}
	return n.ids.replace(s)
}

func sortKey(v any, key string) string {
	if m, ok := v.(map[string]any); ok {
		return fmt.Sprint(m[key])
	}
	return fmt.Sprint(v)
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

// diff reports differences between expected and actual normalized JSON.
func diff(path string, want, got any, out *[]string) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: want object, got %s", orRoot(path), brief(got)))
			return
		}
		keys := map[string]bool{}
		for k := range w {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			wv, wok := w[k]
			gv, gok := g[k]
			switch {
			case !gok:
				*out = append(*out, fmt.Sprintf("%s: missing key (want %s)", join(path, k), brief(wv)))
			case !wok:
				*out = append(*out, fmt.Sprintf("%s: unexpected key (got %s)", join(path, k), brief(gv)))
			default:
				diff(join(path, k), wv, gv, out)
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: want array, got %s", orRoot(path), brief(got)))
			return
		}
		if len(w) != len(g) {
			*out = append(*out, fmt.Sprintf("%s: want %d items, got %d", orRoot(path), len(w), len(g)))
		}
		for i := range min(len(w), len(g)) {
			diff(fmt.Sprintf("%s[%d]", path, i), w[i], g[i], out)
		}
	case float64:
		// JSON numbers: 65535.0 (Python) and 65535 (Go) are the same value.
		if g, ok := got.(float64); !ok || g != w {
			*out = append(*out, fmt.Sprintf("%s: want %s, got %s", orRoot(path), brief(want), brief(got)))
		}
	default:
		if fmt.Sprintf("%T:%v", want, want) != fmt.Sprintf("%T:%v", got, got) {
			*out = append(*out, fmt.Sprintf("%s: want %s, got %s", orRoot(path), brief(want), brief(got)))
		}
	}
}

func orRoot(p string) string {
	if p == "" {
		return "(root)"
	}
	return p
}

func brief(v any) string {
	s := fmt.Sprintf("%#v", v)
	if v == nil {
		s = "null"
	}
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}
