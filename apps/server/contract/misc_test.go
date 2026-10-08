package contract

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// headerStep records some response headers as a step (actor "headers"),
// for endpoints whose headers are part of the contract: cache_page's
// caching headers and attachment downloads. HTTP dates become "<http-date>"
// and an Age becomes "<present>": both follow the wall clock.
func headerStep(s *Scenario, label string, r *Response, names ...string) {
	s.t.Helper()
	body := map[string]any{}
	for _, n := range names {
		v, ok := r.Header[http.CanonicalHeaderKey(n)]
		if !ok {
			body[n] = nil
			continue
		}
		val := strings.Join(v, ", ")
		switch n {
		case "Expires":
			if _, err := http.ParseTime(val); err == nil {
				val = "<http-date>"
			}
		case "Age":
			val = "<present>"
		}
		body[n] = val
	}
	s.steps = append(s.steps, Step{Actor: "headers", Method: "HEADERS", Path: label, Body: body})
}

func TestTimezones(t *testing.T) {
	Run(t, "misc_timezones", func(s *Scenario) {
		// The offsets (and so the order) follow the current date's DST rules
		// in Django itself, so the golden keeps neither; the ordering and
		// formatting are checked at fixed dates by api.TestTimezoneList.
		tz := []Opt{Mask("timezones[].utc_offset", "timezones[].gmt_offset"), SortBy("timezones", "label")}
		cacheHeaders := []string{"Cache-Control", "Expires", "Age"}

		// cache_page also keeps the page in Redis, keyed by the full URL; Go
		// doesn't (DEVIATIONS.md). Each read whose headers are recorded uses
		// a fresh query string, so Django serves it uncached too.
		anon := s.ClientFrom("anon", "10.0.41.1")
		first := anon.Get("/api/timezones/", tz...)
		headerStep(s, "first", first, cacheHeaders...)
		other := anon.Get("/api/timezones/?x=1", tz...)
		headerStep(s, "other key", other, cacheHeaders...)

		alice := s.ClientFrom("alice", "10.0.41.2")
		signUp(alice, "alice@example.com", strongPassword)
		r := alice.Get("/api/timezones/?signed-in=1", tz...)
		headerStep(s, "signed in", r, cacheHeaders...)
		alice.Post("/api/timezones/", map[string]any{})

		// AuthenticationThrottle counts anonymous requests only.
		for range 10 {
			anon.Get("/api/timezones/", tz...)
		}
		for range 11 {
			alice.Get("/api/timezones/", tz...)
		}
		anon.Get("/api/timezones/?x=1", tz...)

		// Throttling comes before the method check, and counts every method.
		other2 := s.ClientFrom("anon2", "10.0.41.3")
		for range 9 {
			other2.Post("/api/timezones/", map[string]any{})
		}
		other2.Delete("/api/timezones/")
		other2.Post("/api/timezones/", nil)
		other2.Get("/api/timezones/", tz...)
	})
}

// profileTeam seeds the data the profile pages aggregate: projectTeam's PL
// plus SE (bob and dan), TH (dan's, without alice) and OL (archived later),
// issues in every state, priority and lifecycle, assignees (some removed),
// subscribers, labels and cycles.
type profileTeam struct {
	alice, bob, carol, dan, outsider *Client
	pl, se, th, ol                   string
	bug                              string
	todo, started, done              string
}

func seedProfiles(s *Scenario, subnet string) *profileTeam {
	alice, bob, carol, dan, outsider, pl := projectTeam(s, subnet)
	const ws = "/api/workspaces/acme/"
	tm := &profileTeam{alice: alice, bob: bob, carol: carol, dan: dan, outsider: outsider, pl: pl}
	ids := Unordered("*.label_ids", "*.assignee_ids", "*.module_ids")
	a, b, d := userID(alice), userID(bob), userID(dan)

	project := func(c *Client, name, ident string, members ...string) string {
		id := c.Post(ws+"projects/", map[string]any{"name": name, "identifier": ident}, projMask).String("id")
		var list []any
		for _, m := range members {
			list = append(list, map[string]any{"member_id": m, "role": 15})
		}
		c.Post(ws+"projects/"+id+"/members/", map[string]any{"members": list})
		return id
	}
	tm.se = project(alice, "Second", "SE", b, d)
	tm.th = project(dan, "Third", "TH", b)
	tm.ol = project(alice, "Old", "OL", b)

	stateOf := func(c *Client, p, name string) string {
		return idOf(findBy(c.Get(ws+"projects/"+p+"/states/"), "name", name))
	}
	p := ws + "projects/" + pl + "/"
	tm.todo, tm.started, tm.done = stateOf(alice, pl, "Todo"), stateOf(alice, pl, "In Progress"), stateOf(alice, pl, "Done")
	cancelled := stateOf(alice, pl, "Cancelled")
	tm.bug = alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")

	issue := func(c *Client, project string, body map[string]any) string {
		return c.Post(ws+"projects/"+project+"/issues/", body, ids).String("id")
	}
	alpha := issue(alice, pl, map[string]any{"name": "Alpha", "state_id": tm.todo, "priority": "high",
		"assignee_ids": []any{b}, "label_ids": []any{tm.bug}, "start_date": "2026-10-01", "target_date": "2026-10-20"})
	bravo := issue(alice, pl, map[string]any{"name": "Bravo", "state_id": tm.done, "priority": "urgent",
		"assignee_ids": []any{a, b}, "label_ids": []any{tm.bug}, "target_date": "2026-10-20"})
	issue(bob, pl, map[string]any{"name": "Charlie", "priority": "none", "description_html": "<p>" + mention(alice) + "</p>"})
	delta := issue(alice, pl, map[string]any{"name": "Delta", "state_id": tm.started, "assignee_ids": []any{b}})
	alice.Patch(p+"issues/"+delta+"/", map[string]any{"assignee_ids": []any{}})
	issue(bob, pl, map[string]any{"name": "Echo", "state_id": cancelled, "priority": "low", "assignee_ids": []any{b}})
	issue(bob, pl, map[string]any{"name": "Echo child", "parent_id": alpha, "priority": "medium", "assignee_ids": []any{b, a}})
	alice.Post(p+"issues/", map[string]any{"name": "Foxtrot", "archived_at": "2026-01-01", "assignee_ids": []any{b}})
	alice.Post(p+"issues/", map[string]any{"name": "Golf", "is_draft": true, "assignee_ids": []any{b}})
	hotel := issue(alice, pl, map[string]any{"name": "Hotel", "assignee_ids": []any{b}, "priority": "high"})
	alice.Delete(p + "issues/" + hotel + "/")
	// Assigned, removed and assigned again: two assignee rows, one live.
	india := issue(alice, pl, map[string]any{"name": "India", "assignee_ids": []any{b}, "state_id": tm.done, "priority": "medium"})
	alice.Patch(p+"issues/"+india+"/", map[string]any{"assignee_ids": []any{}})
	alice.Patch(p+"issues/"+india+"/", map[string]any{"assignee_ids": []any{b}})
	issue(bob, tm.se, map[string]any{"name": "Kilo", "priority": "medium", "assignee_ids": []any{d, b},
		"description_html": "<p>" + mention(dan) + "</p>"})
	issue(dan, tm.se, map[string]any{"name": "Lima", "state_id": stateOf(dan, tm.se, "Done"), "assignee_ids": []any{b}})
	issue(dan, tm.th, map[string]any{"name": "Mike", "assignee_ids": []any{b}, "priority": "urgent"})
	issue(alice, tm.ol, map[string]any{"name": "November", "assignee_ids": []any{b}, "priority": "low"})
	alice.Post(ws+"projects/"+tm.ol+"/archive/", nil, Mask("archived_at")) // str(timezone.now())

	// Cycles (not ported yet, so seeded directly): one running, one
	// upcoming, one over; Bravo's membership in the running one is removed.
	cycle := func(project, name, start, end string) string {
		return s.DBStrings(`INSERT INTO cycles (id, created_at, updated_at, name, description, start_date, end_date,
				owned_by_id, project_id, workspace_id, view_props, sort_order, progress_snapshot, logo_props, timezone, version)
			SELECT gen_random_uuid(), now(), now(), $2, '', now() + $3::interval, now() + $4::interval, p.created_by_id,
				p.id, p.workspace_id, '{}', 65535, '{}', '{}', 'UTC', 1
			FROM projects p WHERE p.id = $1 RETURNING id::text`, project, name, start, end)[0]
	}
	running := cycle(pl, "Running", "-2 days", "5 days")
	upcoming := cycle(tm.se, "Upcoming", "10 days", "20 days")
	over := cycle(pl, "Over", "-20 days", "-10 days")
	elsewhere := cycle(tm.th, "Elsewhere", "-1 day", "3 days")
	addCycle := func(cyc, iss string, at string, deleted bool) {
		s.DBStrings(`INSERT INTO cycle_issues (id, created_at, updated_at, cycle_id, issue_id, project_id, workspace_id, deleted_at)
			SELECT gen_random_uuid(), $3::timestamptz, $3::timestamptz, c.id, $2, c.project_id, c.workspace_id,
				CASE WHEN $4 THEN now() END
			FROM cycles c WHERE c.id = $1 RETURNING id::text`, cyc, iss, at, deleted)
	}
	addCycle(running, alpha, "2026-03-01T10:00:00Z", false)
	addCycle(running, bravo, "2026-03-01T11:00:00Z", true)
	addCycle(running, india, "2026-03-01T12:00:00Z", false)
	addCycle(over, bravo, "2026-03-01T13:00:00Z", false)
	byName := func(name string) string {
		return s.DBStrings(`SELECT id::text FROM issues WHERE name = $1`, name)[0]
	}
	addCycle(upcoming, byName("Kilo"), "2026-03-01T14:00:00Z", false)
	addCycle(elsewhere, byName("Mike"), "2026-03-01T15:00:00Z", false)

	// Activity timestamps are pinned an hour apart, so pages, dates and the
	// CSV are reproducible; the first ones fall on 2026-03-01 in UTC but
	// 2026-03-02 in alice's Asia/Kolkata. They are numbered by issue, then
	// by content: one request's activities are created in set order.
	s.DBStrings(`WITH o AS (SELECT a.id, row_number() OVER (ORDER BY i.created_at, a.verb, a.field NULLS FIRST,
				a.old_value NULLS FIRST, a.new_value NULLS FIRST, a.created_at) AS n
			FROM issue_activities a LEFT JOIN issues i ON i.id = a.issue_id)
		UPDATE issue_activities a SET created_at = '2026-03-01T17:00:00Z'::timestamptz + o.n * interval '1 hour',
			updated_at = '2026-03-01T17:30:00Z'::timestamptz + o.n * interval '1 hour',
			epoch = extract(epoch FROM '2026-03-01T17:00:00Z'::timestamptz + o.n * interval '1 hour')
		FROM o WHERE o.id = a.id RETURNING a.id::text`)
	// A hard-deleted issue leaves its activity behind with no issue.
	s.DBStrings(`UPDATE issue_activities SET issue_id = NULL WHERE verb = 'deleted' RETURNING id::text`)
	alice.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
	// A display name starting with a formula character is quoted in the
	// CSV export.
	bob.Patch("/api/users/me/", map[string]any{"display_name": "@bob"}, Mask("token"))
	return tm
}

func TestUserProfilePages(t *testing.T) {
	Run(t, "misc_profile", func(s *Scenario) {
		tm := seedProfiles(s, "42")
		alice, bob, carol, dan, outsider := tm.alice, tm.bob, tm.carol, tm.dan, tm.outsider
		const ws = "/api/workspaces/acme/"
		a, b := userID(alice), userID(bob)
		anon := s.Client("anon")

		// user-stats
		stats := func(c *Client, user, q string) { c.Get(ws + "user-stats/" + user + "/" + q) }
		for _, c := range []*Client{alice, bob, carol, dan, outsider} {
			stats(c, b, "")
		}
		stats(alice, a, "")
		stats(bob, userID(dan), "")
		stats(alice, dead, "")
		stats(anon, b, "")
		alice.Get("/api/workspaces/nope/user-stats/" + b + "/")
		alice.Get(ws + "user-stats/nope/")
		for _, q := range []string{"project=" + tm.pl, "project=" + tm.pl + "," + tm.se, "created_by=" + b,
			"created_at=2000-01-01;after", "subscriber=" + a, "priority=high", "state_group=completed", "assignees=" + b,
			"labels=" + tm.bug, "cycle=None", "state=nope", "start_date=nope;after", "logged_by=" + a} {
			stats(alice, b, "?"+q)
		}

		// user-profile
		// project_data is a GROUP BY without ORDER BY: Postgres picks the order.
		profile := func(c *Client, user string) { c.Get(ws+"user-profile/"+user+"/", Unordered("project_data")) }
		for _, c := range []*Client{alice, bob, carol, dan, outsider, anon} {
			profile(c, b)
		}
		profile(alice, a)
		profile(bob, a)
		profile(alice, userID(outsider))
		profile(alice, dead)
		alice.Get("/api/workspaces/nope/user-profile/" + b + "/")

		// user-activity
		act := func(c *Client, user, q string) { c.Get(ws + "user-activity/" + user + "/" + q) }
		for _, c := range []*Client{alice, bob, carol, dan, outsider, anon} {
			act(c, b, "")
		}
		act(alice, a, "")
		act(alice, dead, "")
		for _, q := range []string{"?project=" + tm.pl, "?project=" + tm.pl + "&project=" + tm.se, "?project=" + tm.ol,
			"?project=nope", "?project=", "?per_page=3", "?per_page=3&cursor=3:1:0", "?per_page=3&cursor=3:9:0",
			"?order_by=created_at", "?order_by=-updated_at", "?order_by=updated_at&per_page=2", "?order_by=name",
			"?order_by=--created_at", "?per_page=0", "?cursor=x"} {
			act(alice, b, q)
		}
		act(carol, a, "")
		alice.Post(ws+"user-activity/"+b+"/", map[string]any{})

		// user-activity export
		export := func(c *Client, user string, body any) *Response {
			r := c.Post(ws+"user-activity/"+user+"/export/", body)
			if r.Header.Get("Content-Type") == "text/csv" {
				headerStep(s, "export", r, "Content-Disposition", "Content-Type")
			}
			return r
		}
		for _, date := range []string{"2026-03-01", "2026-03-02", "2026-03-03", "2026-03-02T05:00:00", "nope", ""} {
			export(alice, b, map[string]any{"date": date})
		}
		export(alice, a, map[string]any{"date": "2026-03-02"})
		export(alice, a, map[string]any{"date": "2026-03-03"})
		export(dan, b, map[string]any{"date": "2026-03-02"})
		export(bob, b, map[string]any{"date": "2026-03-01"})
		export(carol, b, map[string]any{"date": "2026-03-02"})
		export(outsider, b, map[string]any{"date": "2026-03-02"})
		export(alice, b, map[string]any{})
		export(alice, b, map[string]any{"date": 5})
		export(alice, b, []any{"2026-03-02"})
		export(alice, dead, map[string]any{"date": "2026-03-02"})
		alice.PostForm(ws+"user-activity/"+b+"/export/", url.Values{"date": {"2026-03-02"}})
		alice.Get(ws + "user-activity/" + b + "/export/")
	})
}

func TestUserProfileIssues(t *testing.T) {
	Run(t, "misc_user_issues", func(s *Scenario) {
		tm := seedProfiles(s, "43")
		alice, bob, carol, dan, outsider := tm.alice, tm.bob, tm.carol, tm.dan, tm.outsider
		const ws = "/api/workspaces/acme/"
		a, b := userID(alice), userID(bob)
		ids := Unordered("*.label_ids", "*.assignee_ids", "*.module_ids")
		// Which relation row a grouped issue shows first is Postgres's choice
		// between tied rows of the same issue.
		ties := Mask("*.labels__id", "*.assignees__id", "*.issue_module__module_id")
		list := func(c *Client, user, q string) { c.Get(ws+"user-issues/"+user+"/"+q, ids, ties) }

		for _, c := range []*Client{alice, bob, carol, dan, outsider, s.Client("anon")} {
			list(c, b, "")
		}
		list(alice, a, "")
		list(bob, a, "")
		list(alice, dead, "")
		list(alice, b, "?"+richFilter(map[string]any{})+"&layout=list&cursor=100:0:0&per_page=100&order_by=-created_at")
		list(alice, b, "?per_page=2")
		list(alice, b, "?per_page=2&cursor=2:1:0")
		list(alice, b, "?per_page=2&cursor=2:9:0")
		list(alice, b, "?per_page=0")
		list(alice, b, "?cursor=x")
		for _, o := range []string{"sort_order", "-updated_at", "priority", "-priority", "state__group", "-state__group",
			"state__name", "labels__name", "-assignees__first_name", "target_date", "-start_date", "sequence_id", "bogus"} {
			list(alice, b, "?order_by="+o)
		}
		for _, g := range []string{"state_id", "priority", "labels__id", "assignees__id", "state__group", "cycle_id",
			"issue_module__module_id", "project_id", "created_by", "target_date", "start_date"} {
			list(alice, b, "?"+richFilter(map[string]any{})+"&layout=list&group_by="+g+"&cursor=50:0:0&per_page=50&order_by=sort_order")
		}
		list(carol, b, "?group_by=state_id")
		list(dan, b, "?group_by=assignees__id")
		list(alice, b, "?group_by=name")
		list(alice, b, "?group_by=priority&per_page=1")
		list(alice, b, "?group_by=labels__id&per_page=1&cursor=1:1:0&order_by=labels__name")
		for _, g := range [][2]string{{"state_id", "priority"}, {"priority", "labels__id"}, {"labels__id", "assignees__id"},
			{"assignees__id", "state__group"}, {"project_id", "created_by"}, {"cycle_id", "target_date"}, {"priority", "priority"},
			{"priority", "bogus"}} {
			list(alice, b, "?layout=kanban&group_by="+g[0]+"&sub_group_by="+g[1]+"&cursor=10:0:0&per_page=10&order_by=sort_order")
		}
		for _, f := range []any{
			map[string]any{"priority__in": "high,urgent"},
			map[string]any{"and": []any{map[string]any{"assignee_id__in": a}}},
			map[string]any{"and": []any{map[string]any{"project_id__in": tm.pl}}},
			map[string]any{"not": map[string]any{"label_id__in": tm.bug}},
			map[string]any{"and": []any{map[string]any{"subscriber_id__in": a}}},
			map[string]any{"or": []any{map[string]any{"state_group__in": "completed"}, map[string]any{"priority": "none"}}},
		} {
			list(alice, b, "?"+richFilter(f))
		}
		list(alice, b, "?filters="+url.QueryEscape(`{"bogus": 1}`))
		for _, q := range []string{"priority=high,medium", "assignees=" + a, "assignees=None", "labels=" + tm.bug,
			"sub_issue=false", "cycle=None", "subscriber=" + a, "project=" + tm.se, "state=" + tm.done, "created_by=" + b,
			"mentions=" + a, "target_date=2026-10-20", "updated_at__gt=2099-01-01T00:00:00Z", "start_date=nope;after", "logged_by=" + a} {
			list(alice, b, "?"+q)
		}
		list(alice, b, "?group_by=assignees__id&assignees="+a)
		list(alice, b, "?group_by=state_id&sub_group_by=labels__id&labels="+tm.bug)
	})
}
