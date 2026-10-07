// Package sanitize ports plane.utils.content_validator.validate_html_content:
// nh3.clean (the ammonia crate) with Plane's tag and attribute allowlists,
// serialized the way html5ever does. Golden fixtures recorded from nh3 live in
// testdata/nh3.json (contract/reference/gen_nh3_fixtures.py).
package sanitize

import (
	"errors"
	"strings"

	"golang.org/x/net/html/atom"

	html "plane-lite/server/internal/sanitize/html5"
)

// MaxSize is content_validator.MAX_SIZE, in UTF-8 bytes.
const MaxSize = 10 * 1024 * 1024

// ErrTooLarge is validate_html_content's size check failing.
var ErrTooLarge = errors.New("HTML content exceeds maximum size limit (10MB)")

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// allowedTags is nh3.ALLOWED_TAGS | CUSTOM_TAGS.
var allowedTags = set(
	"a", "abbr", "acronym", "area", "article", "aside", "b", "bdi", "bdo", "blockquote", "br", "caption",
	"center", "cite", "code", "col", "colgroup", "data", "dd", "del", "details", "dfn", "div", "dl", "dt",
	"em", "figcaption", "figure", "footer", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hgroup", "hr",
	"i", "img", "ins", "kbd", "li", "map", "mark", "nav", "ol", "p", "pre", "q", "rp", "rt", "rtc", "ruby",
	"s", "samp", "small", "span", "strike", "strong", "sub", "summary", "sup", "table", "tbody", "td",
	"th", "thead", "time", "tr", "tt", "u", "ul", "var", "wbr",
	"mention-component", "label", "input", "image-component",
)

// cleanContentTags are removed along with everything inside them.
var cleanContentTags = set("script", "style")

// genericAttrs is ATTRIBUTES["*"]; it replaces ammonia's default (lang, title).
var genericAttrs = set(
	"class", "id", "title", "role", "aria-label", "aria-hidden", "style", "start", "type", "xmlns",
	"data-tight", "data-node-type", "data-type", "data-checked", "data-background-color",
	"data-text-color", "data-name", "data-id", "data-icon-name", "data-icon-color", "data-background",
	"data-emoji-unicode", "data-emoji-url", "data-logo-in-use", "data-block-type",
)

// tagAttrs is the rest of ATTRIBUTES; it replaces ammonia's per-tag defaults.
// The camelCase names can never match (the tokenizer lowercases), as in nh3.
var tagAttrs = map[string]map[string]bool{
	"a":                 set("href", "target"),
	"image-component":   set("id", "width", "height", "aspectRatio", "aspectratio", "src", "alignment", "status"),
	"img":               set("width", "height", "aspectRatio", "aspectratio", "alignment", "src", "alt", "title"),
	"mention-component": set("id", "entity_identifier", "entity_name"),
	"th":                set("colspan", "rowspan", "colwidth", "background", "style"),
	"td":                set("colspan", "rowspan", "colwidth", "background", "textColor", "textcolor", "style"),
	"tr":                set("background", "textColor", "textcolor", "style"),
	"pre":               set("language"),
	"code":              set("language", "spellcheck"),
	"input":             set("type", "checked"),
}

// urlSchemes is SAFE_PROTOCOLS.
var urlSchemes = set("http", "https", "mailto", "tel")

const linkRel = "noopener noreferrer"

// voidElements have no end tag in html5ever's serializer.
var voidElements = set("area", "base", "basefont", "bgsound", "br", "col", "embed", "frame", "hr", "img",
	"input", "keygen", "link", "meta", "param", "source", "track", "wbr")

// HTML returns nh3's cleaning of src and the text that Python's
// HTMLParser-based strip_tags would extract from the result.
func HTML(src string) (clean, text string, err error) {
	if len(src) > MaxSize {
		return "", "", ErrTooLarge
	}
	context := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(src), context)
	if err != nil {
		return "", "", err
	}
	var c cleaner
	for _, n := range nodes {
		c.walk(n, "", "html")
	}
	return c.out.String(), c.text.String(), nil
}

type cleaner struct {
	out, text strings.Builder
}

// walk is ammonia's clean_dom loop. parentNS and parentLocal describe the
// nearest kept ancestor: removed elements are unwrapped, their children
// moving up to it.
func (c *cleaner) walk(n *html.Node, parentNS, parentLocal string) {
	switch n.Type {
	case html.TextNode:
		c.out.WriteString(escape(n.Data, false))
		c.text.WriteString(n.Data)
		return
	case html.ElementNode:
	default: // comments, doctypes
		return
	}
	if cleanContentTags[n.Data] {
		return
	}
	// html5ever keeps a template's contents outside the tree.
	if n.Namespace == "" && n.Data == "template" {
		return
	}
	if !allowedTags[n.Data] || !expectedNamespace(parentNS, parentLocal, n.Namespace, n.Data) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			c.walk(ch, parentNS, parentLocal)
		}
		return
	}
	c.out.WriteByte('<')
	c.out.WriteString(n.Data)
	seen := map[string]bool{}
	for _, a := range n.Attr {
		// The tokenizer keeps the first of duplicate attributes.
		if a.Namespace != "" || seen[a.Key] {
			continue
		}
		seen[a.Key] = true
		if !allowedAttr(n.Data, a.Key, a.Val) {
			continue
		}
		writeAttr(&c.out, a.Key, a.Val)
	}
	if n.Data == "a" {
		writeAttr(&c.out, "rel", linkRel)
	}
	c.out.WriteByte('>')
	if voidElements[n.Data] {
		return
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch, n.Namespace, n.Data)
	}
	c.out.WriteString("</")
	c.out.WriteString(n.Data)
	c.out.WriteByte('>')
}

func allowedAttr(tag, name, value string) bool {
	if !genericAttrs[name] && !tagAttrs[tag][name] {
		return false
	}
	if isURLAttr(tag, name) {
		scheme, relative, ok := parseURL(value)
		if relative {
			return true // UrlRelative::PassThrough
		}
		return ok && urlSchemes[scheme]
	}
	return true
}

func isURLAttr(tag, name string) bool {
	return name == "href" || name == "src" ||
		tag == "form" && name == "action" ||
		tag == "object" && name == "data" ||
		(tag == "button" || tag == "input") && name == "formaction" ||
		tag == "a" && name == "ping" ||
		tag == "video" && name == "poster"
}

// expectedNamespace is ammonia's check_expected_namespace. Namespaces are
// x/net/html's: "" for HTML, "svg" and "math".
func expectedNamespace(parentNS, parentLocal, ns, local string) bool {
	switch {
	case parentNS == "" && ns == "svg":
		return local == "svg"
	case parentNS == "" && ns == "math":
		return local == "math"
	case parentNS == "math" && ns != "math":
		switch parentLocal {
		case "mi", "mo", "mn", "ms", "mtext", "annotation-xml":
			return true
		}
		return false
	case parentNS == "svg" && ns != "svg":
		return parentLocal == "foreignObject"
	case ns == "svg":
		return svgTags[local]
	case ns == "math":
		return mathTags[local]
	case ns == "":
		if !svgTags[local] && !mathTags[local] {
			return true
		}
		switch local {
		case "title", "style", "font", "a", "script", "span":
			return true
		}
		return false
	}
	return parentNS == ns
}

func writeAttr(b *strings.Builder, name, value string) {
	b.WriteByte(' ')
	b.WriteString(name)
	b.WriteString(`="`)
	b.WriteString(escape(value, true))
	b.WriteByte('"')
}

// escape is html5ever's serializer escaping.
func escape(s string, attr bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == ' ':
			b.WriteString("&nbsp;")
		case r == '"' && attr:
			b.WriteString("&quot;")
		case r == '<' && !attr:
			b.WriteString("&lt;")
		case r == '>' && !attr:
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var svgTags = set("a", "animate", "animateMotion", "animateTransform", "circle", "clipPath", "defs", "desc",
	"discard", "ellipse", "feBlend", "feColorMatrix", "feComponentTransfer", "feComposite", "feConvolveMatrix",
	"feDiffuseLighting", "feDisplacementMap", "feDistantLight", "feDropShadow", "feFlood", "feFuncA", "feFuncB",
	"feFuncG", "feFuncR", "feGaussianBlur", "feImage", "feMerge", "feMergeNode", "feMorphology", "feOffset",
	"fePointLight", "feSpecularLighting", "feSpotLight", "feTile", "feTurbulence", "filter", "foreignObject", "g",
	"image", "line", "linearGradient", "marker", "mask", "metadata", "mpath", "path", "pattern", "polygon",
	"polyline", "radialGradient", "rect", "script", "set", "stop", "style", "svg", "switch", "symbol", "text",
	"textPath", "title", "tspan", "use", "view")

var mathTags = set("abs", "and", "annotation", "annotation-xml", "apply", "approx", "arccos", "arccosh",
	"arccot", "arccoth", "arccsc", "arccsch", "arcsec", "arcsech", "arcsin", "arcsinh", "arctan", "arctanh",
	"arg", "bind", "bvar", "card", "cartesianproduct", "cbytes", "ceiling", "cerror", "ci", "cn", "codomain",
	"complexes", "compose", "condition", "conjugate", "cos", "cosh", "cot", "coth", "cs", "csc", "csch",
	"csymbol", "curl", "declare", "degree", "determinant", "diff", "divergence", "divide", "domain",
	"domainofapplication", "emptyset", "eq", "equivalent", "eulergamma", "exists", "exp", "exponentiale",
	"factorial", "factorof", "false", "floor", "fn", "forall", "gcd", "geq", "grad", "gt", "ident", "image",
	"imaginary", "imaginaryi", "implies", "in", "infinity", "int", "integers", "intersect", "interval",
	"inverse", "lambda", "laplacian", "lcm", "leq", "limit", "list", "ln", "log", "logbase", "lowlimit", "lt",
	"maction", "maligngroup", "malignmark", "math", "matrix", "matrixrow", "max", "mean", "median", "menclose",
	"merror", "mfenced", "mfrac", "mglyph", "mi", "min", "minus", "mlabeledtr", "mlongdiv", "mmultiscripts",
	"mn", "mo", "mode", "moment", "momentabout", "mover", "mpadded", "mphantom", "mprescripts", "mroot", "mrow",
	"ms", "mscarries", "mscarry", "msgroup", "msline", "mspace", "msqrt", "msrow", "mstack", "mstyle", "msub",
	"msubsup", "msup", "mtable", "mtd", "mtext", "mtr", "munder", "munderover", "naturalnumbers", "neq", "none",
	"not", "notanumber", "notin", "notprsubset", "notsubset", "or", "otherwise", "outerproduct", "partialdiff",
	"pi", "piece", "piecewise", "plus", "power", "primes", "product", "prsubset", "quotient", "rationals",
	"real", "reals", "reln", "rem", "root", "scalarproduct", "sdev", "sec", "sech", "selector", "semantics",
	"sep", "set", "setdiff", "share", "sin", "sinh", "span", "subset", "sum", "tan", "tanh", "tendsto", "times",
	"transpose", "true", "union", "uplimit", "variance", "vector", "vectorproduct", "xor")
