package contract

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"time"
)

// pageIDs marks the array_agg(DISTINCT uuid) columns of the page querysets,
// whose order follows the random ids in Django too.
var pageIDs = Unordered("label_ids", "[].label_ids", "project_ids", "[].project_ids")

// Fixed component ids (the editor's per-node ids), so page_logs rows can be
// told apart.
const (
	pageTx1 = "11111111-1111-4111-8111-111111111111"
	pageTx2 = "22222222-2222-4222-8222-222222222222"
	pageTx3 = "33333333-3333-4333-8333-333333333333"
	pageTx4 = "44444444-4444-4444-8444-444444444444"
	pageTx5 = "55555555-5555-4555-8555-555555555555"
)

// pageMention is an editor mention-component with its node id.
func pageMention(id, entity, name string) string {
	return `<mention-component id="` + id + `" entity_identifier="` + entity + `" entity_name="` + name + `"></mention-component>`
}

// pageFavorite favorites a page for c, as the (later) favorites endpoints
// would: UserFavorite(user, entity_type="page", entity_identifier, project).
func pageFavorite(s *Scenario, c *Client, page, project string) {
	s.DBStrings(`INSERT INTO user_favorites (id, created_at, updated_at, entity_type, entity_identifier, is_folder,
			sequence, created_by_id, project_id, user_id, workspace_id)
		SELECT gen_random_uuid(), now(), now(), 'page', $1, false, 65535, $2, $3, $2, workspace_id
		FROM projects WHERE id = $3 RETURNING id::text`, page, userID(c), project)
}

// pageShapeStep records whether value matches pattern, for values that
// carry the wall-clock time (archive's str(datetime.now())). "<today>" in
// the pattern stands for today's UTC date.
func pageShapeStep(s *Scenario, label, value, pattern string) {
	re := regexp.MustCompile(strings.ReplaceAll(pattern, "<today>", time.Now().UTC().Format(time.DateOnly)))
	s.steps = append(s.steps, Step{Actor: "check", Method: "MATCH", Path: label,
		Body: map[string]any{"pattern": pattern, "matches": re.MatchString(value)}})
}

// pageBytesStep records a binary response body (base64) and its download
// headers: apps/live reads description_binary byte for byte.
func pageBytesStep(s *Scenario, label string, r *Response) {
	headerStep(s, label, r, "Content-Type", "Content-Disposition")
	s.steps = append(s.steps, Step{Actor: "bytes", Method: "BODY", Path: label,
		Body: base64.StdEncoding.EncodeToString(r.Raw)})
}

// pageRows records every table the page views and their tasks write.
func pageRows(s *Scenario) {
	s.DBRows("pages", `SELECT pg.name, o.email AS owned_by, pg.access, pg.color, pg.archived_at::text,
			pg.is_locked, par.name AS parent, pg.description_html, pg.description_stripped, pg.description_json,
			encode(pg.description_binary, 'base64') AS description_binary, pg.view_props, pg.logo_props, pg.is_global,
			pg.sort_order, cb.email AS created_by, ub.email AS updated_by,
			pg.updated_at > (SELECT min(x.created_at) FROM project_pages x WHERE x.page_id = pg.id) AS saved_again,
			pg.deleted_at IS NULL AS live
		FROM pages pg JOIN users o ON o.id = pg.owned_by_id LEFT JOIN pages par ON par.id = pg.parent_id
		LEFT JOIN users cb ON cb.id = pg.created_by_id LEFT JOIN users ub ON ub.id = pg.updated_by_id
		ORDER BY pg.name, o.email, pg.created_at`)
	s.DBRows("project_pages", `SELECT pg.name, p.identifier, cb.email AS created_by, ub.email AS updated_by,
			x.deleted_at IS NULL AS live
		FROM project_pages x JOIN pages pg ON pg.id = x.page_id JOIN projects p ON p.id = x.project_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY pg.name, pg.created_at, p.identifier`)
	s.DBRows("page_labels", `SELECT pg.name, l.name AS label, cb.email AS created_by, ub.email AS updated_by,
			x.deleted_at IS NULL AS live
		FROM page_labels x JOIN pages pg ON pg.id = x.page_id JOIN labels l ON l.id = x.label_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY pg.name, l.name, x.deleted_at IS NULL, x.created_at`)
	s.DBRows("page_logs", `SELECT pg.name, x.transaction, x.entity_name, x.entity_type, x.entity_identifier,
			x.created_by_id IS NULL AS no_creator, x.updated_at >= x.created_at AS ordered_times,
			x.deleted_at IS NULL AS live
		FROM page_logs x JOIN pages pg ON pg.id = x.page_id
		ORDER BY pg.name, pg.created_at, x.transaction`)
	s.DBRows("page_versions", `SELECT pg.name, o.email AS owned_by, v.description_html, v.deleted_at IS NULL AS live
		FROM page_versions v JOIN pages pg ON pg.id = v.page_id JOIN users o ON o.id = v.owned_by_id
		ORDER BY pg.name, v.created_at`)
	s.DBRows("user_favorites", `SELECT u.email, pg.name, p.identifier, f.deleted_at IS NULL AS live
		FROM user_favorites f JOIN users u ON u.id = f.user_id LEFT JOIN pages pg ON pg.id = f.entity_identifier
		LEFT JOIN projects p ON p.id = f.project_id
		WHERE f.entity_type = 'page'
		ORDER BY u.email, pg.name`)
	s.DBRows("recent_visits", `SELECT u.email, pg.name, p.identifier, v.created_by_id = v.user_id AS by_user,
			v.deleted_at IS NULL AS live
		FROM user_recent_visits v JOIN users u ON u.id = v.user_id LEFT JOIN pages pg ON pg.id = v.entity_identifier
		LEFT JOIN projects p ON p.id = v.project_id
		WHERE v.entity_name = 'page'
		ORDER BY u.email, pg.name`)
}

func TestPageCRUD(t *testing.T) {
	Run(t, "page_crud", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "150")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		pages := p + "pages/"
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")
		feature := alice.Post(p+"issue-labels/", map[string]any{"name": "Feature"}).String("id")
		issue := alice.Post(p+"issues/", map[string]any{"name": "Mentioned issue"}, Unordered("label_ids", "assignee_ids", "module_ids")).String("id")
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")

		// Create: project admins and members.
		pub := alice.Post(pages, map[string]any{
			"name": "Alice public",
			"description_html": `<p>Hi ` + pageMention(pageTx1, userID(bob), "user_mention") + ` see ` +
				pageMention(pageTx2, issue, "issue") + `<script>x()</script> &amp; more</p>`,
			"description_json": map[string]any{"type": "doc"},
		}, pageIDs).String("id")
		bobPage := bob.Post(pages, map[string]any{"name": "Bob page", "access": 0, "logo_props": map[string]any{"in_use": "emoji"}}, pageIDs).String("id")
		carol.Post(pages, map[string]any{"name": "Guest page"}, pageIDs)
		dan.Post(pages, map[string]any{"name": "Dan page"}, pageIDs)
		outsider.Post(pages, map[string]any{"name": "Outsider page"}, pageIDs)
		alice.Post(pages, []any{"x"}, pageIDs)
		alice.Post(pages, map[string]any{
			"access": 3, "is_locked": "maybe", "archived_at": "2026-13-01", "parent": dead, "labels": []any{dead, "nope"},
			"label_ids": []any{"nope"}, "name": nil, "color": "#" + strings.Repeat("c", 255),
			"created_by": dead, "view_props": nil,
		}, pageIDs)
		priv := alice.Post(pages, map[string]any{
			"name": "Alice private", "access": "1", "color": "#fff", "labels": []any{bug, feature},
			"view_props": map[string]any{"full_width": true}, "created_by": userID(bob), "updated_by": userID(bob),
			"id": dead, "workspace": dead, "owned_by": userID(bob), "is_favorite": true, "description_binary": nil,
		}, pageIDs).String("id")
		alice.Post(pages, map[string]any{"name": "Child", "parent": pub}, pageIDs)
		child := s.DBStrings(`SELECT id::text FROM pages WHERE name = 'Child'`)[0]
		alice.Post(pages, map[string]any{"name": "With ids", "label_ids": []any{bug}}, pageIDs)
		alice.Post(pages, map[string]any{"name": "Archived", "archived_at": "2026-01-01", "is_locked": true}, pageIDs)
		alice.Post(pages, map[string]any{}, pageIDs)
		alice.Post(pages, map[string]any{"name": "Null html", "description_html": nil}, pageIDs)
		alice.Post(pages, map[string]any{"name": "Null json", "description_json": nil}, pageIDs)
		alice.Post(pages, map[string]any{"name": "Bad mention", "description_html": pageMention(pageTx3, "nope", "user_mention")}, pageIDs)
		alice.Post(pages, map[string]any{"name": "Searched", "description_html": "<p>a<br>b</p>"}, pageIDs)
		alice.Post(pages+"?search=nothing", map[string]any{"name": "Hidden by search"}, pageIDs)
		alice.Post(pages, map[string]any{"name": "Raw", "description_html": `<p>a<script>if (a<b) x()</script> &amp; b &bogus; ` +
			`c&nbsp;d</p><!-- note --><style>p{}</style><p title="&lt;t&gt;">e</p>&amp`}, pageIDs)
		alice.Post(pages, map[string]any{"name": "Binary", "description_binary": "AQID"}, pageIDs)
		alice.Post(pages, map[string]any{"name": "Number html", "description_html": 5}, pageIDs)

		// List.
		alice.Get(pages, pageIDs)
		bob.Get(pages, pageIDs)
		carol.Get(pages, pageIDs)
		dan.Get(pages, pageIDs)
		outsider.Get(pages, pageIDs)
		for _, q := range []string{"?order_by=name", "?order_by=-name", "?order_by=-updated_at",
			"?order_by=owned_by__password", "?order_by=--name", "?search=alice", "?search=PUB,%20bob", "?search=zzz",
			"?search=%22public%20alice%22", "?search=%22alice%20pub%22,raw", "?search=%00"} {
			alice.Get(pages+q, pageIDs)
		}
		// Every page has the default sort_order, so the "id" tiebreak (a
		// random uuid in Django too) decides the order.
		alice.Get(pages+"?order_by=sort_order", pageIDs, SortBy("", "name"))
		pageFavorite(s, alice, bobPage, pl)
		pageFavorite(s, bob, pub, pl)
		alice.Get(pages+"?order_by=name", pageIDs)
		bob.Get(pages+"?order_by=name", pageIDs)

		// Retrieve.
		alice.Get(pages+pub+"/", pageIDs)
		alice.Get(pages+pub+"/?track_visit=false", pageIDs)
		alice.Get(pages+priv+"/?track_visit=False", pageIDs)
		bob.Get(pages+pub+"/?track_visit=true", pageIDs)
		bob.Get(pages+priv+"/", pageIDs)
		carol.Get(pages+pub+"/", pageIDs)
		carol.Get(pages+pub+"/?search=zzz", pageIDs)
		dan.Get(pages+pub+"/", pageIDs)
		outsider.Get(pages+pub+"/", pageIDs)
		alice.Get(pages+child+"/", pageIDs)
		alice.Get(pages+dead+"/", pageIDs)
		alice.Get(pages+"nope/", pageIDs)
		alice.Get(pages+pub+"/?search=zzz", pageIDs)
		alice.Get(ws+"projects/"+ot+"/pages/"+pub+"/", pageIDs)
		alice.Patch(p, map[string]any{"guest_view_all_features": true}, projMask)
		carol.Get(pages, pageIDs)
		carol.Get(pages+pub+"/", pageIDs)

		// Update: admins and members, the owner alone changing access.
		bob.Patch(pages+pub+"/", map[string]any{"name": "Renamed by bob", "color": "#000"})
		carol.Patch(pages+pub+"/", map[string]any{"name": "Guest edit"})
		dan.Patch(pages+pub+"/", map[string]any{"name": "Dan edit"})
		bob.Patch(pages+pub+"/", map[string]any{"access": 1})
		bob.Patch(pages+pub+"/", map[string]any{"access": 0, "name": "Same access"})
		bob.Patch(pages+priv+"/", map[string]any{"name": "Private edit"})
		alice.Patch(pages+pub+"/", map[string]any{"parent": dead})
		alice.Patch(pages+pub+"/", map[string]any{"parent": "nope"})
		alice.Patch(pages+pub+"/", map[string]any{"description_html": ""})
		alice.Patch(pages+pub+"/", map[string]any{"name": nil, "access": "x", "is_locked": "maybe", "labels": "nope"})
		alice.Patch(pages+pub+"/", []any{"x"})
		alice.Patch(pages+pub+"/", map[string]any{
			"description_html": ` <p>Now ` + pageMention(pageTx1, userID(bob), "user_mention") + ` and ` +
				pageMention(pageTx4, userID(carol), "user_mention") + `</p> `,
		})
		alice.Patch(pages+pub+"/", map[string]any{"labels": []any{bug, feature}})
		alice.Patch(pages+pub+"/", map[string]any{"labels": []any{feature}, "created_by": userID(bob), "updated_by": userID(carol)})
		alice.Patch(pages+pub+"/", map[string]any{"label_ids": []any{bug}, "project_ids": []any{pl, ot}, "access": 1})
		alice.Patch(pages+pub+"/", map[string]any{"access": 0, "view_props": map[string]any{"full_width": true},
			"archived_at": "2026-02-01", "logo_props": map[string]any{"in_use": "icon"}})
		alice.Get(pages+pub+"/?track_visit=false", pageIDs)
		alice.Patch(pages+bobPage+"/", map[string]any{"parent": pub, "is_locked": true})
		alice.Patch(pages+bobPage+"/", map[string]any{"name": "Locked edit"})
		alice.Patch(pages+dead+"/", map[string]any{"name": "Nope"})
		alice.Get(pages, pageIDs)
		pageRows(s)
	})
}

func TestPageLifecycle(t *testing.T) {
	Run(t, "page_lifecycle", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "151")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		pages := p + "pages/"
		s.AliasToday()
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")

		root := alice.Post(pages, map[string]any{"name": "Root", "labels": []any{bug},
			"description_html": "<p>" + pageMention(pageTx1, userID(bob), "user_mention") + "</p>"}, pageIDs).String("id")
		alice.Post(pages, map[string]any{"name": "Child", "parent": root}, pageIDs)
		child := s.DBStrings(`SELECT id::text FROM pages WHERE name = 'Child'`)[0]
		alice.Post(pages, map[string]any{"name": "Grandchild", "parent": child}, pageIDs)
		grandchild := s.DBStrings(`SELECT id::text FROM pages WHERE name = 'Grandchild'`)[0]
		bobPage := bob.Post(pages, map[string]any{"name": "Bob page"}, pageIDs).String("id")
		bobPrivate := bob.Post(pages, map[string]any{"name": "Bob private", "access": 1}, pageIDs).String("id")
		pageFavorite(s, alice, root, pl)
		pageFavorite(s, bob, root, pl)
		pageFavorite(s, bob, bobPage, pl)
		alice.Get(pages+root+"/", pageIDs)
		bob.Get(pages+root+"/", pageIDs)

		// Archive: the owner or a project admin.
		carol.Post(pages+root+"/archive/", nil)
		dan.Post(pages+root+"/archive/", nil)
		bob.Post(pages+root+"/archive/", nil)
		alice.Post(pages+bobPrivate+"/archive/", nil)
		alice.Post(pages+dead+"/archive/", nil)
		r := alice.Post(pages+bobPage+"/archive/", nil, Mask("archived_at")) // str(datetime.now()): wall clock
		pageShapeStep(s, "archive response", r.String("archived_at"), `^<today> \d{2}:\d{2}:\d{2}(\.\d{6})?$`)
		alice.Post(pages+root+"/archive/", nil, Mask("archived_at")) // wall clock, as above
		alice.Get(pages, pageIDs)
		alice.Get(pages+child+"/?track_visit=false", pageIDs)
		alice.Patch(pages+child+"/description/", map[string]any{"description_html": "<p>Archived edit</p>"})
		alice.Patch(pages+root+"/", map[string]any{"name": "Archived rename"})

		// Unarchive: a child of an archived parent is cut loose.
		bob.Delete(pages + root + "/archive/")
		carol.Delete(pages + root + "/archive/")
		alice.Delete(pages + child + "/archive/")
		alice.Get(pages, pageIDs)
		alice.Delete(pages + root + "/archive/")
		bob.Delete(pages + bobPage + "/archive/")
		alice.Get(pages, pageIDs)

		// Lock and unlock.
		bob.Post(pages+root+"/lock/", nil)
		carol.Post(pages+root+"/lock/", nil)
		alice.Patch(pages+root+"/", map[string]any{"name": "Locked rename"})
		alice.Patch(pages+root+"/description/", map[string]any{"description_html": "<p>Locked edit</p>"})
		bob.Delete(pages + root + "/lock/")
		alice.Delete(pages + root + "/lock/")
		alice.Delete(pages + root + "/lock/")
		bob.Post(pages+bobPage+"/lock/", nil)
		bob.Delete(pages + bobPage + "/lock/")
		alice.Post(pages+dead+"/lock/", nil)

		// Access: only the owner may change it.
		bob.Post(pages+root+"/access/", map[string]any{"access": 1})
		bob.Post(pages+root+"/access/", map[string]any{"access": 0})
		bob.Post(pages+root+"/access/", map[string]any{})
		carol.Post(pages+root+"/access/", map[string]any{"access": 0})
		alice.Post(pages+root+"/access/", map[string]any{"access": 1})
		bob.Get(pages+root+"/", pageIDs)
		alice.Post(pages+root+"/access/", map[string]any{})
		alice.Post(pages+root+"/access/", map[string]any{"access": "1"})
		alice.Post(pages+root+"/access/", map[string]any{"access": 7})
		alice.Get(pages+root+"/?track_visit=false", pageIDs)
		alice.Post(pages+root+"/access/", map[string]any{"access": -1})
		alice.Post(pages+root+"/access/", map[string]any{"access": "x"})
		alice.Post(pages+root+"/access/", map[string]any{"access": nil})
		alice.Post(pages+root+"/access/", []any{1})
		alice.Post(pages+root+"/access/", map[string]any{"access": true})
		alice.Post(pages+root+"/access/", map[string]any{"access": 0})

		// Duplicate.
		img := "66666666-6666-4666-8666-666666666666"
		dupSrc := alice.Post(pages, map[string]any{"name": "Rich", "description_html": `<p class="x" data-id='a"b'>Hi&nbsp;` +
			pageMention(pageTx2, userID(carol), "user_mention") + `<br>&lt;tag&gt; &amp; <b>bold</b></p>` +
			`<image-component id="` + pageTx3 + `" src="` + img + `"></image-component><!-- note --><input checked>`,
			"description_json": map[string]any{"type": "doc"}, "color": "#123", "labels": []any{bug}}, pageIDs).String("id")
		s.DBStrings(`UPDATE pages SET description_binary = '\x01020304'::bytea WHERE id = $1 RETURNING id::text`, dupSrc)
		alice.Post(pages+dupSrc+"/duplicate/", nil, pageIDs)
		bobCopy := bob.Post(pages+dupSrc+"/duplicate/", nil, pageIDs).String("id")
		// A unique name: ?order_by=name breaks ties on the random ids.
		bob.Patch(pages+bobCopy+"/", map[string]any{"name": "Rich (Bob's copy)"}, pageIDs)
		carol.Post(pages+dupSrc+"/duplicate/", nil, pageIDs)
		dan.Post(pages+dupSrc+"/duplicate/", nil, pageIDs)
		outsider.Post(pages+dupSrc+"/duplicate/", nil, pageIDs)
		bob.Post(pages+bobPrivate+"/duplicate/", nil, pageIDs)
		alice.Post(pages+bobPrivate+"/duplicate/", nil, pageIDs)
		alice.Post(pages+dead+"/duplicate/", nil, pageIDs)
		badImg := alice.Post(pages, map[string]any{"name": "Bad image",
			"description_html": `<p>x<br></p><image-component src="not-a-uuid"></image-component>`}, pageIDs).String("id")
		alice.Post(pages+badImg+"/duplicate/", nil, pageIDs)
		// A page linked to a second project copies both links.
		s.DBStrings(`INSERT INTO project_pages (id, created_at, updated_at, page_id, project_id, workspace_id, created_by_id)
			SELECT gen_random_uuid(), now(), now(), id, $2, workspace_id, owned_by_id FROM pages WHERE id = $1
			RETURNING id::text`, child, ot)
		alice.Post(pages+child+"/duplicate/", nil, pageIDs)
		alice.Get(pages+child+"/?track_visit=false", pageIDs)
		alice.Post(pages+root+"/lock/", nil)
		alice.Post(pages+root+"/duplicate/", nil, pageIDs)
		alice.Delete(pages + root + "/lock/")
		alice.Post(pages, map[string]any{"name": "Spacing", "description_html": "<pre>  x  </pre>\n\n<p> </p>  <p>&#169; &#x41;" +
			"&#128;&copy &bogus; &amp-x &#1114112; &#129; &amp</p><script>if (a<b && c>d) {}</script><BR></br><Img SRC=x /><p>" +
			`<image-component src="` + strings.ToUpper(img) + `" id="x"></image-component><mention-component id=` +
			pageTx4 + ` entity_name=user_mention entity_identifier=` + userID(dan) + ` /></p><unclosed><b>bold`}, pageIDs)
		spacing := s.DBStrings(`SELECT id::text FROM pages WHERE name = 'Spacing'`)[0]
		alice.Post(pages+spacing+"/duplicate/", nil, pageIDs)
		// A NUL from "&#0;" fails the copy task's save: the copy keeps its html.
		nul := alice.Post(pages, map[string]any{"name": "Nul", "description_html": "<p>a&#0;b</p><br>"}, pageIDs).String("id")
		alice.Post(pages+nul+"/duplicate/", nil, pageIDs)
		alice.Get(pages+"?order_by=name", pageIDs)

		// Delete: archived first, then the owner or a project admin.
		alice.Patch(pages+child+"/", map[string]any{"parent": root})
		alice.Delete(pages + root + "/")
		alice.Post(pages+root+"/archive/", nil, Mask("archived_at"))    // wall clock, as above
		alice.Post(pages+bobPage+"/archive/", nil, Mask("archived_at")) // wall clock, as above
		carol.Delete(pages + root + "/")
		dan.Delete(pages + root + "/")
		bob.Delete(pages + root + "/")
		bob.Delete(pages + bobPage + "/")
		alice.Get(pages+grandchild+"/?track_visit=false", pageIDs)
		alice.Delete(pages + root + "/")
		alice.Delete(pages + root + "/")
		alice.Get(pages+root+"/", pageIDs)
		alice.Delete(pages + dead + "/")
		alice.Get(pages+"?order_by=name", pageIDs)
		pageRows(s)
	})
}

func TestPageDescription(t *testing.T) {
	Run(t, "page_description", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "152")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		pages := p + "pages/"
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		pg := alice.Post(pages, map[string]any{"name": "Doc", "description_html": "<p>Start</p>"}, pageIDs).String("id")
		other := alice.Post(pages, map[string]any{"name": "Other doc"}, pageIDs).String("id")
		bobPrivate := bob.Post(pages, map[string]any{"name": "Bob private", "access": 1}, pageIDs).String("id")
		desc := pages + pg + "/description/"
		b64 := func(b string) string { return base64.StdEncoding.EncodeToString([]byte(b)) }

		// apps/live's first load: an empty binary, then the details.
		pageBytesStep(s, "empty description", alice.Get(desc))
		alice.Get(pages+pg+"/", pageIDs)
		pageBytesStep(s, "guest read", carol.Get(desc))
		dan.Get(desc)
		outsider.Get(desc)
		alice.Get(pages + dead + "/description/")
		alice.Get(pages + bobPrivate + "/description/")
		pageBytesStep(s, "owner private read", bob.Get(pages+bobPrivate+"/description/"))

		// Saves.
		alice.Patch(desc, map[string]any{
			"description_binary": b64("\x01\x02\x03\x04yjs-state"),
			"description_html": "<p>Hello " + pageMention(pageTx1, userID(bob), "user_mention") +
				`<script>x()</script><a href="javascript:x()">link</a></p>`,
			"description_json": map[string]any{"type": "doc", "content": []any{}},
		})
		pageBytesStep(s, "saved description", alice.Get(desc))
		bob.Patch(desc, map[string]any{"description_binary": b64("\x05\x06\x07\x08bob"),
			"description_html": "<p>Bob " + pageMention(pageTx2, userID(carol), "user_mention") + "</p>"})
		carol.Patch(desc, map[string]any{"description_html": "<p>Guest</p>"})
		dan.Patch(desc, map[string]any{"description_html": "<p>Dan</p>"})
		alice.Patch(desc, map[string]any{"description_json": map[string]any{"only": "json"}})
		alice.Patch(desc, map[string]any{})
		alice.Patch(desc, []any{"x"})
		alice.Patch(desc, map[string]any{"description_binary": "AAA"})
		alice.Patch(desc, map[string]any{"description_binary": "AAAA"})
		alice.Patch(desc, map[string]any{"description_binary": b64("<html><body>hi</body></html>")})
		alice.Patch(desc, map[string]any{"description_binary": b64("\x00\x01 DATA:text/plain")})
		alice.Patch(desc, map[string]any{"description_binary": true, "description_html": 5, "description_json": "{"})
		alice.Patch(desc, map[string]any{"description_binary": nil, "description_html": nil})
		alice.Patch(desc, map[string]any{"description_binary": " " + b64("\x01\x02\x03\x04padded") + " "})
		pageBytesStep(s, "trimmed binary", alice.Get(desc))
		alice.Patch(desc, map[string]any{"description_binary": "!!" + b64("\x09\x09\x09\x09junk") + "!!"})
		pageBytesStep(s, "junk-tolerant base64", alice.Get(desc))
		alice.Patch(desc, map[string]any{"description_binary": "!!!!"})
		pageBytesStep(s, "decoded to nothing", alice.Get(desc))
		alice.Patch(desc, map[string]any{"description_json": nil})
		alice.Patch(desc, map[string]any{"description_binary": ""})
		pageBytesStep(s, "blank binary", alice.Get(desc))
		alice.Patch(desc, map[string]any{"description_html": 5})
		alice.Get(pages+pg+"/?track_visit=false", pageIDs)
		alice.Patch(desc, map[string]any{"description_html": ""})
		alice.Get(pages+pg+"/?track_visit=false", pageIDs)
		alice.Patch(desc, map[string]any{"description_html": "<p>" + pageMention(pageTx3, issueLikeID, "issue") +
			`<image-component id="` + pageTx4 + `" src="` + issueLikeID + `"></image-component></p>`})
		alice.Get(pages+pg+"/?track_visit=false", pageIDs)
		alice.Patch(desc, map[string]any{"description_html": `<p><image-component id="` + pageTx5 + `" src="https://x/y.png"></image-component></p>`})
		alice.Patch(pages+pg+"/", map[string]any{"description_html": "<p>Via page " + pageMention(pageTx2, userID(carol), "user_mention") + "</p>"})
		alice.Get(ws + "projects/" + ot + "/pages/" + pg + "/description/")

		// Versions: track_page_version never writes one (it reads
		// page.description, which Page lacks), so they are seeded.
		vs := pages + pg + "/versions/"
		alice.Get(vs)
		for i, html := range []string{"<p>v1</p>", "<p>v2</p>"} {
			s.DBStrings(`INSERT INTO page_versions (id, created_at, updated_at, last_saved_at, description_binary,
					description_html, description_stripped, description_json, owned_by_id, page_id, workspace_id,
					created_by_id, sub_pages_data)
				SELECT gen_random_uuid(), now() + make_interval(secs => $3), now() + make_interval(secs => $3), now(),
					CASE WHEN $3 = 0 THEN '\x0102'::bytea END, $2, 'stripped', '{"v": 1}', owned_by_id, id,
					workspace_id, owned_by_id, '{}'
				FROM pages WHERE id = $1 RETURNING id::text`, pg, html, i)
		}
		s.DBStrings(`INSERT INTO page_versions (id, created_at, updated_at, last_saved_at, description_html,
				description_json, owned_by_id, page_id, workspace_id, sub_pages_data)
			SELECT gen_random_uuid(), now(), now(), now(), '<p>other</p>', '{}', owned_by_id, id, workspace_id, '{}'
			FROM pages WHERE id = $1 RETURNING id::text`, other)
		list := alice.Get(vs)
		bob.Get(vs)
		carol.Get(vs)
		dan.Get(vs)
		otherVersion := alice.Get(pages + other + "/versions/").String("0.id")
		v1 := list.String("1.id")
		alice.Get(vs + v1 + "/")
		bob.Get(vs + list.String("0.id") + "/")
		carol.Get(vs + v1 + "/")
		dan.Get(vs + v1 + "/")
		alice.Get(vs + otherVersion + "/")
		alice.Get(vs + dead + "/")
		alice.Get(vs + "nope/")
		alice.Get(pages + dead + "/versions/")
		alice.Get(ws + "projects/" + ot + "/pages/" + pg + "/versions/")
		alice.Get(pages + bobPrivate + "/versions/")
		pageRows(s)
	})
}

// issueLikeID is a UUID that names no row, for embeds whose target is
// never resolved.
const issueLikeID = "77777777-7777-4777-8777-777777777777"
