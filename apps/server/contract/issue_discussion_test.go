package contract

import (
	"strings"
	"testing"
)

// discussionRows records what comment, reaction and subscription writes
// touch, by name and email rather than by id. Rows are ordered by content or
// by creation time, never by a random id.
func discussionRows(s *Scenario) {
	s.DBRows("issues", `SELECT i.name, p.identifier, ub.email AS updated_by,
			(SELECT count(*) FROM issue_activities a WHERE a.issue_id = i.id AND a.created_at > i.updated_at) AS activities_after_touch
		FROM issues i JOIN projects p ON p.id = i.project_id LEFT JOIN users ub ON ub.id = i.updated_by_id
		ORDER BY p.identifier, i.sequence_id`)
	s.DBRows("issue_comments", `SELECT i.name AS issue, p.identifier, w.slug, c.comment_html, c.comment_stripped,
			c.comment_json, c.attachments, c.access, c.external_source, c.external_id, c.edited_at IS NOT NULL AS edited,
			a.email AS actor, cb.email AS created_by, ub.email AS updated_by, par.comment_html AS parent,
			c.updated_at >= c.created_at AS touched, c.deleted_at IS NULL AS live,
			d.description_html, d.description_stripped, d.description_json, d.description_binary IS NULL AS no_binary,
			dp.identifier AS description_project, dw.slug AS description_slug, dcb.email AS description_created_by,
			dub.email AS description_updated_by, d.updated_at = c.updated_at AS description_synced,
			d.created_at > c.created_at AS description_after, d.deleted_at IS NULL AS description_live
		FROM issue_comments c JOIN issues i ON i.id = c.issue_id JOIN projects p ON p.id = c.project_id
		JOIN workspaces w ON w.id = c.workspace_id
		LEFT JOIN users a ON a.id = c.actor_id LEFT JOIN users cb ON cb.id = c.created_by_id
		LEFT JOIN users ub ON ub.id = c.updated_by_id LEFT JOIN issue_comments par ON par.id = c.parent_id
		LEFT JOIN descriptions d ON d.id = c.description_id LEFT JOIN projects dp ON dp.id = d.project_id
		LEFT JOIN workspaces dw ON dw.id = d.workspace_id
		LEFT JOIN users dcb ON dcb.id = d.created_by_id LEFT JOIN users dub ON dub.id = d.updated_by_id
		ORDER BY c.created_at`)
	s.DBRows("descriptions", `SELECT count(*) AS total,
			count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM issue_comments c WHERE c.description_id = d.id)) AS orphans
		FROM descriptions d`)
	s.DBRows("issue_reactions", `SELECT i.name AS issue, p.identifier, w.slug, r.reaction, a.email AS actor,
			cb.email AS created_by, ub.email AS updated_by, r.deleted_at IS NULL AS live
		FROM issue_reactions r JOIN issues i ON i.id = r.issue_id JOIN projects p ON p.id = r.project_id
		JOIN workspaces w ON w.id = r.workspace_id JOIN users a ON a.id = r.actor_id
		LEFT JOIN users cb ON cb.id = r.created_by_id LEFT JOIN users ub ON ub.id = r.updated_by_id
		ORDER BY r.created_at`)
	s.DBRows("comment_reactions", `SELECT c.comment_html AS comment, p.identifier, w.slug, r.reaction, a.email AS actor,
			cb.email AS created_by, ub.email AS updated_by, r.deleted_at IS NULL AS live
		FROM comment_reactions r JOIN issue_comments c ON c.id = r.comment_id JOIN projects p ON p.id = r.project_id
		JOIN workspaces w ON w.id = r.workspace_id JOIN users a ON a.id = r.actor_id
		LEFT JOIN users cb ON cb.id = r.created_by_id LEFT JOIN users ub ON ub.id = r.updated_by_id
		ORDER BY r.created_at`)
	s.DBRows("issue_activities", `SELECT i.name AS issue, p.identifier, a.verb, a.field, a.old_value, a.new_value,
			a.comment, a.old_identifier, a.new_identifier, c.comment_html AS issue_comment, u.email AS actor,
			a.epoch IS NOT NULL AS has_epoch, cb.email AS created_by, a.deleted_at IS NULL AS live
		FROM issue_activities a LEFT JOIN issues i ON i.id = a.issue_id JOIN projects p ON p.id = a.project_id
		LEFT JOIN issue_comments c ON c.id = a.issue_comment_id
		LEFT JOIN users u ON u.id = a.actor_id LEFT JOIN users cb ON cb.id = a.created_by_id
		ORDER BY a.created_at`)
	s.DBRows("issue_subscribers", `SELECT i.name, p.identifier, u.email, cb.email AS created_by, ub.email AS updated_by,
			x.deleted_at IS NULL AS live
		FROM issue_subscribers x JOIN issues i ON i.id = x.issue_id JOIN projects p ON p.id = x.project_id
		JOIN users u ON u.id = x.subscriber_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY i.name, p.identifier, u.email, x.deleted_at IS NULL, x.created_at`)
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
			n.data->'issue_activity'->>'actor' AS actor,
			EXISTS (SELECT 1 FROM issue_activities a WHERE a.id::text = n.data->'issue_activity'->>'id') AS activity_ok,
			n.message_html, n.read_at IS NULL AS unread
		FROM notifications n JOIN users r ON r.id = n.receiver_id LEFT JOIN users t ON t.id = n.triggered_by_id
		LEFT JOIN issues i ON i.id = n.entity_identifier LEFT JOIN projects p ON p.id = n.project_id
		ORDER BY r.email, n.sender, n.title, n.message::text, verb, field, new_value, old_value, issue_comment,
			t.email, n.data->'issue'->>'name'`)
	s.DBRows("email_notification_logs", `SELECT r.email AS receiver, t.email AS triggered_by, e.entity_name, e.entity,
			i.name AS issue, e.data->'issue' AS issue_data,
			e.data->'issue_activity'->>'verb' AS verb, e.data->'issue_activity'->>'field' AS field,
			e.data->'issue_activity'->>'old_value' AS old_value, e.data->'issue_activity'->>'new_value' AS new_value,
			e.data->'issue_activity'->>'issue_comment' AS issue_comment,
			e.data->'issue_activity' ? 'activity_time' AS has_time, e.processed_at IS NULL AS pending
		FROM email_notification_logs e JOIN users r ON r.id = e.receiver_id LEFT JOIN users t ON t.id = e.triggered_by_id
		LEFT JOIN issues i ON i.id = e.entity_identifier
		ORDER BY r.email, verb, field, new_value, old_value, issue_comment, t.email, e.data->'issue'->>'name'`)
}

func TestIssueComments(t *testing.T) {
	Run(t, "issue_comments", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "40")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		bob.Patch("/api/users/me/", map[string]any{"user_timezone": "Asia/Kolkata"}, Mask("token"))
		issue := alice.Post(p+"issues/", map[string]any{"name": "Login bug", "assignee_ids": []any{userID(bob)}}, ids).String("id")
		other := alice.Post(p+"issues/", map[string]any{"name": "Other bug"}, ids).String("id")
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		otIssue := alice.Post(ws+"projects/"+ot+"/issues/", map[string]any{"name": "Elsewhere"}, ids).String("id")
		comments := func(id string) string { return p + "issues/" + id + "/comments/" }
		comment := func(id, pk string) string { return comments(id) + pk + "/" }
		history := p + "issues/" + issue + "/history/"
		// epoch is int(timezone.now().timestamp()) at the write: wall-clock time.
		epoch := Mask("[].epoch")

		// Create: any project role; a guest only on issues they created,
		// unless the project lets guests see everything.
		firstResp := bob.Post(comments(issue), map[string]any{
			"comment_html": `<p>Hey ` + mention(alice) + `, ` + mention(carol) + ` and ` + mention(dan) +
				` <script>x()</script><b onclick="y">look</b></p>`,
			"comment_json": map[string]any{"type": "doc"}, "access": "EXTERNAL", "actor": userID(alice),
			"issue": dead, "project": dead, "created_by": userID(alice), "external_source": "github", "external_id": "7",
		})
		first, firstDesc := firstResp.String("id"), firstResp.String("description")
		carol.Post(comments(issue), map[string]any{"comment_html": "<p>Guest</p>"})
		dan.Post(comments(issue), map[string]any{"comment_html": "<p>Dan</p>"})
		outsider.Post(comments(issue), map[string]any{"comment_html": "<p>Outsider</p>"})
		alice.Post(comments(dead), map[string]any{"comment_html": "<p>Nowhere</p>"})
		alice.Post(comments(issue), []any{"x"})
		alice.Post(comments(issue), map[string]any{
			"comment_html": nil, "comment_json": nil, "access": "nope", "parent": dead, "actor": "nope",
			"attachments": "x", "edited_at": "bad", "external_id": strings.Repeat("y", 256), "description": dead,
		})
		alice.Post(comments(issue), map[string]any{"attachments": []any{"not a url", "https://example.com/a.png"}})
		var many []any
		for range 11 {
			many = append(many, "https://example.com/a.png")
		}
		alice.Post(comments(issue), map[string]any{"attachments": many})
		alice.Post(comments(issue), map[string]any{})
		// A description belongs to one comment (the OneToOneField's UniqueValidator).
		alice.Post(comments(issue), map[string]any{"description": firstDesc})
		reply := alice.Post(comments(issue), map[string]any{
			"comment_html": "<p>Reply to " + mention(bob) + "</p>", "parent": first,
			"attachments": []any{"https://example.com/a.png"}, "edited_at": "2026-01-01T00:00:00Z",
		}).String("id")
		// The issue is looked up in any project.
		alice.Post(comments(otIssue), map[string]any{"comment_html": "<p>Cross project</p>"})
		bob.Post(comments(other), map[string]any{"comment_html": ""})
		alice.Patch(p, map[string]any{"guest_view_all_features": true}, projMask)
		guest := carol.Post(comments(issue), map[string]any{"comment_html": "<p>Guest " + mention(bob) + "</p>"}).String("id")

		// Update: project admins and the comment's creator.
		bob.Patch(comment(issue, first), map[string]any{
			"comment_html": `<p>Edited for ` + mention(alice) + ` and ` + mention(bob) + `<script>z()</script></p>`,
		})
		carol.Patch(comment(issue, first), map[string]any{"comment_html": "<p>Guest edit</p>"})
		bob.Patch(comment(issue, reply), map[string]any{"comment_html": "<p>Member edit</p>"})
		dan.Patch(comment(issue, first), map[string]any{"comment_html": "<p>Dan edit</p>"})
		outsider.Patch(comment(issue, first), map[string]any{"comment_html": "<p>Outsider edit</p>"})
		alice.Patch(comment(issue, first), map[string]any{"comment_html": "<p>Admin edit " + mention(carol) + "</p>"})
		alice.Patch(comment(issue, first), map[string]any{"comment_html": "<p>Admin edit " + mention(carol) + "</p>"})
		alice.Patch(comment(issue, first), map[string]any{"access": "INTERNAL", "actor": userID(carol)})
		alice.Patch(comment(issue, first), map[string]any{"access": "nope"})
		alice.Patch(comment(issue, first), map[string]any{"description": firstDesc, "comment_json": map[string]any{"type": "doc"}})
		alice.Patch(comment(issue, first), []any{"x"})
		alice.Patch(comment(issue, dead), map[string]any{"comment_html": "<p>Nope</p>"})
		alice.Patch(comment(other, first), map[string]any{"comment_html": "<p>Wrong issue</p>"})
		carol.Patch(comment(issue, guest), map[string]any{"comment_html": "<p>Guest edited</p>"})
		alice.Patch(comment(issue, reply), map[string]any{"comment_html": ""})

		// History: activity and comments, merged by creation time.
		alice.Get(history, epoch)
		bob.Get(history, epoch)
		carol.Get(history, epoch)
		dan.Get(history, epoch)
		outsider.Get(history, epoch)
		alice.Get(history+"?activity_type=issue-comment", epoch)
		alice.Get(history+"?activity_type=issue-property", epoch)
		alice.Get(history+"?created_at__gt=bad", epoch)
		alice.Get(history+"?created_at__gt=", epoch)
		alice.Get(history+"?created_at__gt=2000-01-01T00:00:00Z&activity_type=issue-comment", epoch)
		alice.Get(history+"?created_at__gt=2999-01-01", epoch)
		alice.Get(history+"?created_at__gt=2000-01-01%2010:00&activity_type=issue-property", epoch)
		alice.Get(p+"issues/"+dead+"/history/", epoch)
		alice.Get(p+"issues/"+otIssue+"/history/", epoch)

		// Delete: project admins and the comment's creator.
		carol.Delete(comment(issue, reply))
		bob.Delete(comment(issue, reply))
		carol.Delete(comment(issue, guest))
		alice.Delete(comment(issue, guest))
		alice.Delete(comment(issue, dead))
		alice.Delete(comment(other, first))
		// Deleting a comment soft-deletes its replies.
		bob.Delete(comment(issue, first))
		alice.Delete(comment(issue, reply))
		alice.Get(history, epoch)
		bob.Get(p+"issues/"+issue+"/", ids)
		discussionRows(s)
	})
}

func TestIssueReactions(t *testing.T) {
	Run(t, "issue_reactions", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "41")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		issue := alice.Post(p+"issues/", map[string]any{"name": "Login bug"}, ids).String("id")
		other := alice.Post(p+"issues/", map[string]any{"name": "Other bug"}, ids).String("id")
		reactions := p + "issues/" + issue + "/reactions/"
		// epoch is int(timezone.now().timestamp()) at the write: wall-clock time.
		epoch := Mask("[].epoch")

		// Issue reactions.
		alice.Get(reactions)
		alice.Post(reactions, map[string]any{"reaction": "128077"})
		bob.Post(reactions, map[string]any{"reaction": "128077", "issue": dead, "actor": userID(alice), "created_by": userID(alice)})
		carol.Post(reactions, map[string]any{"reaction": "🎉"})
		dan.Post(reactions, map[string]any{"reaction": "128077"})
		outsider.Post(reactions, map[string]any{"reaction": "128077"})
		alice.Post(reactions, map[string]any{"reaction": "128077"})
		alice.Post(reactions, map[string]any{})
		alice.Post(reactions, map[string]any{"reaction": ""})
		alice.Post(reactions, map[string]any{"reaction": nil})
		alice.Post(reactions, map[string]any{"reaction": true})
		alice.Post(reactions, []any{"x"})
		alice.Post(reactions, map[string]any{"reaction": "x", "created_by": "nope", "updated_by": dead})
		alice.Post(reactions, map[string]any{"reaction": 5})
		alice.Post(p+"issues/"+dead+"/reactions/", map[string]any{"reaction": "128077"})
		alice.Post(p+"issues/"+other+"/reactions/", map[string]any{"reaction": "128077"})
		alice.Get(reactions)
		bob.Get(reactions)
		carol.Get(reactions)
		dan.Get(reactions)
		outsider.Get(reactions)
		alice.Get(p + "issues/" + dead + "/reactions/")
		alice.Delete(reactions + "128077/")
		alice.Delete(reactions + "128077/")
		bob.Delete(reactions + "🎉/")
		carol.Delete(reactions + "🎉/")
		dan.Delete(reactions + "128077/")
		outsider.Delete(reactions + "128077/")
		alice.Post(reactions, map[string]any{"reaction": "128077"})
		alice.Get(reactions)

		// Comment reactions.
		c1 := bob.Post(p+"issues/"+issue+"/comments/", map[string]any{"comment_html": "<p>First</p>"}).String("id")
		c2 := alice.Post(p+"issues/"+other+"/comments/", map[string]any{"comment_html": "<p>Second</p>"}).String("id")
		creactions := func(id string) string { return p + "comments/" + id + "/reactions/" }
		alice.Get(creactions(c1))
		alice.Post(creactions(c1), map[string]any{"reaction": "128077", "comment": c2, "actor": userID(bob)})
		bob.Post(creactions(c1), map[string]any{"reaction": "128077"})
		carol.Post(creactions(c1), map[string]any{"reaction": "😀"})
		dan.Post(creactions(c1), map[string]any{"reaction": "128077"})
		outsider.Post(creactions(c1), map[string]any{"reaction": "128077"})
		alice.Post(creactions(c1), map[string]any{"reaction": "128077"})
		alice.Post(creactions(c1), map[string]any{})
		alice.Post(creactions(c1), map[string]any{"reaction": ""})
		alice.Post(creactions(c1), []any{"x"})
		alice.Post(creactions(dead), map[string]any{"reaction": "128077"})
		alice.Post(creactions(c2), map[string]any{"reaction": "128077"})
		alice.Get(creactions(c1))
		carol.Get(creactions(c1))
		dan.Get(creactions(c1))
		outsider.Get(creactions(c1))
		alice.Get(creactions(dead))
		alice.Get(p+"issues/"+issue+"/history/", epoch)
		alice.Delete(creactions(c1) + "128077/")
		alice.Delete(creactions(c1) + "128077/")
		carol.Delete(creactions(c1) + "😀/")
		dan.Delete(creactions(c1) + "128077/")
		bob.Delete(creactions(c2) + "128077/")
		alice.Delete(creactions(c2) + "128077/")
		// A deleted comment takes its reactions with it; reacting to it
		// still works but records no activity.
		bob.Delete(p + "issues/" + issue + "/comments/" + c1 + "/")
		bob.Delete(creactions(c1) + "128077/")
		bob.Get(creactions(c1))
		bob.Post(creactions(c1), map[string]any{"reaction": "128078"})
		bob.Delete(creactions(c1) + "128078/")
		alice.Get(p+"issues/"+issue+"/history/", epoch)
		discussionRows(s)
	})
}

func TestIssueSubscribe(t *testing.T) {
	Run(t, "issue_subscribe", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "42")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		issue := alice.Post(p+"issues/", map[string]any{"name": "Login bug"}, ids).String("id")
		sub := p + "issues/" + issue + "/subscribe/"

		alice.Get(sub)
		bob.Get(sub)
		bob.Post(sub, map[string]any{"subscriber": userID(alice), "issue": dead})
		bob.Post(sub, nil)
		bob.Get(sub)
		carol.Post(sub, nil)
		carol.Get(sub)
		dan.Get(sub)
		dan.Post(sub, nil)
		dan.Delete(sub)
		outsider.Get(sub)
		outsider.Post(sub, nil)
		bob.Delete(sub)
		bob.Delete(sub)
		bob.Get(sub)
		bob.Post(sub, nil)
		alice.Delete(sub)
		alice.Post(sub, nil)
		alice.Get(p + "issues/" + dead + "/subscribe/")
		alice.Post(p+"issues/"+dead+"/subscribe/", nil)
		alice.Delete(p + "issues/" + dead + "/subscribe/")
		// Commenting subscribes the commenter.
		carol.Delete(sub)
		alice.Patch(p, map[string]any{"guest_view_all_features": true}, projMask)
		carol.Post(p+"issues/"+issue+"/comments/", map[string]any{"comment_html": "<p>Hi</p>"})
		carol.Get(sub)
		bob.Get(p+"issues/"+issue+"/", ids)
		discussionRows(s)
	})
}
