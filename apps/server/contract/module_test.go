package contract

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// moduleRows records the module tables, by name and email rather than id.
func moduleRows(s *Scenario) {
	s.DBRows("modules", `SELECT m.name, p.identifier AS project, m.description, m.description_text, m.description_html,
			m.start_date::text, m.target_date::text, m.status, l.email AS lead, m.view_props, m.sort_order,
			m.external_source, m.external_id, m.archived_at IS NOT NULL AS archived, m.logo_props,
			cb.email AS created_by, ub.email AS updated_by, m.workspace_id = p.workspace_id AS in_workspace,
			m.deleted_at IS NULL AS live, m.updated_at >= m.created_at AS touched
		FROM modules m JOIN projects p ON p.id = m.project_id LEFT JOIN users l ON l.id = m.lead_id
		LEFT JOIN users cb ON cb.id = m.created_by_id LEFT JOIN users ub ON ub.id = m.updated_by_id
		ORDER BY m.name, m.deleted_at IS NULL`)
	s.DBRows("module_members", `SELECT m.name AS module, u.email, cb.email AS created_by, ub.email AS updated_by,
			x.project_id = m.project_id AS in_project, x.workspace_id = m.workspace_id AS in_workspace,
			x.deleted_at IS NULL AS live
		FROM module_members x JOIN modules m ON m.id = x.module_id JOIN users u ON u.id = x.member_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY m.name, u.email, x.deleted_at IS NULL`)
	s.DBRows("module_issues", `SELECT m.name AS module, i.name AS issue, cb.email AS created_by, ub.email AS updated_by,
			x.project_id = m.project_id AS in_project, x.workspace_id = m.workspace_id AS in_workspace,
			x.deleted_at IS NULL AS live
		FROM module_issues x JOIN modules m ON m.id = x.module_id JOIN issues i ON i.id = x.issue_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY m.name, i.name, x.deleted_at IS NULL`)
	s.DBRows("module_links", `SELECT m.name AS module, x.title, x.url, x.metadata, cb.email AS created_by,
			ub.email AS updated_by, x.project_id = m.project_id AS in_project,
			x.workspace_id = m.workspace_id AS in_workspace, x.deleted_at IS NULL AS live
		FROM module_links x JOIN modules m ON m.id = x.module_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY m.name, x.url, x.deleted_at IS NULL`)
	s.DBRows("module_user_properties", `SELECT m.name AS module, u.email, x.filters, x.display_filters,
			x.display_properties, x.rich_filters, cb.email AS created_by, ub.email AS updated_by,
			x.project_id = m.project_id AS in_project, x.workspace_id = m.workspace_id AS in_workspace,
			x.deleted_at IS NULL AS live
		FROM module_user_properties x JOIN modules m ON m.id = x.module_id JOIN users u ON u.id = x.user_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY m.name, u.email, x.deleted_at IS NULL`)
	s.DBRows("module_favorites", `SELECT u.email, m.name AS module, f.deleted_at IS NULL AS live
		FROM user_favorites f JOIN users u ON u.id = f.user_id LEFT JOIN modules m ON m.id = f.entity_identifier
		WHERE f.entity_type = 'module' ORDER BY u.email, m.name, f.deleted_at IS NULL`)
	s.DBRows("module_recent_visits", `SELECT u.email, m.name AS module, v.deleted_at IS NULL AS live
		FROM user_recent_visits v JOIN users u ON u.id = v.user_id LEFT JOIN modules m ON m.id = v.entity_identifier
		WHERE v.entity_name = 'module' ORDER BY u.email, m.name`)
}

// moduleDay is a date n days from today (UTC), aliased so goldens don't
// depend on the day they run.
func moduleDay(s *Scenario, n int) string {
	d := time.Now().UTC().AddDate(0, 0, n).Format(time.DateOnly)
	s.Alias(d, fmt.Sprintf("<today%+d>", n))
	return d
}

// moduleFavorite stars a module for a user. The favorites endpoints are
// another area's, so the row is written directly (identically in both
// modes).
func moduleFavorite(s *Scenario, c *Client, module string) {
	s.DBStrings(`INSERT INTO user_favorites (id, created_at, updated_at, entity_type, entity_identifier, is_folder,
			sequence, user_id, workspace_id, project_id, created_by_id)
		SELECT gen_random_uuid(), now(), now(), 'module', m.id, false, 65535, u.id, m.workspace_id, m.project_id, u.id
		FROM modules m, users u WHERE m.id = $1 AND u.email = $2
		RETURNING id::text`, module, c.name+"@example.com")
}

// moduleFixture is the project the module scenarios share: labelled,
// assigned and estimated issues in every state group.
type moduleFixture struct {
	alice, bob, carol, dan, outsider *Client
	pl, ws, p                        string
	issues                           map[string]string
}

func newModuleFixture(s *Scenario, subnet string) *moduleFixture {
	f := &moduleFixture{ws: "/api/workspaces/acme/", issues: map[string]string{}}
	f.alice, f.bob, f.carol, f.dan, f.outsider, f.pl = projectTeam(s, subnet)
	f.p = f.ws + "projects/" + f.pl + "/"
	// Distinct first names keep the assignee distributions' order total.
	for _, c := range []*Client{f.alice, f.bob, f.carol} {
		c.Patch("/api/users/me/", map[string]any{"first_name": strings.ToUpper(c.name[:1]) + c.name[1:]}, Mask("token"))
	}
	states := f.alice.Get(f.p + "states/")
	state := func(name string) string { return idOf(findBy(states, "name", name)) }
	bug := f.alice.Post(f.p+"issue-labels/", map[string]any{"name": "Bug", "color": "#ff0000"}).String("id")
	feature := f.alice.Post(f.p+"issue-labels/", map[string]any{"name": "Feature", "color": "#00ff00"}).String("id")
	fib := f.alice.Post(f.p+"estimates/", map[string]any{
		"estimate":        map[string]any{"name": "Fibonacci", "type": "points"},
		"estimate_points": []any{map[string]any{"key": 1, "value": "1"}, map[string]any{"key": 2, "value": "2"}, map[string]any{"key": 3, "value": "5"}},
	}).String("id")
	f.alice.Patch(f.p, map[string]any{"estimate": fib}, projMask)
	point := func(v string) string {
		return s.DBStrings(`SELECT id::text FROM estimate_points WHERE value = $1 AND estimate_id = $2`, v, fib)[0]
	}
	a, b := userID(f.alice), userID(f.bob)
	// The issue id arrays are array_agg(DISTINCT id): their order follows
	// the random ids.
	ids := Unordered("label_ids", "assignee_ids", "module_ids")
	issue := func(name string, body map[string]any) {
		body["name"] = name
		f.issues[name] = f.alice.Post(f.p+"issues/", body, ids).String("id")
	}
	issue("One", map[string]any{"state_id": state("Todo"), "assignee_ids": []any{a, b}, "label_ids": []any{bug},
		"estimate_point": point("2")})
	issue("Two", map[string]any{"state_id": state("In Progress"), "assignee_ids": []any{b}, "label_ids": []any{feature},
		"estimate_point": point("5")})
	issue("Three", map[string]any{"state_id": state("Done"), "assignee_ids": []any{a}, "label_ids": []any{bug, feature},
		"estimate_point": point("1")})
	issue("Four", map[string]any{"state_id": state("Cancelled"), "estimate_point": point("2")})
	issue("Five", map[string]any{"state_id": state("Backlog"), "parent_id": f.issues["One"]})
	issue("Six", map[string]any{"state_id": state("Done"), "assignee_ids": []any{b}})
	issue("Outside", map[string]any{})
	// Archived issues and drafts are not issue_objects.
	f.alice.Post(f.p+"issues/", map[string]any{"name": "Shelved", "archived_at": "2026-01-01", "state_id": state("Done")})
	f.alice.Post(f.p+"issues/", map[string]any{"name": "Drafted", "is_draft": true, "assignee_ids": []any{a}})
	f.issues["Shelved"] = s.DBStrings(`SELECT id::text FROM issues WHERE name = 'Shelved'`)[0]
	f.issues["Drafted"] = s.DBStrings(`SELECT id::text FROM issues WHERE name = 'Drafted'`)[0]
	// Bob comes off Two: the distributions join issue_assignees without
	// looking at deleted_at.
	f.alice.Patch(f.p+"issues/"+f.issues["Two"]+"/", map[string]any{"assignee_ids": []any{}, "label_ids": []any{}})
	return f
}

func TestModules(t *testing.T) {
	Run(t, "modules", func(s *Scenario) {
		f := newModuleFixture(s, "90")
		alice, bob, carol, dan, outsider := f.alice, f.bob, f.carol, f.dan, f.outsider
		p := f.p
		mods := p + "modules/"
		mod := func(id string) string { return mods + id + "/" }
		// member_ids is array_agg(DISTINCT member id): its order follows the
		// random user ids.
		ids := Unordered("*.member_ids")
		a, b, c := userID(alice), userID(bob), userID(carol)
		start, target := moduleDay(s, -3), moduleDay(s, 3)
		moduleDay(s, -2)
		moduleDay(s, -1)
		moduleDay(s, 0)
		moduleDay(s, 1)
		moduleDay(s, 2)

		// Create: project admins and members.
		carol.Post(mods, map[string]any{"name": "Guest"}, ids)
		dan.Post(mods, map[string]any{"name": "Dan"}, ids)
		outsider.Post(mods, map[string]any{"name": "Outsider"}, ids)
		alice.Post(mods, map[string]any{}, ids)
		alice.Post(mods, []any{"x"}, ids)
		alice.Post(mods, map[string]any{"name": strings.Repeat("x", 256), "status": "nope", "start_date": "tomorrow",
			"target_date": "2026-10-01T00:00:00Z", "lead_id": "x", "member_ids": "x"}, ids)
		alice.Post(mods, map[string]any{"name": "Bad members", "member_ids": []any{a, "x", dead, nil, true}}, ids)
		alice.Post(mods, map[string]any{"name": "Bad lead", "lead_id": dead, "lead": dead}, ids)
		alice.Post(mods, map[string]any{"name": "Backwards", "start_date": target, "target_date": start}, ids)
		alice.Post(mods, map[string]any{"name": "", "view_props": nil, "logo_props": nil, "sort_order": "x"}, ids)
		alpha := alice.Post(mods, map[string]any{"name": "Alpha", "description": "first", "lead_id": b,
			"member_ids": []any{a, b, a}, "start_date": start, "target_date": target, "status": "in-progress",
			"description_text": map[string]any{"t": 1}, "description_html": "<p>x</p>"}, ids).String("id")
		alice.Post(mods, map[string]any{"name": "Alpha"}, ids)
		beta := bob.Post(mods, map[string]any{"name": "Beta", "status": "completed", "member_ids": []any{c},
			"lead": a, "sort_order": 12.5, "external_source": "jira", "external_id": "J-1", "view_props": map[string]any{"v": 1},
			"logo_props": map[string]any{"in_use": "emoji"}, "id": dead, "archived_at": "2026-01-01T00:00:00Z",
			"project": dead, "created_by": c, "deleted_at": "2026-01-01T00:00:00Z"}, ids).String("id")
		gamma := alice.Post(mods, map[string]any{"name": "Gamma", "start_date": start, "member_ids": []any{}}, ids).String("id")
		delta := alice.Post(mods, map[string]any{"name": "Delta", "start_date": start, "target_date": target}, ids).String("id")

		// Issues go into Alpha (another module's endpoints; counts need them).
		var all []any
		for _, n := range []string{"One", "Two", "Three", "Four", "Five", "Six", "Shelved", "Drafted"} {
			all = append(all, f.issues[n])
		}
		alice.Post(mod(alpha)+"issues/", map[string]any{"issues": all})
		alice.Post(mod(beta)+"issues/", map[string]any{"issues": []any{f.issues["One"], f.issues["Three"]}})
		alice.Post(mod(delta)+"issues/", map[string]any{"issues": []any{f.issues["Two"]}})
		// Complete Two: Delta's chart counts it down from today.
		states := alice.Get(p + "states/")
		alice.Patch(p+"issues/"+f.issues["Two"]+"/", map[string]any{"state_id": idOf(findBy(states, "name", "Done"))})
		alice.Post(mod(alpha)+"module-links/", map[string]any{"url": "http://localhost/spec", "title": "Spec"})
		alice.Post(mod(alpha)+"module-links/", map[string]any{"url": "http://localhost/plan"})
		moduleFavorite(s, alice, gamma)
		moduleFavorite(s, bob, beta)

		// List: any project role.
		alice.Get(mods, ids)
		bob.Get(mods, ids)
		carol.Get(mods, ids)
		dan.Get(mods, ids)
		outsider.Get(mods, ids)
		alice.Get(mods+"?fields=id,name", ids)
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		bob.Get(mods, ids)

		// Retrieve: project admins and members.
		alice.Get(mod(alpha), ids)
		bob.Get(mod(gamma), ids)
		carol.Get(mod(alpha), ids)
		dan.Get(mod(alpha), ids)
		alice.Get(mod(dead), ids)
		alice.Get(mod(delta), ids)
		alice.Get(mod(beta), ids)
		// Without a points estimate there is no estimate distribution.
		alice.Patch(p, map[string]any{"estimate": nil}, projMask)
		alice.Get(mod(alpha), ids)
		alice.Get(mod(delta), ids)

		// The workspace list: modules of the user's projects.
		alice.Get(f.ws+"modules/", ids)
		carol.Get(f.ws+"modules/", ids)
		dan.Get(f.ws+"modules/", ids)
		outsider.Get(f.ws+"modules/", ids)

		// Update: project admins and members.
		carol.Patch(mod(alpha), map[string]any{"name": "Guest"}, ids)
		dan.Patch(mod(alpha), map[string]any{"name": "Dan"}, ids)
		alice.Patch(mod(dead), map[string]any{"name": "Nope"}, ids)
		alice.Patch(mod(alpha), []any{"x"}, ids)
		alice.Patch(mod(alpha), map[string]any{"name": "", "status": "nope", "member_ids": []any{"x"}}, ids)
		alice.Patch(mod(alpha), map[string]any{"name": "Beta"}, ids)
		alice.Patch(mod(alpha), map[string]any{"start_date": target, "target_date": start}, ids)
		alice.Patch(mod(alpha), map[string]any{"target_date": moduleDay(s, -5)}, ids)
		alice.Patch(mod(alpha), map[string]any{"target_date": target, "name": "Alpha", "member_ids": []any{b, c},
			"lead_id": nil, "description": "second", "status": "paused", "logo_props": map[string]any{"in_use": "icon"}}, ids)
		bob.Patch(mod(gamma), map[string]any{"member_ids": []any{a}, "lead": c, "sort_order": 1}, ids)
		alice.Patch(mod(gamma), map[string]any{}, ids)
		alice.Get(mods, ids)

		// User properties: any project role; GET creates the row.
		props := mod(alpha) + "user-properties/"
		alice.Patch(props, map[string]any{"filters": map[string]any{"priority": []any{"high"}}})
		alice.Get(props)
		alice.Get(props)
		alice.Patch(props, map[string]any{"filters": map[string]any{"priority": []any{"high"}}, "display_filters": "x",
			"display_properties": nil, "rich_filters": []any{1}, "user": dead, "module": dead})
		alice.Patch(props, map[string]any{})
		alice.Patch(props, []any{"x"})
		carol.Get(props)
		carol.Patch(props, map[string]any{"display_filters": map[string]any{"layout": "kanban"}})
		dan.Get(props)
		outsider.Patch(props, map[string]any{})
		alice.Get(mod(dead) + "user-properties/")

		// Delete: project admins, and the module's creator.
		carol.Delete(mod(alpha))
		dan.Delete(mod(alpha))
		bob.Delete(mod(alpha))
		bob.Delete(mod(beta))
		alice.Delete(mod(dead))
		alice.Delete(mod(alpha))
		alice.Delete(mod(alpha))
		alice.Get(mods, ids)
		alice.Get(f.ws+"modules/", ids)
		// array_agg(DISTINCT id) arrays, ordered by the random ids.
		alice.Get(p+"issues/"+f.issues["One"]+"/", Unordered("label_ids", "assignee_ids", "module_ids"))
		moduleRows(s)
		issueRows(s)
	})
}

func TestModuleIssues(t *testing.T) {
	Run(t, "module_issues", func(s *Scenario) {
		f := newModuleFixture(s, "91")
		alice, bob, carol, dan, outsider := f.alice, f.bob, f.carol, f.dan, f.outsider
		p, is := f.p, f.issues
		mods := p + "modules/"
		mod := func(id string) string { return mods + id + "/" }
		// array_agg(DISTINCT id) arrays (member_ids, the issue id arrays):
		// their order follows the random ids.
		ids := Unordered("*.member_ids")
		issueIDs := Unordered("*.label_ids", "*.assignee_ids", "*.module_ids")
		alpha := alice.Post(mods, map[string]any{"name": "Alpha"}, ids).String("id")
		beta := bob.Post(mods, map[string]any{"name": "Beta"}, ids).String("id")
		gone := alice.Post(mods, map[string]any{"name": "Gone"}, ids).String("id")
		ot := alice.Post(f.ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		otIssue := alice.Post(f.ws+"projects/"+ot+"/issues/", map[string]any{"name": "Elsewhere"}, issueIDs).String("id")

		// Add issues to a module: project admins and members; only the
		// project's issue_objects are linked.
		add := mod(alpha) + "issues/"
		carol.Post(add, map[string]any{"issues": []any{is["One"]}})
		dan.Post(add, map[string]any{"issues": []any{is["One"]}})
		outsider.Post(add, map[string]any{"issues": []any{is["One"]}})
		alice.Post(add, map[string]any{})
		alice.Post(add, []any{"x"})
		alice.Post(add, map[string]any{"issues": []any{}})
		alice.Post(add, map[string]any{"issues": "x"})
		alice.Post(add, map[string]any{"issues": 5})
		alice.Post(add, map[string]any{"issues": []any{is["One"], is["Two"], is["Shelved"], is["Drafted"], otIssue, dead, nil}})
		alice.Post(add, map[string]any{"issues": []any{is["One"]}})
		bob.Post(add, map[string]any{"issues": map[string]any{is["Three"]: 1}})
		alice.Post(mod(dead)+"issues/", map[string]any{"issues": []any{is["Four"]}})
		alice.Post(mod(gone)+"issues/", map[string]any{"issues": []any{is["One"], is["Four"]}})

		// Set an issue's modules: added and removed in one call.
		one := p + "issues/" + is["One"] + "/modules/"
		carol.Post(one, map[string]any{"modules": []any{beta}})
		dan.Post(one, map[string]any{"modules": []any{beta}})
		alice.Post(one, []any{"x"})
		alice.Post(one, map[string]any{})
		alice.Post(one, map[string]any{"modules": []any{beta}, "removed_modules": []any{alpha}})
		// Already linked: still an activity (alice again, so the activity rows
		// tie only where they are identical).
		alice.Post(one, map[string]any{"modules": []any{beta}})
		alice.Post(one, map[string]any{"removed_modules": []any{dead}})
		alice.Post(one, map[string]any{"modules": []any{"x"}})
		alice.Post(one, map[string]any{"modules": []any{dead}})
		alice.Post(one, map[string]any{"modules": []any{nil}})
		alice.Post(one, map[string]any{"modules": 5})
		alice.Post(one, map[string]any{"removed_modules": "ab"})
		alice.Post(one, map[string]any{"removed_modules": nil})
		alice.Post(one, map[string]any{"removed_modules": []any{nil}})
		// Other spellings of Beta's id. The normalizer knows lowercase UUIDs
		// and hex, so the uppercase one is aliased.
		upper, hex := strings.ToUpper(beta), strings.ReplaceAll(beta, "-", "")
		s.Alias(upper, "<beta upper>")
		alice.Post(p+"issues/"+is["Five"]+"/modules/", map[string]any{"modules": []any{upper}, "removed_modules": []any{hex}})
		alice.Post(p+"issues/"+dead+"/modules/", map[string]any{"modules": []any{alpha}})
		alice.Post(p+"issues/"+otIssue+"/modules/", map[string]any{"modules": []any{alpha}})

		// Remove one issue from a module.
		carol.Delete(mod(alpha) + "issues/" + is["Two"] + "/")
		dan.Delete(mod(alpha) + "issues/" + is["Two"] + "/")
		alice.Delete(mod(alpha) + "issues/" + is["Two"] + "/")
		alice.Delete(mod(alpha) + "issues/" + is["Two"] + "/")
		alice.Delete(mod(dead) + "issues/" + is["Two"] + "/")
		bob.Delete(mod(beta) + "issues/" + is["One"] + "/")

		// A deleted module: its links go, and re-adding it names nothing.
		alice.Delete(mod(gone))
		alice.Post(one, map[string]any{"modules": []any{gone}})
		alice.Post(p+"issues/"+is["Four"]+"/modules/", map[string]any{"removed_modules": []any{gone}})

		alice.Get(mods, ids)
		alice.Get(f.ws+"modules/", ids)
		alice.Get(p+"issues/?module="+alpha, issueIDs)
		alice.Get(p+"issues/"+is["One"]+"/", issueIDs)
		moduleRows(s)
		issueRows(s)
	})
}

func TestModuleLinks(t *testing.T) {
	Run(t, "module_links", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "92")
		ws := "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		mods := p + "modules/"
		// member_ids is array_agg(DISTINCT member id): its order follows the
		// random user ids.
		ids := Unordered("*.member_ids")
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		alpha := alice.Post(mods, map[string]any{"name": "Alpha"}, ids).String("id")
		beta := alice.Post(mods, map[string]any{"name": "Beta"}, ids).String("id")
		links := mods + alpha + "/module-links/"
		link := func(id string) string { return links + id + "/" }

		// Create: project admins and members.
		docs := bob.Post(links, map[string]any{"url": "http://localhost/docs", "title": "Docs"}).String("id")
		carol.Post(links, map[string]any{"url": "http://localhost/guest"})
		dan.Post(links, map[string]any{"url": "http://localhost/dan"})
		outsider.Post(links, map[string]any{"url": "http://localhost/outsider"})
		alice.Post(links, map[string]any{})
		alice.Post(links, []any{"x"})
		alice.Post(links, map[string]any{"url": 5})
		alice.Post(links, map[string]any{"url": ""})
		alice.Post(links, map[string]any{"url": nil})
		alice.Post(links, map[string]any{"url": "not a url"})
		alice.Post(links, map[string]any{"url": "ftp://localhost/file"})
		alice.Post(links, map[string]any{"url": "http://localhost/" + strings.Repeat("x", 200)})
		alice.Post(links, map[string]any{"url": "http://localhost/docs"})
		alice.Post(links, map[string]any{"url": "http://localhost/long", "title": strings.Repeat("x", 256)})
		alice.Post(links, map[string]any{"url": "http://localhost/meta", "metadata": nil})
		bare := alice.Post(links, map[string]any{"url": "localhost/bare", "title": nil, "metadata": map[string]any{"a": 1}}).String("id")
		alice.Post(links, map[string]any{"url": "https://example.com/hidden", "title": "", "id": dead, "module": beta,
			"project": dead, "created_by": userID(bob), "deleted_at": "2030-01-01T00:00:00Z"})
		alice.Post(links, map[string]any{"url": "https://example.com/hidden"})
		alice.Post(mods+dead+"/module-links/", map[string]any{"url": "http://localhost/dead"})
		other := alice.Post(mods+beta+"/module-links/", map[string]any{"url": "http://localhost/docs"}).String("id")

		// Update: a body without a url always fails.
		bob.Patch(link(docs), map[string]any{"title": "Docs v2"})
		bob.Patch(link(docs), map[string]any{"url": "localhost/v2", "title": "Docs v2"})
		carol.Patch(link(docs), map[string]any{"url": "http://localhost/guest"})
		dan.Patch(link(docs), map[string]any{"url": "http://localhost/dan"})
		alice.Patch(link(docs), map[string]any{"url": "http://localhost/bare"})
		alice.Patch(link(docs), map[string]any{"url": "bad url"})
		alice.Patch(link(docs), map[string]any{"url": ""})
		alice.Patch(link(docs), []any{"x"})
		alice.Patch(link(dead), map[string]any{"url": "http://localhost/x"})
		alice.Patch(link(other), map[string]any{"url": "http://localhost/x"})
		alice.Patch(link(bare), map[string]any{"url": "http://localhost/bare", "metadata": map[string]any{"b": 2},
			"title": "Bare", "deleted_at": nil})
		alice.Get(mods+alpha+"/", ids)
		bob.Get(mods+alpha+"/", ids)

		// Delete.
		carol.Delete(link(docs))
		dan.Delete(link(docs))
		bob.Delete(link(docs))
		bob.Delete(link(docs))
		alice.Delete(link(dead))
		alice.Delete(link(other))
		alice.Get(mods+alpha+"/", ids)
		moduleRows(s)
	})
}

func TestModuleArchive(t *testing.T) {
	Run(t, "module_archive", func(s *Scenario) {
		f := newModuleFixture(s, "93")
		alice, bob, carol, dan, outsider := f.alice, f.bob, f.carol, f.dan, f.outsider
		p, is := f.p, f.issues
		mods := p + "modules/"
		mod := func(id string) string { return mods + id + "/" }
		archived := p + "archived-modules/"
		// member_ids is array_agg(DISTINCT member id): its order follows the
		// random user ids.
		ids := Unordered("*.member_ids")
		// archived_at is str(timezone.now()) at the request.
		stamp := Mask("archived_at")
		a, b := userID(alice), userID(bob)
		start, target := moduleDay(s, -2), moduleDay(s, 2)
		moduleDay(s, -1)
		moduleDay(s, 0)
		moduleDay(s, 1)
		alpha := alice.Post(mods, map[string]any{"name": "Alpha", "status": "completed", "start_date": start,
			"target_date": target, "member_ids": []any{a, b}}, ids).String("id")
		beta := alice.Post(mods, map[string]any{"name": "Beta", "status": "in-progress"}, ids).String("id")
		gamma := bob.Post(mods, map[string]any{"name": "Gamma", "status": "cancelled"}, ids).String("id")
		alice.Post(mod(alpha)+"issues/", map[string]any{"issues": []any{is["One"], is["Two"], is["Three"], is["Four"], is["Five"]}})
		alice.Post(mod(gamma)+"issues/", map[string]any{"issues": []any{is["Six"]}})
		alice.Post(mod(alpha)+"module-links/", map[string]any{"url": "http://localhost/spec"})
		// The archive view keeps removed members and, for points, removed
		// issues' labels.
		alice.Patch(mod(alpha), map[string]any{"member_ids": []any{b}}, ids)
		alice.Delete(mod(alpha) + "issues/" + is["Three"] + "/")
		moduleFavorite(s, alice, alpha)
		moduleFavorite(s, bob, alpha)
		moduleFavorite(s, bob, gamma)

		// Archive: project admins and members, completed or cancelled
		// modules only.
		alice.Get(archived, ids)
		carol.Post(mod(alpha)+"archive/", nil, stamp)
		dan.Post(mod(alpha)+"archive/", nil, stamp)
		alice.Post(mod(dead)+"archive/", nil, stamp)
		alice.Post(mod(beta)+"archive/", nil, stamp)
		alice.Post(mod(alpha)+"archive/", nil, stamp)
		alice.Post(mod(alpha)+"archive/", map[string]any{"archived_at": "2020-01-01"}, stamp)
		bob.Post(mod(gamma)+"archive/", nil, stamp)
		moduleFavorite(s, alice, gamma)

		// Archived reads: any project role.
		alice.Get(archived, ids)
		bob.Get(archived, ids)
		carol.Get(archived, ids)
		dan.Get(archived, ids)
		outsider.Get(archived, ids)
		alice.Get(archived+alpha+"/", ids)
		carol.Get(archived+gamma+"/", ids)
		alice.Get(archived+beta+"/", ids)
		alice.Get(archived+dead+"/", ids)
		alice.Patch(p, map[string]any{"estimate": nil}, projMask)
		alice.Get(archived+alpha+"/", ids)

		// Archived modules leave the live views.
		alice.Get(mods, ids)
		alice.Get(mod(alpha), ids)
		alice.Patch(mod(alpha), map[string]any{"name": "Renamed"}, ids)
		alice.Get(f.ws+"modules/", ids)
		// array_agg(DISTINCT id) arrays, ordered by the random ids.
		alice.Get(p+"issues/"+is["One"]+"/", Unordered("label_ids", "assignee_ids", "module_ids"))

		// Unarchive.
		carol.Delete(mod(alpha) + "archive/")
		dan.Delete(mod(alpha) + "archive/")
		alice.Delete(mod(dead) + "archive/")
		alice.Delete(mod(beta) + "archive/")
		alice.Delete(mod(alpha) + "archive/")
		alice.Get(mods, ids)
		alice.Get(archived, ids)
		moduleRows(s)
	})
}
