# Generates internal/api/testdata/page_html.json: inputs and what the page
# code paths make of them with Python's html.parser: Page.save()'s
# strip_tags, and BeautifulSoup(html, "html.parser") as the page tasks use
# it (find_all of the editor components, and str(soup) for page duplicates).
# Run inside the reference stack:
#   scripts/devstack.sh N shell < contract/reference/gen_page_html_fixtures.py \
#     | sed -n 's/^FIXTURES//p' > internal/api/testdata/page_html.json
import json
from bs4 import BeautifulSoup
from plane.utils.html_processor import strip_tags

cases = [
    "",
    "<p></p>",
    "<p>Hello <b>world</b></p>",
    '<p>Hi <mention-component id="m1" entity_identifier="u1" entity_name="user_mention"></mention-component></p>',
    '<image-component id="i1" src="a" width="35%"></image-component><image-component src=""></image-component>',
    "<p>a<script>if (a<b && c>d) x()</script> &amp; b &bogus; c&nbsp;d</p><!-- note --><style>p{}</style>",
    '<p title="&lt;t&gt;">e</p>&amp',
    "text &amp",
    "text &amp; more",
    "x &ampy &amp-x &copy &#169; &#x41; &#128; &#129; &#0; &#1; &#1114112; &#xD800; &#12a; &#x; &#;",
    "<pre>  x  </pre>\n\n<p> </p>  <p>y</p>",
    "<textarea>\n \n</textarea> \n ",
    "<BR></br><Img SRC=x /><br><br/>x<p>y",
    "<unclosed><b>bold",
    "<x></p></x>q",
    "<!----><!-- a --><!--b--  ><!DOCTYPE html><![CDATA[x]]><?php x ?><!bogus>",
    '<p class="  a   b ">x</p><p class="">x</p><a rel=" n  f " href="x?a=1&b=2">t</a>',
    '<p a="x" A="y" b=\'q"\' c="q\'" d="&quot;\'">',
    '<p a=="x">', '<p a= "x>y', "<p a='x>y</p>", "<p a = b c=d e>t</p>", "<p/a/b=c/>", "<p\x00>t",
    "<a b\x00c>t</a>", '<mention-component id=x entity_name=user_mention entity_identifier=u2 />after',
    "<script>never closed", "<style>a</style >b</STYLE>c", "<script></script x>y</script>z",
    "</>a</ b>c</3>d", "<a <b>c</b>", "a < b > c", "a <", "<p", "<p a", "<p a=", "<p a='x",
    "<![x]>", "<![if x]>y<![endif]>", "&#", "&#;x", "a &# b", "&", "&a", "&#x41", "x&nbsp", "&nbsp;&nbspx&notin;&notit;",
    "İ <DIVİ>x</DIVİ>", "<p> </p>   ",
    '<mention-component id="m1" entity_name="issue" entity_identifier="e"></mention-component><mention-component id="m1"></mention-component>',
    "<p>" + "x" * 40 + " &am",
    "<p>" + "x" * 40 + "&am",
]

out = []
for html in cases:
    case = {"html": html}
    try:
        case["stripped"] = strip_tags(html)
    except Exception as e:
        case["stripped_error"] = type(e).__name__
    try:
        soup = BeautifulSoup(html, "html.parser")
        text = str(soup)
        # A lone surrogate (from "&#xD800;") cannot be saved; Go marks it.
        if any(0xD800 <= ord(c) <= 0xDFFF for c in text):
            case["surrogate"] = True
            text = "".join("\ufffd" if 0xD800 <= ord(c) <= 0xDFFF else c for c in text)
        case["soup"] = text
        case["components"] = {
            c: [{k: t.get(k) for k in ("id", "src", "entity_identifier", "entity_name")} for t in soup.find_all(c)]
            for c in ("mention-component", "image-component")
        }
    except Exception as e:
        case["soup_error"] = type(e).__name__
    out.append(case)
print("FIXTURES" + json.dumps(out, ensure_ascii=False))
