package contract

import (
	"net/url"
	"strings"
	"testing"
)

// viewRows records the saved views and what their deletion touches.
func viewRows(s *Scenario) {
	s.DBRows("issue_views", `SELECT v.name, v.description, p.identifier AS project, w.slug AS workspace, v.query, v.filters,
			v.display_filters, v.display_properties, v.rich_filters, v.access, v.sort_order, v.logo_props, v.is_locked,
			v.archived_at::text, o.email AS owned_by, cb.email AS created_by, ub.email AS updated_by,
			v.deleted_at IS NULL AS live,
			v.updated_at - v.created_at > interval '1 millisecond' AS touched
		FROM issue_views v JOIN workspaces w ON w.id = v.workspace_id LEFT JOIN projects p ON p.id = v.project_id
		JOIN users o ON o.id = v.owned_by_id LEFT JOIN users cb ON cb.id = v.created_by_id
		LEFT JOIN users ub ON ub.id = v.updated_by_id
		ORDER BY v.created_at`)
	s.DBRows("user_favorites", `SELECT u.email, f.entity_type, v.name AS view, p.identifier AS project,
			f.deleted_at IS NULL AS live, f.updated_at = f.created_at AS untouched
		FROM user_favorites f JOIN users u ON u.id = f.user_id LEFT JOIN issue_views v ON v.id = f.entity_identifier
		LEFT JOIN projects p ON p.id = f.project_id
		ORDER BY f.created_at`)
	s.DBRows("user_recent_visits", `SELECT u.email, r.entity_name, v.name AS view, r.entity_identifier::text AS entity,
			p.identifier AS project, w.slug AS workspace, r.deleted_at IS NULL AS live
		FROM user_recent_visits r JOIN users u ON u.id = r.user_id JOIN workspaces w ON w.id = r.workspace_id
		LEFT JOIN issue_views v ON v.id = r.entity_identifier LEFT JOIN projects p ON p.id = r.project_id
		ORDER BY r.created_at`)
}

// favoriteView stars a view for a user. The favorites endpoints belong to a
// later batch, so the row is written directly, as UserFavorite.objects.create
// would (both targets run the same INSERT).
func favoriteView(s *Scenario, user, view, project string) {
	var proj any
	if project != "" {
		proj = project
	}
	s.DBStrings(`INSERT INTO user_favorites (id, created_at, updated_at, entity_type, entity_identifier, is_folder, sequence,
			user_id, project_id, workspace_id, created_by_id)
		SELECT gen_random_uuid(), now(), now(), 'view', $2::uuid, false, 65535, $1::uuid, $3::uuid,
			v.workspace_id, $1::uuid
		FROM issue_views v WHERE v.id = $2::uuid RETURNING id::text`, user, view, proj)
}

func TestViews(t *testing.T) {
	Run(t, "views", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "111")
		anon := s.Client("anon")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		a, b := userID(alice), userID(bob)
		other := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		po := ws + "projects/" + other + "/"
		states := alice.Get(p + "states/")
		todo := idOf(findBy(states, "name", "Todo"))
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")

		// Project views: anyone signed in may create one, in any project.
		alpha := alice.Post(p+"views/", map[string]any{
			"name": "Alpha", "description": "High bugs",
			"filters": map[string]any{"priority": []any{"high", "urgent"}, "labels": []any{bug}, "state": []any{todo},
				"assignees": []any{a}, "created_by": []any{b}, "sub_issue": "false", "type": "active"},
			"display_filters":    map[string]any{"group_by": "state", "order_by": "sort_order", "layout": "kanban"},
			"display_properties": map[string]any{"key": false},
			"rich_filters":       map[string]any{"and": []any{map[string]any{"priority__in": "high"}}},
			"logo_props":         map[string]any{"in_use": "emoji", "emoji": map[string]any{"value": "128512"}},
			"sort_order":         1.5, "access": 0, "is_locked": true, "query": map[string]any{"x": 1},
			"owned_by": b, "project": other, "workspace": dead, "created_by": b, "updated_by": b,
		}).String("id")
		beta := alice.Post(p+"views/", map[string]any{"name": "Beta", "sort_order": 9}).String("id")
		bobs := bob.Post(p+"views/", map[string]any{"name": "Bob's", "filters": map[string]any{
			"state_group": []any{"started"}, "estimate_point": []any{"x"}, "parent": []any{dead}, "mentions": []any{a},
			"logged_by": []any{a}, "name": "fix", "start_date": []any{"2026-01-01;after"}, "target_date": "2026-02-01",
			"created_at": []any{"2026-01-01;after", "2026-03-01;before", "x"}, "updated_at": "ab",
			"completed_at": []any{"2026-01-01"}, "project": []any{pl}, "cycle": []any{dead}, "module": "null",
			"intake_status": []any{1}, "inbox_status": []any{-1}, "subscriber": []any{b}, "start_target_date": "true",
		}}).String("id")
		carols := carol.Post(p+"views/", map[string]any{"name": "Carol's"}).String("id")
		outsiders := outsider.Post(p+"views/", map[string]any{"name": "Outsider's"}).String("id")
		alice.Post(po+"views/", map[string]any{"name": "Elsewhere"})
		alice.Post(ws+"projects/"+dead+"/views/", map[string]any{"name": "Nowhere"})
		alice.Post("/api/workspaces/nope/projects/"+pl+"/views/", map[string]any{"name": "Wrong slug"})
		anon.Post(p+"views/", map[string]any{"name": "Anon"})
		alice.Post(p+"views/", map[string]any{"name": "Ghost", "deleted_at": "2026-01-01T00:00:00Z"})
		alice.Post(p+"views/", map[string]any{"name": "Shelved", "archived_at": "2026-01-01T10:00:00Z"})

		// Validation, and filters issue_filters can't digest.
		for _, body := range []any{
			map[string]any{}, map[string]any{"name": ""}, map[string]any{"name": strings.Repeat("x", 256)},
			[]any{map[string]any{"name": "x"}}, map[string]any{"name": "x", "filters": nil},
			map[string]any{"name": "x", "description": nil, "sort_order": "high", "archived_at": "soon", "logo_props": nil},
			map[string]any{"name": "x", "created_by": dead},
			map[string]any{"name": "Stringy", "filters": "string"}, map[string]any{"name": "x", "filters": "name"},
			map[string]any{"name": "x", "filters": []any{"state"}}, map[string]any{"name": "x", "filters": 5},
			map[string]any{"name": "Zero", "filters": 0}, map[string]any{"name": "x", "filters": map[string]any{"state": 5}},
			map[string]any{"name": "x", "filters": map[string]any{"created_at": []any{5}}},
			map[string]any{"name": "Nulls", "filters": map[string]any{"state": "null", "priority": []any{}, "labels": nil}},
			map[string]any{"name": "x", "filters": map[string]any{"created_at": []any{"2_weeks;after;fromnow"}}},
			map[string]any{"name": "Dict", "filters": map[string]any{"created_at": map[string]any{"2026-01-01;after": 1}}},
		} {
			alice.Post(p+"views/", body)
		}

		// Private and locked views (read-only fields, so set directly).
		s.DBStrings(`UPDATE issue_views SET access = 0 WHERE id = $1 RETURNING id::text`, bobs)
		favoriteView(s, a, beta, pl)
		favoriteView(s, a, alpha, other)
		favoriteView(s, b, alpha, pl)

		for _, c := range []*Client{alice, bob, carol, dan, outsider, anon} {
			c.Get(p + "views/")
		}
		alice.Get(p + "views/?fields=id,name")
		alice.Get(po + "views/")
		alice.Get(ws + "projects/" + dead + "/views/")

		// Retrieve records a visit, even for a view it can't find.
		alice.Get(p + "views/" + alpha + "/")
		alice.Get(p + "views/" + alpha + "/")
		bob.Get(p + "views/" + bobs + "/")
		alice.Get(p + "views/" + bobs + "/")
		alice.Get(p + "views/" + dead + "/")
		carol.Get(p + "views/" + alpha + "/")
		carol.Get(p + "views/" + carols + "/")
		carol.Get(p + "views/" + dead + "/")
		dan.Get(p + "views/" + alpha + "/")
		alice.Get(po + "views/" + alpha + "/")
		alice.Get(p + "views/nope/")

		// Partial updates: only the creator, and only if they own it.
		alice.Patch(p+"views/"+alpha+"/", map[string]any{"name": "Alpha 2", "filters": map[string]any{"priority": []any{"low"}},
			"access": 0, "is_locked": true, "owned_by": b})
		alice.Patch(p+"views/"+alpha+"/", map[string]any{"description": "no filters key"})
		alice.Patch(p+"views/"+beta+"/", map[string]any{"filters": map[string]any{}, "display_filters": map[string]any{"layout": "list"}})
		alice.Patch(p+"views/"+beta+"/", map[string]any{"name": ""})
		alice.Patch(p+"views/"+beta+"/", []any{})
		alice.Patch(p+"views/"+beta+"/", map[string]any{"filters": "name"})
		bob.Patch(p+"views/"+alpha+"/", map[string]any{"name": "Bob was here"})
		carol.Patch(p+"views/"+carols+"/", map[string]any{"name": "Carol's 2"})
		outsider.Patch(p+"views/"+outsiders+"/", map[string]any{"name": "Outsider's 2"})
		alice.Patch(p+"views/"+dead+"/", map[string]any{"name": "x"})
		alice.Patch(po+"views/"+beta+"/", map[string]any{"name": "x"})
		// created_by is writable: beta's creator becomes bob, its owner stays alice.
		alice.Patch(p+"views/"+beta+"/", map[string]any{"created_by": b, "updated_by": b})
		alice.Patch(p+"views/"+beta+"/", map[string]any{"name": "x"})
		bob.Patch(p+"views/"+beta+"/", map[string]any{"name": "x"})
		s.DBStrings(`UPDATE issue_views SET is_locked = true WHERE id = $1 RETURNING id::text`, carols)
		carol.Patch(p+"views/"+carols+"/", map[string]any{"name": "Locked"})

		// Deletes: the creator or a project admin; only the owner or an admin
		// gets through.
		bob.Delete(p + "views/" + alpha + "/")
		bob.Delete(p + "views/" + beta + "/")
		carol.Delete(p + "views/" + carols + "/")
		outsider.Delete(p + "views/" + outsiders + "/")
		alice.Delete(p + "views/" + dead + "/")
		alice.Delete(po + "views/" + alpha + "/")
		alice.Delete(p + "views/" + alpha + "/")
		alice.Delete(p + "views/" + alpha + "/")
		alice.Get(p + "views/")
		viewRows(s)

		// Workspace views.
		w1 := alice.Post(ws+"views/", map[string]any{"name": "All urgent", "filters": map[string]any{"priority": []any{"urgent"}},
			"project": pl, "sort_order": 3}).String("id")
		w2 := bob.Post(ws+"views/", map[string]any{"name": "Bob's board", "display_filters": map[string]any{"layout": "spreadsheet"}}).String("id")
		w3 := carol.Post(ws+"views/", map[string]any{"name": "Carol's list"}).String("id")
		w4 := outsider.Post(ws+"views/", map[string]any{"name": "Gatecrasher"}).String("id")
		alice.Post("/api/workspaces/nope/views/", map[string]any{"name": "Nowhere"})
		alice.Post("/api/workspaces/nope/views/", map[string]any{})
		alice.Post(ws+"views/", map[string]any{"name": "x", "filters": []any{"state"}})
		anon.Post(ws+"views/", map[string]any{"name": "Anon"})
		s.DBStrings(`UPDATE issue_views SET access = 0 WHERE id = $1 RETURNING id::text`, w2)
		favoriteView(s, a, w1, "")

		for _, c := range []*Client{alice, bob, carol, dan, outsider, anon} {
			c.Get(ws + "views/")
		}
		for _, o := range []string{"name", "-name", "updated_at", "-created_at", "--name", "bogus", "sort_order"} {
			alice.Get(ws + "views/?order_by=" + url.QueryEscape(o))
		}

		// Retrieve has no role check at all.
		alice.Get(ws + "views/" + w1 + "/")
		dan.Get(ws + "views/" + w2 + "/")
		bob.Get(ws + "views/" + w2 + "/")
		outsider.Get(ws + "views/" + w3 + "/")
		alice.Get(ws + "views/" + dead + "/")
		alice.Get(ws + "views/" + beta + "/")
		alice.Get("/api/workspaces/nope/views/" + w1 + "/")
		anon.Get(ws + "views/" + w1 + "/")

		alice.Patch(ws+"views/"+w1+"/", map[string]any{"name": "All urgent 2", "filters": map[string]any{"labels": []any{bug}}})
		bob.Patch(ws+"views/"+w1+"/", map[string]any{"name": "x"})
		carol.Patch(ws+"views/"+w3+"/", map[string]any{"name": "Carol's list 2", "rich_filters": map[string]any{"or": []any{}}})
		outsider.Patch(ws+"views/"+w4+"/", map[string]any{"name": "x"})
		alice.Patch(ws+"views/"+dead+"/", map[string]any{"name": "x"})
		// The workspace routes reach project views too.
		alice.Patch(ws+"views/"+beta+"/", map[string]any{"name": "x"})
		bob.Patch(ws+"views/"+beta+"/", map[string]any{"name": "x"})
		carol.Patch(ws+"views/"+w3+"/", map[string]any{"name": ""})

		bob.Delete(ws + "views/" + w1 + "/")
		carol.Delete(ws + "views/" + w3 + "/")
		outsider.Delete(ws + "views/" + w4 + "/")
		alice.Delete(ws + "views/" + w4 + "/")
		alice.Delete(ws + "views/" + dead + "/")
		bob.Delete(ws + "views/" + w2 + "/")
		alice.Delete(ws + "views/" + beta + "/")
		alice.Delete(ws + "views/" + w1 + "/")
		alice.Get(ws + "views/")
		alice.Get(p + "views/")

		// An archived project hides its views.
		alice.Post(p+"archive/", nil, Mask("archived_at"))
		alice.Get(p + "views/")
		alice.Get(p + "views/" + carols + "/")
		carol.Get(p + "views/" + carols + "/")
		viewRows(s)
	})
}

// viewInsertModule adds a module (archived or not) holding issues. The
// module endpoints belong to another batch, so the rows are written
// directly; both targets run the same INSERTs.
func viewInsertModule(s *Scenario, project, name string, archived bool, issues ...string) {
	mod := s.DBStrings(`INSERT INTO modules (id, created_at, updated_at, name, description, status, project_id, workspace_id,
			view_props, sort_order, logo_props, archived_at)
		SELECT gen_random_uuid(), clock_timestamp(), clock_timestamp(), $2, '', 'planned', p.id, p.workspace_id, '{}', 65535, '{}',
			CASE WHEN $3 THEN clock_timestamp() END
		FROM projects p WHERE p.id = $1::uuid RETURNING id::text`, project, name, archived)[0]
	for _, issue := range issues {
		s.DBStrings(`INSERT INTO module_issues (id, created_at, updated_at, issue_id, module_id, project_id, workspace_id)
			SELECT gen_random_uuid(), clock_timestamp(), clock_timestamp(), i.id, $2::uuid, i.project_id, i.workspace_id
			FROM issues i WHERE i.id = $1::uuid RETURNING id::text`, issue, mod)
	}
}

// viewInsertCycle adds a cycle holding issues (cycle endpoints belong to
// another batch).
func viewInsertCycle(s *Scenario, project, owner, name string, issues ...string) {
	cyc := s.DBStrings(`INSERT INTO cycles (id, created_at, updated_at, name, description, owned_by_id, project_id, workspace_id,
			view_props, sort_order, progress_snapshot, logo_props, timezone, version)
		SELECT gen_random_uuid(), clock_timestamp(), clock_timestamp(), $3, '', $2::uuid, p.id, p.workspace_id, '{}', 65535, '{}',
			'{}', 'UTC', 1
		FROM projects p WHERE p.id = $1::uuid RETURNING id::text`, project, owner, name)[0]
	for _, issue := range issues {
		s.DBStrings(`INSERT INTO cycle_issues (id, created_at, updated_at, issue_id, cycle_id, project_id, workspace_id)
			SELECT gen_random_uuid(), clock_timestamp(), clock_timestamp(), i.id, $2::uuid, i.project_id, i.workspace_id
			FROM issues i WHERE i.id = $1::uuid RETURNING id::text`, issue, cyc)
	}
}

func TestWorkspaceViewIssues(t *testing.T) {
	Run(t, "workspace_view_issues", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "112")
		anon := s.Client("anon")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		a, b, d := userID(alice), userID(bob), userID(dan)

		// SE shares everything with guests; bob was removed from it.
		se := alice.Post(ws+"projects/", map[string]any{"name": "Second", "identifier": "SE"}, projMask).String("id")
		ps := ws + "projects/" + se + "/"
		alice.Post(ps+"members/", map[string]any{"members": []any{
			map[string]any{"member_id": d, "role": 15},
			map[string]any{"member_id": userID(carol), "role": 5},
			map[string]any{"member_id": b, "role": 15},
		}})
		alice.Patch(ps, map[string]any{"guest_view_all_features": true}, projMask)
		bobSE := idOf(findBy(alice.Get(ps+"members/"), "member", b))
		alice.Delete(ps + "members/" + bobSE + "/")
		// dan files an issue in PL as a member, then becomes a guest there.
		alice.Post(p+"members/", map[string]any{"members": []any{map[string]any{"member_id": d, "role": 15}}})

		states := alice.Get(p + "states/")
		todo := idOf(findBy(states, "name", "Todo"))
		started := idOf(findBy(states, "name", "In Progress"))
		done := idOf(findBy(states, "name", "Done"))
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")
		feature := alice.Post(p+"issue-labels/", map[string]any{"name": "Feature"}).String("id")
		ops := alice.Post(ps+"issue-labels/", map[string]any{"name": "Ops"}).String("id")

		// The issue create path validates assignees with an unordered
		// ProjectMember query, so the order bulk_create inserts them in (and
		// the prefetch returns them in, newest first) follows the plan; its
		// own response sorts them by (random) id.
		ids := Unordered("*.assignee_ids", "assignee_ids", "label_ids")
		issue := func(c *Client, proj string, body map[string]any) string {
			return c.Post(ws+"projects/"+proj+"/issues/", body, ids).String("id")
		}
		alpha := issue(alice, pl, map[string]any{"name": "Alpha", "state_id": todo, "priority": "high",
			"label_ids": []any{bug, feature}, "assignee_ids": []any{a, b}, "start_date": "2026-10-01", "target_date": "2026-10-20"})
		bravo := issue(bob, pl, map[string]any{"name": "Bravo", "state_id": started, "priority": "urgent", "label_ids": []any{bug},
			"assignee_ids": []any{b}, "target_date": "2026-10-25"})
		charlie := issue(alice, pl, map[string]any{"name": "Charlie"})
		delta := issue(alice, pl, map[string]any{"name": "Delta", "state_id": done, "priority": "low", "label_ids": []any{bug},
			"assignee_ids": []any{a}})
		alice.Patch(p+"issues/"+delta+"/", map[string]any{"label_ids": []any{}, "assignee_ids": []any{}})
		issue(alice, pl, map[string]any{"name": "Echo", "parent_id": alpha, "state_id": todo, "priority": "medium",
			"assignee_ids": []any{a}, "label_ids": []any{feature}, "description_html": "<p>" + mention(bob) + "</p>"})
		issue(dan, pl, map[string]any{"name": "Dan in PL", "priority": "none"})
		gone := issue(alice, pl, map[string]any{"name": "Gone"})
		alice.Delete(p + "issues/" + gone + "/")
		alice.Post(p+"issues/", map[string]any{"name": "Drafted", "is_draft": true})
		alice.Post(p+"issues/", map[string]any{"name": "Shelved", "archived_at": "2026-01-01", "parent_id": alpha, "state_id": done})
		issue(alice, se, map[string]any{"name": "Sierra", "priority": "low", "label_ids": []any{ops}, "assignee_ids": []any{d}})
		issue(dan, se, map[string]any{"name": "Tango", "priority": "medium", "start_date": "2026-09-01"})
		alice.Post(p+"issues/"+alpha+"/issue-links/", map[string]any{"url": "https://example.com/spec", "title": "Spec"})
		pmDan := idOf(findBy(alice.Get(p+"members/"), "member", d))
		alice.Patch(p+"members/"+pmDan+"/", map[string]any{"role": 5})

		// An archived project's issues drop out.
		ar := alice.Post(ws+"projects/", map[string]any{"name": "Attic", "identifier": "AR"}, projMask).String("id")
		issue(alice, ar, map[string]any{"name": "Attic issue"})
		alice.Post(ws+"projects/"+ar+"/archive/", nil, Mask("archived_at"))

		viewInsertModule(s, pl, "Core", false, alpha, charlie)
		viewInsertModule(s, pl, "Legacy", true, bravo)
		viewInsertCycle(s, pl, a, "Sprint 1", alpha, bravo)
		s.DBStrings(`UPDATE module_issues SET deleted_at = clock_timestamp() WHERE issue_id = $1 RETURNING id::text`, charlie)

		list := func(c *Client, q string) *Response { return c.Get(ws+"issues/"+q, ids) }

		// Each role sees the projects it is an active member of; guests of
		// a project that doesn't share everything see their own issues.
		for _, c := range []*Client{alice, bob, carol, dan, outsider, anon} {
			list(c, "")
		}
		// As the spreadsheet layout asks.
		list(alice, "?"+richFilter(map[string]any{})+"&layout=spreadsheet&cursor=100:0:0&per_page=100&order_by=-created_at")
		// Grouping is not passed to the paginator: always a flat list.
		list(alice, "?group_by=state_id&sub_group_by=priority&per_page=3")
		list(alice, "?group_by=bogus")

		// Pagination.
		list(alice, "?per_page=3")
		list(alice, "?per_page=3&cursor=3:1:0")
		list(alice, "?per_page=3&cursor=3:3:0")
		list(alice, "?per_page=3&cursor=3:1:1")
		list(alice, "?per_page=3&cursor=2:1:0")
		list(alice, "?per_page=0")
		list(alice, "?per_page=1001")
		list(alice, "?per_page=x")
		list(alice, "?cursor=x")
		list(alice, "?cursor=2:-1:0")

		for _, o := range []string{"sort_order", "-updated_at", "start_date", "-target_date", "priority", "-priority",
			"state__group", "-state__group", "state__name", "labels__name", "-labels__name", "assignees__first_name",
			"-assignees__first_name", "issue_module__module__name", "sequence_id", "completed_at", "--created_at", "bogus"} {
			list(alice, "?order_by="+o)
		}
		list(alice, "?order_by=labels__name&per_page=2&cursor=2:1:0")

		// Rich filters; joins to several matching relation rows repeat an
		// issue, since nothing makes the query distinct.
		for _, f := range []any{
			map[string]any{"label_id__in": bug + "," + feature},
			map[string]any{"assignee_id__in": a + "," + b},
			map[string]any{"and": []any{map[string]any{"label_id__in": bug}, map[string]any{"label_id__in": feature}}},
			map[string]any{"project_id__in": se},
			map[string]any{"state_group__in": "unstarted,started"},
			map[string]any{"or": []any{map[string]any{"priority": "low"}, map[string]any{"module_id__in": dead}}},
			map[string]any{"not": map[string]any{"label_id__in": bug}},
			map[string]any{"mention_id__in": b},
			map[string]any{"subscriber_id__in": d},
			map[string]any{"is_archived": "true"},
		} {
			list(alice, "?"+richFilter(f))
		}
		list(alice, "?order_by=labels__name&"+richFilter(map[string]any{"label_id__in": bug + "," + feature}))
		list(alice, "?per_page=2&"+richFilter(map[string]any{"assignee_id__in": a + "," + b}))
		for _, bad := range []string{`{"name": "x"}`, `{"priority__in": "nope"}`, `{bad`, `[]`, `{"and": []}`} {
			list(alice, "?filters="+url.QueryEscape(bad))
		}

		// Legacy filters.
		for _, q := range []string{
			"labels=" + bug + "," + feature, "labels=None", "assignees=" + a + "," + b, "assignees=None", "priority=high,urgent",
			"state=" + todo, "state_group=started", "project=" + se, "created_by=" + d, "sub_issue=false", "type=backlog",
			"name=ha", "cycle=None", "module=None", "mentions=" + b, "subscriber=" + b, "start_target_date=true",
			"target_date=2026-10-01;after,2026-10-31;before", "start_date=2026-09-15;before", "parent=" + alpha,
			"updated_at__gt=2000-01-01T00:00:00Z",
		} {
			list(alice, "?"+q)
		}
		list(carol, "?labels="+bug)
		list(dan, "?priority=none,low,medium")
	})
}
