package contract

import (
	"strings"
	"testing"
)

func TestQuickLinks(t *testing.T) {
	Run(t, "quick_links", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "21")
		const ql = "/api/workspaces/acme/quick-links/"
		dead := "00000000-0000-4000-8000-00000000dead"

		alice.Get(ql)
		carol.Get(ql)
		outsider.Get(ql)
		outsider.Post(ql, map[string]any{"url": "https://example.com"})

		// Create: any workspace member, guests included.
		alice.Post(ql, map[string]any{})
		alice.Post(ql, nil)
		alice.Post(ql, []any{"x"})
		alice.Post(ql, map[string]any{"url": ""})
		alice.Post(ql, map[string]any{"url": nil})
		alice.Post(ql, map[string]any{"url": 5})
		alice.Post(ql, map[string]any{"url": []any{"a"}})
		alice.Post(ql, map[string]any{"url": "http://"})
		alice.Post(ql, map[string]any{"url": "not a url"})
		alice.Post(ql, map[string]any{"url": "ftp://files.example.com"})
		alice.Post(ql, map[string]any{"url": "https://example.com", "title": strings.Repeat("x", 256), "metadata": nil, "project": "nope"})
		alice.Post(ql, map[string]any{"url": "https://example.com", "metadata": "text", "project": dead, "created_by": dead, "deleted_at": "x"})
		docs := alice.Post(ql, map[string]any{"url": "https://docs.example.com/a?b=c", "title": "Docs", "metadata": map[string]any{"favicon": "x"}}).String("id")
		alice.Post(ql, map[string]any{"url": "https://docs.example.com/a?b=c"})
		alice.Post(ql, map[string]any{"url": "example.com/bare"})
		alice.Post(ql, map[string]any{"url": "example.com/bare", "title": "again"})
		alice.Post(ql, map[string]any{"url": "https://pl.example.com", "title": nil, "project": pl, "owner": dead, "workspace": dead, "id": dead,
			"created_by": userID(bob), "updated_by": userID(bob)})
		carolLink := carol.Post(ql, map[string]any{"url": "https://docs.example.com/a?b=c", "title": ""}).String("id")
		dan.Post(ql, map[string]any{"url": "http://dan.example.com"})
		alice.Get(ql)
		carol.Get(ql)
		dan.Get(ql)
		bob.Get(ql)

		// Update: the owner's own links only.
		alice.Patch(ql+docs+"/", map[string]any{"title": "Documentation"})
		bob.Patch(ql+docs+"/", map[string]any{"title": "Mine now"})
		outsider.Patch(ql+docs+"/", map[string]any{"title": "Mine now"})
		alice.Patch(ql+dead+"/", map[string]any{"title": "Nothing"})
		alice.Patch(ql+"not-a-uuid/", map[string]any{"title": "Nothing"})
		alice.Patch(ql+docs+"/", map[string]any{})
		alice.Patch(ql+docs+"/", []any{"x"})
		alice.Patch(ql+docs+"/", map[string]any{"url": "nope nope", "title": 5})
		alice.Patch(ql+docs+"/", map[string]any{"url": "example.com/bare"})
		alice.Patch(ql+docs+"/", map[string]any{"url": "docs.example.com/new", "metadata": map[string]any{"a": 1}, "title": nil})
		alice.Patch(ql+docs+"/", map[string]any{"url": "http://docs.example.com/new"})
		alice.Patch(ql+docs+"/", map[string]any{"url": 5})
		alice.Patch(ql+docs+"/", map[string]any{"url": nil})
		alice.Patch(ql+docs+"/", map[string]any{"project": pl, "created_by": userID(carol), "updated_by": userID(carol), "deleted_at": nil})
		carol.Patch(ql+carolLink+"/", map[string]any{"url": "https://docs.example.com/a?b=c"})
		carol.Patch(ql+carolLink+"/", map[string]any{"title": "Carol's"})
		alice.Get(ql)

		// Delete.
		alice.Delete(ql + carolLink + "/")
		outsider.Delete(ql + docs + "/")
		alice.Delete(ql + dead + "/")
		alice.Delete(ql + "not-a-uuid/")
		alice.Delete(ql + docs + "/")
		alice.Delete(ql + docs + "/")
		alice.Patch(ql+docs+"/", map[string]any{"title": "Gone"})
		carol.Delete(ql + carolLink + "/")
		alice.Post(ql, map[string]any{"url": "https://docs.example.com/a?b=c"})
		alice.Get(ql)
		s.DBRows("workspace_user_links", `SELECT l.url, l.title, l.metadata, o.email AS owner, cu.email AS created_by,
				uu.email AS updated_by, l.project_id IS NOT NULL AS in_project, l.deleted_at IS NULL AS live
			FROM workspace_user_links l JOIN users o ON o.id = l.owner_id
			LEFT JOIN users cu ON cu.id = l.created_by_id LEFT JOIN users uu ON uu.id = l.updated_by_id
			ORDER BY l.created_at`)
	})
}

func TestHomePreferences(t *testing.T) {
	Run(t, "home_preferences", func(s *Scenario) {
		alice, bob, _, dan, outsider, _ := projectTeam(s, "22")
		const hp = "/api/workspaces/acme/home-preferences/"

		// Updating before the first read finds nothing.
		bob.Patch(hp+"quick_links/", map[string]any{"is_enabled": false})
		outsider.Get(hp)

		// Reading creates the missing widgets, newest row first.
		alice.Get(hp)
		alice.Get(hp)
		dan.Get(hp)
		s.DBRows("workspace_home_preferences", `SELECT u.email, h.key, h.is_enabled, h.config, h.sort_order,
				h.created_by_id IS NULL AS no_creator
			FROM workspace_home_preferences h JOIN users u ON u.id = h.user_id ORDER BY u.email, h.created_at`)

		// Update: the caller's own rows.
		alice.Patch(hp+"recents/", map[string]any{"is_enabled": false})
		alice.Patch(hp+"recents/", map[string]any{"sort_order": 5.5, "config": map[string]any{"ignored": true}, "id": "x"})
		alice.Patch(hp+"recents/", map[string]any{})
		alice.Patch(hp+"recents/", []any{"x"})
		alice.Patch(hp+"recents/", map[string]any{"is_enabled": "maybe", "sort_order": "x", "key": ""})
		alice.Patch(hp+"recents/", map[string]any{"is_enabled": nil, "sort_order": nil, "key": nil})
		alice.Patch(hp+"recents/", map[string]any{"key": strings.Repeat("k", 256)})
		alice.Patch(hp+"recents/", map[string]any{"key": "quick_links"})
		alice.Patch(hp+"unknown/", map[string]any{"is_enabled": false})
		alice.Patch(hp+"quick_tutorial/", map[string]any{"is_enabled": false})
		outsider.Patch(hp+"recents/", map[string]any{"is_enabled": false})
		dan.Patch(hp+"my_stickies/", map[string]any{"is_enabled": false, "sort_order": 1})
		alice.Get(hp)
		dan.Get(hp)

		// Renaming a row makes the next read create the key again.
		alice.Patch(hp+"quick_links/", map[string]any{"key": "renamed"})
		alice.Get(hp)
		alice.Patch(hp+"renamed/", map[string]any{"is_enabled": false})
		alice.Get(hp)
		s.DBRows("workspace_home_preferences_after", `SELECT u.email, h.key, h.is_enabled, h.sort_order,
				h.created_by_id IS NULL AS no_creator, h.updated_by_id IS NULL AS no_updater, h.deleted_at IS NULL AS live
			FROM workspace_home_preferences h JOIN users u ON u.id = h.user_id ORDER BY u.email, h.created_at`)
	})
}

func TestStickies(t *testing.T) {
	Run(t, "stickies", func(s *Scenario) {
		alice, bob, carol, dan, outsider, _ := projectTeam(s, "23")
		const st = "/api/workspaces/acme/stickies/"
		dead := "00000000-0000-4000-8000-00000000dead"

		alice.Get(st)
		outsider.Get(st)
		outsider.Post(st, map[string]any{"name": "No"})

		// Create: any workspace member.
		alice.Post(st, nil)
		alice.Post(st, []any{"x"})
		alice.Post(st, map[string]any{"name": 5, "description": "text", "description_html": 5, "color": nil, "sort_order": "x", "logo_props": nil})
		alice.Post(st, map[string]any{"description": nil, "description_stripped": nil, "background_color": strings.Repeat("c", 256), "created_by": dead, "deleted_at": "x"})
		alice.Post(st, map[string]any{"description_binary": "text", "description_html": []any{"a"}})
		first := alice.Post(st, map[string]any{"name": "First", "description_html": "<p>Hello <strong>world</strong></p>", "sort_order": 500,
			"color": "yellow", "background_color": "#fff", "logo_props": map[string]any{"in_use": "emoji"},
			"description": map[string]any{"type": "doc"}, "description_stripped": "ignored", "description_binary": "AAAA"}).String("id")
		second := alice.Post(st, map[string]any{"name": "Second", "description_html": "<p>Say &amp; <em>again</em></p><script>alert(1)</script>", "sort_order": 1}).String("id")
		alice.Post(st, map[string]any{"description_html": "<p>unnamed</p><p>sticky</p>"})
		alice.Post(st, map[string]any{"name": nil, "description_html": ""})
		alice.Post(st, map[string]any{"name": "Blank", "description_html": "<script>x</script>"})
		alice.Post(st, map[string]any{"description_html": "a < b <p"})
		alice.Post(st, map[string]any{"name": "  Padded  ", "description_html": "  <p>pad</p>  ", "deleted_at": "2030-01-01T00:00:00Z"})
		bobSticky := bob.Post(st, map[string]any{"name": "Bob's", "description_html": "<p>Bob note</p>"}).String("id")
		carol.Post(st, map[string]any{"name": "Carol's", "created_by": userID(alice)})
		dan.Post(st, map[string]any{"name": "Dan's"})

		// List: the caller's own, newest sort order first.
		alice.Get(st)
		bob.Get(st)
		carol.Get(st)
		dan.Get(st)
		alice.Get(st + "?query=hello")
		alice.Get(st + "?query=HELLO%20WORLD")
		alice.Get(st + "?query=zzz")
		alice.Get(st + "?query=")
		alice.Get(st + "?query=%25")
		alice.Get(st + "?per_page=2")
		alice.Get(st + "?per_page=2&cursor=2:1:0")
		alice.Get(st + "?per_page=2&cursor=2:3:0")
		alice.Get(st + "?per_page=2&cursor=2:0:1")
		alice.Get(st + "?per_page=2&cursor=2:1:1")
		alice.Get(st + "?per_page=1&cursor=1:-1:0")
		alice.Get(st + "?per_page=x")
		alice.Get(st + "?per_page=0")
		alice.Get(st + "?per_page=1001")
		alice.Get(st + "?cursor=bad")
		alice.Get(st + "?cursor=1:x:0")

		// Update and delete: the creator only.
		bob.Patch(st+first+"/", map[string]any{"name": "Stolen"})
		outsider.Patch(st+first+"/", map[string]any{"name": "Stolen"})
		alice.Patch(st+bobSticky+"/", map[string]any{"name": "Stolen"})
		alice.Patch(st+dead+"/", map[string]any{"name": "Nothing"})
		alice.Patch(st+"not-a-uuid/", map[string]any{"name": "Nothing"})
		alice.Patch(st+first+"/", map[string]any{})
		alice.Patch(st+first+"/", []any{"x"})
		alice.Patch(st+first+"/", map[string]any{"name": 7, "description_html": 5, "sort_order": "x", "color": nil, "logo_props": nil})
		alice.Patch(st+first+"/", map[string]any{"name": "Renamed", "sort_order": 3, "description_html": "<p>Changed <a href=\"javascript:x\">link</a></p>"})
		alice.Patch(st+first+"/", map[string]any{"color": "red"})
		alice.Patch(st+first+"/", map[string]any{"description_html": ""})
		alice.Patch(st+first+"/", map[string]any{"description_html": "<p>Back</p>", "description_stripped": "ignored", "workspace": dead, "owner": dead})
		alice.Patch(st+second+"/", map[string]any{"description": map[string]any{"a": 1}, "created_by": userID(bob), "updated_by": userID(carol)})
		alice.Patch(st+second+"/", map[string]any{"name": "Locked out"})
		bob.Patch(st+second+"/", map[string]any{"name": "Taken"})
		alice.Get(st)
		bob.Get(st)

		bob.Delete(st + first + "/")
		outsider.Delete(st + first + "/")
		alice.Delete(st + dead + "/")
		alice.Delete(st + first + "/")
		alice.Delete(st + first + "/")
		alice.Patch(st+first+"/", map[string]any{"name": "Gone"})
		alice.Get(st)
		alice.Post(st, map[string]any{"name": "After delete"})
		alice.Get(st + "?per_page=3")
		s.DBRows("stickies", `SELECT s.name, s.description_html, s.description_stripped, s.description, s.logo_props, s.color,
				s.background_color, s.sort_order, o.email AS owner, cu.email AS created_by, uu.email AS updated_by,
				s.description_binary IS NULL AS no_binary, s.deleted_at IS NULL AS live
			FROM stickies s JOIN users o ON o.id = s.owner_id
			LEFT JOIN users cu ON cu.id = s.created_by_id LEFT JOIN users uu ON uu.id = s.updated_by_id
			ORDER BY s.created_at`)
	})
}
