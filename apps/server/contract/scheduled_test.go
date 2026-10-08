package contract

import (
	"strconv"
	"testing"
)

const (
	taskHardDelete      = "plane.bgtasks.deletion_task.hard_delete"
	taskArchiveAndClose = "plane.bgtasks.issue_automation_task.archive_and_close_old_issues"
	taskStackEmail      = "plane.bgtasks.email_notification_task.stack_email_notification"
	taskEmailLogs       = "plane.bgtasks.cleanup_task.delete_email_notification_logs"
	taskPageVersions    = "plane.bgtasks.cleanup_task.delete_page_versions"
	taskDescVersions    = "plane.bgtasks.cleanup_task.delete_issue_description_versions"
)

// softDeleteCounts records, for every table with a deleted_at column that
// has rows, how many rows it holds and how many are soft-deleted.
func softDeleteCounts(s *Scenario, label string) {
	s.DBRows(label, `SELECT * FROM (SELECT table_name AS tbl,
			(xpath('/row/n/text()', query_to_xml(format('SELECT count(*) AS n FROM %I', table_name),
				false, true, '')))[1]::text::int AS total,
			(xpath('/row/n/text()', query_to_xml(format('SELECT count(*) AS n FROM %I WHERE deleted_at IS NOT NULL',
				table_name), false, true, '')))[1]::text::int AS deleted
		FROM information_schema.columns WHERE table_schema = 'public' AND column_name = 'deleted_at') c
		WHERE total > 0 ORDER BY tbl`)
}

// ageDeleted moves every soft deletion back by days (time travel for the
// HARD_DELETE_AFTER_DAYS cutoff).
func ageDeleted(s *Scenario, days int) {
	s.DBStrings(`DO $$ DECLARE t text; BEGIN
		FOR t IN SELECT table_name FROM information_schema.columns
			WHERE table_schema = 'public' AND column_name = 'deleted_at' LOOP
			EXECUTE format('UPDATE %I SET deleted_at = now() - interval ''` + strconv.Itoa(days) + ` days'' WHERE deleted_at IS NOT NULL', t);
		END LOOP; END $$`)
}

// hardDeleteNames records the named rows hard_delete may remove, live or
// soft-deleted.
func hardDeleteNames(s *Scenario) {
	// A deleted workspace's slug gets a "__<epoch>" suffix.
	s.DBRows("named_rows", `SELECT 'workspace' AS kind, split_part(slug, '__', 1) AS name, deleted_at IS NOT NULL AS deleted
			FROM workspaces
		UNION ALL SELECT 'project', identifier, deleted_at IS NOT NULL FROM projects
		UNION ALL SELECT 'label', name, deleted_at IS NOT NULL FROM labels
		UNION ALL SELECT 'state', p.identifier || '/' || s.name, s.deleted_at IS NOT NULL FROM states s JOIN projects p ON p.id = s.project_id
		UNION ALL SELECT 'issue', name, deleted_at IS NOT NULL FROM issues
		UNION ALL SELECT 'cycle', name, deleted_at IS NOT NULL FROM cycles
		UNION ALL SELECT 'module', name, deleted_at IS NOT NULL FROM modules
		UNION ALL SELECT 'page', name, deleted_at IS NOT NULL FROM pages
		UNION ALL SELECT 'view', name, deleted_at IS NOT NULL FROM issue_views
		ORDER BY 1, 2, 3`)
	s.DBRows("project_default_state", `SELECT p.identifier, st.name AS default_state FROM projects p
		LEFT JOIN states st ON st.id = p.default_state_id ORDER BY 1`)
}

func TestScheduledHardDelete(t *testing.T) {
	Run(t, "scheduled_hard_delete", func(s *Scenario) {
		alice, bob, _, _, _, pl := projectTeam(s, "160")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		a, b := userID(alice), userID(bob)

		// Labels: one deleted long ago, one recently, one kept.
		oldLabel := alice.Post(p+"issue-labels/", map[string]any{"name": "Old label"}).String("id")
		recentLabel := alice.Post(p+"issue-labels/", map[string]any{"name": "Recent label"}).String("id")
		kept := alice.Post(p+"issue-labels/", map[string]any{"name": "Kept label"}).String("id")
		alpha := alice.Post(p+"issues/", map[string]any{"name": "Alpha", "label_ids": []any{oldLabel, recentLabel, kept},
			"assignee_ids": []any{a, b}}, ids).String("id")
		beta := alice.Post(p+"issues/", map[string]any{"name": "Beta", "parent_id": alpha}, ids).String("id")
		// Unassigning soft-deletes the issue_assignees row.
		alice.Patch(p+"issues/"+alpha+"/", map[string]any{"assignee_ids": []any{a}})

		// A deleted comment reaction, link and issue reaction. (A deleted
		// comment stops the task: TestScheduledHardDeleteComment.)
		comment := bob.Post(p+"issues/"+alpha+"/comments/", map[string]any{"comment_html": "<p>Kept</p>"}).String("id")
		alice.Post(p+"comments/"+comment+"/reactions/", map[string]any{"reaction": "128077"})
		alice.Delete(p + "comments/" + comment + "/reactions/128077/")
		link := alice.Post(p+"issues/"+alpha+"/issue-links/", map[string]any{"url": "http://localhost/gone"}).String("id")
		alice.Delete(p + "issues/" + alpha + "/issue-links/" + link + "/")
		alice.Post(p+"issues/"+beta+"/reactions/", map[string]any{"reaction": "128077"})
		alice.Delete(p + "issues/" + beta + "/reactions/128077/")

		// A deleted cycle and module holding Alpha, a deleted page and view.
		cycle := alice.Post(p+"cycles/", map[string]any{"name": "Old cycle"}).String("id")
		alice.Post(p+"cycles/"+cycle+"/cycle-issues/", map[string]any{"issues": []any{alpha}})
		alice.Delete(p + "cycles/" + cycle + "/")
		module := alice.Post(p+"modules/", map[string]any{"name": "Old module"}, ids).String("id")
		alice.Post(p+"modules/"+module+"/issues/", map[string]any{"issues": []any{alpha, beta}})
		alice.Post(p+"modules/"+module+"/module-links/", map[string]any{"url": "http://localhost/spec"})
		alice.Delete(p + "modules/" + module + "/")
		page := alice.Post(p+"pages/", map[string]any{"name": "Old page"}, pageIDs).String("id")
		alice.Post(p+"pages/"+page+"/archive/", nil, Mask("archived_at")) // str(datetime.now()): wall clock
		alice.Delete(p + "pages/" + page + "/")
		view := alice.Post(p+"views/", map[string]any{"name": "Old view"}).String("id")
		alice.Delete(p + "views/" + view + "/")

		// A deleted state that is the project's default_state (SET_NULL).
		zombie := alice.Post(p+"states/", map[string]any{"name": "Zombie", "color": "#000000", "group": "cancelled"}).String("id")
		alice.Patch(p, map[string]any{"default_state": zombie}, projMask)
		alice.Delete(p + "states/" + zombie + "/")

		alice.Delete(p + "issue-labels/" + oldLabel + "/")
		alice.Delete(p + "issue-labels/" + recentLabel + "/")

		// A deleted project, and a deleted workspace, each with work in it.
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		otIssue := alice.Post(ws+"projects/"+ot+"/issues/", map[string]any{"name": "Other issue"}, ids).String("id")
		alice.Post(ws+"projects/"+ot+"/issues/"+otIssue+"/comments/", map[string]any{"comment_html": "<p>Elsewhere</p>"})
		alice.Delete(ws + "projects/" + ot + "/")
		createWorkspace(alice, "Zeta", "zeta")
		zt := alice.Post("/api/workspaces/zeta/projects/", map[string]any{"name": "Zeta", "identifier": "ZT"}, projMask).String("id")
		alice.Post("/api/workspaces/zeta/projects/"+zt+"/issues/", map[string]any{"name": "Zeta issue"}, ids)
		alice.Delete("/api/workspaces/zeta/")

		softDeleteCounts(s, "before")
		hardDeleteNames(s)

		// Nothing is old enough yet.
		s.RunJob(taskHardDelete)
		softDeleteCounts(s, "too_recent")

		// Everything deleted 61 days ago, but the Recent label 59 days ago.
		ageDeleted(s, 61)
		s.DBStrings(`UPDATE labels SET deleted_at = now() - interval '59 days' WHERE name = 'Recent label'`)
		s.RunJob(taskHardDelete)
		softDeleteCounts(s, "after")
		hardDeleteNames(s)
		// flag: soft-deleted for labels and assignees; still pointing at its
		// comment (activity) or parent (Beta).
		s.DBRows("alpha_rows", `SELECT 'label' AS kind, l.name, il.deleted_at IS NOT NULL AS flag FROM issue_labels il
				JOIN labels l ON l.id = il.label_id
			UNION ALL SELECT 'assignee', u.email, ia.deleted_at IS NOT NULL FROM issue_assignees ia JOIN users u ON u.id = ia.assignee_id
			UNION ALL SELECT 'comment_activity', ac.field || ':' || ac.verb, ac.issue_comment_id IS NOT NULL
				FROM issue_activities ac WHERE ac.field = 'comment'
			UNION ALL SELECT 'parent', i.name, i.parent_id IS NOT NULL FROM issues i WHERE i.name = 'Beta'
			ORDER BY 1, 2, 3`)

		// A second run finds nothing more.
		s.RunJob(taskHardDelete)
		softDeleteCounts(s, "again")
	})
}

// TestScheduledHardDeleteDeviation covers a deliberate deviation, so it
// runs against Go only. In Django, hard_delete fails at commit once a
// deleted issue or comment is due (its issue_activities rows point at it
// with on_delete=DO_NOTHING), every night, and the models after it in the
// order are never purged. Go deletes those activities with it, and the run
// completes.
func TestScheduledHardDeleteDeviation(t *testing.T) {
	RunGoOnly(t, func(s *Scenario) {
		alice, bob, _, _, _, pl := projectTeam(s, "161")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		count := func(sql string, args ...any) string { s.t.Helper(); return s.DBStrings(sql, args...)[0] }

		// A deleted issue with a sub-issue and a comment.
		gone := alice.Post(p+"issues/", map[string]any{"name": "Gone"}).String("id")
		alice.Post(p+"issues/", map[string]any{"name": "Child", "parent_id": gone})
		alice.Post(p+"issues/"+gone+"/comments/", map[string]any{"comment_html": "<p>Note</p>"})
		alice.Delete(p + "issues/" + gone + "/")
		// A deleted comment, with a reaction, on a live issue.
		talked := alice.Post(p+"issues/", map[string]any{"name": "Talked about",
			"assignee_ids": []any{userID(alice), userID(bob)}}).String("id")
		comment := bob.Post(p+"issues/"+talked+"/comments/", map[string]any{"comment_html": "<p>Gone</p>"}).String("id")
		alice.Post(p+"comments/"+comment+"/reactions/", map[string]any{"reaction": "128077"})
		bob.Delete(p + "issues/" + talked + "/comments/" + comment + "/")
		// Due rows before issues (a module), after comments (a label) and
		// for the final sweep (an unassigned issue_assignees row).
		module := alice.Post(p+"modules/", map[string]any{"name": "Old module"}).String("id")
		alice.Delete(p + "modules/" + module + "/")
		label := alice.Post(p+"issue-labels/", map[string]any{"name": "Old label"}).String("id")
		alice.Delete(p + "issue-labels/" + label + "/")
		alice.Patch(p+"issues/"+talked+"/", map[string]any{"assignee_ids": []any{userID(alice)}})

		softDeleted := func() string {
			return count(`SELECT sum((xpath('/row/n/text()', query_to_xml(format(
				'SELECT count(*) AS n FROM %I WHERE deleted_at IS NOT NULL', table_name), false, true, '')))[1]::text::int)::text
				FROM information_schema.columns WHERE table_schema = 'public' AND column_name = 'deleted_at'`)
		}
		talkedActivities := count(`SELECT count(*)::text FROM issue_activities WHERE issue_id = $1 AND
			(issue_comment_id IS NULL OR issue_comment_id <> $2)`, talked, comment)
		before := softDeleted()
		if before == "0" {
			t.Fatal("scenario soft-deleted nothing")
		}
		if r := s.RunJob(taskHardDelete); r != "ok" || softDeleted() != before {
			t.Fatalf("too recent: %s, soft-deleted %s -> %s", r, before, softDeleted())
		}

		ageDeleted(s, 61)
		if r := s.RunJob(taskHardDelete); r != "ok" {
			t.Fatalf("hard_delete: %s", r)
		}
		for what, got := range map[string]string{
			"soft-deleted rows left":     softDeleted(),
			"Gone and Child":             count(`SELECT count(*)::text FROM issues WHERE name IN ('Gone', 'Child')`),
			"Gone's activities":          count(`SELECT count(*)::text FROM issue_activities WHERE issue_id = $1`, gone),
			"the comment":                count(`SELECT count(*)::text FROM issue_comments WHERE id = $1`, comment),
			"the comment's activities":   count(`SELECT count(*)::text FROM issue_activities WHERE issue_comment_id = $1`, comment),
			"the comment's reaction":     count(`SELECT count(*)::text FROM comment_reactions WHERE comment_id = $1`, comment),
			"Old module":                 count(`SELECT count(*)::text FROM modules WHERE name = 'Old module'`),
			"Old label (after comments)": count(`SELECT count(*)::text FROM labels WHERE name = 'Old label'`),
			"unassigned row (final sweep)": count(`SELECT count(*)::text FROM issue_assignees WHERE issue_id = $1
				AND deleted_at IS NOT NULL`, talked),
		} {
			if got != "0" {
				t.Errorf("%s: %s rows, want 0", what, got)
			}
		}
		if got := count(`SELECT count(*)::text FROM issues WHERE name = 'Talked about'`); got != "1" {
			t.Errorf("Talked about: %s rows, want 1", got)
		}
		if got := count(`SELECT count(*)::text FROM issue_activities WHERE issue_id = $1`, talked); got != talkedActivities {
			t.Errorf("Talked about keeps %s of its %s other activities", got, talkedActivities)
		}
		if r := s.RunJob(taskHardDelete); r != "ok" {
			t.Errorf("second run: %s", r)
		}
	})
}

// automationRows records the issues the automation may touch and what it
// wrote: state, archive, the activities and notifications from the project
// creator, and the queued notification emails.
func automationRows(s *Scenario) {
	s.DBRows("issues", `SELECT p.identifier, i.name, sp.identifier || '/' || st.name AS state,
			i.archived_at IS NOT NULL AS archived, i.archived_at = current_date AS archived_today,
			i.updated_at > now() - interval '1 hour' AS touched
		FROM issues i JOIN projects p ON p.id = i.project_id LEFT JOIN states st ON st.id = i.state_id
		LEFT JOIN projects sp ON sp.id = st.project_id ORDER BY 1, 2`)
	s.DBRows("automation_activities", `SELECT i.name AS issue, a.verb, a.field, a.comment, a.old_value, a.new_value,
			ns.name AS new_state, u.email AS actor, a.created_by_id IS NULL AS no_created_by
		FROM issue_activities a JOIN issues i ON i.id = a.issue_id LEFT JOIN users u ON u.id = a.actor_id
		LEFT JOIN states ns ON ns.id = a.new_identifier
		WHERE a.comment LIKE 'Plane %' ORDER BY i.name, a.created_at`)
	s.DBRows("automation_notifications", `SELECT r.email AS receiver, i.name AS issue, n.sender, n.title,
			n.data->'issue_activity'->>'field' AS field, n.data->'issue_activity'->>'new_value' AS new_value, t.email AS triggered_by
		FROM notifications n JOIN users r ON r.id = n.receiver_id JOIN issues i ON i.id = n.entity_identifier
		JOIN users t ON t.id = n.triggered_by_id
		WHERE t.email = 'alice@example.com' ORDER BY 1, 2, 5, 6`)
	s.DBRows("automation_emails", `SELECT r.email AS receiver, i.name AS issue, l.data->'issue_activity'->>'field' AS field,
			l.data->'issue_activity'->>'new_value' AS new_value, t.email AS triggered_by
		FROM email_notification_logs l JOIN users r ON r.id = l.receiver_id JOIN issues i ON i.id = l.entity_identifier
		JOIN users t ON t.id = l.triggered_by_id
		WHERE t.email = 'alice@example.com' ORDER BY 1, 2, 3, 4`)
}

func TestScheduledArchiveAndClose(t *testing.T) {
	Run(t, "scheduled_archive_close", func(s *Scenario) {
		alice, bob, carol, _, _, pl := projectTeam(s, "163")
		s.AliasToday()
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		c := userID(carol)

		states := alice.Get(p + "states/")
		state := func(name string) string { return idOf(findBy(states, "name", name)) }
		// The project closes into Stale, which also comes first of all
		// cancelled states by sequence.
		stale := alice.Post(p+"states/", map[string]any{"name": "Stale", "color": "#999999", "group": "cancelled"}).String("id")
		alice.Patch(p+"states/"+stale+"/", map[string]any{"sequence": 1})
		alice.Patch(p, map[string]any{"archive_in": 1, "close_in": 2, "default_state": stale}, projMask)

		issue := func(name, st string) string {
			return bob.Post(p+"issues/", map[string]any{"name": name, "state_id": state(st), "assignee_ids": []any{c}},
				ids).String("id")
		}
		issue("Done old", "Done")
		issue("Done fresh", "Done")
		running := issue("Cancelled in running cycle", "Cancelled")
		ended := issue("Done in ended cycle", "Done")
		twoMods := issue("Done in two ended modules", "Done")
		left := issue("Done left a module", "Done")
		issue("Backlog old", "Backlog")
		issue("Started recently", "In Progress")

		// Cycles and modules get their dates with SQL once the issues are in
		// (a completed cycle takes no new issues).
		runningCycle := alice.Post(p+"cycles/", map[string]any{"name": "Running"}).String("id")
		endedCycle := alice.Post(p+"cycles/", map[string]any{"name": "Ended"}).String("id")
		alice.Post(p+"cycles/"+runningCycle+"/cycle-issues/", map[string]any{"issues": []any{running}})
		alice.Post(p+"cycles/"+endedCycle+"/cycle-issues/", map[string]any{"issues": []any{ended}})
		m1 := alice.Post(p+"modules/", map[string]any{"name": "M1"}, ids).String("id")
		m2 := alice.Post(p+"modules/", map[string]any{"name": "M2"}, ids).String("id")
		m3 := alice.Post(p+"modules/", map[string]any{"name": "M3"}, ids).String("id")
		alice.Post(p+"modules/"+m1+"/issues/", map[string]any{"issues": []any{twoMods}})
		alice.Post(p+"modules/"+m2+"/issues/", map[string]any{"issues": []any{twoMods}})
		alice.Post(p+"modules/"+m3+"/issues/", map[string]any{"issues": []any{left}})
		alice.Delete(p + "modules/" + m3 + "/issues/" + left + "/")

		// A second project closing after a month, with no default_state: it
		// closes into the first cancelled state anywhere, Plane Lite's Stale.
		qt := alice.Post(ws+"projects/", map[string]any{"name": "Quarter", "identifier": "QT"}, projMask).String("id")
		alice.Patch(ws+"projects/"+qt+"/", map[string]any{"close_in": 1}, projMask)
		qtBacklog := idOf(findBy(alice.Get(ws+"projects/"+qt+"/states/"), "name", "Backlog"))
		alice.Post(ws+"projects/"+qt+"/members/", map[string]any{"members": []any{map[string]any{"member_id": userID(bob), "role": 15}}})
		bob.Post(ws+"projects/"+qt+"/issues/", map[string]any{"name": "Quarter backlog old", "state_id": qtBacklog}, ids)

		s.DBStrings(`UPDATE cycles SET start_date = now() - interval '20 days', end_date = now() - interval '10 days'
			WHERE name = 'Ended'`)
		s.DBStrings(`UPDATE cycles SET start_date = now() - interval '20 days', end_date = now() + interval '10 days'
			WHERE name = 'Running'`)
		s.DBStrings(`UPDATE modules SET target_date = current_date - 5 WHERE name IN ('M1', 'M2')`)
		s.DBStrings(`UPDATE modules SET target_date = current_date + 5 WHERE name = 'M3'`)
		// Last touched long ago (after every write above).
		for name, days := range map[string]int{"Done old": 31, "Done fresh": 29, "Cancelled in running cycle": 31,
			"Done in ended cycle": 31, "Done in two ended modules": 31, "Done left a module": 31, "Backlog old": 61,
			"Started recently": 59, "Quarter backlog old": 31} {
			s.DBStrings(`UPDATE issues SET updated_at = now() - make_interval(days => $2) WHERE name = $1`, name, days)
		}

		automationRows(s)
		s.RunJob(taskArchiveAndClose)
		automationRows(s)

		// Again: nothing is due any more.
		s.RunJob(taskArchiveAndClose)
		automationRows(s)
	})
}

// digestRows records the queued notification emails and how far each got.
func digestRows(s *Scenario) {
	s.DBRows("email_notification_logs", `SELECT r.email AS receiver, i.name AS issue, t.email AS triggered_by,
			l.data->'issue_activity'->>'field' AS field, l.data->'issue_activity'->>'new_value' AS new_value,
			l.processed_at IS NOT NULL AS processed, l.sent_at IS NOT NULL AS sent
		FROM email_notification_logs l JOIN users r ON r.id = l.receiver_id JOIN users t ON t.id = l.triggered_by_id
		LEFT JOIN issues i ON i.id = l.entity_identifier
		ORDER BY 1, 2, 3, 4, 5`)
}

// aliasDigestTimes maps the "HH:MM AM/PM" activity times the digest prints
// (the wall clock of each change) to "<time>".
func aliasDigestTimes(s *Scenario) {
	for _, hm := range s.DBStrings(`SELECT DISTINCT substring(data->'issue_activity'->>'activity_time' FROM 12 FOR 5)
			FROM email_notification_logs WHERE data->'issue_activity' ? 'activity_time'`) {
		suffix := " AM"
		if hm[:2] >= "12" {
			suffix = " PM"
		}
		s.Alias(hm+suffix, "<time>")
	}
}

func TestScheduledEmailDigest(t *testing.T) {
	Run(t, "scheduled_email_digest", func(s *Scenario) {
		alice, bob, carol, _, _, pl := projectTeam(s, "164")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		states := alice.Get(p + "states/")
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"}).String("id")

		// Nothing queued yet.
		s.RunJob(taskStackEmail)
		digestRows(s)

		alice.Patch("/api/users/me/", map[string]any{"first_name": "Alice", "last_name": "Admin"}, Mask("token"))
		bob.Patch("/api/users/me/", map[string]any{"first_name": "Bob", "last_name": "Builder"}, Mask("token"))

		// One change per field and actor: several values of one field come
		// out of Django in an order that varies (tied ORDER BY receiver
		// rows, Python set order of assignees), and the mail shows the
		// first or last of them.
		issue := alice.Post(p+"issues/", map[string]any{"name": "Digest <me>", "priority": "low",
			"assignee_ids": []any{userID(bob)}}, ids).String("id")
		carol.Post(p+"issues/"+issue+"/subscribe/", nil)
		other := alice.Post(p+"issues/", map[string]any{"name": "Blocked one"}, ids).String("id")
		alice.Patch(p+"issues/"+issue+"/", map[string]any{"priority": "high"})
		alice.Patch(p+"issues/"+issue+"/", map[string]any{"state_id": idOf(findBy(states, "name", "In Progress"))})
		alice.Patch(p+"issues/"+issue+"/", map[string]any{"name": "Digest & more"})
		alice.Patch(p+"issues/"+issue+"/", map[string]any{"target_date": "2030-01-15", "label_ids": []any{bug}})
		alice.Post(p+"issues/"+issue+"/issue-links/", map[string]any{"url": "http://localhost/spec"})
		alice.Post(p+"issues/"+issue+"/issue-relation/", map[string]any{"relation_type": "blocking", "issues": []any{other}})
		bob.Post(p+"issues/"+issue+"/comments/", map[string]any{
			"comment_html": "<p>Looks good, " + mention(carol) + " can you check?</p>"})
		digestRows(s)

		aliasDigestTimes(s)
		s.RunJob(taskStackEmail)
		s.LatestEmail("bob@example.com")
		s.LatestEmail("carol@example.com")
		s.LatestEmail("alice@example.com")
		digestRows(s)

		// Nothing new: the logs are processed.
		s.RunJob(taskStackEmail)
		digestRows(s)

		// A change on an issue deleted before the digest runs: processed,
		// never sent.
		doomed := alice.Post(p+"issues/", map[string]any{"name": "Doomed", "assignee_ids": []any{userID(bob)}}, ids).String("id")
		alice.Patch(p+"issues/"+doomed+"/", map[string]any{"priority": "medium"})
		alice.Delete(p + "issues/" + doomed + "/")
		s.RunJob(taskStackEmail)
		digestRows(s)
	})
}

func cleanupRows(s *Scenario) {
	s.DBRows("email_notification_logs", `SELECT i.name AS issue, l.data->'issue_activity'->>'field' AS field,
			l.processed_at IS NOT NULL AS processed, l.sent_at IS NOT NULL AS sent
		FROM email_notification_logs l LEFT JOIN issues i ON i.id = l.entity_identifier ORDER BY 1, 2, 3, 4`)
	s.DBRows("page_versions", `SELECT pg.name AS page, v.description_html, v.deleted_at IS NOT NULL AS deleted
		FROM page_versions v JOIN pages pg ON pg.id = v.page_id ORDER BY pg.name, v.created_at DESC`)
	s.DBRows("issue_description_versions", `SELECT i.name AS issue, v.description_html, v.deleted_at IS NOT NULL AS deleted
		FROM issue_description_versions v JOIN issues i ON i.id = v.issue_id ORDER BY i.name, v.created_at DESC`)
}

func TestScheduledCleanup(t *testing.T) {
	Run(t, "scheduled_cleanup", func(s *Scenario) {
		alice, bob, carol, _, _, pl := projectTeam(s, "165")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")

		// Email logs: two sent issues and one never sent (deleted first). A
		// receiver's every mail marks all their logs sent, so the unsent one
		// goes to someone else.
		for _, name := range []string{"Sent long ago", "Sent recently", "Never sent"} {
			to := bob
			if name == "Never sent" {
				to = carol
			}
			id := alice.Post(p+"issues/", map[string]any{"name": name, "assignee_ids": []any{userID(to)}}, ids).String("id")
			alice.Patch(p+"issues/"+id+"/", map[string]any{"priority": "high"})
			if name == "Never sent" {
				alice.Delete(p + "issues/" + id + "/")
			}
		}
		s.RunJob(taskStackEmail)
		s.DBStrings(`UPDATE email_notification_logs l SET sent_at = now() - make_interval(days => CASE i.name
				WHEN 'Sent long ago' THEN 8 ELSE 6 END)
			FROM issues i WHERE i.id = l.entity_identifier AND l.sent_at IS NOT NULL`)
		s.DBStrings(`UPDATE email_notification_logs SET created_at = now() - interval '30 days',
			processed_at = now() - interval '30 days' WHERE sent_at IS NULL`)

		// Versions, seeded with SQL a hour apart (the version tasks keep one
		// per 10 minutes): a page with 22 (one soft-deleted), a page with 3,
		// an issue with its own version plus 21 older ones.
		long := alice.Post(p+"pages/", map[string]any{"name": "Long"}, pageIDs).String("id")
		short := alice.Post(p+"pages/", map[string]any{"name": "Short"}, pageIDs).String("id")
		for page, n := range map[string]int{long: 22, short: 3} {
			s.DBStrings(`INSERT INTO page_versions (id, created_at, updated_at, last_saved_at, description_html,
					description_json, sub_pages_data, owned_by_id, page_id, workspace_id, deleted_at)
				SELECT gen_random_uuid(), now() - make_interval(hours => n), now(), now(), '<p>v' || n || '</p>', '{}', '{}',
					pg.owned_by_id, pg.id, pg.workspace_id, CASE WHEN n = 3 THEN now() END
				FROM pages pg, generate_series(1, $2) n WHERE pg.id = $1 RETURNING id::text`, page, n)
		}
		alice.Post(p+"issues/", map[string]any{"name": "Versioned", "description_html": "<p>current</p>"}, ids)
		s.DBStrings(`INSERT INTO issue_description_versions (id, created_at, updated_at, last_saved_at, description_html,
				description_json, owned_by_id, issue_id, project_id, workspace_id)
			SELECT gen_random_uuid(), now() - make_interval(hours => n), now(), now(), '<p>d' || n || '</p>', '{}',
				i.created_by_id, i.id, i.project_id, i.workspace_id
			FROM issues i, generate_series(1, 21) n WHERE i.name = 'Versioned' RETURNING id::text`)
		cleanupRows(s)

		s.RunJob(taskEmailLogs)
		s.RunJob(taskPageVersions)
		s.RunJob(taskDescVersions)
		cleanupRows(s)

		// Again: nothing left over the limits.
		s.RunJob(taskEmailLogs)
		s.RunJob(taskPageVersions)
		s.RunJob(taskDescVersions)
		cleanupRows(s)
	})
}
