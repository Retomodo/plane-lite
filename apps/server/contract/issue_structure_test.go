package contract

import (
	"strings"
	"testing"
)

// structureRows records the issue-structure tables (links, relations) and
// everything issueRows covers (activities, notifications, subscribers).
func structureRows(s *Scenario) {
	s.DBRows("issue_links", `SELECT i.name AS issue, p.identifier AS project, l.title, l.url, l.metadata,
			cb.email AS created_by, ub.email AS updated_by, l.updated_at >= l.created_at AS touched,
			l.deleted_at IS NULL AS live
		FROM issue_links l JOIN issues i ON i.id = l.issue_id JOIN projects p ON p.id = l.project_id
		LEFT JOIN users cb ON cb.id = l.created_by_id LEFT JOIN users ub ON ub.id = l.updated_by_id
		ORDER BY i.name, l.url, l.deleted_at IS NULL`)
	s.DBRows("issue_relations", `SELECT i.name AS issue, r.name AS related, x.relation_type, p.identifier AS project,
			cb.email AS created_by, ub.email AS updated_by, x.deleted_at IS NULL AS live
		FROM issue_relations x JOIN issues i ON i.id = x.issue_id JOIN issues r ON r.id = x.related_issue_id
		JOIN projects p ON p.id = x.project_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY i.name, r.name, x.relation_type, x.deleted_at IS NULL`)
	issueRows(s)
}

func TestIssueLinks(t *testing.T) {
	Run(t, "issue_links", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "61")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		issue := alice.Post(p+"issues/", map[string]any{"name": "Linked", "assignee_ids": []any{userID(bob)}}, ids).String("id")
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		otIssue := alice.Post(ws+"projects/"+ot+"/issues/", map[string]any{"name": "Elsewhere"}, ids).String("id")
		links := p + "issues/" + issue + "/issue-links/"

		// Create: project admins and members. Every URL here fails the
		// crawler's SSRF guard or DNS, so the stored metadata is fixed.
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
		alice.Post(links, map[string]any{"url": "http://localhost/docs"})
		alice.Post(links, map[string]any{"url": "http://localhost/long", "title": strings.Repeat("x", 256)})
		bare := alice.Post(links, map[string]any{"url": "127.0.0.1:9/bare", "title": nil, "metadata": map[string]any{"a": 1}}).String("id")
		alice.Post(links, map[string]any{"url": "https://nowhere.invalid/page", "title": "", "id": dead, "issue": dead,
			"created_by": userID(bob)})
		alice.Post(p+"issues/"+dead+"/issue-links/", map[string]any{"url": "http://localhost/dead"})
		alice.Post(p+"issues/"+otIssue+"/issue-links/", map[string]any{"url": "http://10.1.2.3/other"})

		// List.
		alice.Get(links)
		bob.Get(links)
		carol.Get(links)
		dan.Get(links)
		outsider.Get(links)
		alice.Get(p + "issues/" + dead + "/issue-links/")
		alice.Get(p + "issues/" + otIssue + "/issue-links/")

		// Update.
		link := func(id string) string { return links + id + "/" }
		bob.Patch(link(docs), map[string]any{"title": "Docs v2"})
		bob.Patch(link(docs), map[string]any{"url": "localhost/v2"})
		bob.Patch(link(docs), map[string]any{"url": "http://localhost/v2"})
		carol.Patch(link(docs), map[string]any{"title": "Guest"})
		dan.Patch(link(docs), map[string]any{"title": "Dan"})
		alice.Patch(link(docs), map[string]any{"url": "http://127.0.0.1:9/bare"})
		alice.Patch(link(docs), map[string]any{"url": "bad url"})
		alice.Patch(link(docs), map[string]any{"url": ""})
		alice.Patch(link(docs), map[string]any{"title": strings.Repeat("y", 256)})
		alice.Patch(link(docs), []any{"x"})
		alice.Patch(link(dead), map[string]any{"title": "Nope"})
		alice.Patch(link(bare), map[string]any{"metadata": map[string]any{"b": 2}, "title": "Bare"})
		alice.Get(links)

		// Delete.
		carol.Delete(link(docs))
		dan.Delete(link(docs))
		bob.Delete(link(docs))
		bob.Delete(link(docs))
		alice.Delete(link(dead))
		alice.Get(links)
		alice.Get(p+"issues/"+issue+"/?expand=issue_link", ids)
		structureRows(s)
	})
}

func TestIssueRelations(t *testing.T) {
	Run(t, "issue_relations", func(s *Scenario) {
		alice, bob, carol, dan, _, pl := projectTeam(s, "62")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		// The bucket queries have no ORDER BY: rows come in the plan's
		// order, which follows the random issue ids.
		buckets := []Opt{Unordered("*.label_ids", "*.assignee_ids")}
		for _, b := range []string{"blocking", "blocked_by", "duplicate", "relates_to", "start_after", "start_before",
			"finish_after", "finish_before"} {
			buckets = append(buckets, SortBy(b, "name"))
		}
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")
		issue := func(body map[string]any) string { return alice.Post(p+"issues/", body, ids).String("id") }
		a := issue(map[string]any{"name": "Alpha", "assignee_ids": []any{userID(bob)}, "label_ids": []any{bug}})
		b := issue(map[string]any{"name": "Beta", "priority": "high", "assignee_ids": []any{userID(bob), userID(carol)}})
		c := issue(map[string]any{"name": "Gamma"})
		d := issue(map[string]any{"name": "Delta"})
		// Archived issues and drafts fall out of the create response.
		issue(map[string]any{"name": "Archived", "archived_at": "2026-01-01"})
		issue(map[string]any{"name": "Draft", "is_draft": true})
		f := s.DBStrings(`SELECT id::text FROM issues WHERE name = 'Archived'`)[0]
		g := s.DBStrings(`SELECT id::text FROM issues WHERE name = 'Draft'`)[0]
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		e := alice.Post(ws+"projects/"+ot+"/issues/", map[string]any{"name": "Epsilon"}, ids).String("id")
		rel := func(id string) string { return p + "issues/" + id + "/issue-relation/" }
		rm := func(id string) string { return p + "issues/" + id + "/remove-relation/" }
		post := func(c *Client, id, typ string, issues any) *Response {
			return c.Post(rel(id), map[string]any{"relation_type": typ, "issues": issues})
		}

		alice.Get(rel(a), buckets...)
		post(bob, a, "blocking", []any{b})
		post(alice, a, "blocked_by", []any{c, e})
		post(alice, a, "relates_to", []any{d})
		post(alice, d, "relates_to", []any{a})
		post(alice, a, "duplicate", []any{b})
		post(alice, a, "blocked_by", []any{c})
		post(alice, a, "start_after", []any{c})
		post(alice, b, "start_before", []any{d})
		post(alice, b, "finish_after", []any{c})
		post(alice, c, "finish_before", []any{d})
		post(alice, a, "finish_before", []any{d})
		post(alice, a, "implements", []any{b})
		post(alice, c, "implemented_by", []any{d, a})
		post(alice, a, "nonsense", []any{f})
		post(alice, a, "relates_to", []any{g, dead})
		post(alice, a, "relates_to", []any{a})
		alice.Post(rel(a), map[string]any{"issues": []any{b}})
		alice.Post(rel(a), []any{"x"})
		post(alice, a, "blocking", "x")
		post(alice, a, "blocking", nil)
		alice.Post(rel(a), map[string]any{"relation_type": "blocking"})
		post(alice, a, strings.Repeat("x", 21), []any{d})
		post(carol, a, "blocking", []any{d})
		post(dan, a, "blocking", []any{d})
		post(alice, dead, "blocking", []any{d})

		// List.
		alice.Get(rel(a), buckets...)
		bob.Get(rel(a), buckets...)
		carol.Get(rel(a), buckets...)
		dan.Get(rel(a), buckets...)
		alice.Get(rel(b), buckets...)
		alice.Get(rel(c), buckets...)
		alice.Get(rel(d), buckets...)
		alice.Get(rel(dead), buckets...)
		alice.Get(ws+"projects/"+ot+"/issues/"+e+"/issue-relation/", buckets...)

		// Remove: the newest relation between the two issues goes, whatever
		// its type.
		carol.Post(rm(a), map[string]any{"related_issue": b, "relation_type": "blocking"})
		dan.Post(rm(a), map[string]any{"related_issue": b, "relation_type": "blocking"})
		bob.Post(rm(a), map[string]any{"related_issue": b, "relation_type": "blocking"})
		alice.Post(rm(a), map[string]any{"related_issue": c})
		alice.Post(rm(c), map[string]any{"related_issue": a, "relation_type": "blocked_by"})
		alice.Post(rm(a), map[string]any{"related_issue": dead, "relation_type": "blocking"})
		alice.Post(rm(a), map[string]any{})
		alice.Post(rm(a), map[string]any{"related_issue": "x"})
		alice.Post(rm(a), []any{"x"})
		alice.Get(rel(a), buckets...)
		alice.Get(rel(b), buckets...)
		structureRows(s)
	})
}

func TestSubIssues(t *testing.T) {
	Run(t, "sub_issues", func(s *Scenario) {
		alice, bob, carol, dan, _, pl := projectTeam(s, "63")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		lists := Unordered("*.label_ids", "*.assignee_ids", "*.module_ids")
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		states := alice.Get(p + "states/")
		todo := idOf(findBy(states, "name", "Todo"))
		done := idOf(findBy(states, "name", "Done"))
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")
		zap := alice.Post(p+"issue-labels/", map[string]any{"name": "Zap"}).String("id")
		issue := func(c *Client, body map[string]any) string { return c.Post(p+"issues/", body, ids).String("id") }
		parent := issue(alice, map[string]any{"name": "Parent", "assignee_ids": []any{userID(bob)}})
		x := issue(bob, map[string]any{"name": "Xray", "priority": "low", "state_id": done, "label_ids": []any{zap},
			"assignee_ids": []any{userID(alice), userID(dan)}, "target_date": "2026-11-01"})
		y := issue(alice, map[string]any{"name": "Yankee", "priority": "urgent", "state_id": todo, "label_ids": []any{bug, zap}})
		z := issue(alice, map[string]any{"name": "Zulu", "target_date": "2026-10-15"})
		issue(alice, map[string]any{"name": "Archived", "archived_at": "2026-01-01"})
		v := s.DBStrings(`SELECT id::text FROM issues WHERE name = 'Archived'`)[0]
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		w := alice.Post(ws+"projects/"+ot+"/issues/", map[string]any{"name": "Whiskey"}, ids).String("id")
		sub := func(id string) string { return p + "issues/" + id + "/sub-issues/" }

		alice.Get(sub(parent))
		bob.Post(sub(parent), map[string]any{"sub_issue_ids": []any{x, y, w, v, dead}}, lists)
		carol.Post(sub(parent), map[string]any{"sub_issue_ids": []any{z}})
		dan.Post(sub(parent), map[string]any{"sub_issue_ids": []any{z}})
		alice.Post(sub(parent), map[string]any{})
		alice.Post(sub(parent), map[string]any{"sub_issue_ids": []any{}})
		alice.Post(sub(parent), map[string]any{"sub_issue_ids": "abc"})
		alice.Post(sub(parent), map[string]any{"sub_issue_ids": 5})
		alice.Post(sub(parent), map[string]any{"sub_issue_ids": nil})
		alice.Post(sub(parent), []any{z})
		alice.Post(sub(dead), map[string]any{"sub_issue_ids": []any{z}})
		alice.Post(sub(w), map[string]any{"sub_issue_ids": []any{z}})
		alice.Post(sub(v), map[string]any{"sub_issue_ids": []any{z}})
		alice.Post(sub(parent), map[string]any{"sub_issue_ids": []any{z, z}}, lists)

		// List: every role that can read, orderings and groupings.
		alice.Get(sub(parent), lists)
		bob.Get(sub(parent), lists)
		carol.Get(sub(parent), lists)
		dan.Get(sub(parent), lists)
		alice.Get(sub(dead), lists)
		for _, o := range []string{"priority", "-priority", "state__group", "-state__group", "labels__name",
			"-labels__name", "assignees__first_name", "sequence_id", "-target_date", "target_date", "state__name",
			"-state__name", "created_at", "nope", ""} {
			alice.Get(sub(parent)+"?order_by="+o, lists)
		}
		// Not completed_at (a raw timestamp key) nor label_ids (its list
		// repr follows the random label ids).
		for _, g := range []string{"state_id", "priority", "assignees__ids", "state_group", "sort_order", "target_date",
			"nope", "", "assignee_ids", "module_ids", "is_draft", "sub_issues_count"} {
			alice.Get(sub(parent)+"?group_by="+g, lists)
		}
		alice.Get(sub(parent)+"?group_by=priority&order_by=sequence_id", lists)

		// Re-parent, and an issue made its own parent.
		alice.Post(sub(x), map[string]any{"sub_issue_ids": []any{y}}, lists)
		alice.Post(sub(z), map[string]any{"sub_issue_ids": []any{z}}, lists)
		alice.Get(sub(parent), lists)
		alice.Get(sub(x), lists)
		alice.Get(sub(z), lists)
		structureRows(s)
	})
}

func TestDescriptionVersions(t *testing.T) {
	Run(t, "issue_description_versions", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "64")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		issue := alice.Post(p+"issues/", map[string]any{"name": "Versioned", "description_html": "<p>One</p>"}, ids).String("id")
		other := alice.Post(p+"issues/", map[string]any{"name": "Other", "description_html": "<p>Other</p>"}, ids).String("id")
		alice.Post(p+"issues/", map[string]any{"name": "Archived", "description_html": "<p>A</p>",
			"archived_at": "2026-01-01"}, ids)
		archived := s.DBStrings(`SELECT id::text FROM issues WHERE name = 'Archived'`)[0]
		bob.Patch(p+"issues/"+issue+"/", map[string]any{"description_html": "<p>Two</p>"})
		alice.Patch(p+"issues/"+issue+"/", map[string]any{"description_html": "<p>Three</p>"})
		dv := func(id string) string { return p + "work-items/" + id + "/description-versions/" }

		list := alice.Get(dv(issue))
		bob.Get(dv(issue))
		carol.Get(dv(issue))
		dan.Get(dv(issue))
		outsider.Get(dv(issue))
		for _, cur := range []string{"1:0:0", "1:1:0", "1:2:0", "1:5:0", "2:1:0", "5000:0:0", "1:-1:0", "x", "1:2",
			"0:0:0", "-1:0:0", "a:b:c"} {
			alice.Get(dv(issue) + "?cursor=" + cur)
		}
		alice.Get(dv(dead))
		alice.Get(dv(archived))
		otherVersion := alice.Get(dv(other)).String("results.0.id")

		version := list.String("results.0.id")
		alice.Get(dv(issue) + version + "/")
		bob.Get(dv(issue) + version + "/")
		carol.Get(dv(issue) + version + "/")
		dan.Get(dv(issue) + version + "/")
		alice.Get(dv(issue) + dead + "/")
		alice.Get(dv(issue) + otherVersion + "/")
		alice.Get(dv(dead) + version + "/")

		alice.Patch(p, map[string]any{"guest_view_all_features": true}, projMask)
		carol.Get(dv(issue))
		carol.Get(dv(issue) + version + "/")
		alice.Delete(p + "issues/" + other + "/")
		alice.Get(dv(other))
		structureRows(s)
	})
}
