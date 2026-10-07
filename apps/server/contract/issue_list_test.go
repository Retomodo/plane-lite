package contract

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// richFilter renders a filters= query value.
func richFilter(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return "filters=" + url.QueryEscape(string(b))
}

func TestIssueList(t *testing.T) {
	Run(t, "issue_list", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "17")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("*.label_ids", "*.assignee_ids", "*.module_ids")
		// Which relation row a grouped issue shows first is Postgres's choice
		// between tied rows of the same issue.
		ties := Mask("*.labels__id", "*.assignees__id", "*.issue_module__module_id")
		list := func(c *Client, q string) *Response { return c.Get(p+"issues/"+q, ids, ties) }

		states := alice.Get(p + "states/")
		todo := idOf(findBy(states, "name", "Todo"))
		started := idOf(findBy(states, "name", "In Progress"))
		done := idOf(findBy(states, "name", "Done"))
		cancelled := idOf(findBy(states, "name", "Cancelled"))
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")
		feature := alice.Post(p+"issue-labels/", map[string]any{"name": "Feature"}).String("id")
		docs := alice.Post(p+"issue-labels/", map[string]any{"name": "Docs"}).String("id")
		a, b := userID(alice), userID(bob)

		issue := func(c *Client, body map[string]any) string {
			return c.Post(p+"issues/", body, ids).String("id")
		}
		alpha := issue(alice, map[string]any{"name": "Alpha", "state_id": todo, "priority": "high",
			"label_ids": []any{bug, feature}, "assignee_ids": []any{a, b}, "start_date": "2026-10-01", "target_date": "2026-10-20"})
		issue(alice, map[string]any{"name": "Bravo", "state_id": started, "priority": "urgent", "label_ids": []any{bug},
			"assignee_ids": []any{b}, "target_date": "2026-10-20"})
		issue(alice, map[string]any{"name": "Charlie"})
		delta := issue(alice, map[string]any{"name": "Delta", "state_id": done, "priority": "low", "label_ids": []any{docs},
			"assignee_ids": []any{a}})
		alice.Patch(p+"issues/"+delta+"/", map[string]any{"label_ids": []any{}, "assignee_ids": []any{}})
		issue(alice, map[string]any{"name": "Echo", "parent_id": alpha, "state_id": todo, "priority": "medium",
			"assignee_ids": []any{a}, "label_ids": []any{feature}})
		issue(bob, map[string]any{"name": "Foxtrot", "priority": "medium", "label_ids": []any{feature},
			"description_html": "<p>" + mention(alice) + "</p>"})
		issue(alice, map[string]any{"name": "Golf", "state_id": cancelled, "priority": "high", "start_date": "2026-09-01",
			"target_date": "2099-01-01"})
		alice.Post(p+"issues/", map[string]any{"name": "Drafted", "is_draft": true})
		alice.Post(p+"issues/", map[string]any{"name": "Shelved", "archived_at": "2026-01-01", "label_ids": []any{bug},
			"state_id": done})
		alice.Post(p+"issues/", map[string]any{"name": "Shelved too", "archived_at": "2026-02-01", "parent_id": alpha,
			"state_id": cancelled})

		// Ungrouped, as the list and spreadsheet layouts ask.
		list(alice, "?"+richFilter(map[string]any{})+"&layout=list&cursor=100:0:0&per_page=100&sub_issue=true&order_by=-created_at")
		list(alice, "")
		list(alice, "?sub_issue=false")
		list(bob, "?per_page=2")
		list(alice, "?per_page=2&cursor=2:1:0")
		list(alice, "?per_page=2&cursor=2:3:0")
		list(alice, "?per_page=2&cursor=2:1:1")
		list(alice, "?per_page=3&cursor=2:1:0")
		list(alice, "?per_page=0")
		list(alice, "?per_page=1001")
		list(alice, "?per_page=x")
		list(alice, "?cursor=x")
		list(alice, "?cursor=2:-1:0")
		for _, o := range []string{"sort_order", "-updated_at", "start_date", "-start_date", "target_date", "priority",
			"-priority", "state__group", "-state__group", "state__name", "labels__name", "-labels__name",
			"assignees__first_name", "-assignees__first_name", "issue_module__module__name", "sequence_id", "--created_at", "bogus"} {
			list(alice, "?per_page=100&order_by="+o)
		}

		// Guests see their own issues unless the project shares everything.
		list(carol, "")
		list(dan, "")
		list(outsider, "")
		list(alice, "?per_page=1&cursor=1:0:0&group_by=state_id")

		// Grouped, as the list and kanban layouts ask.
		for _, g := range []string{"state_id", "priority", "labels__id", "state__group", "assignees__id", "cycle_id",
			"issue_module__module_id", "target_date", "start_date", "project_id", "created_by"} {
			list(alice, "?"+richFilter(map[string]any{})+"&layout=list&group_by="+g+"&cursor=50:0:0&per_page=50&sub_issue=true&order_by=sort_order")
		}
		list(alice, "?group_by=state_id&per_page=1&order_by=-priority")
		list(alice, "?group_by=labels__id&per_page=1&cursor=1:1:0&order_by=labels__name")
		list(alice, "?group_by=assignees__id&order_by=-assignees__first_name")
		list(alice, "?group_by=name")
		list(alice, "?group_by=state_id&sub_group_by=state_id")
		list(alice, "?group_by=state_id&sub_group_by=bogus")
		for _, g := range [][2]string{{"state_id", "priority"}, {"priority", "labels__id"}, {"labels__id", "assignees__id"},
			{"assignees__id", "state__group"}, {"state__group", "target_date"}} {
			list(alice, "?layout=kanban&group_by="+g[0]+"&sub_group_by="+g[1]+"&cursor=10:0:0&per_page=10&order_by=sort_order")
		}
		list(alice, "?group_by=priority&sub_group_by=state_id&per_page=1&cursor=1:1:0")

		// "Load more" in one group: the group becomes a legacy filter.
		list(alice, "?state="+todo+"&cursor=50:1:0&per_page=1&order_by=sort_order")
		list(alice, "?labels="+bug+"&cursor=1:1:0&per_page=1")
		list(alice, "?assignees="+a+"&priority=high,urgent&sub_group_by=state_id&group_by=labels__id&per_page=10")

		// Rich filters, as the filter bar sends them.
		for _, f := range []any{
			map[string]any{"and": []any{map[string]any{"state_id__in": todo + "," + started}, map[string]any{"priority__in": "urgent,high"}}},
			map[string]any{"and": []any{map[string]any{"label_id__in": bug + "," + feature}}},
			map[string]any{"and": []any{map[string]any{"label_id__in": bug}, map[string]any{"label_id__in": feature}}},
			map[string]any{"and": []any{map[string]any{"assignee_id__in": a}, map[string]any{"and": []any{map[string]any{"state_group__in": "unstarted,started"}}}}},
			map[string]any{"and": []any{map[string]any{"created_by_id__in": b}}},
			map[string]any{"and": []any{map[string]any{"mention_id__in": a}}},
			map[string]any{"and": []any{map[string]any{"subscriber_id__in": b}}},
			map[string]any{"and": []any{map[string]any{"project_id__in": pl}}},
			map[string]any{"and": []any{map[string]any{"cycle_id__in": dead}, map[string]any{"module_id__in": dead}}},
			map[string]any{"and": []any{map[string]any{"target_date__exact": "2026-10-20"}}},
			map[string]any{"and": []any{map[string]any{"start_date__range": "2026-09-01,2026-09-30"}}},
			map[string]any{"and": []any{map[string]any{"created_at__range": "2000-01-01,2099-12-31"}, map[string]any{"updated_at__range": "2000-01-01,2099-12-31"}}},
			map[string]any{"and": []any{map[string]any{"created_at__exact": "2000-01-01"}}},
			map[string]any{"or": []any{map[string]any{"label_id__in": bug}, map[string]any{"state_group__in": "backlog"}}},
			map[string]any{"not": map[string]any{"label_id__in": bug}},
			map[string]any{"not": map[string]any{"priority": "high", "state_group__in": "unstarted"}},
			map[string]any{"is_archived": "false", "is_draft": "false", "priority": "none", "state_id": todo},
			map[string]any{"label_id": bug, "assignee_id": b},
			map[string]any{"OR": []any{map[string]any{"priority": "low"}}},
		} {
			list(alice, "?per_page=100&"+richFilter(f))
		}
		list(alice, "?group_by=labels__id&"+richFilter(map[string]any{"and": []any{map[string]any{"label_id__in": bug}}}))
		list(alice, "?group_by=assignees__id&sub_group_by=priority&"+richFilter(map[string]any{"assignee_id__in": a}))
		for _, bad := range []string{
			`{"name": "x"}`, `{"state_id": "nope"}`, `{"priority__in": "nope"}`, `{"target_date__exact": "20-10-2026"}`,
			`{"created_at__range": "2026-01-01"}`, `{"and": []}`, `{"and": {}}`, `{"or": ["x"]}`, `{"not": []}`,
			`{"and": [{}]}`, `{}`, `[]`, `"x"`, `{bad`, `{"priority": ["high", "low"]}`, `{"priority": []}`,
			`{"priority": {"a": 1}}`, `{"and": [{"priority": "high"}], "or": [{"priority": "low"}]}`,
			`{"and": [{"priority": "high"}], "priority": "low"}`, `{"priority": null}`, `{"is_archived": "maybe"}`,
			`{"and":[{"and":[{"and":[{"and":[{"and":[{"priority":"high"}]}]}]}]}]}`,
		} {
			list(alice, "?per_page=100&filters="+url.QueryEscape(bad))
		}

		// Legacy comma-separated filters.
		for _, q := range []string{
			"state=" + todo + "," + started, "state=null", "state=nope", "state_group=started,completed", "priority=high,urgent",
			"priority=null,", "labels=" + bug, "labels=None", "labels=None," + feature, "assignees=" + a, "assignees=None",
			"created_by=" + b, "created_by=None", "mentions=" + a, "subscriber=" + b, "project=" + pl, "parent=" + alpha,
			"parent=None", "name=lph", "name=", "type=backlog", "type=active", "type=all", "cycle=None", "module=None",
			"cycle=" + dead, "target_date=2026-10-01;after,2026-10-31;before", "target_date=2026-10-20",
			"start_date=2026-09-15;after", "target_date=2_weeks;after;fromnow", "target_date=2_weeks;before;fromnow",
			"target_date=3_months;after", "start_target_date=true", "estimate_point=nope", "intake_status=1",
			"completed_at=2000-01-01;after", "created_at=2000-01-01;after", "updated_at=2099-01-01;before",
			"logged_by=" + a, "updated_at__gt=2000-01-01T00:00:00Z", "updated_at__gt=nope",
		} {
			list(alice, "?per_page=100&"+q)
		}
		// The calendar layout.
		list(alice, "?"+richFilter(map[string]any{})+"&layout=calendar&group_by=target_date&target_date=2026-10-01;after,2026-10-31;before&cursor=4:0:0&per_page=4&sub_issue=true")

		// issues/list/: the store's refetch by id.
		all := s.DBStrings(`SELECT id::text FROM issues WHERE project_id = $1 ORDER BY sequence_id`, pl)
		alice.Get(p+"issues/list/?issues="+strings.Join(all, ","), ids)
		alice.Get(p+"issues/list/?issues="+alpha+",,"+dead, ids)
		alice.Get(p+"issues/list/?issues="+alpha+"&order_by=priority&group_by=labels__id", ids)
		alice.Get(p+"issues/list/?issues="+alpha+","+delta+"&labels="+bug, ids)
		alice.Get(p+"issues/list/?issues=", ids)
		alice.Get(p+"issues/list/", ids)
		alice.Get(p+"issues/list/?issues=nope", ids)
		carol.Get(p+"issues/list/?issues="+strings.Join(all, ","), ids)
		outsider.Get(p+"issues/list/?issues="+alpha, ids)

		// Archived issues.
		arch := func(c *Client, q string) *Response { return c.Get(p+"archived-issues/"+q, ids, ties) }
		arch(alice, "?"+richFilter(map[string]any{})+"&layout=list&cursor=100:0:0&per_page=100&sub_issue=true")
		arch(alice, "?show_sub_issues=false")
		arch(alice, "?group_by=state_id")
		arch(alice, "?group_by=labels__id&sub_group_by=priority")
		arch(alice, "?group_by=target_date")
		arch(alice, "?labels="+bug+"&order_by=priority")
		arch(bob, "")
		arch(carol, "")
		arch(outsider, "")

		alice.Patch(p, map[string]any{"guest_view_all_features": true}, projMask)
		list(carol, "?group_by=priority")
		carol.Get(p+"issues/list/?issues="+alpha, ids)
		s.DBRows("recent_visits", `SELECT u.email, v.entity_name, v.entity_identifier = v.project_id AS project_entity,
				v.deleted_at IS NULL AS live
			FROM user_recent_visits v JOIN users u ON u.id = v.user_id
			ORDER BY u.email, v.entity_name`)
	})
}
