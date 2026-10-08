package contract

import (
	"net/url"
	"testing"
	"time"
)

// searchFixture is the data the search scenarios look through: workspace
// acme with project PL (projectTeam), a secret project OT (alice, dan), a
// public project PU (alice only), an archived project AR, a second workspace,
// and issues, cycles, modules, views and pages in every state the search
// filters care about (deleted, archived, drafts, triage, other projects).
type searchFixture struct {
	alice, bob, carol, dan, erin, frank, outsider, anon *Client
	ws, p, pl, ot                                       string
	issues                                              map[string]string
	module                                              string
}

// searchQuery builds a query string, escaped as a browser would.
func searchQuery(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return "?" + v.Encode()
}

func newSearchFixture(s *Scenario, subnet string) *searchFixture {
	f := &searchFixture{ws: "/api/workspaces/acme/", issues: map[string]string{}}
	f.alice, f.bob, f.carol, f.dan, f.outsider, f.pl = projectTeam(s, subnet)
	f.anon = s.Client("anon")
	f.p = f.ws + "projects/" + f.pl + "/"
	ws, p := f.ws, f.p
	alice := f.alice
	day := cycleDays(s, time.UTC)
	// Issue create answers carry id arrays in uuid order, which varies run
	// to run.
	ids := Unordered("label_ids", "assignee_ids", "module_ids")

	// Erin joins PL and is removed again (is_active false); robot is a bot
	// member of the workspace and of PL.
	f.erin = s.ClientFrom("erin", "10.0."+subnet+".6")
	signUp(f.erin, "erin@example.com", strongPassword)
	robot := s.ClientFrom("robot", "10.0."+subnet+".7")
	signUp(robot, "robot@example.com", strongPassword)
	// Frank joins the workspace and leaves it again.
	f.frank = s.ClientFrom("frank", "10.0."+subnet+".8")
	signUp(f.frank, "frank@example.com", strongPassword)
	addToWorkspace(s, alice, "acme", []*Client{f.erin, robot, f.frank},
		[]string{"erin@example.com", "robot@example.com", "frank@example.com"}, []int{15, 15, 15})
	f.frank.Post(ws+"members/leave/", nil)
	alice.Post(p+"members/", map[string]any{"members": []any{
		map[string]any{"member_id": userID(f.erin), "role": 15},
		map[string]any{"member_id": userID(robot), "role": 15},
	}})
	erinPM := s.DBStrings(`SELECT pm.id::text FROM project_members pm JOIN users u ON u.id = pm.member_id
		WHERE u.email = 'erin@example.com' AND pm.project_id = $1`, f.pl)[0]
	alice.Delete(p + "members/" + erinPM + "/")
	// There is no endpoint that makes a bot; this is the column Django reads.
	s.DBStrings(`UPDATE users SET is_bot = true WHERE email = 'robot@example.com' RETURNING id::text`)

	// Names and avatars for the mention search. Bob's avatar is an uploaded
	// asset; file assets are a later batch, so the row is written directly.
	alice.Patch("/api/users/me/", map[string]any{"first_name": "Alice", "last_name": "Anders",
		"avatar": "https://img.example.com/alice.png"}, Mask("token"))
	f.bob.Patch("/api/users/me/", map[string]any{"first_name": "Bob", "last_name": "O'Brien"}, Mask("token"))
	f.carol.Patch("/api/users/me/", map[string]any{"first_name": "Carol", "last_name": "100% Real"}, Mask("token"))
	s.DBStrings(`WITH a AS (
			INSERT INTO file_assets (id, created_at, updated_at, attributes, asset, is_deleted, is_archived, is_uploaded, size,
				entity_type, user_id)
			SELECT gen_random_uuid(), now(), now(), '{}', 'avatar.png', false, false, true, 1, 'USER_AVATAR', u.id
			FROM users u WHERE u.email = 'bob@example.com' RETURNING id, user_id)
		UPDATE users SET avatar_asset_id = a.id FROM a WHERE users.id = a.user_id RETURNING users.id::text`)

	// A second workspace of alice's.
	createWorkspace(alice, "Beta 100%", "beta-ws")

	// OT is secret (network 0), PU public, AR archived at the end.
	f.ot = alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT", "network": 0}, projMask).String("id")
	alice.Post(ws+"projects/"+f.ot+"/members/", map[string]any{"members": []any{
		map[string]any{"member_id": userID(f.dan), "role": 15},
	}})
	pu := alice.Post(ws+"projects/", map[string]any{"name": "Public", "identifier": "PU", "network": 2}, projMask).String("id")
	ar := alice.Post(ws+"projects/", map[string]any{"name": "Archived", "identifier": "AR"}, projMask).String("id")

	states := alice.Get(p + "states/")
	issue := func(c *Client, name string, body map[string]any) string {
		body["name"] = name
		f.issues[name] = c.Post(p+"issues/", body, ids).String("id")
		return f.issues[name]
	}
	one := issue(alice, "Fix login bug", map[string]any{"target_date": "2030-01-15", "start_date": "2030-01-01",
		"priority": "high"})
	issue(alice, "Ship 100% coverage", map[string]any{"state_id": idOf(findBy(states, "name", "In Progress"))})
	issue(alice, "under_score task", map[string]any{})
	issue(alice, `Quote "double" and 'single'`, map[string]any{})
	child := issue(alice, "Child of one", map[string]any{"parent_id": one})
	related := issue(f.bob, "Related to one", map[string]any{})
	inCycle := issue(alice, "In cycle", map[string]any{})
	inModule := issue(alice, "In module", map[string]any{"target_date": "2030-02-01"})
	leftCycle := issue(alice, "Left the cycle", map[string]any{})
	// Guests can't create issues; carol is made the creator of this one so
	// the guest filter (created_by) has a row to keep.
	carols := issue(alice, "Carol bug report", map[string]any{})
	s.DBStrings(`UPDATE issues SET created_by_id = (SELECT id FROM users WHERE email = 'carol@example.com')
		WHERE id = $1 RETURNING id::text`, carols)
	moved := issue(alice, "Moved modules", map[string]any{})
	issue(alice, "Version 12.5 notes", map[string]any{})
	gone := issue(alice, "Deleted bug", map[string]any{})
	issue(alice, "Grandchild bug", map[string]any{"parent_id": child})
	// Archived and draft issues are created, but the create view's answer
	// crashes (its queryset hides them), so their ids come from the table.
	alice.Post(p+"issues/", map[string]any{"name": "Archived bug", "archived_at": "2026-01-01",
		"state_id": idOf(findBy(states, "name", "Done"))})
	alice.Post(p+"issues/", map[string]any{"name": "Draft bug", "is_draft": true})
	triage := issue(alice, "Triage bug", map[string]any{})
	// Intake (cut) is what puts issues in the triage state; this is the row it
	// leaves.
	s.DBStrings(`UPDATE issues SET state_id = (SELECT id FROM states WHERE project_id = $1 AND "group" = 'triage')
		WHERE id = $2 RETURNING id::text`, f.pl, triage)
	alice.Delete(p + "issues/" + gone + "/")
	alice.Post(p+"issues/"+one+"/issue-relation/", map[string]any{"relation_type": "blocked_by", "issues": []any{related}})
	// A relation that was removed again no longer counts.
	ship, under := f.issues["Ship 100% coverage"], f.issues["under_score task"]
	alice.Post(p+"issues/"+ship+"/issue-relation/", map[string]any{"relation_type": "blocking", "issues": []any{under}})
	alice.Post(p+"issues/"+ship+"/remove-relation/", map[string]any{"relation_type": "blocking", "related_issue": under})

	otIssue := alice.Post(ws+"projects/"+f.ot+"/issues/", map[string]any{"name": "Other project bug"}, ids).String("id")
	f.issues["Other project bug"] = otIssue
	alice.Post(ws+"projects/"+pu+"/issues/", map[string]any{"name": "Public project bug"}, ids)
	alice.Post(ws+"projects/"+ar+"/issues/", map[string]any{"name": "Archived project bug"}, ids)

	// Cycles in every status; the archived one stays searchable.
	cy := p + "cycles/"
	mkCycle := func(project, name string, body map[string]any) string {
		body["name"] = name
		return alice.Post(ws+"projects/"+project+"/cycles/", body, Unordered("assignee_ids")).String("id")
	}
	current := mkCycle(f.pl, "Sprint current", map[string]any{"start_date": day(-3), "end_date": day(4)})
	mkCycle(f.pl, "Sprint upcoming", map[string]any{"start_date": day(7), "end_date": day(14)})
	past := mkCycle(f.pl, "Sprint done", map[string]any{"start_date": day(-14), "end_date": day(-7)})
	mkCycle(f.pl, "Sprint draft", map[string]any{})
	goneCycle := mkCycle(f.pl, "Sprint gone", map[string]any{})
	mkCycle(f.ot, "Other sprint", map[string]any{})
	mkCycle(ar, "Archived project sprint", map[string]any{})
	alice.Post(cy+current+"/cycle-issues/", map[string]any{"issues": []any{inCycle, leftCycle}})
	alice.Delete(cy + current + "/cycle-issues/" + leftCycle + "/")
	alice.Post(cy+past+"/archive/", nil, Mask("archived_at")) // wall clock
	alice.Delete(cy + goneCycle + "/")

	// Modules, one archived and one deleted.
	mkModule := func(project, name string, body map[string]any) string {
		body["name"] = name
		return alice.Post(ws+"projects/"+project+"/modules/", body, Unordered("member_ids")).String("id")
	}
	f.module = mkModule(f.pl, "Module alpha", map[string]any{"status": "in-progress"})
	shelf := mkModule(f.pl, "Module shelved", map[string]any{"status": "completed"})
	goneModule := mkModule(f.pl, "Module gone", map[string]any{})
	mkModule(f.ot, "Other module", map[string]any{})
	mkModule(ar, "Archived project module", map[string]any{})
	beta := mkModule(f.pl, "Module beta", map[string]any{})
	alice.Post(p+"modules/"+f.module+"/issues/", map[string]any{"issues": []any{inModule, moved}})
	alice.Post(p+"modules/"+beta+"/issues/", map[string]any{"issues": []any{moved}})
	alice.Delete(p + "modules/" + f.module + "/issues/" + moved + "/")
	alice.Post(p+"modules/"+shelf+"/archive/", nil, Mask("archived_at")) // archived_at is the wall clock
	alice.Delete(p + "modules/" + goneModule + "/")

	// Views: project ones, a deleted one and a workspace view.
	alice.Post(p+"views/", map[string]any{"name": "View of bugs"})
	goneView := alice.Post(p+"views/", map[string]any{"name": "View gone"}).String("id")
	alice.Delete(p + "views/" + goneView + "/")
	alice.Post(ws+"projects/"+f.ot+"/views/", map[string]any{"name": "Other view"})
	alice.Post(ws+"projects/"+ar+"/views/", map[string]any{"name": "Archived project view"})
	alice.Post(ws+"views/", map[string]any{"name": "Workspace view"})

	// Pages: public, private, archived, deleted, in OT, and one linked to
	// both PL and OT (the page API links one project; the second link is
	// written as ProjectPage.objects.create would).
	pageIDs := Unordered("label_ids", "project_ids")
	mkPage := func(c *Client, project, name string, body map[string]any) string {
		body["name"] = name
		return c.Post(ws+"projects/"+project+"/pages/", body, pageIDs).String("id")
	}
	shared := mkPage(alice, f.pl, "Page shared", map[string]any{"logo_props": map[string]any{"in_use": "emoji"}})
	mkPage(alice, f.pl, "Page private", map[string]any{"access": 1})
	mkPage(f.bob, f.pl, "Page by bob", map[string]any{})
	shelvedPage := mkPage(alice, f.pl, "Page shelved", map[string]any{})
	gonePage := mkPage(alice, f.pl, "Page gone", map[string]any{})
	mkPage(alice, f.ot, "Other page", map[string]any{})
	mkPage(alice, ar, "Archived project page", map[string]any{})
	alice.Post(p+"pages/"+shelvedPage+"/archive/", nil, Mask("archived_at")) // wall clock
	alice.Post(p+"pages/"+gonePage+"/archive/", nil, Mask("archived_at"))    // wall clock
	alice.Delete(p + "pages/" + gonePage + "/")
	s.DBStrings(`INSERT INTO project_pages (id, created_at, updated_at, page_id, project_id, workspace_id, created_by_id)
		SELECT gen_random_uuid(), now(), now(), pg.id, $2::uuid, pg.workspace_id, pg.owned_by_id
		FROM pages pg WHERE pg.id = $1::uuid RETURNING id::text`, shared, f.ot)
	// A page whose project link is soft-deleted (written directly; no
	// endpoint unlinks a page): the search joins ignore the link's
	// deleted_at, the project filter's subquery doesn't.
	unlinked := mkPage(alice, f.pl, "Page unlinked", map[string]any{})
	s.DBStrings(`UPDATE project_pages SET deleted_at = now() WHERE page_id = $1::uuid RETURNING id::text`, unlinked)
	// is_global has no endpoint in this edition; workspace-wide mentions
	// only list global pages.
	s.DBStrings(`UPDATE pages SET is_global = true WHERE id = $1::uuid RETURNING id::text`, shared)

	alice.Post(ws+"projects/"+ar+"/archive/", nil, Mask("archived_at")) // wall clock
	return f
}

func TestGlobalSearch(t *testing.T) {
	Run(t, "search_global", func(s *Scenario) {
		f := newSearchFixture(s, "160")
		w := f.ws + "search/"
		// array_agg(DISTINCT project id) orders by the random ids.
		ids := Unordered("results.page[].project_ids", "results.page[].project_identifiers")
		get := func(c *Client, kv ...string) { c.Get(w+searchQuery(kv...), ids) }

		// Everything, per role.
		get(f.alice)
		get(f.bob)
		get(f.carol)
		get(f.dan)
		get(f.erin)
		get(f.frank)
		get(f.outsider)
		get(f.anon)
		f.alice.Get("/api/workspaces/nope/search/", ids)

		// Queries: names, identifiers, sequence ids and special characters.
		for _, q := range []string{"bug", "BUG", "pl", "PL-1", "1", "12", "12.5", "pl1", "beta", "100%", "%", "_",
			`"`, "'", "' OR 1=1 --", "\\", " ", "  login  ", "", "٣", "99999999999999999999999", "Other", "Sprint",
			"module", "view", "page", "OT-1 PL-3"} {
			get(f.alice, "search", q)
		}
		get(f.bob, "search", "page")
		get(f.carol, "search", "bug")
		get(f.dan, "search", "other")

		// Entity subsets.
		get(f.alice, "search", "bug", "entities", "issue,cycle")
		get(f.alice, "search", "o", "entities", " issue , bogus,,page ")
		get(f.alice, "entities", "bogus")
		get(f.alice, "entities", ",")
		get(f.alice, "entities", "")
		get(f.alice, "entities", "intake,workspace")
		get(f.alice, "entities", "issue,issue")

		// Project scope: only when workspace_search is "false".
		get(f.alice, "search", "o", "project_id", f.pl)
		get(f.alice, "search", "o", "project_id", f.pl, "workspace_search", "false")
		get(f.alice, "search", "o", "project_id", f.pl, "workspace_search", "true")
		get(f.alice, "search", "o", "project_id", f.ot, "workspace_search", "False")
		get(f.alice, "search", "o", "project_id", dead)
		get(f.alice, "search", "page", "project_id", f.pl)
		get(f.alice, "search", "page", "project_id", f.ot)
		get(f.alice, "search", "o", "project_id", "")
		get(f.alice, "search", "o", "project_id", "nope")
		get(f.alice, "search", "o", "project_id", "nope", "entities", "workspace,project")
		get(f.alice, "search", "o", "project_id", "nope", "entities", "page")
	})
}

func TestIssueSearch(t *testing.T) {
	Run(t, "search_issues", func(s *Scenario) {
		f := newSearchFixture(s, "161")
		is := f.issues
		path := func(project string) string { return f.ws + "projects/" + project + "/search-issues/" }
		get := func(c *Client, kv ...string) { c.Get(path(f.pl) + searchQuery(kv...)) }

		// Roles: guests only see their own issues; no project check at all.
		get(f.alice)
		get(f.bob)
		get(f.carol)
		get(f.dan)
		get(f.erin)
		get(f.frank)
		get(f.outsider)
		get(f.anon)
		f.dan.Get(path(f.ot) + searchQuery())
		f.alice.Get(path(dead) + searchQuery())
		f.alice.Get(path("nope") + searchQuery())
		f.alice.Get("/api/workspaces/nope/projects/" + f.pl + "/search-issues/")

		// Workspace-wide: every project the user is in; the guest check
		// still follows the URL's project.
		get(f.alice, "workspace_search", "true")
		get(f.carol, "workspace_search", "true")
		f.carol.Get(path(f.ot) + searchQuery("workspace_search", "true"))
		f.dan.Get(path(f.pl) + searchQuery("workspace_search", "true"))
		get(f.alice, "workspace_search", "True")

		// Queries.
		for _, q := range []string{"bug", "pl", "PL-1", "1", "5 and 6", "12.5", "pl1", "100%", "%", "_", `"`, "'",
			"\\", " ", "", "٣", "99999999999999999999", "123456789012345678901", "a long query about bugs",
			"Other"} {
			get(f.alice, "search", q)
		}
		get(f.alice, "search", "1", "workspace_search", "true")

		// Parent picker: not the issue, its parent or its children.
		get(f.alice, "parent", "true", "issue_id", is["Child of one"])
		get(f.alice, "parent", "true", "issue_id", is["Fix login bug"], "search", "o")
		get(f.alice, "parent", "true")
		get(f.alice, "parent", "True", "issue_id", is["Child of one"])
		get(f.alice, "parent", "true", "issue_id", dead)
		get(f.alice, "parent", "true", "issue_id", is["Archived bug"])
		get(f.alice, "parent", "true", "issue_id", "nope")
		get(f.alice, "parent", "true", "issue_id", is["Other project bug"], "workspace_search", "true")
		get(f.alice, "parent", "true", "issue_id", is["Fix login bug"], "epic", "true")

		// Relations: not the issue or anything related to it.
		get(f.alice, "issue_relation", "true", "issue_id", is["Fix login bug"])
		get(f.alice, "issue_relation", "true", "issue_id", is["Related to one"])
		get(f.alice, "issue_relation", "true", "issue_id", is["Ship 100% coverage"])
		get(f.alice, "issue_relation", "true", "issue_id", is["under_score task"])
		get(f.alice, "issue_relation", "true", "issue_id", dead)
		get(f.alice, "issue_relation", "true", "issue_id", "nope")
		get(f.alice, "issue_relation", "true")

		// Sub-issue picker: root issues only, not the issue or its parent.
		get(f.alice, "sub_issue", "true", "issue_id", is["Fix login bug"])
		get(f.alice, "sub_issue", "true", "issue_id", is["Grandchild bug"])
		get(f.alice, "sub_issue", "true", "issue_id", is["Child of one"])
		get(f.alice, "sub_issue", "true", "issue_id", dead)
		get(f.alice, "sub_issue", "true", "issue_id", "nope")
		get(f.alice, "sub_issue", "true", "issue_id", dead, "module", "nope")
		get(f.alice, "module", "nope", "parent", "true", "issue_id", dead)
		get(f.alice, "sub_issue", "true")

		// Cycle and module pickers, and target dates.
		get(f.alice, "cycle", "true")
		get(f.alice, "cycle", "1")
		get(f.alice, "module", f.module)
		get(f.alice, "module", dead)
		get(f.alice, "module", "")
		get(f.alice, "module", "nope")
		get(f.alice, "target_date", "none")
		get(f.alice, "target_date", "None")
		get(f.alice, "target_date", "none", "cycle", "true", "module", f.module, "search", "bug")
		get(f.carol, "target_date", "none", "cycle", "true")
		get(f.alice, "sub_issue", "true", "parent", "true", "issue_relation", "true", "issue_id", is["Fix login bug"],
			"search", "o")
	})
}

func TestEntitySearch(t *testing.T) {
	Run(t, "search_entity", func(s *Scenario) {
		f := newSearchFixture(s, "162")
		w := f.ws + "entity-search/"
		get := func(c *Client, kv ...string) {
			scoped := false
			for i := 0; i+1 < len(kv); i += 2 {
				scoped = scoped || kv[i] == "project_id" && kv[i+1] != ""
			}
			if scoped {
				c.Get(w + searchQuery(kv...))
				return
			}
			// Workspace-wide, "page" lists the one global page once per
			// project it is in: rows tied on created_at, which DISTINCT
			// orders by the random project ids. (SortBy would sort on the
			// raw ids, so the rows are compared as a set.)
			c.Get(w+searchQuery(kv...), Unordered("page"))
		}
		all := "user_mention,project,issue,cycle,module,page"

		// Default: user mentions, workspace-wide; roles.
		get(f.alice)
		get(f.bob)
		get(f.carol)
		get(f.dan)
		get(f.erin)
		get(f.frank)
		get(f.outsider)
		get(f.anon)
		f.alice.Get("/api/workspaces/nope/entity-search/")

		// Every type, workspace-wide and in a project, per role.
		for _, c := range []*Client{f.alice, f.bob, f.carol, f.dan, f.erin} {
			get(c, "query_type", all, "count", "50")
			get(c, "query_type", all, "count", "50", "project_id", f.pl)
		}
		get(f.dan, "query_type", all, "count", "50", "project_id", f.ot)
		// No check that the user is in the project asked for.
		get(f.bob, "query_type", all, "count", "50", "project_id", f.ot)
		get(f.alice, "query_type", all, "count", "50", "project_id", f.ot)

		// Queries.
		for _, q := range []string{"a", "AL", "o'b", "100%", "%", "_", "bob", "carol 100", "pl", "PL-1", "1", "12.5",
			"٣", "sprint", "module", "page", " ", ""} {
			get(f.alice, "query", q, "query_type", all, "count", "50")
			get(f.alice, "query", q, "query_type", all, "count", "50", "project_id", f.pl)
		}

		// Counts and types.
		get(f.alice, "query_type", all)
		get(f.alice, "query_type", all, "project_id", f.pl)
		get(f.alice, "query_type", all, "count", "2")
		get(f.alice, "query_type", all, "count", "0")
		get(f.alice, "query_type", all, "count", "-1")
		get(f.alice, "query_type", all, "count", "abc")
		get(f.alice, "query_type", all, "count", " 3 ")
		get(f.alice, "query_type", "issue", "count", "1_0")
		get(f.alice, "query_type", "issue", "count", "9223372036854775807")
		get(f.alice, "query_type", "issue", "count", "9223372036854775808")
		get(f.alice, "query_type", "bogus", "count", "-1")
		get(f.alice, "query_type", "user_mention", "count", "-1", "project_id", "nope")
		get(f.alice, "query_type", " issue , bogus,,cycle")
		get(f.alice, "query_type", "")
		get(f.alice, "query_type", "issue,issue", "project_id", f.pl)
		get(f.alice, "query_type", "user_mention", "project_id", dead)
		get(f.alice, "query_type", "user_mention", "project_id", "nope")
		get(f.alice, "query_type", "project", "project_id", "nope")
		get(f.alice, "query_type", "page", "project_id", "nope")
		get(f.alice, "query_type", "issue", "project_id", "")
	})
}
