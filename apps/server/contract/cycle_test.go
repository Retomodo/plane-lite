package contract

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// cycleDays aliases the dates around today in loc (the project's timezone,
// where Django's convert_to_utc reads "today") to "<today+k>", and returns
// the date k days from today. Every cycle date in the scenarios comes from
// here, so no literal date reaches a golden.
func cycleDays(s *Scenario, loc *time.Location) func(k int) string {
	today := time.Now().In(loc)
	day := func(k int) string { return today.AddDate(0, 0, k).Format(time.DateOnly) }
	for k := -40; k <= 40; k++ {
		s.Alias(day(k), fmt.Sprintf("<today%+d>", k))
	}
	return day
}

// cycleRows records everything the cycle views write, by name and email.
// Cycle dates are shown in the project's timezone; a start date of "today"
// is stored as the current time, shown as "now".
func cycleRows(s *Scenario) {
	s.DBRows("cycles", `SELECT p.identifier, c.name, c.description,
			CASE WHEN abs(extract(epoch FROM c.start_date - now())) < 600 THEN 'now'
				ELSE to_char(c.start_date AT TIME ZONE p.timezone, 'YYYY-MM-DD HH24:MI:SS') END AS local_start,
			to_char(c.end_date AT TIME ZONE p.timezone, 'YYYY-MM-DD HH24:MI:SS') AS local_end,
			o.email AS owned_by, cb.email AS created_by, ub.email AS updated_by, c.sort_order, c.view_props,
			c.logo_props, c.progress_snapshot, c.external_source, c.external_id, c.timezone, c.version,
			c.archived_at IS NOT NULL AS archived, c.deleted_at IS NULL AS live
		FROM cycles c JOIN projects p ON p.id = c.project_id JOIN users o ON o.id = c.owned_by_id
		LEFT JOIN users cb ON cb.id = c.created_by_id LEFT JOIN users ub ON ub.id = c.updated_by_id
		ORDER BY p.identifier, c.name, c.created_at`)
	s.DBRows("cycle_issues", `SELECT c.name AS cycle, i.name AS issue, cb.email AS created_by, ub.email AS updated_by,
			ci.deleted_at IS NULL AS live, i.updated_at >= ci.created_at AS issue_touched
		FROM cycle_issues ci JOIN cycles c ON c.id = ci.cycle_id JOIN issues i ON i.id = ci.issue_id
		LEFT JOIN users cb ON cb.id = ci.created_by_id LEFT JOIN users ub ON ub.id = ci.updated_by_id
		ORDER BY c.name, i.name, ci.deleted_at IS NULL`)
	s.DBRows("cycle_user_properties", `SELECT c.name AS cycle, u.email, x.filters, x.display_filters,
			x.display_properties, x.rich_filters, cb.email AS created_by, ub.email AS updated_by,
			x.deleted_at IS NULL AS live
		FROM cycle_user_properties x JOIN cycles c ON c.id = x.cycle_id JOIN users u ON u.id = x.user_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY c.name, u.email, x.deleted_at IS NULL`)
	s.DBRows("user_favorites", `SELECT u.email, f.entity_type, c.name AS cycle, f.deleted_at IS NULL AS live
		FROM user_favorites f JOIN users u ON u.id = f.user_id LEFT JOIN cycles c ON c.id = f.entity_identifier
		ORDER BY u.email, c.name`)
	s.DBRows("recent_visits", `SELECT u.email, v.entity_name, c.name AS cycle, v.deleted_at IS NULL AS live
		FROM user_recent_visits v JOIN users u ON u.id = v.user_id LEFT JOIN cycles c ON c.id = v.entity_identifier
		WHERE v.entity_name = 'cycle' ORDER BY u.email, c.name`)
	s.DBRows("cycle_activities", `SELECT i.name AS issue, a.verb, a.field, a.old_value, a.new_value, a.comment,
			oc.name AS old_cycle, nc.name AS new_cycle, u.email AS actor, a.epoch IS NOT NULL AS has_epoch,
			cb.email AS created_by, a.deleted_at IS NULL AS live
		FROM issue_activities a LEFT JOIN issues i ON i.id = a.issue_id LEFT JOIN users u ON u.id = a.actor_id
		LEFT JOIN users cb ON cb.id = a.created_by_id LEFT JOIN cycles oc ON oc.id = a.old_identifier
		LEFT JOIN cycles nc ON nc.id = a.new_identifier
		WHERE a.field = 'cycles'
		ORDER BY i.name, a.verb, a.old_value, a.new_value, a.comment`)
}

// favoriteCycle stars a cycle for a user. The favorites endpoints are
// ported later, so the row is written directly.
func favoriteCycle(s *Scenario, user *Client, cycle string) {
	s.DBStrings(`INSERT INTO user_favorites (id, created_at, updated_at, entity_type, entity_identifier, is_folder,
			sequence, user_id, project_id, workspace_id)
		SELECT gen_random_uuid(), now(), now(), 'cycle', c.id, false, 65535, $2, c.project_id, c.workspace_id
		FROM cycles c WHERE c.id = $1 RETURNING id::text`, cycle, userID(user))
}

func TestCycleCRUD(t *testing.T) {
	Run(t, "cycle_crud", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "51")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		cy := p + "cycles/"
		// array_agg(DISTINCT ...) orders ids by value, which varies run to run.
		ids := Unordered("assignee_ids", "[].assignee_ids")
		// The project runs on India time, so dates convert to 18:30 UTC the
		// day before; today is India's today.
		alice.Patch(p, map[string]any{"timezone": "Asia/Kolkata"}, projMask)
		kolkata, _ := time.LoadLocation("Asia/Kolkata")
		day := cycleDays(s, kolkata)
		a, b := userID(alice), userID(bob)

		states := alice.Get(p + "states/")
		todo := idOf(findBy(states, "name", "Todo"))
		done := idOf(findBy(states, "name", "Done"))
		cancelled := idOf(findBy(states, "name", "Cancelled"))
		issue := func(body map[string]any) string {
			// Id arrays come in uuid order, which varies run to run.
			return alice.Post(p+"issues/", body, Unordered("label_ids", "assignee_ids", "module_ids")).String("id")
		}
		alpha := issue(map[string]any{"name": "Alpha", "state_id": todo, "assignee_ids": []any{b, a}})
		bravo := issue(map[string]any{"name": "Bravo", "state_id": done, "assignee_ids": []any{b}})
		charlie := issue(map[string]any{"name": "Charlie", "state_id": cancelled})
		delta := issue(map[string]any{"name": "Delta", "parent_id": alpha})
		echo := issue(map[string]any{"name": "Echo"})

		// Create: project admins and members, both dates or neither.
		create := func(c *Client, body any) *Response { return c.Post(cy, body, ids) }
		past := create(alice, map[string]any{"name": "Sprint Past", "start_date": day(-20), "end_date": day(-10)}).String("id")
		now := create(alice, map[string]any{"name": "Sprint Now", "description": "the current one", "start_date": day(-2),
			"end_date": day(5), "view_props": map[string]any{"x": 1}, "logo_props": map[string]any{"in_use": "emoji"},
			"sort_order": 5, "external_source": "jira", "external_id": "J-1", "timezone": "Asia/Kolkata", "version": 3,
			"owned_by": b, "archived_at": "2020-01-01T00:00:00Z", "project": dead}).String("id")
		next := create(bob, map[string]any{"name": "Sprint Next", "start_date": day(10), "end_date": day(20)}).String("id")
		draft := create(alice, map[string]any{"name": "Backlog", "start_date": nil, "end_date": nil}).String("id")
		today := create(bob, map[string]any{"name": "Today", "start_date": day(0), "end_date": day(3)}).String("id")
		// An aware datetime is read in the user's timezone (UTC) before its
		// date is taken.
		create(alice, map[string]any{"name": "Offset", "start_date": day(30) + "T20:00:00-05:00", "end_date": day(32) + "T01:00:00+09:00"})
		create(carol, map[string]any{"name": "Guest"})
		create(dan, map[string]any{"name": "Dan"})
		create(outsider, map[string]any{"name": "Outsider"})
		create(alice, map[string]any{})
		create(alice, []any{"x"})
		create(alice, map[string]any{"name": "Half", "start_date": day(1)})
		create(alice, map[string]any{"name": "Half", "end_date": day(1), "start_date": nil})
		create(alice, map[string]any{"name": "Backwards", "start_date": day(9), "end_date": day(8)})
		create(alice, map[string]any{"name": "Bad", "start_date": "the 13th", "end_date": "soon"})
		create(alice, map[string]any{"name": strings.Repeat("x", 256), "description": nil, "timezone": "Mars/Base",
			"view_props": nil, "sort_order": "high", "version": "v2", "external_id": strings.Repeat("y", 256)})
		create(alice, map[string]any{"name": "", "start_date": "", "end_date": ""})
		create(alice, map[string]any{"name": "Elsewhere", "start_date": day(1), "end_date": day(2), "project_id": dead})

		// Issues in the cycles: counts, assignees and sub-issues.
		alice.Post(cy+now+"/cycle-issues/", map[string]any{"issues": []any{alpha, bravo, charlie, delta}})
		alice.Post(cy+today+"/cycle-issues/", map[string]any{"issues": []any{echo}})
		favoriteCycle(s, alice, next)
		favoriteCycle(s, bob, now)

		// List: any project member; favorites first, then newest.
		alice.Get(cy, ids)
		bob.Get(cy, ids)
		carol.Get(cy, ids)
		dan.Get(cy, ids)
		outsider.Get(cy, ids)
		alice.Get(cy+"?cycle_view=current", ids)
		alice.Get(cy+"?cycle_view=upcoming", ids)

		// Retrieve: admins and members.
		alice.Get(cy+now+"/", ids)
		bob.Get(cy+next+"/", ids)
		alice.Get(cy+draft+"/", ids)
		carol.Get(cy+now+"/", ids)
		dan.Get(cy+now+"/", ids)
		alice.Get(cy+dead+"/", ids)
		alice.Get(cy+"nope/", ids)

		// Update.
		patch := func(c *Client, id string, body any) *Response { return c.Patch(cy+id+"/", body, ids) }
		patch(alice, now, map[string]any{"name": "Sprint Now!", "description": "renamed", "logo_props": map[string]any{"in_use": "icon"}})
		patch(bob, next, map[string]any{"start_date": day(11), "end_date": day(21)})
		patch(alice, draft, map[string]any{"start_date": day(25)}) // one date: stored as given, unconverted
		patch(alice, draft, map[string]any{"end_date": day(26)})
		patch(alice, next, map[string]any{"start_date": day(21), "end_date": day(11)})
		patch(alice, next, map[string]any{"name": ""})
		patch(alice, next, []any{"x"})
		patch(alice, past, map[string]any{"name": "Renamed past"})
		// A completed cycle accepts a body with sort_order, and then all of it.
		patch(alice, past, map[string]any{"sort_order": 1, "name": "Sprint Past (done)"})
		patch(carol, now, map[string]any{"name": "Guest"})
		patch(dan, now, map[string]any{"name": "Dan"})
		patch(alice, dead, map[string]any{"name": "Dead"})

		// Date check: an overlap with any live cycle of the project.
		check := func(c *Client, body any) { c.Post(cy+"date-check/", body) }
		check(alice, map[string]any{"start_date": day(3), "end_date": day(4)})
		check(alice, map[string]any{"start_date": day(-30), "end_date": day(-25)})
		check(alice, map[string]any{"start_date": day(-30), "end_date": day(30)})
		check(alice, map[string]any{"start_date": day(12), "end_date": day(13), "cycle_id": next})
		check(alice, map[string]any{"start_date": day(12), "end_date": day(13), "cycle_id": "nope"})
		check(alice, map[string]any{"start_date": day(12)})
		check(alice, map[string]any{"start_date": "", "end_date": day(13)})
		check(alice, map[string]any{"start_date": "13/10/2026", "end_date": day(13)})
		check(alice, []any{"x"})
		check(carol, map[string]any{"start_date": day(3), "end_date": day(4)})
		check(dan, map[string]any{"start_date": day(3), "end_date": day(4)})

		// User properties: created on first read, any project member.
		props := cy + now + "/user-properties/"
		alice.Patch(props, map[string]any{"filters": map[string]any{"priority": []any{"high"}}})
		alice.Get(props)
		alice.Get(props)
		carol.Get(props)
		dan.Get(props)
		alice.Patch(props, map[string]any{"filters": map[string]any{"priority": []any{"high"}},
			"display_filters": map[string]any{"layout": "kanban"}, "rich_filters": map[string]any{"and": []any{}}, "ignored": 1})
		alice.Patch(props, map[string]any{"display_properties": "flat"})
		carol.Patch(props, map[string]any{"filters": nil})
		alice.Patch(props, []any{"x"})
		alice.Get(cy + dead + "/user-properties/")

		// Archive: completed cycles only; archived cycles leave the list.
		arch := func(c *Client, id string) { c.Post(cy+id+"/archive/", nil, Mask("archived_at")) } // str(timezone.now())
		arch(alice, now)
		arch(alice, draft)
		arch(carol, past)
		arch(alice, dead)
		arch(bob, past)
		alice.Get(p+"archived-cycles/", ids)
		alice.Get(p+"archived-cycles/"+past+"/", ids)
		carol.Get(p+"archived-cycles/", ids)
		alice.Get(p+"archived-cycles/"+now+"/", ids)
		alice.Get(cy, ids)
		alice.Get(cy+past+"/", ids)
		patch(alice, past, map[string]any{"sort_order": 2})
		alice.Delete(cy + past + "/archive/")
		alice.Get(p+"archived-cycles/", ids)
		alice.Delete(cy + now + "/archive/")
		alice.Delete(cy + dead + "/archive/")
		carol.Delete(cy + past + "/archive/")
		arch(alice, past)

		// The workspace's cycles: any workspace member, in their projects.
		wc := func(c *Client) { c.Get(ws + "cycles/") }
		wc(alice)
		wc(carol)
		wc(dan)
		wc(outsider)

		cycleRows(s)

		// Delete: project admins, or the cycle's creator.
		alice.Get(cy+next+"/", ids)
		bob.Get(cy+next+"/", ids)
		bob.Delete(cy + now + "/")
		carol.Delete(cy + now + "/")
		dan.Delete(cy + next + "/")
		bob.Delete(cy + next + "/")
		alice.Delete(cy + today + "/")
		alice.Delete(cy + dead + "/")
		alice.Delete(cy + today + "/")
		alice.Get(cy+today+"/", ids)
		alice.Get(cy, ids)
		alice.Get(cy + today + "/user-properties/")
		check(alice, map[string]any{"start_date": day(1), "end_date": day(2)})
		cycleRows(s)
	})
}

func TestCycleIssues(t *testing.T) {
	Run(t, "cycle_issues", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "52")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		cy := p + "cycles/"
		day := cycleDays(s, time.UTC)
		// array_agg(DISTINCT ...) orders ids by value, which varies run to run.
		ids := Unordered("*.label_ids", "*.assignee_ids", "*.module_ids")
		// Which relation row a grouped issue shows first is Postgres's choice
		// between tied rows of the same issue.
		ties := Mask("*.labels__id", "*.assignees__id", "*.issue_module__module_id")
		a, b := userID(alice), userID(bob)

		states := alice.Get(p + "states/")
		todo := idOf(findBy(states, "name", "Todo"))
		started := idOf(findBy(states, "name", "In Progress"))
		done := idOf(findBy(states, "name", "Done"))
		cancelled := idOf(findBy(states, "name", "Cancelled"))
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")
		feature := alice.Post(p+"issue-labels/", map[string]any{"name": "Feature"}).String("id")
		issue := func(body map[string]any) string {
			// Id arrays come in uuid order, which varies run to run.
			return alice.Post(p+"issues/", body, Unordered("label_ids", "assignee_ids", "module_ids")).String("id")
		}
		alpha := issue(map[string]any{"name": "Alpha", "state_id": todo, "priority": "high", "label_ids": []any{bug, feature},
			"assignee_ids": []any{a, b}})
		bravo := issue(map[string]any{"name": "Bravo", "state_id": started, "priority": "urgent", "label_ids": []any{bug},
			"assignee_ids": []any{b}})
		charlie := issue(map[string]any{"name": "Charlie", "state_id": done, "priority": "low"})
		delta := issue(map[string]any{"name": "Delta", "state_id": cancelled, "parent_id": alpha})
		echo := issue(map[string]any{"name": "Echo", "label_ids": []any{feature}})
		foxtrot := issue(map[string]any{"name": "Foxtrot", "state_id": todo})
		// Draft and archived issues are created, but the create view's answer
		// crashes (its queryset hides them), so their ids come from the table.
		alice.Post(p+"issues/", map[string]any{"name": "Drafted", "is_draft": true})
		alice.Post(p+"issues/", map[string]any{"name": "Shelved", "archived_at": day(-5)})
		drafted := s.DBStrings(`SELECT id::text FROM issues WHERE name = 'Drafted'`)[0]
		shelved := s.DBStrings(`SELECT id::text FROM issues WHERE name = 'Shelved'`)[0]
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		elsewhere := alice.Post(ws+"projects/"+ot+"/issues/", map[string]any{"name": "Elsewhere"}).String("id")

		mk := func(name string, start, end int) string {
			return alice.Post(cy, map[string]any{"name": name, "start_date": day(start), "end_date": day(end)},
				Unordered("assignee_ids")).String("id") // empty here; uuid order otherwise
		}
		current := mk("Current", -3, 4)
		upcoming := mk("Upcoming", 7, 14)
		over := mk("Over", -14, -7)
		otCycle := alice.Post(ws+"projects/"+ot+"/cycles/", map[string]any{"name": "Other cycle"}).String("id")

		// Add: project admins and members; issues move between cycles.
		add := func(c *Client, cycle string, body any) { c.Post(cy+cycle+"/cycle-issues/", body) }
		add(alice, current, map[string]any{"issues": []any{alpha, bravo, charlie, delta, drafted, shelved, elsewhere, dead}})
		add(bob, upcoming, map[string]any{"issues": []any{charlie, echo}})
		add(alice, current, map[string]any{"issues": []any{foxtrot, charlie}})
		add(alice, over, map[string]any{"issues": []any{echo}})
		add(alice, current, map[string]any{"issues": []any{alpha}})
		add(alice, current, map[string]any{"issues": []any{}})
		add(alice, current, map[string]any{})
		add(alice, current, map[string]any{"issues": "nope"})
		add(alice, current, map[string]any{"issues": []any{"nope"}})
		add(alice, current, []any{alpha})
		add(alice, dead, map[string]any{"issues": []any{echo}})
		add(alice, otCycle, map[string]any{"issues": []any{echo}})
		add(carol, upcoming, map[string]any{"issues": []any{echo}})
		add(dan, upcoming, map[string]any{"issues": []any{echo}})
		add(outsider, upcoming, map[string]any{"issues": []any{echo}})

		// The cycle's issue list: admins and members, with the issue list's
		// filters, ordering, grouping and pagination.
		list := func(c *Client, cycle, q string) {
			c.Get(cy+cycle+"/cycle-issues/"+q, ids, ties)
		}
		list(alice, current, "")
		list(alice, current, "?"+richFilter(map[string]any{})+"&layout=list&cursor=100:0:0&per_page=100&sub_issue=true&order_by=sort_order")
		list(alice, current, "?per_page=1&cursor=1:1:0")
		list(alice, current, "?order_by=priority")
		list(alice, current, "?order_by=labels__name")
		list(alice, current, "?sub_issue=false")
		list(alice, current, "?priority=high,urgent")
		list(alice, current, "?labels="+bug)
		list(alice, current, "?cycle="+upcoming)
		list(alice, current, "?updated_at__gt="+day(-30))
		list(alice, current, "?"+richFilter(map[string]any{"and": []any{map[string]any{"state_group__in": "unstarted,started"}}}))
		list(alice, current, "?"+richFilter(map[string]any{"cycle_id__in": upcoming}))
		list(alice, current, "?filters="+url.QueryEscape(`{"bogus": 1}`))
		for _, g := range []string{"state_id", "priority", "labels__id", "assignees__id", "cycle_id", "state__group",
			"issue_module__module_id", "project_id"} {
			list(alice, current, "?"+richFilter(map[string]any{})+"&layout=list&group_by="+g+"&cursor=50:0:0&per_page=50&order_by=sort_order")
		}
		list(alice, current, "?group_by=target_date")
		list(alice, current, "?group_by=created_by")
		list(alice, current, "?group_by=name")
		list(alice, current, "?group_by=priority&sub_group_by=priority")
		list(alice, current, "?layout=kanban&group_by=state_id&sub_group_by=priority&cursor=10:0:0&per_page=10")
		list(alice, current, "?group_by=labels__id&sub_group_by=assignees__id")
		list(alice, current, "?group_by=priority&per_page=1&cursor=1:1:0")
		list(bob, upcoming, "")
		list(alice, over, "")
		list(alice, dead, "")
		list(carol, current, "")
		list(dan, current, "")
		list(outsider, current, "")

		// The project issue list by cycle.
		issues := func(q string) { alice.Get(p+"issues/"+q, ids, ties) }
		issues("?cycle=" + current)
		issues("?cycle=" + current + "," + upcoming + "&group_by=cycle_id")
		issues("?cycle=None")
		issues("?" + richFilter(map[string]any{"cycle_id__in": current}))
		issues("?group_by=cycle_id&order_by=sort_order")

		// Remove one issue: admins and members.
		alice.Delete(cy + current + "/cycle-issues/" + foxtrot + "/")
		alice.Delete(cy + current + "/cycle-issues/" + foxtrot + "/")
		alice.Delete(cy + current + "/cycle-issues/" + echo + "/") // not in this cycle
		alice.Delete(cy + current + "/cycle-issues/" + dead + "/")
		alice.Delete(cy + dead + "/cycle-issues/" + alpha + "/")
		carol.Delete(cy + current + "/cycle-issues/" + alpha + "/")
		dan.Delete(cy + current + "/cycle-issues/" + alpha + "/")
		list(alice, current, "")

		// Transfer the unfinished issues to another cycle.
		tr := func(c *Client, from string, body any) { c.Post(cy+from+"/transfer-issues/", body) }
		tr(alice, current, map[string]any{})
		tr(alice, current, map[string]any{"new_cycle_id": ""})
		tr(alice, current, map[string]any{"new_cycle_id": over})
		tr(alice, current, map[string]any{"new_cycle_id": dead})
		tr(alice, current, map[string]any{"new_cycle_id": "nope"})
		tr(alice, dead, map[string]any{"new_cycle_id": upcoming})
		tr(alice, current, []any{"x"})
		tr(carol, current, map[string]any{"new_cycle_id": upcoming})
		tr(dan, current, map[string]any{"new_cycle_id": upcoming})
		tr(alice, current, map[string]any{"new_cycle_id": upcoming})
		list(alice, current, "")
		list(alice, upcoming, "")
		alice.Get(cy+"?read=1", Unordered("[].assignee_ids")) // uuid order varies
		// Adding to a second cycle moves the issue; transfers then move what
		// is unfinished, and a transfer onto the same cycle is a no-op move.
		add(alice, current, map[string]any{"issues": []any{foxtrot}})
		add(alice, upcoming, map[string]any{"issues": []any{foxtrot}})
		tr(bob, current, map[string]any{"new_cycle_id": upcoming})
		alice.Patch(p+"issues/"+foxtrot+"/", map[string]any{"state_id": done})
		tr(bob, current, map[string]any{"new_cycle_id": upcoming})
		tr(bob, upcoming, map[string]any{"new_cycle_id": upcoming})
		cycleRows(s)
		s.DBRows("issue_activities", `SELECT i.name AS issue, a.verb, a.field, count(*) AS n
			FROM issue_activities a LEFT JOIN issues i ON i.id = a.issue_id
			GROUP BY i.name, a.verb, a.field ORDER BY i.name, a.verb, a.field NULLS FIRST`)
	})
}

func TestCycleAnalytics(t *testing.T) {
	Run(t, "cycle_analytics", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "53")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		cy := p + "cycles/"
		day := cycleDays(s, time.UTC)
		a, b := userID(alice), userID(bob)
		// array_agg(DISTINCT ...) orders ids by value, which varies run to run.
		ids := Unordered("assignee_ids", "[].assignee_ids")

		states := alice.Get(p + "states/")
		backlog := idOf(findBy(states, "name", "Backlog"))
		todo := idOf(findBy(states, "name", "Todo"))
		started := idOf(findBy(states, "name", "In Progress"))
		done := idOf(findBy(states, "name", "Done"))
		cancelled := idOf(findBy(states, "name", "Cancelled"))
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug", "color": "#ff0000"}).String("id")
		feature := alice.Post(p+"issue-labels/", map[string]any{"name": "Feature", "color": "#00ff00"}).String("id")
		est := alice.Post(p+"estimates/", map[string]any{
			"estimate":        map[string]any{"name": "Fibonacci", "type": "points"},
			"estimate_points": []any{map[string]any{"key": 1, "value": "1"}, map[string]any{"key": 2, "value": "2"}, map[string]any{"key": 3, "value": "5"}},
		}).String("id")
		point := func(v string) string {
			return s.DBStrings(`SELECT id::text FROM estimate_points WHERE estimate_id = $1 AND value = $2`, est, v)[0]
		}
		p1, p2, p5 := point("1"), point("2"), point("5")
		// First names keep the archive detail's assignees (ordered by
		// first_name, last_name) from tying.
		alice.Patch("/api/users/me/", map[string]any{"first_name": "Alice"}, Mask("token"))
		bob.Patch("/api/users/me/", map[string]any{"display_name": "bobby", "first_name": "Bob"}, Mask("token"))

		issue := func(body map[string]any) string {
			// Id arrays come in uuid order, which varies run to run.
			return alice.Post(p+"issues/", body, Unordered("label_ids", "assignee_ids", "module_ids")).String("id")
		}
		mk := func(name string, start, end int) string {
			return alice.Post(cy, map[string]any{"name": name, "start_date": day(start), "end_date": day(end)}, ids).String("id")
		}
		current := mk("Current", -3, 3)
		next := mk("Next", 5, 9)
		draft := alice.Post(cy, map[string]any{"name": "Draft"}, ids).String("id")
		old := mk("Old", -10, 1)

		var all []any
		for _, body := range []map[string]any{
			{"name": "Alpha", "state_id": backlog, "estimate_point": p1, "assignee_ids": []any{a, b}, "label_ids": []any{bug}},
			{"name": "Bravo", "state_id": todo, "estimate_point": p2, "assignee_ids": []any{b}, "label_ids": []any{bug, feature}},
			{"name": "Charlie", "state_id": started, "estimate_point": p5, "assignee_ids": []any{a}},
			{"name": "Delta", "state_id": done, "estimate_point": p2, "assignee_ids": []any{b}, "label_ids": []any{feature}},
			{"name": "Echo", "state_id": done, "estimate_point": p5},
			{"name": "Foxtrot", "state_id": cancelled, "estimate_point": p1, "label_ids": []any{bug}},
			{"name": "Golf", "state_id": todo},
		} {
			all = append(all, issue(body))
		}
		hotel := issue(map[string]any{"name": "Hotel", "parent_id": all[0], "assignee_ids": []any{b}})
		all = append(all, hotel)
		var older []any
		for _, body := range []map[string]any{
			{"name": "India", "state_id": started, "estimate_point": p2, "assignee_ids": []any{a}, "label_ids": []any{bug}},
			{"name": "Juliet", "state_id": done, "estimate_point": p1, "assignee_ids": []any{a, b}},
			{"name": "Kilo", "state_id": todo, "label_ids": []any{feature}},
			{"name": "Lima", "state_id": cancelled, "estimate_point": p5},
		} {
			older = append(older, issue(body))
		}
		// Delta and Juliet were finished two days ago, so the burndowns drop
		// before today.
		s.DBStrings(`UPDATE issues SET completed_at = now() - interval '2 days' WHERE name IN ('Delta', 'Juliet') RETURNING id::text`)
		alice.Post(cy+current+"/cycle-issues/", map[string]any{"issues": all})
		alice.Post(cy+old+"/cycle-issues/", map[string]any{"issues": older})
		// Removed assignees and deleted issues.
		alice.Patch(p+"issues/"+hotel+"/", map[string]any{"assignee_ids": []any{}})
		alice.Delete(p + "issues/" + all[6].(string) + "/")

		// Progress and analytics: any project member.
		prog := func(c *Client, id, q string) { c.Get(cy + id + "/progress/" + q) }
		ana := func(c *Client, id, q string) { c.Get(cy + id + "/analytics/" + q) }
		prog(alice, current, "")
		prog(carol, current, "")
		prog(dan, current, "")
		prog(alice, draft, "")
		prog(alice, dead, "")
		ana(alice, current, "")
		ana(alice, current, "?type=issues")
		ana(alice, current, "?type=bogus")
		ana(carol, current, "")
		ana(dan, current, "")
		ana(outsider, current, "")
		ana(alice, draft, "")
		ana(alice, dead, "")
		ana(alice, next, "")
		// Points need the project to use a points estimate.
		ana(alice, current, "?type=points")
		alice.Patch(p, map[string]any{"estimate": est}, projMask)
		ana(alice, current, "?type=points")
		prog(alice, current, "?read=1")
		alice.Get(cy+current+"/", ids)
		alice.Get(cy+"?read=1", ids)
		alice.Get(ws + "cycles/")

		// Transfer snapshots the old cycle's progress.
		alice.Post(cy+old+"/transfer-issues/", map[string]any{"new_cycle_id": next})
		prog(alice, old, "")
		ana(alice, old, "")
		ana(alice, old, "?type=points")
		alice.Get(cy+"?read=2", ids)

		// An over cycle, archived, with its distributions.
		alice.Patch(cy+current+"/", map[string]any{"start_date": day(-12), "end_date": day(-6)}, ids)
		alice.Post(cy+current+"/archive/", nil, Mask("archived_at")) // str(timezone.now())
		alice.Get(p+"archived-cycles/", ids)
		alice.Get(p+"archived-cycles/"+current+"/", ids)
		alice.Patch(p, map[string]any{"estimate": nil}, projMask)
		alice.Get(p+"archived-cycles/"+current+"/?read=1", ids)
		bob.Get(p+"archived-cycles/"+current+"/", ids)
		carol.Get(p+"archived-cycles/"+current+"/", ids)
		prog(alice, current, "?read=2")
		ana(alice, current, "?read=2")
		cycleRows(s)
	})
}
