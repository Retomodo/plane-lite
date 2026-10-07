package contract

import (
	"strings"
	"testing"
)

const (
	dead        = "00000000-0000-4000-8000-00000000dead"
	issueExpand = "?expand=issue_reactions,issue_attachments,issue_link,parent"
)

func mention(c *Client) string {
	return `<mention-component entity_identifier="` + userID(c) + `" entity_name="user_mention"></mention-component>`
}

// issueRows records everything an issue write touches, by name and email
// rather than by id.
func issueRows(s *Scenario) {
	s.DBRows("issues", `SELECT p.identifier, i.sequence_id, i.name, i.priority, st.name AS state, i.sort_order,
			i.start_date::text, i.target_date::text, i.completed_at IS NOT NULL AS completed, i.archived_at::text,
			i.is_draft, i.point, i.description_html, i.description_stripped, i.description_json,
			i.external_source, i.external_id, par.name AS parent, cb.email AS created_by, ub.email AS updated_by,
			i.deleted_at IS NULL AS live, i.updated_at >= i.created_at AS touched
		FROM issues i JOIN projects p ON p.id = i.project_id LEFT JOIN states st ON st.id = i.state_id
		LEFT JOIN issues par ON par.id = i.parent_id
		LEFT JOIN users cb ON cb.id = i.created_by_id LEFT JOIN users ub ON ub.id = i.updated_by_id
		ORDER BY p.identifier, i.sequence_id, i.name`)
	s.DBRows("issue_sequences", `SELECT p.identifier, q.sequence, i.name, q.deleted, q.deleted_at IS NULL AS live
		FROM issue_sequences q JOIN projects p ON p.id = q.project_id LEFT JOIN issues i ON i.id = q.issue_id
		ORDER BY p.identifier, q.sequence`)
	s.DBRows("issue_assignees", `SELECT i.name, u.email, cb.email AS created_by, ub.email AS updated_by,
			a.deleted_at IS NULL AS live
		FROM issue_assignees a JOIN issues i ON i.id = a.issue_id JOIN users u ON u.id = a.assignee_id
		LEFT JOIN users cb ON cb.id = a.created_by_id LEFT JOIN users ub ON ub.id = a.updated_by_id
		ORDER BY i.name, u.email, a.deleted_at IS NULL`)
	s.DBRows("issue_labels", `SELECT i.name, l.name AS label, cb.email AS created_by, ub.email AS updated_by,
			il.deleted_at IS NULL AS live
		FROM issue_labels il JOIN issues i ON i.id = il.issue_id JOIN labels l ON l.id = il.label_id
		LEFT JOIN users cb ON cb.id = il.created_by_id LEFT JOIN users ub ON ub.id = il.updated_by_id
		ORDER BY i.name, l.name, il.deleted_at IS NULL`)
	s.DBRows("issue_activities", `SELECT i.name AS issue, a.verb, a.field, a.old_value, a.new_value, a.comment,
			a.old_identifier, a.new_identifier, u.email AS actor, a.epoch IS NOT NULL AS has_epoch,
			cb.email AS created_by, a.deleted_at IS NULL AS live
		FROM issue_activities a LEFT JOIN issues i ON i.id = a.issue_id LEFT JOIN users u ON u.id = a.actor_id
		LEFT JOIN users cb ON cb.id = a.created_by_id
		ORDER BY i.name, a.verb, a.field NULLS FIRST, a.comment, a.new_value NULLS FIRST, a.old_value NULLS FIRST`)
	s.DBRows("issue_subscribers", `SELECT i.name, u.email, cb.email AS created_by, ub.email AS updated_by,
			x.deleted_at IS NULL AS live
		FROM issue_subscribers x JOIN issues i ON i.id = x.issue_id JOIN users u ON u.id = x.subscriber_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY i.name, u.email`)
	s.DBRows("issue_mentions", `SELECT i.name, u.email, m.deleted_at IS NULL AS live
		FROM issue_mentions m JOIN issues i ON i.id = m.issue_id JOIN users u ON u.id = m.mention_id
		ORDER BY i.name, u.email, m.deleted_at IS NULL`)
	s.DBRows("notifications", `SELECT r.email AS receiver, t.email AS triggered_by, n.sender, n.title, n.message,
			n.entity_name, i.name AS issue, p.identifier, n.data->'issue' AS issue_data,
			n.data->'issue_activity'->>'verb' AS verb, n.data->'issue_activity'->>'field' AS field,
			n.data->'issue_activity'->>'old_value' AS old_value, n.data->'issue_activity'->>'new_value' AS new_value,
			n.data->'issue_activity'->>'issue_comment' AS issue_comment,
			n.data->'issue_activity'->>'old_identifier' AS old_identifier,
			n.data->'issue_activity'->>'new_identifier' AS new_identifier,
			EXISTS (SELECT 1 FROM issue_activities a WHERE a.id::text = n.data->'issue_activity'->>'id'
				AND a.actor_id::text = n.data->'issue_activity'->>'actor') AS activity_ok,
			n.message_html, n.read_at IS NULL AS unread
		FROM notifications n JOIN users r ON r.id = n.receiver_id LEFT JOIN users t ON t.id = n.triggered_by_id
		LEFT JOIN issues i ON i.id = n.entity_identifier LEFT JOIN projects p ON p.id = n.project_id
		ORDER BY r.email, n.sender, n.title, n.message::text, verb, field, new_value, old_value,
			n.data->'issue'->>'name'`)
	s.DBRows("email_notification_logs", `SELECT r.email AS receiver, t.email AS triggered_by, e.entity_name, e.entity,
			i.name AS issue, e.data->'issue' AS issue_data,
			e.data->'issue_activity'->>'verb' AS verb, e.data->'issue_activity'->>'field' AS field,
			e.data->'issue_activity'->>'old_value' AS old_value, e.data->'issue_activity'->>'new_value' AS new_value,
			e.data->'issue_activity' ? 'activity_time' AS has_time, e.processed_at IS NULL AS pending
		FROM email_notification_logs e JOIN users r ON r.id = e.receiver_id LEFT JOIN users t ON t.id = e.triggered_by_id
		LEFT JOIN issues i ON i.id = e.entity_identifier
		ORDER BY r.email, verb, field, new_value, old_value, e.data->'issue'->>'name'`)
	s.DBRows("issue_description_versions", `SELECT i.name, o.email AS owned_by, v.description_html,
			v.description_stripped, v.description_json, cb.email AS created_by, ub.email AS updated_by,
			v.last_saved_at >= v.created_at AS saved_after
		FROM issue_description_versions v JOIN issues i ON i.id = v.issue_id JOIN users o ON o.id = v.owned_by_id
		LEFT JOIN users cb ON cb.id = v.created_by_id LEFT JOIN users ub ON ub.id = v.updated_by_id
		ORDER BY i.name, v.created_at`)
	s.DBRows("recent_visits", `SELECT u.email, v.entity_name, i.name AS issue, p.identifier,
			v.created_by_id = v.user_id AS by_user, v.deleted_at IS NULL AS live
		FROM user_recent_visits v JOIN users u ON u.id = v.user_id LEFT JOIN issues i ON i.id = v.entity_identifier
		LEFT JOIN projects p ON p.id = v.project_id
		WHERE v.entity_name = 'issue'
		ORDER BY u.email, i.name`)
}

func TestIssueCRUD(t *testing.T) {
	Run(t, "issue_crud", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "16")
		const ws = "/api/workspaces/acme/"
		proj := ws + "projects/" + pl + "/"
		p := proj
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		states := alice.Get(p + "states/")
		todo := idOf(findBy(states, "name", "Todo"))
		started := idOf(findBy(states, "name", "In Progress"))
		done := idOf(findBy(states, "name", "Done"))
		triage := s.DBStrings(`SELECT id::text FROM states WHERE "group" = 'triage' AND project_id = $1`, pl)[0]
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")
		feature := alice.Post(p+"issue-labels/", map[string]any{"name": "Feature"}).String("id")
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		otState := s.DBStrings(`SELECT id::text FROM states WHERE project_id = $1 AND name = 'Backlog'`, ot)[0]
		otLabel := alice.Post(ws+"projects/"+ot+"/issue-labels/", map[string]any{"name": "Elsewhere"}).String("id")
		otIssue := alice.Post(ws+"projects/"+ot+"/issues/", map[string]any{"name": "Other issue"}, ids).String("id")

		// Create: project admins and members.
		first := bob.Post(p+"issues/", map[string]any{"name": "First issue"}, ids).String("id")
		carol.Post(p+"issues/", map[string]any{"name": "Guest issue"}, ids)
		dan.Post(p+"issues/", map[string]any{"name": "Dan issue"}, ids)
		outsider.Post(p+"issues/", map[string]any{"name": "Outsider issue"}, ids)
		alice.Post(p+"issues/", map[string]any{}, ids)
		alice.Post(p+"issues/", []any{"x"}, ids)
		alice.Post(p+"issues/", map[string]any{
			"name": strings.Repeat("x", 256), "priority": "nope", "start_date": "2026-13-01", "point": 13,
			"sort_order": "x", "is_draft": "maybe", "description_html": nil, "label_ids": "nope",
			"assignee_ids": []any{"nope"}, "state_id": "nope", "parent_id": dead, "estimate_point": dead, "type": dead,
			"external_id": strings.Repeat("y", 256), "description_json": "{}",
		}, ids)
		alice.Post(p+"issues/", map[string]any{"name": "Dates", "start_date": "2026-10-10", "target_date": "2026-10-01"}, ids)
		alice.Post(p+"issues/", map[string]any{"name": "Other state", "state_id": otState}, ids)
		alice.Post(p+"issues/", map[string]any{"name": "Triage", "state_id": triage}, ids)
		alice.Post(p+"issues/", map[string]any{"name": "Other parent", "parent_id": otIssue}, ids)
		alice.Post(p+"issues/", map[string]any{"name": "Bad label", "label_ids": []any{dead}}, ids)
		alice.Post(p+"issues/", map[string]any{"name": "Other label", "label_ids": []any{otLabel}}, ids)
		full := alice.Post(p+"issues/", map[string]any{
			"name": "Fix login",
			"description_html": `<p>Hey ` + mention(bob) + ` and ` + mention(dan) + `, see <script>x()</script>` +
				`<a href="javascript:x()">this</a> &amp; <b onclick="y">that</b></p>`,
			"description_json": map[string]any{"type": "doc"}, "priority": "high", "state_id": todo,
			"label_ids": []any{bug, feature, otLabel}, "assignee_ids": []any{userID(bob), userID(carol), userID(dan), userID(alice)},
			"start_date": "2026-10-01", "target_date": "2026-10-31", "parent_id": first, "sort_order": 5,
			"sequence_id": 99, "point": 3, "external_source": "github", "external_id": "42",
			"id": dead, "project_id": dead, "completed_at": "2020-01-01T00:00:00Z", "created_by": userID(bob),
		}, ids).String("id")
		// state_id and state share a source; the later field (state) wins.
		alice.Post(p+"issues/", map[string]any{"name": "Both states", "state_id": todo, "state": started}, ids)
		// Drafts and archived issues are created but fall out of the response query.
		alice.Post(p+"issues/", map[string]any{"name": "Draft", "is_draft": true}, ids)
		alice.Post(p+"issues/", map[string]any{"name": "Archived", "archived_at": "2026-01-01"}, ids)
		alice.Post(p+"issues/", map[string]any{"name": "Done already", "state_id": done, "description_html": ""}, ids)
		bob.Post(p+"issues/", map[string]any{"name": "By bob", "description_html": "<p>" + mention(alice) + "</p>", "assignee_ids": []any{userID(alice)}}, ids)
		alice.Patch(proj, map[string]any{"default_assignee": userID(bob)}, projMask)
		alice.Post(p+"issues/", map[string]any{"name": "Defaulted"}, ids)
		alice.Post(p+"issues/", map[string]any{"name": "Explicit", "assignee_ids": []any{userID(alice)}}, ids)
		draft := s.DBStrings(`SELECT id::text FROM issues WHERE name = 'Draft'`)[0]

		// Retrieve.
		alice.Get(p+"issues/"+full+"/"+issueExpand, ids)
		alice.Get(p+"issues/"+first+"/"+issueExpand, ids)
		bob.Get(p+"issues/"+full+"/", ids)
		carol.Get(p+"issues/"+full+"/", ids)
		dan.Get(p+"issues/"+full+"/", ids)
		outsider.Get(p+"issues/"+full+"/", ids)
		alice.Get(p+"issues/"+dead+"/", ids)
		alice.Get(p+"issues/"+draft+"/", ids)
		alice.Get(p+"issues/"+full+"/meta/", ids)
		carol.Get(p+"issues/"+full+"/meta/", ids)
		alice.Get(p+"issues/"+draft+"/meta/", ids)
		dan.Get(p+"issues/"+full+"/meta/", ids)
		seq := s.DBStrings(`SELECT sequence_id::text FROM issues WHERE id = $1`, full)[0]
		alice.Get(ws+"work-items/PL-"+seq+"/"+issueExpand, ids)
		bob.Get(ws+"work-items/pl-"+seq+"/", ids)
		alice.Get(ws+"work-items/PL-x/", ids)
		alice.Get(ws+"work-items/PL--1/", ids)
		alice.Get(ws+"work-items/PL-999/", ids)
		alice.Get(ws+"work-items/ZZ-1/", ids)
		dan.Get(ws+"work-items/PL-"+seq+"/", ids)
		carol.Get(ws+"work-items/PL-"+seq+"/", ids)
		alice.Patch(proj, map[string]any{"guest_view_all_features": true}, projMask)
		carol.Get(p+"issues/"+full+"/", ids)

		// Update: admins, members and the creator.
		bob.Patch(p+"issues/"+full+"/", map[string]any{"name": "Fix the login", "priority": "urgent"})
		carol.Patch(p+"issues/"+full+"/", map[string]any{"name": "Guest edit"})
		dan.Patch(p+"issues/"+full+"/", map[string]any{"name": "Dan edit"})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"state_id": done})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"state_id": todo})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"label_ids": []any{feature}, "assignee_ids": []any{userID(alice), userID(dan)}})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"description_html": "<p>Now " + mention(alice) + " and " + mention(bob) + "</p>"})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"description_html": "<p>Second edit</p>"})
		bob.Patch(p+"issues/"+full+"/", map[string]any{"description_html": "<p>Bob edit " + mention(carol) + "</p>"})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"start_date": "2026-11-15"})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"start_date": "2026-12-01", "target_date": "2026-11-01"})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"parent_id": nil, "target_date": nil, "start_date": nil})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"state_id": triage})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"name": "", "priority": "nope"})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"description_html": "<p>Migrated</p>", "skip_activity": true})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"name": "Renamed quietly", "skip_activity": true})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"is_draft": true, "sort_order": 1.5, "point": 2})
		alice.Patch(p+"issues/"+full+"/", map[string]any{"is_draft": false})
		alice.Patch(p+"issues/"+full+"/", []any{"x"})
		alice.Patch(p+"issues/"+dead+"/", map[string]any{"name": "Nope"})
		alice.Get(p+"issues/"+full+"/"+issueExpand, ids)
		alice.Get(ws+"work-items/PL-"+seq+"/"+issueExpand, ids)

		// Delete: admins and the creator.
		carol.Delete(p + "issues/" + full + "/")
		bob.Delete(p + "issues/" + full + "/")
		alice.Delete(p + "issues/" + full + "/")
		alice.Delete(p + "issues/" + full + "/")
		alice.Get(p+"issues/"+full+"/", ids)
		bob.Delete(p + "issues/" + first + "/")
		issueRows(s)
	})
}
