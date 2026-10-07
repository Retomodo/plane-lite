package sanitize

import (
	"slices"
	"testing"
)

func TestMentions(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
		ok   bool
	}{
		{`<p>Now <mention-component entity_identifier="a" entity_name="user_mention"></mention-component> and ` +
			`<mention-component entity_name="user_mention" entity_identifier="b"/></p>`, []string{"a", "b"}, true},
		{`<mention-component entity_identifier="a" entity_name="issue_mention"></mention-component>`, nil, true},
		{`<script><mention-component entity_identifier="a" entity_name="user_mention"></script>`, nil, true},
		{`<textarea><mention-component entity_identifier="a" entity_name="user_mention"></textarea>`, []string{"a"}, true},
		{`<mention-component entity_name="user_mention"></mention-component>`, nil, false},
		{`<mention-component entity_identifier="a" entity_identifier="b" entity_name="user_mention">`, []string{"b"}, true},
	} {
		got, ok := Mentions(tc.in)
		if !slices.Equal(got, tc.want) || ok != tc.ok {
			t.Errorf("Mentions(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
