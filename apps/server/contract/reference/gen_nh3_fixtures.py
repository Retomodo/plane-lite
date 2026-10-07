# Generates internal/sanitize/testdata/nh3.json: inputs and what
# plane.utils.content_validator.validate_html_content (nh3/ammonia) makes of
# them. Run inside the reference stack:
#   docker compose -f docker-compose.dev.yml exec -T reference python manage.py shell \
#     < contract/reference/gen_nh3_fixtures.py | sed -n 's/^FIXTURES//p' > internal/sanitize/testdata/nh3.json
import json
from plane.utils.content_validator import validate_html_content
from plane.utils.html_processor import strip_tags

cases = [
    # Plane editor output
    "<p></p>",
    "<p>Hello world</p>",
    "<p>Fix the <strong>login</strong> bug &amp; <em>tidy</em> up</p>",
    '<p><a target="_blank" rel="noopener noreferrer nofollow" class="text-custom-primary-300 underline" href="https://example.com">link</a></p>',
    '<mention-component entity_identifier="0b6d8f0e-1c51-4c55-9a4e-6f3f1d6c3d01" entity_name="user_mention" id="x"></mention-component>',
    '<image-component src="0b6d8f0e1c514c559a4e6f3f1d6c3d01.png" width="35%" height="auto" aspectratio="1.5" alignment="left" id="i1"></image-component>',
    '<ul data-type="taskList"><li data-checked="true" data-type="taskItem"><label><input type="checkbox" checked="checked"><span></span></label><div><p>done</p></div></li></ul>',
    '<pre><code class="language-go" language="go">func main() {\n\tfmt.Println("hi &lt;3")\n}</code></pre>',
    "<pre>\nleading newline</pre>",
    "<pre>\n\ntwo newlines</pre>",
    "<textarea>\nx</textarea>",
    '<table><tbody><tr><th colspan="1" rowspan="1" colwidth="150"><p>H</p></th></tr><tr><td colspan="1" rowspan="1" background="#fff" textcolor="red" style="color: red"><p>c</p></td></tr></tbody></table>',
    '<div data-block-type="callout-component" data-icon-name="info" data-icon-color="#fff" data-background="gray" data-logo-in-use="emoji" data-emoji-unicode="128161" data-emoji-url="https://x/y.png"><p>note</p></div>',
    '<blockquote><p>quote</p></blockquote><hr><h1>Title</h1><h6>small</h6>',
    '<p style="text-align: center" class="editor-paragraph-block">centered</p>',
    '<span data-text-color="peach" data-background-color="gray" style="color: var(--x)">colored</span>',
    "<ol start=\"3\"><li><p>three</p></li></ol>",
    "<p>a<br>b<br/>c</p>",
    '<img src="https://example.com/a.png" alt="a" width="10" onerror="alert(1)">',
    # Attributes and URLs
    '<a href="javascript:alert(1)">x</a>',
    '<a href="JaVaScRiPt:alert(1)">x</a>',
    '<a href="  javascript:alert(1)">x</a>',
    '<a href="mailto:a@b.com">m</a><a href="tel:+1">t</a><a href="ftp://x">f</a>',
    '<a href="/relative/path">r</a><a href="#frag">h</a><a href="?q=1">q</a><a href="//evil.com/x">p</a>',
    '<a href="data:text/html,<script>alert(1)</script>">d</a>',
    '<a href="http://example.com/?a=1&b=2">amp</a>',
    '<a href="https://example.com" rel="nofollow">rel</a>',
    "<a>no href</a>",
    '<a href="https://ex.com" title="t" id="i" class="c" data-x="1" onclick="x()">attrs</a>',
    '<img src="javascript:alert(1)">',
    '<img src="data:image/png;base64,AAAA">',
    '<p id="a" ID="b" Class="C">dupe attrs</p>',
    '<p title=\'single "quoted"\'>q</p>',
    '<p title="a&nbsp;b &lt; &gt; &amp;  ">entities</p>',
    '<input type="text" value="v" checked><input type="checkbox" disabled>',
    '<label for="x">lab</label>',
    '<td background="x">orphan cell</td>',
    '<div xmlns="http://www.w3.org/1999/xhtml" role="note" aria-label="l" aria-hidden="true" start="1" type="t">generic</div>',
    '<code spellcheck="false" language="py">x</code>',
    # Dropped elements
    "<script>alert(1)</script><p>after</p>",
    "<style>p{color:red}</style><p>styled</p>",
    "<p>a<!-- comment -->b</p>",
    '<iframe src="https://evil.com"></iframe><p>x</p>',
    "<form action=\"/x\"><button>b</button><select><option>o</option></select></form>",
    "<custom-thing>inner <b>bold</b></custom-thing>",
    "<svg><circle r=\"1\"></circle><text>svg text</text></svg>",
    "<math><mi>x</mi></math>",
    "<object data=\"x.swf\">fallback</object>",
    "<noscript><p>ns</p></noscript>",
    "<template><p>tpl</p></template>",
    "<title>t</title><meta charset=\"utf-8\"><link rel=\"x\">",
    "<html><head><title>T</title></head><body><p>body</p></body></html>",
    "<body onload=\"x\"><p>b</p></body>",
    "<xmp><b>raw</b></xmp>",
    "<plaintext><b>rest",
    # Text and escaping
    "plain text",
    "a < b > c & d",
    "&lt;script&gt;alert(1)&lt;/script&gt;",
    "&amp;amp; &copy; &#169; &#x41; &unknown; &",
    "non breaking",
    "quotes \" and ' here",
    "emoji 😀 and accents é",
    "tab\there\r\nnewline",
    "\u0000null",
    "  leading and trailing  ",
    "<p>unclosed <b>bold <i>italic</p>",
    "</p>stray end",
    "<b><p>misnested</b></p>",
    "<table><p>foster</p><tr><td>c</td></tr></table>",
    "<li>orphan li</li>",
    "<p><div>block in p</div></p>",
    "<a href=\"https://a.com\"><a href=\"https://b.com\">nested</a></a>",
    "<p>a</p\n>b",
    "<P CLASS=\"x\">UPPER</P>",
    "<p class=x>unquoted</p>",
    "<p class>empty attr</p>",
    "<br>",
    "<img>",
    "<wbr><hr/>",
    "<details><summary>s</summary><p>d</p></details>",
    "<ruby>漢<rt>kan</rt></ruby>",
    "<del>d</del><ins>i</ins><s>s</s><u>u</u><mark>m</mark><sub>1</sub><sup>2</sup><kbd>k</kbd>",
    "<time datetime=\"2026\">t</time><data value=\"1\">d</data><abbr title=\"a\">ab</abbr>",
    "<p>" + "x" * 50 + "</p>",
    "<div><div><div><p>deep</p></div></div></div>",
    "<image-component src=\"https://evil.com/x\" onload=\"x\"></image-component>",
    "<mention-component entity_identifier=\"u\" entity_name=\"user_mention\" class=\"m\" style=\"x\"></mention-component> text",
    "<p>caf\u00e9 &amp; <b>bar</b>\u00a0baz &lt;3</p>",
]
# URL handling (the url crate's WHATWG parser decides relative/valid).
for href in ["", " ", "http://", "https://", "http:example.com", "http:/example.com", "http:\\\\example.com",
        "https://user:pw@example.com/", "https://@example.com", "https://example.com:8080/x", "https://example.com:99999",
        "https://example.com:8a", "https://exa mple.com", "https://exa%20mple.com", "https://ex%41mple.com",
        "https://[::1]/", "https://[::1", "https://1.2.3.4/", "https://1.2.3.256/", "https://0x7f.1/", "https://1.2.3.4.5/",
        "https://bücher.de/", "https://xn--bcher-kva.de/", "https://a..b/", "https://-a-.com/", "https://a_b.com/",
        "https://example.com/pa th?q=a b#f g", "HTTPS://EXAMPLE.COM", "mailto:", "mailto://x y", "tel:+1 555",
        "java\tscript:alert(1)", "\x01javascript:alert(1)", "1http://x", "a+b.c-d:x", "./x", "x:", "c:\\path",
        "https://%zz.com/", "https://example.com.", "https://12345/", "https://0/", "https://999999999999/",
        "https:///example.com", "https://ex\u200dample.com/", "https://例え.テスト/", "wss://x", "file:///etc/passwd"]:
    cases.append('<a href="%s">x</a>' % href.replace('"', "&quot;"))
    cases.append('<img src="%s">' % href.replace('"', "&quot;"))
# Seeded random fragments: nesting, misnesting, foreign content, entities.
import random
rng = random.Random(7)
TAGS = ["p", "b", "i", "a", "code", "em", "strong", "s", "u", "div", "span", "table", "tr", "td", "th", "tbody",
    "ul", "ol", "li", "pre", "br", "img", "input", "label", "svg", "math", "mi", "foreignObject", "title", "style",
    "script", "textarea", "select", "option", "font", "h1", "blockquote", "mention-component", "image-component",
    "template", "noscript", "iframe", "xmp", "nobr", "button", "form", "hr", "caption", "colgroup", "col", "dl", "dd"]
ATTRS = ["class", "id", "href", "src", "style", "title", "onclick", "data-type", "target", "rel", "colspan",
    "entity_name", "checked", "type", "width", "xlink:href", "language", "TITLE"]
VALUES = ["x", "https://e.com/a?b=1&c=2", "javascript:alert(1)", "/rel", "a\"b", "a'b", "\u00a0", "&amp;", "&lt;b&gt;", ""]
TEXT = ["hi", "a & b", "<", ">", "&nbsp;", "\u00a0", "&copy", "x\ny", " ", "\u00e9", "&#0;", "]]>"]
def frag(depth):
    out = []
    for _ in range(rng.randint(1, 4)):
        r = rng.random()
        if r < 0.3 or depth > 4:
            out.append(rng.choice(TEXT))
        elif r < 0.4:
            out.append("</%s>" % rng.choice(TAGS))
        else:
            t = rng.choice(TAGS)
            attrs = "".join(' %s="%s"' % (rng.choice(ATTRS), rng.choice(VALUES)) for _ in range(rng.randint(0, 3)))
            close = "</%s>" % t if rng.random() < 0.8 else ""
            out.append("<%s%s>%s%s" % (t, attrs, frag(depth + 1), close))
    return "".join(out)
for _ in range(400):
    cases.append(frag(0))
out = []
for c in cases:
    ok, err, clean = validate_html_content(c)
    out.append([c, clean, None if clean in ("", None) else strip_tags(clean)])
print("FIXTURES" + json.dumps(out, ensure_ascii=False))
