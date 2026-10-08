package contract

import (
	"strings"
	"testing"
)

// favRows records every user_favorites row (live or not) with the entity's
// name and the creator and updater, by email.
func favRows(s *Scenario) {
	s.DBRows("user_favorites", `SELECT u.email, f.entity_type, f.entity_identifier IS NULL AS no_entity,
			coalesce(c.name, m.name, v.name, pg.name, ep.name) AS entity, f.name, f.is_folder, f.sequence,
			par.name AS parent, p.identifier AS project, w.slug AS workspace,
			cb.email AS created_by, ub.email AS updated_by, f.deleted_at IS NULL AS live
		FROM user_favorites f JOIN users u ON u.id = f.user_id JOIN workspaces w ON w.id = f.workspace_id
		LEFT JOIN projects p ON p.id = f.project_id LEFT JOIN user_favorites par ON par.id = f.parent_id
		LEFT JOIN cycles c ON c.id = f.entity_identifier AND f.entity_type = 'cycle'
		LEFT JOIN modules m ON m.id = f.entity_identifier AND f.entity_type = 'module'
		LEFT JOIN issue_views v ON v.id = f.entity_identifier AND f.entity_type = 'view'
		LEFT JOIN pages pg ON pg.id = f.entity_identifier AND f.entity_type = 'page'
		LEFT JOIN projects ep ON ep.id = f.entity_identifier AND f.entity_type = 'project'
		LEFT JOIN users cb ON cb.id = f.created_by_id LEFT JOIN users ub ON ub.id = f.updated_by_id
		ORDER BY u.email, f.entity_type, f.name, coalesce(c.name, m.name, v.name, pg.name, ep.name), f.sequence, f.deleted_at IS NULL`)
}

// favFixture builds the entities the favorites point at: in project PL a
// cycle, a module, a view, two pages and an issue, and in project OT (where
// only alice is a member) one cycle.
type favFixture struct {
	pl, ot                   string
	cycle, module, view      string
	page, archivedPage       string
	issue, otherCycle, oPage string
}

func newFavFixture(s *Scenario, alice, bob *Client, pl string) favFixture {
	const ws = "/api/workspaces/acme/"
	p := ws + "projects/" + pl + "/"
	f := favFixture{pl: pl}
	f.ot = alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
	po := ws + "projects/" + f.ot + "/"
	ids := Unordered("assignee_ids", "[].assignee_ids", "member_ids", "[].member_ids", "label_ids", "[].label_ids",
		"project_ids", "[].project_ids")
	f.cycle = alice.Post(p+"cycles/", map[string]any{"name": "Sprint", "logo_props": map[string]any{"in_use": "emoji"}}, ids).String("id")
	f.otherCycle = alice.Post(po+"cycles/", map[string]any{"name": "Elsewhere"}, ids).String("id")
	f.module = alice.Post(p+"modules/", map[string]any{"name": "Mod"}, ids).String("id")
	f.view = alice.Post(p+"views/", map[string]any{"name": "View", "logo_props": map[string]any{"in_use": "icon"}}).String("id")
	f.page = alice.Post(p+"pages/", map[string]any{"name": "Doc", "logo_props": map[string]any{"in_use": "emoji"}}, ids).String("id")
	f.archivedPage = alice.Post(p+"pages/", map[string]any{"name": "Old doc"}, ids).String("id")
	// Archived by SQL: the archive response carries the wall-clock time.
	s.DBStrings(`UPDATE pages SET archived_at = now() WHERE id = $1 RETURNING id::text`, f.archivedPage)
	f.oPage = alice.Post(po+"pages/", map[string]any{"name": "Other doc"}, ids).String("id")
	// Assignees come back newest user first in the recent visits.
	f.issue = alice.Post(p+"issues/", map[string]any{"name": "Issue", "assignee_ids": []any{userID(bob), userID(alice)}},
		Unordered("label_ids", "assignee_ids", "module_ids")).String("id")
	return f
}

func TestFavoritesWorkspace(t *testing.T) {
	Run(t, "favorite_workspace", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "160")
		const ws = "/api/workspaces/acme/"
		const fav = ws + "user-favorites/"
		f := newFavFixture(s, alice, bob, pl)
		// Another workspace and project, to favorite across workspaces.
		createWorkspace(alice, "Beta", "beta")
		beta := alice.Post("/api/workspaces/beta/projects/", map[string]any{"name": "Beta", "identifier": "BT"}, projMask).String("id")

		bobID := userID(bob)

		// Nothing yet.
		alice.Get(fav)

		// Folders: no entity, the sequence starts at the default.
		folder := alice.Post(fav, map[string]any{"entity_type": "folder", "name": "Folder A", "is_folder": true}).String("id")
		alice.Post(fav, map[string]any{"entity_type": "folder", "name": "Folder A", "is_folder": true})
		second := alice.Post(fav, map[string]any{"entity_type": "folder", "name": "Folder B", "is_folder": true, "sequence": 5}).String("id")

		// Entities. A repeat answers the existing favorite with 200.
		cyc := alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": f.cycle, "project_id": pl}).String("id")
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": f.cycle, "project_id": pl, "name": "ignored"})
		alice.Post(fav, map[string]any{"entity_type": "module", "entity_identifier": f.module, "project_id": pl})
		alice.Post(fav, map[string]any{"entity_type": "view", "entity_identifier": f.view, "project_id": pl})
		alice.Post(fav, map[string]any{"entity_type": "page", "entity_identifier": f.page, "project_id": pl})
		alice.Post(fav, map[string]any{"entity_type": "page", "entity_identifier": f.archivedPage, "project_id": pl})
		alice.Post(fav, map[string]any{"entity_type": "project", "entity_identifier": pl, "project_id": pl})
		// A page favorite without a project is hidden from the list.
		alice.Post(fav, map[string]any{"entity_type": "page", "entity_identifier": f.oPage})
		// Entities without a lite serializer, or that do not exist, have no entity_data.
		alice.Post(fav, map[string]any{"entity_type": "issue", "entity_identifier": f.issue, "project_id": pl})
		alice.Post(fav, map[string]any{"entity_type": "banana", "entity_identifier": dead})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": dead, "project_id": pl})
		// A cycle of another project the user belongs to.
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": f.otherCycle, "project_id": f.ot})
		// The sequence given is replaced once the workspace has favorites.
		alice.Post(fav, map[string]any{"entity_type": "folder", "name": "Late", "sequence": 3.5, "parent": folder})
		// Writable fields: name on an entity, a parent, is_folder, ignored ones.
		alice.Post(fav, map[string]any{"entity_type": "module", "entity_identifier": dead, "name": "Named", "is_folder": true,
			"parent": second, "project_id": pl, "id": dead, "workspace_id": dead, "user": dead, "created_by": dead, "deleted_at": "2020-01-01"})
		// Integer and odd-case identifiers.
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": 0})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": 7})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": strings.ToUpper(f.cycle), "project_id": strings.ToUpper(pl)})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": nil, "name": "null id"})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "name": "no id"})

		// Other users.
		bobCycle := bob.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": f.cycle, "project_id": pl}).String("id")
		bob.Post(fav, map[string]any{"entity_type": "folder", "name": "Bob's", "is_folder": true})
		carol.Post(fav, map[string]any{"entity_type": "folder", "name": "Guest"})
		// Nothing checks that the user belongs to the project.
		dan.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": f.cycle, "project_id": pl})
		dan.Post(fav, map[string]any{"entity_type": "folder", "name": "Dan's"})
		outsider.Post(fav, map[string]any{"entity_type": "folder", "name": "Outsider"})
		s.Client("anon").Post(fav, map[string]any{"entity_type": "folder"})

		// A project in another workspace: the favorite moves to that workspace
		// (and takes its sequence from there) ...
		far := "00000000-0000-4000-8000-00000000f001"
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": far, "project_id": beta, "name": "far"})
		// ... so the same entity in this workspace trips the unique index.
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": far, "name": "again"})

		// A soft-deleted project is still found by save(): self.project uses the base manager.
		gone := alice.Post(ws+"projects/", map[string]any{"name": "Gone", "identifier": "GN"}, projMask).String("id")
		s.DBStrings(`UPDATE projects SET deleted_at = now() WHERE id = $1 RETURNING id::text`, gone)
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": "00000000-0000-4000-8000-00000000f002", "project_id": gone})

		// Project ids that fail (each with an entity not favorited yet).
		for i, pid := range []any{dead, "nope", []any{pl}, 5, "", true, 0, map[string]any{}} {
			alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": "00000000-0000-4000-8000-00000000f10" + string(rune('0'+i)), "project_id": pid})
		}

		// Validation.
		alice.Post(fav, map[string]any{})
		alice.Post(fav, map[string]any{"entity_type": nil, "name": nil})
		alice.Post(fav, map[string]any{"entity_type": strings.Repeat("x", 101), "name": strings.Repeat("n", 256)})
		alice.Post(fav, map[string]any{"entity_type": "", "name": ""})
		alice.Post(fav, map[string]any{"entity_type": "folder", "is_folder": "maybe", "sequence": "x", "parent": dead})
		alice.Post(fav, map[string]any{"entity_type": "folder", "parent": "nope"})
		alice.Post(fav, map[string]any{"entity_type": "folder", "parent": []any{folder}})
		alice.Post(fav, map[string]any{"entity_type": "folder", "parent": true})
		alice.Post(fav, map[string]any{"entity_type": "folder", "parent": nil, "is_folder": nil, "sequence": nil})
		alice.Post(fav, map[string]any{"entity_type": []any{"x"}})
		alice.Post(fav, map[string]any{"entity_type": 12})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": ""})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": "nope"})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": []any{"x"}})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": map[string]any{"a": 1}})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": 1.5})
		alice.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": true})
		alice.Post(fav, map[string]any{"entity_identifier": f.cycle})
		alice.Post(fav, []any{"x"})
		alice.Post(fav, "text")
		alice.Post(fav, nil)

		// Roles on the workspace.
		for _, c := range []*Client{bob, dan} {
			c.Get(fav)
		}
		carol.Get(fav)
		outsider.Get(fav)
		s.Client("anon").Get(fav)
		alice.Get("/api/workspaces/nope/user-favorites/")
		alice.Post("/api/workspaces/nope/user-favorites/", map[string]any{"entity_type": "folder"})

		// The list: roots only, hidden when the user left the project.
		alice.Get(fav)
		s.DBStrings(`UPDATE project_members SET is_active = false WHERE project_id = $1 AND member_id = $2 RETURNING id::text`, pl, bobID)
		bob.Get(fav)
		s.DBStrings(`UPDATE project_members SET is_active = true WHERE project_id = $1 AND member_id = $2 RETURNING id::text`, pl, bobID)
		// The membership join does not look at deleted_at: an old, soft-deleted
		// membership row that is still marked active doubles the project's
		// favorites. A soft-deleted project keeps its favorites in the list too.
		s.DBStrings(`INSERT INTO project_members (id, created_at, updated_at, role, is_active, member_id, project_id, workspace_id,
				deleted_at, sort_order, view_props, default_props, preferences)
			SELECT gen_random_uuid(), created_at, now(), role, true, member_id, project_id, workspace_id, now(), sort_order,
				view_props, default_props, preferences FROM project_members WHERE project_id = $1 AND member_id = $2
			RETURNING id::text`, pl, bobID)
		bob.Get(fav)
		alice.Get(fav + folder + "/group/")
		s.DBStrings(`UPDATE projects SET deleted_at = now() WHERE id = $1 RETURNING id::text`, f.ot)
		alice.Get(fav)
		s.DBStrings(`UPDATE projects SET deleted_at = NULL, archived_at = now() WHERE id = $1 RETURNING id::text`, f.ot)
		alice.Get(fav)

		// Group: the children of a folder.
		alice.Get(fav + folder + "/group/")
		alice.Get(fav + second + "/group/")
		alice.Get(fav + cyc + "/group/")
		alice.Get(fav + dead + "/group/")
		bob.Get(fav + folder + "/group/")
		carol.Get(fav + folder + "/group/")
		outsider.Get(fav + folder + "/group/")
		alice.Get(fav + "nope/group/")

		// Entities that are deleted (here by SQL: the delete endpoints would
		// also remove the favorites) have no entity_data.
		s.DBStrings(`UPDATE cycles SET deleted_at = now() WHERE id = $1 RETURNING id::text`, f.cycle)
		s.DBStrings(`UPDATE modules SET deleted_at = now() WHERE id = $1 RETURNING id::text`, f.module)
		s.DBStrings(`UPDATE pages SET deleted_at = now() WHERE id = $1 RETURNING id::text`, f.archivedPage)
		alice.Get(fav)
		alice.Get(fav + folder + "/group/")
		alice.Get(fav + second + "/group/")

		// Update.
		alice.Patch(fav+folder+"/", map[string]any{"name": "Renamed"})
		alice.Patch(fav+folder+"/", map[string]any{})
		alice.Patch(fav+second+"/", map[string]any{"sequence": 123.5, "is_folder": false, "parent": folder})
		alice.Patch(fav+cyc+"/", map[string]any{"parent": second, "name": "in B", "entity_type": "view", "entity_identifier": f.view, "project_id": dead})
		alice.Patch(fav+second+"/", map[string]any{"parent": nil, "name": nil})
		alice.Patch(fav+second+"/", map[string]any{"parent": second})
		alice.Patch(fav+second+"/", map[string]any{"parent": dead, "sequence": "x", "is_folder": "maybe", "entity_type": strings.Repeat("x", 101)})
		alice.Patch(fav+second+"/", map[string]any{"entity_type": nil, "entity_identifier": "nope", "name": strings.Repeat("n", 256)})
		alice.Patch(fav+second+"/", []any{"x"})
		alice.Patch(fav+second+"/", "text")
		// Changing it into a favorite that exists breaks the unique index.
		alice.Patch(fav+folder+"/", map[string]any{"entity_type": "view", "entity_identifier": f.view})
		alice.Patch(fav+dead+"/", map[string]any{"name": "x"})
		alice.Patch(fav+bobCycle+"/", map[string]any{"name": "stolen"})
		bob.Patch(fav+bobCycle+"/", map[string]any{"name": "mine"})
		carol.Patch(fav+folder+"/", map[string]any{"name": "x"})
		outsider.Patch(fav+folder+"/", map[string]any{"name": "x"})
		alice.Patch(fav+"nope/", map[string]any{"name": "x"})
		alice.Get(fav)
		alice.Get(fav + folder + "/group/")

		// A soft-deleted favorite does not count: it can be favorited again
		// and gets a new sequence.
		s.DBStrings(`UPDATE user_favorites SET deleted_at = now() WHERE id = $1 RETURNING id::text`, bobCycle)
		bob.Patch(fav+bobCycle+"/", map[string]any{"name": "gone"})
		bob.Delete(fav + bobCycle + "/")
		bob.Post(fav, map[string]any{"entity_type": "cycle", "entity_identifier": f.cycle, "project_id": pl})

		// Delete: hard, and with the folder's children.
		carol.Delete(fav + folder + "/")
		outsider.Delete(fav + folder + "/")
		bob.Delete(fav + folder + "/")
		alice.Delete(fav + dead + "/")
		alice.Delete(fav + "nope/")
		favRows(s)
		alice.Delete(fav + folder + "/")
		alice.Delete(fav + folder + "/")
		alice.Get(fav)
		alice.Delete(fav + second + "/")
		alice.Get(fav)
		favRows(s)
	})
}

func TestFavoritesEntity(t *testing.T) {
	Run(t, "favorite_entities", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "161")
		const ws = "/api/workspaces/acme/"
		const fav = ws + "user-favorites/"
		p := ws + "projects/" + pl + "/"
		f := newFavFixture(s, alice, bob, pl)
		anon := s.Client("anon")
		po := ws + "projects/" + f.ot + "/"

		// Each entity: POST with the entity in the body (the page in the URL),
		// DELETE with it in the URL, for every role.
		type kind struct{ post, del, key, entity string }
		for _, k := range []kind{
			{"user-favorite-cycles/", "user-favorite-cycles/", "cycle", f.cycle},
			{"user-favorite-modules/", "user-favorite-modules/", "module", f.module},
			{"user-favorite-views/", "user-favorite-views/", "view", f.view},
		} {
			body := map[string]any{k.key: k.entity}
			alice.Post(p+k.post, body)
			alice.Post(p+k.post, body) // a duplicate: IntegrityError
			bob.Post(p+k.post, body)
			carol.Post(p+k.post, body)
			dan.Post(p+k.post, body)
			outsider.Post(p+k.post, body)
			anon.Post(p+k.post, body)
			alice.Get(fav)
			bob.Get(fav)
			favRows(s)
			// Odd bodies. Nothing checks the entity exists or is in the project.
			alice.Post(p+k.post, map[string]any{k.key: dead})
			alice.Post(p+k.post, map[string]any{k.key: nil})
			alice.Post(p+k.post, map[string]any{})
			alice.Post(p+k.post, nil)
			alice.Post(p+k.post, map[string]any{k.key: "nope"})
			alice.Post(p+k.post, map[string]any{k.key: []any{k.entity}})
			alice.Post(p+k.post, map[string]any{k.key: 1.5})
			alice.Post(p+k.post, map[string]any{k.key: 3})
			alice.Post(p+k.post, map[string]any{k.key: strings.ToUpper(f.otherCycle)})
			alice.Post(p+k.post, map[string]any{k.key: true})
			alice.Post(p+k.post, map[string]any{k.key: ""})
			alice.Post(p+k.post, []any{"x"})
			alice.Post(p+k.post, "text")
			alice.Post(po+k.post, map[string]any{k.key: f.otherCycle})
			// Other projects and workspaces.
			alice.Post(ws+"projects/"+dead+"/"+k.post, body)
			alice.Post(ws+"projects/nope/"+k.post, body)
			alice.Post("/api/workspaces/nope/projects/"+pl+"/"+k.post, body)
			s.Client("anon").Delete(p + k.del + k.entity + "/")
			// Delete: only your own favorite, found by project too.
			carol.Delete(p + k.del + k.entity + "/")
			dan.Delete(p + k.del + k.entity + "/")
			outsider.Delete(p + k.del + k.entity + "/")
			alice.Delete(po + k.del + k.entity + "/")
			alice.Delete(p + k.del + dead + "/")
			alice.Delete(p + k.del + "nope/")
			alice.Delete(ws + "projects/nope/" + k.del + k.entity + "/")
			bob.Delete(p + k.del + k.entity + "/")
			bob.Delete(p + k.del + k.entity + "/")
			alice.Delete(p + k.del + k.entity + "/")
			alice.Delete(p + k.del + dead + "/")
			alice.Delete(p + k.del + "00000000-0000-4000-8000-0000000000e0/")
			favRows(s)
			alice.Get(fav)
		}

		// Pages: the page comes from the URL.
		fp := p + "favorite-pages/"
		alice.Post(fp+f.page+"/", nil)
		alice.Post(fp+f.page+"/", map[string]any{"ignored": 1})
		bob.Post(fp+f.page+"/", []any{"ignored"})
		carol.Post(fp+f.page+"/", nil)
		dan.Post(fp+f.page+"/", nil)
		outsider.Post(fp+f.page+"/", nil)
		anon.Post(fp+f.page+"/", nil)
		alice.Post(fp+dead+"/", nil)
		alice.Post(fp+f.archivedPage+"/", nil)
		alice.Post(fp+"nope/", nil)
		alice.Post(po+"favorite-pages/"+f.oPage+"/", nil)
		alice.Post(ws+"projects/"+dead+"/favorite-pages/"+f.page+"/", nil)
		alice.Get(fav)
		bob.Get(fav)
		favRows(s)

		// Projects: any signed-in user, any workspace, any project.
		fpj := ws + "user-favorite-projects/"
		body := map[string]any{"project": pl}
		alice.Post(fpj, body)
		alice.Post(fpj, body)
		bob.Post(fpj, body)
		carol.Post(fpj, body)
		dan.Post(fpj, body)
		outsider.Post(fpj, body)
		anon.Post(fpj, body)
		alice.Post(fpj, map[string]any{"project": f.ot})
		bob.Post(fpj, map[string]any{"project": f.ot})
		alice.Post("/api/workspaces/nope/user-favorite-projects/", map[string]any{"project": f.ot})
		alice.Post(fpj, map[string]any{})
		alice.Post(fpj, map[string]any{"project": nil})
		alice.Post(fpj, nil)
		alice.Post(fpj, map[string]any{"project": dead})
		alice.Post(fpj, map[string]any{"project": "nope"})
		alice.Post(fpj, map[string]any{"project": ""})
		alice.Post(fpj, map[string]any{"project": []any{pl}})
		alice.Post(fpj, map[string]any{"project": 1.5})
		alice.Post(fpj, map[string]any{"project": 3})
		alice.Post(fpj, map[string]any{"project": true})
		alice.Post(fpj, map[string]any{"project": strings.ToUpper(pl)})
		alice.Post(fpj, []any{"x"})
		alice.Post(fpj, "text")
		alice.Get(fav)
		outsider.Get(fav)
		favRows(s)
		anon.Delete(fpj + pl + "/")
		carol.Delete(fpj + pl + "/")
		alice.Delete(fpj + dead + "/")
		alice.Delete(fpj + "nope/")
		alice.Delete("/api/workspaces/nope/user-favorite-projects/" + pl + "/")
		alice.Delete("/api/workspaces/nope/user-favorite-projects/" + f.ot + "/")
		alice.Delete(fpj + pl + "/")
		alice.Delete(fpj + pl + "/")
		outsider.Delete(fpj + pl + "/")
		bob.Delete(fpj + f.ot + "/")
		favRows(s)
		alice.Get(fav)
	})
}

func TestRecentVisits(t *testing.T) {
	Run(t, "recent_visits", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "162")
		const ws = "/api/workspaces/acme/"
		const rv = ws + "recent-visits/"
		p := ws + "projects/" + pl + "/"
		f := newFavFixture(s, alice, bob, pl)
		anon := s.Client("anon")
		// Id arrays come back in uuid order, which varies run to run.
		noIDs := Unordered("label_ids", "assignee_ids", "module_ids", "*.label_ids", "*.assignee_ids", "*.module_ids")

		// Visits are written by the retrieve views and the project issue lists.
		alice.Get(rv)
		alice.Get(p+"issues/"+f.issue+"/", noIDs)
		alice.Get(p + "pages/" + f.page + "/")
		alice.Get(p + "pages/" + f.archivedPage + "/")
		alice.Get(p + "cycles/" + f.cycle + "/")
		alice.Get(p + "modules/" + f.module + "/")
		alice.Get(p + "views/" + f.view + "/")
		alice.Get(p+"issues/?per_page=10", noIDs)
		bob.Get(p+"issues/"+f.issue+"/", noIDs)
		bob.Get(p + "pages/" + f.page + "/")
		carol.Get(p+"issues/"+f.issue+"/", noIDs)
		alice.Get(rv)
		bob.Get(rv)
		carol.Get(rv)
		dan.Get(rv)
		outsider.Get(rv)
		anon.Get(rv)
		alice.Get("/api/workspaces/nope/recent-visits/")

		// The filter: one entity_name, only issue, page and project are listed.
		for _, q := range []string{"issue", "page", "project", "cycle", "module", "view", "", "bogus", "page&entity_name=issue",
			"issue&entity_name=page", "ISSUE", "%20issue"} {
			alice.Get(rv + "?entity_name=" + q)
		}
		alice.Get(rv + "?other=1")

		// A revisit keeps the order (it follows creation, not the visit) ...
		alice.Get(p+"issues/"+f.issue+"/", noIDs)
		alice.Get(rv)
		// ... other workspaces do not show ...
		createWorkspace(alice, "Beta", "beta")
		beta := alice.Post("/api/workspaces/beta/projects/", map[string]any{"name": "Beta", "identifier": "BT"}, projMask).String("id")
		bi := alice.Post("/api/workspaces/beta/projects/"+beta+"/issues/", map[string]any{"name": "Beta issue"}, noIDs).String("id")
		alice.Get("/api/workspaces/beta/projects/" + beta + "/issues/" + bi + "/")
		alice.Get("/api/workspaces/beta/recent-visits/")
		alice.Get(rv)
		// ... and entities that are gone have no entity_data.
		s.DBStrings(`UPDATE pages SET deleted_at = now() WHERE id = $1 RETURNING id::text`, f.page)
		s.DBStrings(`UPDATE projects SET deleted_at = now() WHERE id = $1 RETURNING id::text`, pl)
		alice.Get(rv)
		s.DBStrings(`UPDATE projects SET deleted_at = NULL WHERE id = $1 RETURNING id::text`, pl)
		s.DBStrings(`UPDATE user_recent_visits SET deleted_at = now() WHERE entity_name = 'view' RETURNING id::text`)
		alice.Get(rv)
		s.DBStrings(`UPDATE issues SET deleted_at = now() WHERE id = $1 RETURNING id::text`, f.issue)
		alice.Get(rv)
		// A visit without an entity.
		s.DBStrings(`INSERT INTO user_recent_visits (id, created_at, updated_at, entity_name, entity_identifier, visited_at, user_id,
				workspace_id, project_id)
			SELECT gen_random_uuid(), now(), now(), 'page', NULL, now(), u.id, p.workspace_id, p.id
			FROM users u, projects p WHERE u.email = 'alice@example.com' AND p.id = $1 RETURNING id::text`, pl)
		alice.Get(rv)

		// More than twenty: visiting makes room by soft-deleting the oldest.
		var issues []string
		for i := 0; i < 22; i++ {
			issues = append(issues, alice.Post(p+"issues/", map[string]any{"name": "Bulk " + string(rune('A'+i))}, noIDs).String("id"))
		}
		for _, id := range issues {
			alice.Get(p + "issues/" + id + "/")
		}
		alice.Get(rv)
		alice.Get(rv + "?entity_name=issue")
		alice.Get(rv + "?entity_name=page")
		s.DBRows("recent_visits", `SELECT u.email, v.entity_name, v.deleted_at IS NULL AS live, v.project_id IS NOT NULL AS in_project,
				v.created_by_id = v.user_id AS by_user
			FROM user_recent_visits v JOIN users u ON u.id = v.user_id
			ORDER BY u.email, v.entity_name, v.deleted_at IS NULL, v.created_at`)

		// Past twenty live rows (seeded by SQL), the list stops at twenty.
		s.DBStrings(`INSERT INTO user_recent_visits (id, created_at, updated_at, entity_name, entity_identifier, visited_at, user_id,
				workspace_id, project_id)
			SELECT gen_random_uuid(), now() + n * interval '1 second', now(), 'issue', gen_random_uuid(), now(), u.id, p.workspace_id, p.id
			FROM users u, projects p, generate_series(1, 25) n WHERE u.email = 'bob@example.com' AND p.id = $1 RETURNING id::text`, pl)
		bob.Get(rv)
		bob.Get(rv + "?entity_name=page")
		bob.Get(rv + "?entity_name=issue")
	})
}
