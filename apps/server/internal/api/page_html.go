package api

import (
	"errors"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"unicode"

	html "plane-lite/server/internal/sanitize/html5"
)

// Pages store their description_html unsanitized (only the description
// endpoint runs nh3), so the HTML that Page.save()'s strip_tags and the page
// tasks' BeautifulSoup(html, "html.parser") see can be anything. This file
// ports the Python 3.12 html.parser.HTMLParser they both sit on, html.unescape,
// and the parts of BeautifulSoup 4.12 the page tasks use (tree building,
// find_all and str(soup)).

// errPageMarkup is the AssertionError HTMLParser raises on a marked section
// it cannot name (e.g. "<![x"); strip_tags lets it escape, BeautifulSoup
// turns it into ParserRejectedMarkup.
var errPageMarkup = errors.New("api: html.parser rejected the markup")

// pageIsSpace is Python's \s and str.isspace().
func pageIsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func pageIsASCIILetter(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }

func pageIsASCIIAlnum(r rune) bool { return pageIsASCIILetter(r) || r >= '0' && r <= '9' }

func pageIsHex(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}

// pageLower is str.lower(): Go's simple mapping plus the one unconditional
// full mapping (U+0130).
func pageLower(s string) string {
	if !strings.ContainsRune(s, 'İ') {
		return strings.ToLower(s)
	}
	var b strings.Builder
	for _, r := range s {
		if r == 'İ' {
			b.WriteString("i̇")
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func pageStrip(rs []rune) []rune {
	i, j := 0, len(rs)
	for i < j && pageIsSpace(rs[i]) {
		i++
	}
	for j > i && pageIsSpace(rs[j-1]) {
		j--
	}
	return rs[i:j]
}

// pageCharrefRe is html._charref.
var pageCharrefRe = regexp.MustCompile(`&(#[0-9]+;?|#[xX][0-9a-fA-F]+;?|[^\t\n\f <&#;]{1,32};?)`)

// pageInvalidCharrefs is html._invalid_charrefs.
var pageInvalidCharrefs = map[int64]string{
	0x00: "\ufffd", 0x0d: "\r", 0x80: "\u20ac", 0x81: "\u0081", 0x82: "\u201a", 0x83: "\u0192", 0x84: "\u201e",
	0x85: "\u2026", 0x86: "\u2020", 0x87: "\u2021", 0x88: "\u02c6", 0x89: "\u2030", 0x8a: "\u0160", 0x8b: "\u2039",
	0x8c: "\u0152", 0x8d: "\u008d", 0x8e: "\u017d", 0x8f: "\u008f", 0x90: "\u0090", 0x91: "\u2018", 0x92: "\u2019",
	0x93: "\u201c", 0x94: "\u201d", 0x95: "\u2022", 0x96: "\u2013", 0x97: "\u2014", 0x98: "\u02dc", 0x99: "\u2122",
	0x9a: "\u0161", 0x9b: "\u203a", 0x9c: "\u0153", 0x9d: "\u009d", 0x9e: "\u017e", 0x9f: "\u0178",
}

// pageInvalidCodepoint is html._invalid_codepoints.
func pageInvalidCodepoint(n int64) bool {
	switch {
	case n >= 0x1 && n <= 0x8, n == 0xb, n >= 0xe && n <= 0x1f, n >= 0x7f && n <= 0x9f, n >= 0xfdd0 && n <= 0xfdef:
		return true
	}
	return n <= 0x10ffff && n&0xfffe == 0xfffe
}

// pageUnescape is html.unescape.
func pageUnescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return pageCharrefRe.ReplaceAllStringFunc(s, func(m string) string {
		ref := m[1:]
		if ref[0] == '#' {
			digits, base := strings.TrimSuffix(ref[1:], ";"), 10
			if digits[0] == 'x' || digits[0] == 'X' {
				digits, base = digits[1:], 16
			}
			n, _ := new(big.Int).SetString(digits, base)
			if !n.IsInt64() || n.Int64() > 0x10ffff {
				return "�"
			}
			num := n.Int64()
			if r, ok := pageInvalidCharrefs[num]; ok {
				return r
			}
			if num >= 0xd800 && num <= 0xdfff {
				return "�"
			}
			if pageInvalidCodepoint(num) {
				return ""
			}
			return string(rune(num))
		}
		if v, ok := html.LookupEntity(ref); ok {
			return v
		}
		// The longest matching name, as the standard defines it.
		rs := []rune(ref)
		for x := len(rs) - 1; x > 1; x-- {
			if v, ok := html.LookupEntity(string(rs[:x])); ok {
				return v + string(rs[x:])
			}
		}
		return "&" + ref
	})
}

// pageAttr is one (name, value) pair from parse_starttag; a nil value is
// an attribute without "=".
type pageAttr struct {
	name  string
	value *string
}

// pageHTMLHandler receives HTMLParser's callbacks.
type pageHTMLHandler interface {
	startTag(tag string, attrs []pageAttr, selfClosing bool)
	endTag(tag string)
	data(s string)
	charref(name string)
	entityref(name string)
	comment(s string)
	decl(s string)
	pi(s string)
	unknownDecl(s string)
}

// pageHTMLParser is html.parser.HTMLParser over one string, fed once.
// Positions are code points, as Python indexes str.
type pageHTMLParser struct {
	raw             []rune
	convertCharrefs bool
	cdataElem       string
	h               pageHTMLHandler
}

func (p *pageHTMLParser) find(r rune, from int) int {
	for i := max(from, 0); i < len(p.raw); i++ {
		if p.raw[i] == r {
			return i
		}
	}
	return -1
}

func (p *pageHTMLParser) startsWith(s string, i int) bool {
	rs := []rune(s)
	if i < 0 || i+len(rs) > len(p.raw) {
		return false
	}
	return slices.Equal(p.raw[i:i+len(rs)], rs)
}

func (p *pageHTMLParser) text(i, j int) string { return string(p.raw[i:j]) }

// emit is handle_data for text between markup, unescaped when charrefs
// are converted (and outside script/style).
func (p *pageHTMLParser) emit(i, j int) {
	if p.convertCharrefs && p.cdataElem == "" {
		p.h.data(pageUnescape(p.text(i, j)))
	} else {
		p.h.data(p.text(i, j))
	}
}

// cdataEnd is the cdata-mode "interesting" regex, </\s*elem\s*> ignoring
// case: the start of the next match at or after i, or -1.
func (p *pageHTMLParser) cdataEnd(i int) int {
	elem := []rune(p.cdataElem)
	for s := i; s < len(p.raw); s++ {
		if p.raw[s] != '<' || s+1 >= len(p.raw) || p.raw[s+1] != '/' {
			continue
		}
		k := s + 2
		for k < len(p.raw) && pageIsSpace(p.raw[k]) {
			k++
		}
		if k+len(elem) > len(p.raw) || pageLower(string(p.raw[k:k+len(elem)])) != p.cdataElem {
			continue
		}
		k += len(elem)
		for k < len(p.raw) && pageIsSpace(p.raw[k]) {
			k++
		}
		if k < len(p.raw) && p.raw[k] == '>' {
			return s
		}
	}
	return -1
}

// goahead is HTMLParser.goahead; end is close()'s flush. It returns where
// it stopped: the unprocessed rest is what a later call would see.
func (p *pageHTMLParser) goahead(end bool) (int, error) {
	raw, n := p.raw, len(p.raw)
	i := 0
	for i < n {
		var j int
		if p.convertCharrefs && p.cdataElem == "" {
			j = p.find('<', i)
			if j < 0 {
				// A charref may be cut in half at the end: wait for more text.
				amp := -1
				for k := n - 1; k >= max(i, n-34); k-- {
					if raw[k] == '&' {
						amp = k
						break
					}
				}
				if amp >= 0 && !slices.ContainsFunc(raw[amp:], func(r rune) bool { return pageIsSpace(r) || r == ';' }) {
					break
				}
				j = n
			}
		} else if p.cdataElem != "" {
			j = p.cdataEnd(i)
			if j < 0 {
				break
			}
		} else {
			j = slices.IndexFunc(raw[i:], func(r rune) bool { return r == '&' || r == '<' })
			if j < 0 {
				j = n
			} else {
				j += i
			}
		}
		if i < j {
			p.emit(i, j)
		}
		i = j
		if i == n {
			break
		}
		switch {
		case raw[i] == '<':
			var (
				k   int
				err error
			)
			switch {
			case i+1 < n && pageIsASCIILetter(raw[i+1]):
				k = p.parseStartTag(i)
			case p.startsWith("</", i):
				k = p.parseEndTag(i)
			case p.startsWith("<!--", i):
				k = p.parseComment(i)
			case p.startsWith("<?", i):
				k = p.parsePI(i)
			case p.startsWith("<!", i):
				k, err = p.parseHTMLDeclaration(i)
				if err != nil {
					return 0, err
				}
			case i+1 < n:
				p.h.data("<")
				k = i + 1
			default:
				goto done
			}
			if k < 0 {
				if !end {
					goto done
				}
				k = p.find('>', i+1)
				if k < 0 {
					k = p.find('<', i+1)
					if k < 0 {
						k = i + 1
					}
				} else {
					k++
				}
				p.emit(i, k)
			}
			i = k
		case p.startsWith("&#", i):
			if name, k, ok := p.matchCharref(i); ok {
				p.h.charref(name)
				i = k
				continue
			}
			if slices.Contains(raw[i:], ';') { // bail by consuming &#
				p.h.data("&#")
				i += 2
			}
			goto done
		case raw[i] == '&':
			if name, k, ok := p.matchEntityref(i); ok {
				p.h.entityref(name)
				i = k
				continue
			}
			if i+1 < n && (pageIsASCIILetter(raw[i+1]) || raw[i+1] == '#') { // incomplete
				if end && i+2 == n {
					i++
				}
				goto done
			}
			if i+1 < n {
				p.h.data("&")
				i++
			} else {
				goto done
			}
		}
	}
done:
	if end && i < n && p.cdataElem == "" {
		p.emit(i, n)
		i = n
	}
	return i, nil
}

// matchCharref is the charref regex, &#(?:[0-9]+|[xX][0-9a-fA-F]+)[^0-9a-fA-F],
// at i: the reference's name and where parsing resumes.
func (p *pageHTMLParser) matchCharref(i int) (string, int, bool) {
	raw, n := p.raw, len(p.raw)
	k := i + 2
	var isDigit func(rune) bool
	switch {
	case k < n && raw[k] >= '0' && raw[k] <= '9':
		isDigit = func(r rune) bool { return r >= '0' && r <= '9' }
	case k+1 < n && (raw[k] == 'x' || raw[k] == 'X') && pageIsHex(raw[k+1]):
		k++
		isDigit = pageIsHex
	default:
		return "", 0, false
	}
	for k < n && isDigit(raw[k]) {
		k++
	}
	if k >= n || pageIsHex(raw[k]) {
		return "", 0, false
	}
	name := p.text(i+2, k)
	if raw[k] == ';' {
		k++
	}
	return name, k, true
}

// matchEntityref is the entityref regex, &([a-zA-Z][-.a-zA-Z0-9]*)[^a-zA-Z0-9],
// at i, backtracking over trailing "-" and ".".
func (p *pageHTMLParser) matchEntityref(i int) (string, int, bool) {
	raw, n := p.raw, len(p.raw)
	if i+1 >= n || !pageIsASCIILetter(raw[i+1]) {
		return "", 0, false
	}
	e := i + 2
	for e < n && (pageIsASCIIAlnum(raw[e]) || raw[e] == '-' || raw[e] == '.') {
		e++
	}
	for ; e > i+1; e-- {
		if e < n && !pageIsASCIIAlnum(raw[e]) {
			k := e
			if raw[e] == ';' {
				k++
			}
			return p.text(i+1, e), k, true
		}
	}
	return "", 0, false
}

// tagNameEnd scans [^\t\n\r\f />\x00]* from i.
func (p *pageHTMLParser) tagNameEnd(i int) int {
	for i < len(p.raw) && !strings.ContainsRune("\t\n\r\f />\x00", p.raw[i]) {
		i++
	}
	return i
}

// skipAttrSep scans (?:\s|/(?!>))* from i.
func (p *pageHTMLParser) skipAttrSep(i int) int {
	for i < len(p.raw) && (pageIsSpace(p.raw[i]) || p.raw[i] == '/' && !(i+1 < len(p.raw) && p.raw[i+1] == '>')) {
		i++
	}
	return i
}

func (p *pageHTMLParser) skipSpace(i int) int {
	for i < len(p.raw) && pageIsSpace(p.raw[i]) {
		i++
	}
	return i
}

// attrNameAt matches (?<=['"\s/])[^\s/>][^\s/=>]* at i: its end, or -1.
func (p *pageHTMLParser) attrNameAt(i int) int {
	raw, n := p.raw, len(p.raw)
	if i == 0 || i >= n {
		return -1
	}
	if prev := raw[i-1]; prev != '\'' && prev != '"' && prev != '/' && !pageIsSpace(prev) {
		return -1
	}
	if c := raw[i]; pageIsSpace(c) || c == '/' || c == '>' {
		return -1
	}
	i++
	for i < n && !pageIsSpace(raw[i]) && !strings.ContainsRune("/=>", raw[i]) {
		i++
	}
	return i
}

// attrValueAt matches \s*=+\s*('[^']*'|"[^"]*"|(?!['"])[^>\s]*) at i, with
// the regex's backtracking: the end of the match, the value's span, and
// whether it matched.
func (p *pageHTMLParser) attrValueAt(i int) (end, vs, ve int, ok bool) {
	raw, n := p.raw, len(p.raw)
	k := p.skipSpace(i)
	eqs := 0
	for k+eqs < n && raw[k+eqs] == '=' {
		eqs++
	}
	for e := eqs; e >= 1; e-- {
		q0 := k + e
		ws := p.skipSpace(q0) - q0
		for w := ws; w >= 0; w-- {
			q := q0 + w
			switch {
			case q < n && (raw[q] == '\'' || raw[q] == '"'):
				if c := p.find(raw[q], q+1); c >= 0 {
					return c + 1, q, c + 1, true
				}
			default:
				v := q
				for v < n && raw[v] != '>' && !pageIsSpace(raw[v]) {
					v++
				}
				return v, q, v, true
			}
		}
	}
	return 0, 0, 0, false
}

// locateStartTagEnd is locatestarttagend_tolerant.match(rawdata, i).end().
func (p *pageHTMLParser) locateStartTagEnd(i int) int {
	raw, n := p.raw, len(p.raw)
	k := p.tagNameEnd(i + 2)
	for k < n && (pageIsSpace(raw[k]) || raw[k] == '/') {
		k++
	}
	for {
		a := p.attrNameAt(k)
		if a < 0 {
			break
		}
		if end, _, _, ok := p.attrValueAt(a); ok {
			a = p.skipSpace(end)
		}
		k = p.skipAttrSep(a)
	}
	return p.skipSpace(k)
}

// checkForWholeStartTag is HTMLParser.check_for_whole_start_tag.
func (p *pageHTMLParser) checkForWholeStartTag(i int) int {
	j := p.locateStartTagEnd(i)
	if j >= len(p.raw) {
		return -1
	}
	switch next := p.raw[j]; {
	case next == '>':
		return j + 1
	case next == '/':
		if p.startsWith("/>", j) {
			return j + 2
		}
		return -1 // "/" at the buffer's end
	case pageIsASCIILetter(next) || next == '=':
		return -1
	}
	if j > i {
		return j
	}
	return i + 1
}

// parseStartTag is HTMLParser.parse_starttag.
func (p *pageHTMLParser) parseStartTag(i int) int {
	endpos := p.checkForWholeStartTag(i)
	if endpos < 0 {
		return endpos
	}
	nameEnd := p.tagNameEnd(i + 2)
	tag := pageLower(p.text(i+1, nameEnd))
	k := p.skipAttrSep(nameEnd)
	var attrs []pageAttr
	for k < endpos {
		a := p.attrNameAt(k)
		if a < 0 {
			break
		}
		attr := pageAttr{name: pageLower(p.text(k, a))}
		if end, vs, ve, ok := p.attrValueAt(a); ok {
			v := p.raw[vs:ve]
			if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
				v = v[1 : len(v)-1]
			}
			s := string(v)
			if s != "" {
				s = pageUnescape(s)
			}
			attr.value = &s
			a = end
		}
		attrs = append(attrs, attr)
		k = p.skipAttrSep(a)
	}
	switch rest := string(pageStrip(p.raw[min(k, endpos):endpos])); rest {
	case ">":
		p.h.startTag(tag, attrs, false)
		if tag == "script" || tag == "style" {
			p.cdataElem = tag
		}
	case "/>":
		p.h.startTag(tag, attrs, true)
	default:
		p.h.data(p.text(i, endpos))
	}
	return endpos
}

// parseEndTag is HTMLParser.parse_endtag.
func (p *pageHTMLParser) parseEndTag(i int) int {
	raw, n := p.raw, len(p.raw)
	gt := p.find('>', i+1)
	if gt < 0 {
		return -1
	}
	gtpos := gt + 1
	// endtagfind: </\s*([a-zA-Z][-.a-zA-Z0-9:_]*)\s*>
	k := p.skipSpace(i + 2)
	elem := ""
	if k < n && pageIsASCIILetter(raw[k]) {
		s := k
		for k < n && (pageIsASCIIAlnum(raw[k]) || strings.ContainsRune("-.:_", raw[k])) {
			k++
		}
		e := k
		k = p.skipSpace(k)
		if k < n && raw[k] == '>' {
			elem = pageLower(p.text(s, e))
		}
	}
	if elem == "" {
		if p.cdataElem != "" {
			p.h.data(p.text(i, gtpos))
			return gtpos
		}
		if i+2 >= n || !pageIsASCIILetter(raw[i+2]) {
			if p.startsWith("</>", i) {
				return i + 3
			}
			return p.parseBogusComment(i)
		}
		nameEnd := p.tagNameEnd(i + 3)
		p.h.endTag(pageLower(p.text(i+2, nameEnd)))
		return p.find('>', p.skipAttrSep(nameEnd)) + 1
	}
	if p.cdataElem != "" && elem != p.cdataElem {
		p.h.data(p.text(i, gtpos))
		return gtpos
	}
	p.h.endTag(elem)
	p.cdataElem = ""
	return gtpos
}

// parseComment is ParserBase.parse_comment: up to --\s*>.
func (p *pageHTMLParser) parseComment(i int) int {
	for s := i + 4; s+1 < len(p.raw); s++ {
		if p.raw[s] != '-' || p.raw[s+1] != '-' {
			continue
		}
		k := p.skipSpace(s + 2)
		if k < len(p.raw) && p.raw[k] == '>' {
			p.h.comment(p.text(i+4, s))
			return k + 1
		}
	}
	return -1
}

func (p *pageHTMLParser) parsePI(i int) int {
	j := p.find('>', i+2)
	if j < 0 {
		return -1
	}
	p.h.pi(p.text(i+2, j))
	return j + 1
}

func (p *pageHTMLParser) parseBogusComment(i int) int {
	pos := p.find('>', i+2)
	if pos < 0 {
		return -1
	}
	p.h.comment(p.text(i+2, pos))
	return pos + 1
}

// parseHTMLDeclaration is HTMLParser.parse_html_declaration.
func (p *pageHTMLParser) parseHTMLDeclaration(i int) (int, error) {
	switch {
	case p.startsWith("<![", i):
		return p.parseMarkedSection(i)
	case i+9 <= len(p.raw) && pageLower(p.text(i, i+9)) == "<!doctype":
		gt := p.find('>', i+9)
		if gt < 0 {
			return -1, nil
		}
		p.h.decl(p.text(i+2, gt))
		return gt + 1, nil
	}
	return p.parseBogusComment(i), nil
}

// parseMarkedSection is ParserBase.parse_marked_section.
func (p *pageHTMLParser) parseMarkedSection(i int) (int, error) {
	raw, n := p.raw, len(p.raw)
	s := i + 3
	if s == n {
		return -1, nil
	}
	if !pageIsASCIILetter(raw[s]) {
		return 0, errPageMarkup
	}
	e := s + 1
	for e < n && (pageIsASCIIAlnum(raw[e]) || strings.ContainsRune("-_.", raw[e])) {
		e++
	}
	name := pageLower(p.text(s, e))
	if p.skipSpace(e) == n {
		return -1, nil
	}
	var closeAt func(int) (int, int)
	switch name {
	case "temp", "cdata", "ignore", "include", "rcdata":
		closeAt = func(k int) (int, int) { // ]\s*]\s*>
			k = p.skipSpace(k + 1)
			if k < n && raw[k] == ']' {
				k = p.skipSpace(k + 1)
				if k < n && raw[k] == '>' {
					return k + 1, 0
				}
			}
			return -1, 0
		}
	case "if", "else", "endif":
		closeAt = func(k int) (int, int) { // ]\s*>
			k = p.skipSpace(k + 1)
			if k < n && raw[k] == '>' {
				return k + 1, 0
			}
			return -1, 0
		}
	default:
		return 0, errPageMarkup
	}
	for k := s; k < n; k++ {
		if raw[k] != ']' {
			continue
		}
		if end, _ := closeAt(k); end >= 0 {
			p.h.unknownDecl(p.text(s, k))
			return end, nil
		}
	}
	return -1, nil
}

// pageTextCollector is plane.utils.html_processor.MLStripper.
type pageTextCollector struct{ b strings.Builder }

func (t *pageTextCollector) startTag(string, []pageAttr, bool) {}
func (t *pageTextCollector) endTag(string)                     {}
func (t *pageTextCollector) data(s string)                     { t.b.WriteString(s) }
func (t *pageTextCollector) charref(string)                    {}
func (t *pageTextCollector) entityref(string)                  {}
func (t *pageTextCollector) comment(string)                    {}
func (t *pageTextCollector) decl(string)                       {}
func (t *pageTextCollector) pi(string)                         {}
func (t *pageTextCollector) unknownDecl(string)                {}

// pageStripTags is Page.save()'s description_stripped: None for an empty
// description, else strip_tags (an HTMLParser fed once and never closed,
// so text after the last complete construct can be held back).
func pageStripTags(src string) (*string, error) {
	if src == "" {
		return nil, nil
	}
	var t pageTextCollector
	p := &pageHTMLParser{raw: []rune(src), convertCharrefs: true, h: &t}
	if _, err := p.goahead(false); err != nil {
		return nil, err
	}
	s := t.b.String()
	return &s, nil
}

// BeautifulSoup(markup, "html.parser").

const (
	pageSoupElement = iota
	pageSoupText
	pageSoupComment
	pageSoupDoctype
	pageSoupCData
	pageSoupPI
	pageSoupDeclaration
)

type pageSoupNode struct {
	kind     int
	name     string
	attrs    []pageSoupAttr
	children []*pageSoupNode
	text     string
}

type pageSoupAttr struct{ name, value string }

func (n *pageSoupNode) get(name string) (string, bool) {
	for _, a := range n.attrs {
		if a.name == name {
			return a.value, true
		}
	}
	return "", false
}

// pageSoupVoid is HTMLTreeBuilder.empty_element_tags.
var pageSoupVoid = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true,
	"keygen": true, "link": true, "menuitem": true, "meta": true, "param": true, "source": true, "track": true,
	"wbr": true, "basefont": true, "bgsound": true, "command": true, "frame": true, "image": true, "isindex": true,
	"nextid": true, "spacer": true,
}

// pageSoupListAttr reports HTMLTreeBuilder.DEFAULT_CDATA_LIST_ATTRIBUTES:
// values split on whitespace, re-joined with single spaces on output.
func pageSoupListAttr(tag, attr string) bool {
	switch attr {
	case "class", "accesskey", "dropzone":
		return true
	}
	switch tag {
	case "a", "link":
		return attr == "rel" || attr == "rev"
	case "td", "th":
		return attr == "headers"
	case "form":
		return attr == "accept-charset"
	case "object":
		return attr == "archive"
	case "area":
		return attr == "rel"
	case "icon":
		return attr == "sizes"
	case "iframe":
		return attr == "sandbox"
	case "output":
		return attr == "for"
	}
	return false
}

// pageSoupDoc is a BeautifulSoup object fed through BeautifulSoupHTMLParser.
type pageSoupDoc struct {
	root          *pageSoupNode
	stack         []*pageSoupNode
	openCount     map[string]int
	preserve      int // open pre/textarea tags
	current       []string
	alreadyClosed []string
	// surrogate marks a lone surrogate from a numeric charref: Python
	// builds the string, then fails to encode it when saving.
	surrogate bool
}

func pageSoup(src string) (*pageSoupDoc, error) {
	s := &pageSoupDoc{root: &pageSoupNode{kind: pageSoupElement, name: "[document]"}, openCount: map[string]int{}}
	s.stack = []*pageSoupNode{s.root}
	// feed() then close(): close() resumes on whatever feed() left.
	p := &pageHTMLParser{raw: []rune(src), h: s}
	i, err := p.goahead(false)
	if err != nil {
		return nil, err
	}
	p.raw = p.raw[i:]
	if _, err := p.goahead(true); err != nil {
		return nil, err
	}
	s.endData(pageSoupText)
	return s, nil
}

func (s *pageSoupDoc) top() *pageSoupNode { return s.stack[len(s.stack)-1] }

func (s *pageSoupDoc) endData(kind int) {
	if len(s.current) == 0 {
		return
	}
	data := strings.Join(s.current, "")
	s.current = nil
	if s.preserve == 0 && strings.Trim(data, " \n\t\f\r") == "" {
		if strings.Contains(data, "\n") {
			data = "\n"
		} else {
			data = " "
		}
	}
	parent := s.top()
	parent.children = append(parent.children, &pageSoupNode{kind: kind, text: data})
}

func (s *pageSoupDoc) startTag(name string, attrs []pageAttr, selfClosing bool) {
	var list []pageSoupAttr
	for _, a := range attrs {
		v := ""
		if a.value != nil {
			v = *a.value
		}
		if i := slices.IndexFunc(list, func(x pageSoupAttr) bool { return x.name == a.name }); i >= 0 {
			list[i].value = v // on_duplicate_attribute=REPLACE
			continue
		}
		list = append(list, pageSoupAttr{a.name, v})
	}
	for i, a := range list {
		if pageSoupListAttr(name, a.name) {
			list[i].value = strings.Join(strings.FieldsFunc(a.value, pageIsSpace), " ")
		}
	}
	s.endData(pageSoupText)
	tag := &pageSoupNode{kind: pageSoupElement, name: name, attrs: list}
	parent := s.top()
	parent.children = append(parent.children, tag)
	s.stack = append(s.stack, tag)
	s.openCount[name]++
	if name == "pre" || name == "textarea" {
		s.preserve++
	}
	if selfClosing {
		s.endTag(name)
		return
	}
	if pageSoupVoid[name] {
		s.soupEndTag(name)
		s.alreadyClosed = append(s.alreadyClosed, name)
	}
}

func (s *pageSoupDoc) endTag(name string) {
	if i := slices.Index(s.alreadyClosed, name); i >= 0 {
		s.alreadyClosed = slices.Delete(s.alreadyClosed, i, i+1)
		return
	}
	s.soupEndTag(name)
}

// soupEndTag is BeautifulSoup.handle_endtag: pop up to and including the
// most recent open tag of that name, if any.
func (s *pageSoupDoc) soupEndTag(name string) {
	s.endData(pageSoupText)
	for len(s.stack) > 1 && s.openCount[name] > 0 {
		t := s.stack[len(s.stack)-1]
		s.stack = s.stack[:len(s.stack)-1]
		s.openCount[t.name]--
		if t.name == "pre" || t.name == "textarea" {
			s.preserve--
		}
		if t.name == name {
			break
		}
	}
}

func (s *pageSoupDoc) data(d string) { s.current = append(s.current, d) }

// pageWindows1252 maps the bytes 0x80-0x9F that windows-1252 defines.
var pageWindows1252 = map[int64]rune{
	0x80: '\u20ac', 0x82: '\u201a', 0x83: '\u0192', 0x84: '\u201e', 0x85: '\u2026', 0x86: '\u2020', 0x87: '\u2021',
	0x88: '\u02c6', 0x89: '\u2030', 0x8a: '\u0160', 0x8b: '\u2039', 0x8c: '\u0152', 0x8e: '\u017d', 0x91: '\u2018',
	0x92: '\u2019', 0x93: '\u201c', 0x94: '\u201d', 0x95: '\u2022', 0x96: '\u2013', 0x97: '\u2014', 0x98: '\u02dc',
	0x99: '\u2122', 0x9a: '\u0161', 0x9b: '\u203a', 0x9c: '\u0153', 0x9e: '\u017e', 0x9f: '\u0178',
}

// charref is BeautifulSoupHTMLParser.handle_charref.
func (s *pageSoupDoc) charref(name string) {
	digits, base := name, 10
	if name[0] == 'x' || name[0] == 'X' {
		digits, base = strings.TrimLeft(name, name[:1]), 16
	}
	n, _ := new(big.Int).SetString(digits, base)
	switch {
	case !n.IsInt64() || n.Int64() > 0x10ffff:
		s.data("�")
	case n.Int64() >= 0x80 && n.Int64() <= 0x9f:
		if r, ok := pageWindows1252[n.Int64()]; ok {
			s.data(string(r))
		} else {
			s.data(string(rune(n.Int64())))
		}
	case n.Int64() >= 0xd800 && n.Int64() <= 0xdfff:
		s.surrogate = true
		s.data("�")
	default:
		s.data(string(rune(n.Int64())))
	}
}

// entityref is BeautifulSoupHTMLParser.handle_entityref: a known name
// (with or without its semicolon), else the text "&name".
func (s *pageSoupDoc) entityref(name string) {
	if v, ok := html.LookupEntity(name + ";"); ok {
		s.data(v)
	} else if v, ok := html.LookupEntity(name); ok {
		s.data(v)
	} else {
		s.data("&" + name)
	}
}

func (s *pageSoupDoc) special(kind int, d string) {
	s.endData(pageSoupText)
	s.data(d)
	s.endData(kind)
}

func (s *pageSoupDoc) comment(d string) { s.special(pageSoupComment, d) }

func (s *pageSoupDoc) decl(d string) {
	if r := []rune(d); len(r) > len("DOCTYPE ") {
		d = string(r[len("DOCTYPE "):])
	} else {
		d = ""
	}
	s.special(pageSoupDoctype, d)
}

func (s *pageSoupDoc) pi(d string) { s.special(pageSoupPI, d) }

func (s *pageSoupDoc) unknownDecl(d string) {
	if strings.HasPrefix(strings.ToUpper(d), "CDATA[") {
		s.special(pageSoupCData, d[len("CDATA["):])
		return
	}
	s.special(pageSoupDeclaration, d)
}

// findAll is soup.find_all(name): matching elements in document order.
func (s *pageSoupDoc) findAll(name string) []*pageSoupNode {
	var out []*pageSoupNode
	var walk func(*pageSoupNode)
	walk = func(n *pageSoupNode) {
		for _, c := range n.children {
			if c.kind != pageSoupElement {
				continue
			}
			if c.name == name {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(s.root)
	return out
}

// pageSoupEscape is EntitySubstitution.substitute_xml (the "minimal"
// formatter).
var pageSoupEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

// String is str(soup).
func (s *pageSoupDoc) String() string {
	var b strings.Builder
	var write func(n *pageSoupNode, parent string)
	write = func(n *pageSoupNode, parent string) {
		switch n.kind {
		case pageSoupText:
			if parent == "script" || parent == "style" {
				b.WriteString(n.text)
			} else {
				b.WriteString(pageSoupEscape(n.text))
			}
		case pageSoupComment:
			b.WriteString("<!--" + n.text + "-->")
		case pageSoupDoctype:
			b.WriteString("<!DOCTYPE " + n.text + ">\n")
		case pageSoupCData:
			b.WriteString("<![CDATA[" + n.text + "]]>")
		case pageSoupPI:
			b.WriteString("<?" + n.text + ">")
		case pageSoupDeclaration:
			b.WriteString("<?" + n.text + "?>")
		case pageSoupElement:
			b.WriteString("<" + n.name)
			attrs := slices.Clone(n.attrs)
			slices.SortFunc(attrs, func(x, y pageSoupAttr) int { return strings.Compare(x.name, y.name) })
			for _, a := range attrs {
				v := pageSoupEscape(a.value)
				quote := `"`
				if strings.Contains(v, `"`) {
					if strings.Contains(v, "'") {
						v = strings.ReplaceAll(v, `"`, "&quot;")
					} else {
						quote = "'"
					}
				}
				b.WriteString(" " + a.name + "=" + quote + v + quote)
			}
			if len(n.children) == 0 && pageSoupVoid[n.name] {
				b.WriteString("/>")
				return
			}
			b.WriteString(">")
			for _, c := range n.children {
				write(c, n.name)
			}
			b.WriteString("</" + n.name + ">")
		}
	}
	for _, c := range s.root.children {
		write(c, s.root.name)
	}
	return b.String()
}
