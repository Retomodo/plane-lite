package contract

import "testing"

func TestIssueBulk(t *testing.T) {
	Run(t, "issue_bulk", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "18")
		s.AliasToday() // archived_at
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("*.label_ids", "*.assignee_ids", "*.module_ids")
		states := alice.Get(p + "states/")
		todo := idOf(findBy(states, "name", "Todo"))
		done := idOf(findBy(states, "name", "Done"))
		cancelled := idOf(findBy(states, "name", "Cancelled"))
		issue := func(c *Client, body map[string]any) string { return c.Post(p+"issues/", body, ids).String("id") }
		a := issue(alice, map[string]any{"name": "Shipped", "state_id": done, "assignee_ids": []any{userID(bob)}})
		b := issue(alice, map[string]any{"name": "Dropped", "state_id": cancelled})
		c := issue(bob, map[string]any{"name": "Open", "state_id": todo, "start_date": "2026-10-01"})
		d := issue(alice, map[string]any{"name": "Done too", "state_id": done, "target_date": "2026-12-01"})
		e := issue(alice, map[string]any{"name": "Later", "state_id": todo})

		// Archive one issue.
		arc := func(id string) string { return p + "issues/" + id + "/archive/" }
		carol.Post(arc(a), nil)
		dan.Post(arc(a), nil)
		outsider.Post(arc(a), nil)
		bob.Post(arc(c), nil)
		bob.Post(arc(dead), nil)
		bob.Post(arc(a), map[string]any{"archived_at": "2020-01-01"})
		bob.Post(arc(a), nil)
		alice.Get(arc(a))
		alice.Get(p+"archived-issues/", ids)
		alice.Get(p+"issues/"+a+"/", ids)
		carol.Delete(arc(a))
		alice.Delete(arc(b))
		alice.Delete(arc(a))
		alice.Delete(arc(a))

		// Bulk archive.
		bulk := p + "bulk-archive-issues/"
		carol.Post(bulk, map[string]any{"issue_ids": []any{a}})
		dan.Post(bulk, map[string]any{"issue_ids": []any{a}})
		alice.Post(bulk, map[string]any{})
		alice.Post(bulk, map[string]any{"issue_ids": []any{}})
		alice.Post(bulk, map[string]any{"issue_ids": ""})
		alice.Post(bulk, map[string]any{"issue_ids": "abc"})
		alice.Post(bulk, map[string]any{"issue_ids": []any{"nope"}})
		alice.Post(bulk, map[string]any{"issue_ids": 5})
		alice.Post(bulk, []any{a})
		alice.Post(bulk, map[string]any{"issue_ids": []any{dead}})
		// Newest first: Later fails before Shipped is touched; Open fails
		// after Done too was handled.
		alice.Post(bulk, map[string]any{"issue_ids": []any{a, e}})
		alice.Post(bulk, map[string]any{"issue_ids": []any{c, d}})
		bob.Post(bulk, map[string]any{"issue_ids": []any{a, b, d}})
		bob.Post(bulk, map[string]any{"issue_ids": []any{a}})
		alice.Get(p+"archived-issues/", ids)

		// Issue dates (the gantt and calendar drag).
		dates := p + "issue-dates/"
		carol.Post(dates, map[string]any{"updates": []any{map[string]any{"id": c, "start_date": "2026-10-02"}}})
		bob.Post(dates, map[string]any{"updates": []any{
			map[string]any{"id": c, "start_date": "2026-10-05", "target_date": "2026-10-20"},
			map[string]any{"id": e, "target_date": "2026-11-15"},
			map[string]any{"id": dead, "start_date": "2026-01-01"},
		}})
		bob.Post(dates, map[string]any{"updates": []any{map[string]any{"id": c, "start_date": "2026-10-25"}}})
		bob.Post(dates, map[string]any{"updates": []any{map[string]any{"id": e, "start_date": "2026-11-01", "target_date": ""}}})
		bob.Post(dates, map[string]any{"updates": []any{map[string]any{"id": e, "start_date": "2026-1-2"}}})
		bob.Post(dates, map[string]any{"updates": []any{map[string]any{"id": e, "start_date": "2026-13-01"}}})
		bob.Post(dates, map[string]any{"updates": []any{map[string]any{"id": "nope"}}})
		bob.Post(dates, map[string]any{"updates": []any{map[string]any{"start_date": "2026-10-01"}}})
		bob.Post(dates, map[string]any{"updates": []any{}})
		bob.Post(dates, map[string]any{"updates": map[string]any{}})
		bob.Post(dates, map[string]any{})
		bob.Post(dates, []any{})
		bob.Post(dates, map[string]any{"updates": []any{map[string]any{"id": a, "target_date": "2026-12-31"},
			map[string]any{"id": a, "start_date": "2027-01-01"}}})
		alice.Get(p+"issues/?order_by=sort_order", ids)

		// Bulk delete: project admins only.
		del := p + "bulk-delete-issues/"
		bob.Do("DELETE", del, map[string]any{"issue_ids": []any{e}})
		carol.Do("DELETE", del, map[string]any{"issue_ids": []any{e}})
		alice.Do("DELETE", del, map[string]any{"issue_ids": []any{}})
		alice.Do("DELETE", del, map[string]any{})
		alice.Do("DELETE", del, map[string]any{"issue_ids": []any{"nope"}})
		alice.Do("DELETE", del, map[string]any{"issue_ids": []any{e, b, dead}})
		alice.Do("DELETE", del, map[string]any{"issue_ids": []any{e}})
		alice.Get(p+"issues/", ids)
		issueRows(s)
	})
}
