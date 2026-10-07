package sanitize

import (
	"strings"

	html "plane-lite/server/internal/sanitize/html5"
)

// Mentions is notification_task.extract_mentions on an HTML string:
// BeautifulSoup(html, "html.parser").find_all("mention-component",
// attrs={"entity_name": "user_mention"}), each tag's entity_identifier, in
// document order. ok is false when a matching tag has no entity_identifier
// (the KeyError makes extract_mentions return []).
func Mentions(src string) (ids []string, ok bool) {
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return ids, true
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			tag := tok.Data
			// html.parser only treats script and style as raw text.
			if tag != "script" && tag != "style" {
				z.NextIsNotRawText()
			}
			if tag != "mention-component" {
				continue
			}
			// Duplicate attributes: BeautifulSoup keeps the last.
			attrs := map[string]string{}
			for _, a := range tok.Attr {
				attrs[a.Key] = a.Val
			}
			if attrs["entity_name"] != "user_mention" {
				continue
			}
			id, has := attrs["entity_identifier"]
			if !has {
				return nil, false
			}
			ids = append(ids, id)
		}
	}
}
