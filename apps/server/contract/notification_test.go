package contract

import (
	"testing"
)

// notificationRows records the notifications' state by receiver and issue.
func notificationRows(s *Scenario) {
	s.DBRows("notification_state", `SELECT r.email AS receiver, n.sender, n.title, i.name AS issue,
			n.read_at IS NOT NULL AS is_read, n.snoozed_till IS NOT NULL AS snoozed, n.archived_at IS NOT NULL AS archived,
			ub.email AS updated_by, n.deleted_at IS NULL AS live
		FROM notifications n JOIN users r ON r.id = n.receiver_id LEFT JOIN issues i ON i.id = n.entity_identifier
		LEFT JOIN users ub ON ub.id = n.updated_by_id
		ORDER BY r.email, i.name, n.sender, n.title, n.message::text, n.data->'issue_activity'->>'field',
			n.data->'issue_activity'->>'new_value'`)
}

// notificationIDs lists the receiver's notification ids, oldest first.
func notificationIDs(s *Scenario, email string, mentioned bool) []string {
	return s.DBStrings(`SELECT n.id::text FROM notifications n JOIN users u ON u.id = n.receiver_id
		WHERE u.email = $1 AND n.deleted_at IS NULL AND (n.sender LIKE '%mentioned%') = $2
		ORDER BY n.created_at, n.id`, email, mentioned)
}

func TestNotifications(t *testing.T) {
	Run(t, "notifications", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "30")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		done := idOf(findBy(alice.Get(p+"states/"), "name", "Done"))

		// Rows come from the issue writes: assignees, subscribers, mentions.
		first := alice.Post(p+"issues/", map[string]any{"name": "First", "priority": "high",
			"description_html": "<p>Hi " + mention(bob) + "</p>",
			"assignee_ids":     []any{userID(bob), userID(carol)}}, ids).String("id")
		second := bob.Post(p+"issues/", map[string]any{"name": "Second",
			"description_html": "<p>" + mention(alice) + "</p>", "assignee_ids": []any{userID(alice)}}, ids).String("id")
		third := bob.Post(p+"issues/", map[string]any{"name": "Third"}, ids).String("id")
		alice.Patch(p+"issues/"+first+"/", map[string]any{"priority": "urgent"})
		alice.Patch(p+"issues/"+first+"/", map[string]any{"state_id": done})
		alice.Patch(p+"issues/"+first+"/", map[string]any{"description_html": "<p>Now " + mention(carol) + "</p>"})
		alice.Patch(p+"issues/"+second+"/", map[string]any{"priority": "low", "name": "Second renamed"})
		alice.Patch(p+"issues/"+third+"/", map[string]any{"priority": "medium", "assignee_ids": []any{userID(carol)}})
		carol.Patch(p+"issues/"+third+"/", map[string]any{"priority": "low"})
		notificationRows(s)

		// List: filters, roles, validation.
		base := ws + "users/notifications/"
		bob.Get(base)
		bob.Get(base + "?read=false")
		bob.Get(base + "?read=true")
		bob.Get(base + "?read=maybe")
		bob.Get(base + "?mentioned=true")
		bob.Get(base + "?mentioned=false")
		bob.Get(base + "?mentioned=")
		bob.Get(base + "?type=assigned")
		bob.Get(base + "?type=subscribed")
		bob.Get(base + "?type=created")
		bob.Get(base + "?type=assigned,subscribed")
		bob.Get(base + "?type=assigned,created&mentioned=1")
		bob.Get(base + "?type=nope")
		bob.Get(base + "?type=")
		bob.Get(base + "?snoozed=true")
		bob.Get(base + "?snoozed=nope")
		bob.Get(base + "?archived=true")
		bob.Get(base + "?archived=nope")
		alice.Get(base)
		alice.Get(base + "?mentioned=true")
		alice.Get(base + "?type=created")
		alice.Get(base + "?type=subscribed")
		carol.Get(base)
		carol.Get(base + "?type=created")
		carol.Get(base + "?type=assigned")
		dan.Get(base)
		dan.Get(base + "?type=assigned")
		outsider.Get(base)

		// Pagination needs both per_page and cursor.
		bob.Get(base + "?per_page=2")
		bob.Get(base + "?cursor=2:0:0")
		bob.Get(base + "?per_page=2&cursor=2:0:0")
		bob.Get(base + "?per_page=2&cursor=2:1:0")
		bob.Get(base + "?per_page=2&cursor=2:2:0")
		bob.Get(base + "?per_page=2&cursor=2:3:0")
		bob.Get(base + "?per_page=2&cursor=2:1:0&order_by=created_at")
		bob.Get(base + "?per_page=2&cursor=2:0:0&order_by=updated_at")
		bob.Get(base + "?per_page=2&cursor=2:0:0&order_by=-updated_at")
		bob.Get(base + "?per_page=2&cursor=2:0:0&order_by=title")
		bob.Get(base + "?per_page=2&cursor=2:0:0&order_by=--created_at")
		bob.Get(base + "?per_page=2&cursor=2:0:0&type=created")
		carol.Get(base + "?per_page=2&cursor=2:0:0&type=created")
		bob.Get(base + "?per_page=2&cursor=2:1:1")
		bob.Get(base + "?per_page=2&cursor=2:-1:0")
		bob.Get(base + "?per_page=x&cursor=2:0:0")
		bob.Get(base + "?per_page=1001&cursor=1001:0:0")
		bob.Get(base + "?per_page=0&cursor=0:0:0")
		bob.Get(base + "?per_page=2&cursor=nope")
		bob.Get(base + "?per_page=2&cursor=2:0")
		bob.Get(base + "?per_page=2&cursor=1.5:0:0")
		bob.Get(base + "?per_page=2&cursor=2:0:0&read=false&mentioned=true")
		outsider.Get(base + "?per_page=2&cursor=2:0:0")

		// Unread counts.
		bob.Get(base + "unread/")
		alice.Get(base + "unread/")
		carol.Get(base + "unread/")
		dan.Get(base + "unread/")
		outsider.Get(base + "unread/")

		// Retrieve and mutate.
		bn := notificationIDs(s, "bob@example.com", false)
		bm := notificationIDs(s, "bob@example.com", true)[0]
		b0, b1, b2 := bn[0], bn[1], bn[2]
		bob.Get(base + b0 + "/")
		carol.Get(base + b0 + "/")
		dan.Get(base + b0 + "/")
		outsider.Get(base + b0 + "/")
		bob.Get(base + dead + "/")
		bob.Get(base + "nope/")
		bob.Post(base+b0+"/read/", nil)
		bob.Post(base+b0+"/read/", map[string]any{})
		bob.Get(base + "unread/")
		bob.Get(base + "?read=true")
		bob.Delete(base + b0 + "/read/")
		bob.Delete(base + b0 + "/read/")
		carol.Post(base+b0+"/read/", nil)
		carol.Delete(base + b0 + "/read/")
		outsider.Post(base+b0+"/read/", nil)
		bob.Post(base+dead+"/read/", nil)
		bob.Delete(base + dead + "/read/")
		bob.Post(base+b1+"/archive/", nil)
		bob.Post(base+b1+"/archive/", nil)
		bob.Get(base)
		bob.Get(base + "?archived=true")
		bob.Get(base + "unread/")
		carol.Post(base+b1+"/archive/", nil)
		carol.Delete(base + b1 + "/archive/")
		bob.Delete(base + b1 + "/archive/")
		bob.Delete(base + b1 + "/archive/")
		bob.Post(base+dead+"/archive/", nil)
		bob.Delete(base + dead + "/archive/")
		dan.Post(base+b1+"/archive/", nil)

		// Snooze: any datetime, null, missing.
		bob.Patch(base+b2+"/", map[string]any{"snoozed_till": "2099-01-01T10:00:00Z"})
		bob.Get(base)
		bob.Get(base + "?snoozed=true")
		bob.Get(base + "unread/")
		bob.Patch(base+b2+"/", map[string]any{"snoozed_till": "2000-01-01T10:00:00Z"})
		bob.Get(base)
		bob.Get(base + "?snoozed=true")
		bob.Patch(base+b2+"/", map[string]any{"snoozed_till": "2099-01-01"})
		bob.Patch(base+b2+"/", map[string]any{"snoozed_till": "soon"})
		bob.Patch(base+b2+"/", map[string]any{"snoozed_till": 5})
		bob.Patch(base+b2+"/", map[string]any{"snoozed_till": nil})
		bob.Patch(base+b2+"/", map[string]any{"snoozed_till": "2099-01-01T10:00:00Z"})
		bob.Patch(base+b2+"/", map[string]any{})
		bob.Patch(base+b2+"/", map[string]any{"read_at": "2099-01-01T10:00:00Z", "title": "x", "snoozed_till": "2099-02-01T10:00:00+05:30"})
		bob.Patch(base+b2+"/", []any{"x"})
		bob.Patch(base+b2+"/", "x")
		bob.Patch(base+dead+"/", map[string]any{"snoozed_till": nil})
		carol.Patch(base+b2+"/", map[string]any{"snoozed_till": nil})
		dan.Patch(base+b2+"/", map[string]any{"snoozed_till": nil})
		outsider.Patch(base+b2+"/", map[string]any{"snoozed_till": nil})
		notificationRows(s)

		// Leave one read, one archived and one snoozed, and delete an issue the
		// type filters look at.
		bob.Post(base+b0+"/read/", nil)
		bob.Post(base+b1+"/archive/", nil)
		bob.Post(base+bm+"/read/", nil)
		bob.Post(base+bm+"/archive/", nil)
		bob.Get(base + "?mentioned=true")
		bob.Get(base + "?mentioned=true&archived=true&read=true")
		bob.Get(base + "?read=true")
		bob.Get(base + "?read=false&archived=true")
		bob.Get(base + "?archived=true")
		bob.Get(base + "?archived=true&snoozed=true")
		bob.Get(base + "?snoozed=true&type=assigned")
		bob.Get(base + "?type=created&per_page=3&cursor=3:0:0")
		bob.Delete(p + "issues/" + third + "/")
		bob.Get(base + "?type=created")
		bob.Get(base + "?type=assigned")
		bob.Get(base + "?type=subscribed")
		carol.Get(base + "?type=assigned")
		carol.Get(base + "?type=subscribed")
		bob.Get(base + "unread/")

		// Mark all read.
		carol.Post(base+"mark-all-read/", map[string]any{"type": "created"})
		carol.Get(base + "unread/")
		carol.Post(base+"mark-all-read/", map[string]any{"type": "watching"})
		carol.Get(base + "unread/")
		carol.Post(base+"mark-all-read/", map[string]any{"type": "assigned"})
		carol.Get(base + "unread/")
		dan.Post(base+"mark-all-read/", map[string]any{})
		outsider.Post(base+"mark-all-read/", map[string]any{})
		alice.Post(base+"mark-all-read/", map[string]any{"type": "created"})
		alice.Get(base + "unread/")
		alice.Post(base+"mark-all-read/", map[string]any{"type": "watching"})
		alice.Post(base+"mark-all-read/", []any{"x"})
		alice.Post(base+"mark-all-read/", "x")
		bob.Post(base+"mark-all-read/", map[string]any{"archived": true})
		bob.Get(base + "?archived=true")
		bob.Post(base+"mark-all-read/", map[string]any{"snoozed": "yes", "type": "nope"})
		bob.Get(base + "?snoozed=true")
		bob.Get(base + "unread/")
		bob.Post(base+"mark-all-read/", map[string]any{"type": []any{"assigned"}, "snoozed": 0, "archived": ""})
		bob.Get(base + "unread/")
		bob.Post(base+"mark-all-read/", map[string]any{"type": "assigned"})
		bob.Get(base + "unread/")
		bob.Post(base+"mark-all-read/", nil)
		bob.Get(base + "unread/")
		bob.Get(base + "?read=false")
		notificationRows(s)

		// Preferences.
		const pref = "/api/users/me/notification-preferences/"
		alice.Get(pref)
		bob.Get(pref)
		outsider.Get(pref)
		bob.Patch(pref, map[string]any{"comment": false, "mention": false})
		bob.Get(pref)
		bob.Patch(pref, map[string]any{"property_change": "maybe", "state_change": nil, "issue_completed": "true"})
		bob.Patch(pref, map[string]any{"id": dead, "user": userID(alice), "workspace": dead, "project": pl, "created_by": userID(alice)})
		bob.Patch(pref, map[string]any{"project": dead})
		bob.Patch(pref, map[string]any{})
		bob.Patch(pref, []any{"x"})
		bob.Get(pref)
		alice.Get(pref)
		// Writable relations: another user's id moves the row, a deleted_at hides it.
		wsID := s.DBStrings(`SELECT id::text FROM workspaces WHERE slug = 'acme'`)[0]
		dan.Patch(pref, map[string]any{"workspace": wsID, "project": pl, "created_by": userID(alice), "updated_by": userID(alice)})
		dan.Get(pref)
		dan.Patch(pref, map[string]any{"workspace": nil, "project": nil, "user": nil})
		dan.Patch(pref, map[string]any{"user": dead})
		dan.Patch(pref, map[string]any{"user": userID(alice)})
		dan.Get(pref)
		alice.Get(pref)
		alice.Patch(pref, map[string]any{"comment": false})
		carol.Patch(pref, map[string]any{"deleted_at": "soon"})
		carol.Patch(pref, map[string]any{"deleted_at": "2026-01-01T00:00:00Z"})
		carol.Get(pref)
		carol.Patch(pref, map[string]any{"comment": false})
		s.DBRows("preferences", `SELECT u.email, x.property_change, x.state_change, x.comment, x.mention, x.issue_completed,
				x.workspace_id IS NULL AS no_workspace, x.project_id IS NULL AS no_project,
				cb.email AS created_by, ub.email AS updated_by
			FROM user_notification_preferences x JOIN users u ON u.id = x.user_id
			LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
			ORDER BY u.email, x.comment, x.deleted_at IS NULL`)
		anon := s.Client("anon")
		anon.Get(pref)
		anon.Get(base)
		anon.Get(base + "unread/")
		anon.Post(base+"mark-all-read/", map[string]any{})
	})
}
