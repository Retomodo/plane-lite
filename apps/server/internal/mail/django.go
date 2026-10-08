package mail

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RenderDjango renders a template with the subset of Django's template
// language Plane's notification emails use: {{ var.path|filter:arg }}
// (filters length, add, last, slice, safe), {% if %} / {% elif %} /
// {% else %} with and/or/not and comparisons, and {% for x in list %}.
// Variables resolve as Django's do: dict keys, then list indexes; a missing
// one renders "" and is None in conditions. Context values are maps
// (map[string]any), lists ([]any or []string), strings, ints, bools and nil.
// Output is autoescaped like Django's, except values passed through |safe.
func RenderDjango(name string, ctx map[string]any) (string, error) {
	b, err := templateFS.ReadFile("templates/" + name)
	if err != nil {
		return "", err
	}
	nodes, rest, err := djParse(djTokenize(string(b)), nil)
	if err != nil {
		return "", fmt.Errorf("mail: template %s: %w", name, err)
	}
	if len(rest) > 0 {
		return "", fmt.Errorf("mail: template %s: unexpected {%% %s %%}", name, rest[0].body)
	}
	var sb strings.Builder
	djRender(&sb, nodes, []map[string]any{ctx})
	return sb.String(), nil
}

// djTagRe is django.template.base.tag_re.
var djTagRe = regexp.MustCompile(`({%.*?%}|{{.*?}}|{#.*?#})`)

type djToken struct {
	kind byte // 't' text, 'v' variable, 'b' block
	body string
}

func djTokenize(src string) []djToken {
	var out []djToken
	last := 0
	for _, m := range djTagRe.FindAllStringIndex(src, -1) {
		if m[0] > last {
			out = append(out, djToken{'t', src[last:m[0]]})
		}
		tag := src[m[0]:m[1]]
		switch tag[:2] {
		case "{{":
			out = append(out, djToken{'v', strings.TrimSpace(tag[2 : len(tag)-2])})
		case "{%":
			out = append(out, djToken{'b', strings.TrimSpace(tag[2 : len(tag)-2])})
		}
		last = m[1]
	}
	if last < len(src) {
		out = append(out, djToken{'t', src[last:]})
	}
	return out
}

type djNode interface{}

type djText string

type djVar struct{ expr *djFilterExpr }

type djIf struct {
	conds  []djCond // nil cond is {% else %}
	bodies [][]djNode
}

type djFor struct {
	loopVar string
	seq     *djFilterExpr
	body    []djNode
}

// djParse reads nodes until one of the until block tags, which it leaves at
// the head of the returned tokens.
func djParse(toks []djToken, until []string) ([]djNode, []djToken, error) {
	var nodes []djNode
	for len(toks) > 0 {
		t := toks[0]
		switch t.kind {
		case 't':
			nodes = append(nodes, djText(t.body))
			toks = toks[1:]
		case 'v':
			fe, err := djParseFilterExpr(t.body)
			if err != nil {
				return nil, nil, err
			}
			nodes = append(nodes, djVar{fe})
			toks = toks[1:]
		case 'b':
			word, args, _ := strings.Cut(t.body, " ")
			for _, u := range until {
				if word == u {
					return nodes, toks, nil
				}
			}
			toks = toks[1:]
			switch word {
			case "if":
				n := &djIf{}
				cond, err := djParseCond(args)
				if err != nil {
					return nil, nil, err
				}
				for {
					body, rest, err := djParse(toks, []string{"elif", "else", "endif"})
					if err != nil {
						return nil, nil, err
					}
					if len(rest) == 0 {
						return nil, nil, fmt.Errorf("unclosed {%% if %%}")
					}
					n.conds = append(n.conds, cond)
					n.bodies = append(n.bodies, body)
					w, a, _ := strings.Cut(rest[0].body, " ")
					toks = rest[1:]
					if w == "endif" {
						break
					}
					cond = nil
					if w == "elif" {
						if cond, err = djParseCond(a); err != nil {
							return nil, nil, err
						}
					}
				}
				nodes = append(nodes, n)
			case "for":
				f := strings.Fields(args)
				if len(f) != 3 || f[1] != "in" {
					return nil, nil, fmt.Errorf("unsupported {%% for %s %%}", args)
				}
				seq, err := djParseFilterExpr(f[2])
				if err != nil {
					return nil, nil, err
				}
				body, rest, err := djParse(toks, []string{"endfor"})
				if err != nil {
					return nil, nil, err
				}
				if len(rest) == 0 {
					return nil, nil, fmt.Errorf("unclosed {%% for %%}")
				}
				toks = rest[1:]
				nodes = append(nodes, &djFor{loopVar: f[0], seq: seq, body: body})
			default:
				return nil, nil, fmt.Errorf("unsupported tag {%% %s %%}", t.body)
			}
		}
	}
	return nodes, nil, nil
}

// djSafe marks a value |safe.
type djSafe struct{ s string }

// djInvalid is a variable that failed to resolve.
type djInvalid struct{}

type djFilter struct {
	name string
	arg  *djOperand
}

type djOperand struct {
	literal any
	isLit   bool
	path    []string
}

type djFilterExpr struct {
	base    djOperand
	filters []djFilter
}

func djParseOperand(s string) (djOperand, error) {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return djOperand{literal: s[1 : len(s)-1], isLit: true}, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return djOperand{literal: n, isLit: true}, nil
	}
	if s == "" {
		return djOperand{}, fmt.Errorf("empty variable")
	}
	return djOperand{path: strings.Split(s, ".")}, nil
}

func djParseFilterExpr(s string) (*djFilterExpr, error) {
	parts := strings.Split(s, "|")
	base, err := djParseOperand(strings.TrimSpace(parts[0]))
	if err != nil {
		return nil, err
	}
	fe := &djFilterExpr{base: base}
	for _, p := range parts[1:] {
		name, arg, hasArg := strings.Cut(p, ":")
		f := djFilter{name: strings.TrimSpace(name)}
		switch f.name {
		case "length", "add", "last", "slice", "safe":
		default:
			return nil, fmt.Errorf("unsupported filter %q", f.name)
		}
		if hasArg {
			op, err := djParseOperand(strings.TrimSpace(arg))
			if err != nil {
				return nil, err
			}
			f.arg = &op
		}
		fe.filters = append(fe.filters, f)
	}
	return fe, nil
}

func djLookup(scopes []map[string]any, path []string) any {
	var cur any = djInvalid{}
	for i := len(scopes) - 1; i >= 0; i-- {
		if v, ok := scopes[i][path[0]]; ok {
			cur = v
			break
		}
	}
	for _, part := range path[1:] {
		switch x := cur.(type) {
		case map[string]any:
			v, ok := x[part]
			if !ok {
				return djInvalid{}
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return djInvalid{}
			}
			cur = x[i]
		case []string:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return djInvalid{}
			}
			cur = x[i]
		case string:
			r := []rune(x)
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(r) {
				return djInvalid{}
			}
			cur = string(r[i])
		default:
			return djInvalid{}
		}
	}
	return cur
}

func (o djOperand) resolve(scopes []map[string]any) any {
	if o.isLit {
		return o.literal
	}
	return djLookup(scopes, o.path)
}

func djList(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

func (fe *djFilterExpr) resolve(scopes []map[string]any) any {
	v := fe.base.resolve(scopes)
	for _, f := range fe.filters {
		var arg any
		if f.arg != nil {
			arg = f.arg.resolve(scopes)
		}
		v = djApply(f.name, v, arg)
	}
	return v
}

func djApply(name string, v, arg any) any {
	switch name {
	case "length":
		if l, ok := djList(v); ok {
			return len(l)
		}
		switch x := v.(type) {
		case string:
			return len([]rune(x))
		case djSafe:
			return len([]rune(x.s))
		case map[string]any:
			return len(x)
		}
		return 0
	case "add":
		a, aok := djInt(v)
		b, bok := djInt(arg)
		if aok && bok {
			return a + b
		}
		return ""
	case "last":
		if l, ok := djList(v); ok && len(l) > 0 {
			return l[len(l)-1]
		}
		if s, ok := v.(string); ok && s != "" {
			r := []rune(s)
			return string(r[len(r)-1])
		}
		return ""
	case "slice":
		l, ok := djList(v)
		spec, _ := arg.(string)
		if !ok {
			return v
		}
		lo, hi, _ := strings.Cut(spec, ":")
		start, end := 0, len(l)
		if n, err := strconv.Atoi(lo); err == nil {
			start = djClamp(n, len(l))
		}
		if n, err := strconv.Atoi(hi); err == nil {
			end = djClamp(n, len(l))
		}
		if start > end {
			return []any{}
		}
		return l[start:end]
	case "safe":
		return djSafe{djStr(v)}
	}
	return v
}

// djClamp resolves a Python slice bound.
func djClamp(n, length int) int {
	if n < 0 {
		n += length
	}
	return max(0, min(n, length))
}

// djInt is the int() the add filter tries.
func djInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}

// djStr is Python's str() for the value kinds the templates print.
func djStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case djSafe:
		return x.s
	case int:
		return strconv.Itoa(x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	case djInvalid:
		return ""
	}
	return fmt.Sprint(v)
}

func djTruthy(v any) bool {
	switch x := v.(type) {
	case nil, djInvalid:
		return false
	case string:
		return x != ""
	case djSafe:
		return x.s != ""
	case int:
		return x != 0
	case bool:
		return x
	case map[string]any:
		return len(x) > 0
	}
	if l, ok := djList(v); ok {
		return len(l) > 0
	}
	return true
}

// djCond is a parsed {% if %} condition.
type djCond func(scopes []map[string]any) any

// djParseCond ports django.template.smartif: or < and < not < comparisons.
func djParseCond(s string) (djCond, error) {
	p := &djCondParser{toks: djCondTokens(s)}
	c, err := p.expr(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.toks) {
		return nil, fmt.Errorf("unexpected %q in {%% if %s %%}", p.toks[p.pos], s)
	}
	return c, nil
}

// djCondTokens splits on spaces outside quotes.
func djCondTokens(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
			cur.WriteRune(r)
		case r == ' ':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

type djCondParser struct {
	toks []string
	pos  int
}

var djBinding = map[string]int{"or": 6, "and": 7, "==": 10, "!=": 10, "<": 10, ">": 10, "<=": 10, ">=": 10}

func (p *djCondParser) expr(rbp int) (djCond, error) {
	if p.pos >= len(p.toks) {
		return nil, fmt.Errorf("unexpected end of condition")
	}
	t := p.toks[p.pos]
	p.pos++
	var left djCond
	if t == "not" {
		x, err := p.expr(8)
		if err != nil {
			return nil, err
		}
		left = func(s []map[string]any) any { return !djTruthy(x(s)) }
	} else {
		fe, err := djParseFilterExpr(t)
		if err != nil {
			return nil, err
		}
		left = func(s []map[string]any) any { return fe.resolve(s) }
	}
	for p.pos < len(p.toks) {
		op := p.toks[p.pos]
		bp, ok := djBinding[op]
		if !ok {
			return nil, fmt.Errorf("unsupported operator %q", op)
		}
		if bp <= rbp {
			break
		}
		p.pos++
		right, err := p.expr(bp)
		if err != nil {
			return nil, err
		}
		l := left
		left = func(s []map[string]any) any { return djCompare(op, l, right, s) }
	}
	return left, nil
}

// djCompare evaluates a binary operator. Like smartif, an operand that
// failed to resolve is None, and a comparison Python refuses (None > 1) is
// False.
func djCompare(op string, l, r djCond, s []map[string]any) any {
	switch op {
	case "or":
		if v := l(s); djTruthy(v) {
			return v
		}
		return r(s)
	case "and":
		if v := l(s); !djTruthy(v) {
			return v
		}
		return r(s)
	}
	a, b := djNone(l(s)), djNone(r(s))
	switch op {
	case "==":
		return djEqual(a, b)
	case "!=":
		return !djEqual(a, b)
	}
	if x, ok := a.(int); ok {
		if y, ok := b.(int); ok {
			switch op {
			case "<":
				return x < y
			case ">":
				return x > y
			case "<=":
				return x <= y
			case ">=":
				return x >= y
			}
		}
	}
	if x, ok := a.(string); ok {
		if y, ok := b.(string); ok {
			switch op {
			case "<":
				return x < y
			case ">":
				return x > y
			case "<=":
				return x <= y
			case ">=":
				return x >= y
			}
		}
	}
	return false
}

func djNone(v any) any {
	switch x := v.(type) {
	case djInvalid:
		return nil
	case djSafe:
		return x.s
	}
	return v
}

func djEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case int:
		y, ok := b.(int)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false
}

func djRender(sb *strings.Builder, nodes []djNode, scopes []map[string]any) {
	for _, n := range nodes {
		switch x := n.(type) {
		case djText:
			sb.WriteString(string(x))
		case djVar:
			v := x.expr.resolve(scopes)
			if s, ok := v.(djSafe); ok {
				sb.WriteString(s.s)
			} else {
				sb.WriteString(escaper.Replace(djStr(v)))
			}
		case *djIf:
			for i, c := range x.conds {
				if c == nil || djTruthy(c(scopes)) {
					djRender(sb, x.bodies[i], scopes)
					break
				}
			}
		case *djFor:
			l, _ := djList(x.seq.resolve(scopes))
			for _, item := range l {
				djRender(sb, x.body, append(scopes, map[string]any{x.loopVar: item}))
			}
		}
	}
}
