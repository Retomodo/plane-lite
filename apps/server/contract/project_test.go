package contract

import (
	"strings"
	"testing"
)

// projMask hides random workspace colors in nested bodies.
var projMask = Mask("background_color", "[].background_color")

// addToWorkspace invites each user to the workspace with a role and has
// them accept from their invitations page.
func addToWorkspace(s *Scenario, admin *Client, slug string, members []*Client, emails []string, roles []int) {
	var entries []any
	for i, e := range emails {
		entries = append(entries, map[string]any{"email": e, "role": roles[i]})
	}
	admin.Post("/api/workspaces/"+slug+"/invitations/", map[string]any{"emails": entries})
	aliasInviteTokens(s)
	for _, c := range members {
		r := c.Get("/api/users/me/workspaces/invitations/")
		c.Post("/api/users/me/workspaces/invitations/", map[string]any{"invitations": []any{idOf(findBy(r, "workspace.slug", slug))}})
	}
}

func userID(c *Client) string {
	return c.Get("/api/users/me/", Mask("token")).String("id")
}

func TestProjectCreate(t *testing.T) {
	Run(t, "project_create", func(s *Scenario) {
		alice := s.ClientFrom("alice", "10.0.12.1")
		signUp(alice, "alice@example.com", strongPassword)
		createWorkspace(alice, "Acme", "acme")
		bob := s.ClientFrom("bob", "10.0.12.2")
		signUp(bob, "bob@example.com", strongPassword)
		carol := s.ClientFrom("carol", "10.0.12.3")
		signUp(carol, "carol@example.com", strongPassword)
		addToWorkspace(s, alice, "acme", []*Client{bob, carol}, []string{"bob@example.com", "carol@example.com"}, []int{15, 5})
		bobID := userID(bob)

		const ps = "/api/workspaces/acme/projects/"
		alice.Get(ps, SortBy("", "identifier"))
		alice.Get(ps + "details/")

		// Validation.
		alice.Post(ps, map[string]any{})
		alice.Post(ps, map[string]any{"name": "Plane-Lite", "identifier": "P.L"})
		alice.Post(ps, map[string]any{"name": "", "identifier": strings.Repeat("A", 13), "network": 1, "archive_in": 13, "close_in": -1})
		alice.Post(ps, map[string]any{
			"name": "Bad refs", "identifier": "BAD",
			"project_lead":            "00000000-0000-4000-8000-00000000dead",
			"default_state":           "00000000-0000-4000-8000-00000000dead",
			"estimate":                "nope",
			"timezone":                "Mars/Base",
			"module_view":             "maybe",
			"logo_props":              nil,
			"archived_at":             "soon",
			"workspace":               "00000000-0000-4000-8000-00000000dead",
			"cover_image":             strings.Repeat("x", 10),
			"external_id":             strings.Repeat("x", 256),
			"guest_view_all_features": 1,
		})
		alice.Post(ps, []any{"x"})

		created := alice.Post(ps, map[string]any{
			"name":         "Plane Lite",
			"identifier":   " pl ",
			"description":  "Team notes and tickets",
			"network":      2,
			"logo_props":   map[string]any{"in_use": "emoji", "emoji": map[string]any{"value": "128640"}},
			"project_lead": bobID,
			"cycle_view":   true,
			"module_view":  true,
			"inbox_view":   true, // read-only on create
			"workspace":    "00000000-0000-4000-8000-00000000dead",
		}, projMask)
		pl := created.String("id")
		s.DBRows("PL states", `SELECT name, color, slug, sequence, "group", "default", is_triage, description,
				workspace_id, created_by_id, updated_by_id, external_id, external_source, deleted_at IS NULL AS live
			FROM states WHERE project_id = $1 ORDER BY sequence`, pl)
		s.DBRows("PL members", `SELECT pm.member_id, pm.role, pm.is_active, pm.created_by_id, pm.updated_by_id, pm.sort_order,
				pm.comment, pm.view_props, pm.default_props, pm.preferences, pup.sort_order AS property_sort_order,
				pup.created_by_id AS property_created_by, pup.updated_by_id AS property_updated_by
			FROM project_members pm JOIN project_user_properties pup ON pup.project_id = pm.project_id AND pup.user_id = pm.member_id
			WHERE pm.project_id = $1 ORDER BY pm.created_at`, pl)
		s.DBRows("PL identifier", `SELECT name, project_id, workspace_id, created_by_id, updated_by_id
			FROM project_identifiers WHERE project_id = $1`, pl)
		alice.Post(ps, map[string]any{"name": "Plane Lite", "identifier": "PL"})
		alice.Post(ps, map[string]any{"name": "Secret", "identifier": "pl"})
		secret := alice.Post(ps, map[string]any{"name": "Secret", "identifier": "SEC", "network": 0, "timezone": "Asia/Tokyo"}).String("id")
		bob.Post(ps, map[string]any{"name": "Bobs", "identifier": "BOB"})
		carol.Post(ps, map[string]any{"name": "Guest", "identifier": "GST"})

		// Lists: guests see their projects, members also see public ones.
		alice.Get(ps, SortBy("", "identifier"))
		bob.Get(ps, SortBy("", "identifier"))
		carol.Get(ps)
		alice.Get(ps + "details/")
		bob.Get(ps + "details/")
		carol.Get(ps + "details/")
		alice.Get(ps + "details/?fields=id,name")
		alice.Get(ps + "details/?per_page=1&cursor=1:0:0")
		alice.Get(ps + "details/?per_page=1&cursor=1:1:0&order_by=-name")
		alice.Get(ps + "details/?per_page=1&cursor=nope")
		alice.Get(ps + "details/?per_page=x&cursor=1:0:0")
		alice.Get(ps + "details/?per_page=5000&cursor=1:0:0")
		alice.Get(ps + "details/?per_page=10&cursor=10:0:0&order_by=--name")

		alice.Get(ps + pl + "/")
		bob.Get(ps + pl + "/")
		bob.Get(ps + secret + "/")
		carol.Get(ps + pl + "/")
		alice.Get(ps + "00000000-0000-4000-8000-00000000dead/")
		alice.Get("/api/workspaces/other/projects/" + pl + "/")
		alice.Get(ps + pl + "/")
		s.DBRows("recent visits", `SELECT entity_name, entity_identifier, user_id, project_id, workspace_id, created_by_id,
				updated_by_id, visited_at >= created_at AS touched
			FROM user_recent_visits ORDER BY created_at`)

		// Identifiers.
		alice.Get("/api/workspaces/acme/project-identifiers/?name=pl")
		alice.Get("/api/workspaces/acme/project-identifiers/?name=%20")
		alice.Get("/api/workspaces/acme/project-identifiers/?name=zz")
		carol.Get("/api/workspaces/acme/project-identifiers/?name=pl")

		// Updates: project admins and workspace admins only.
		carol.Patch(ps+pl+"/", map[string]any{"name": "Mine"})
		bob.Patch(ps+pl+"/", map[string]any{"name": "Plane Lite 2", "description": "", "close_in": 3, "timezone": "Europe/Paris"}, projMask)
		alice.Patch(ps+pl+"/", map[string]any{"identifier": "sec"})
		alice.Patch(ps+pl+"/", map[string]any{"name": "Secret", "network": "2", "archive_in": "x"})
		alice.Patch(ps+pl+"/", map[string]any{"inbox_view": true, "page_view": false, "identifier": "plx"}, projMask)
		alice.Patch(ps+pl+"/", map[string]any{"inbox_view": false}, projMask)
		alice.Patch(ps+"00000000-0000-4000-8000-00000000dead/", map[string]any{"name": "X"})
		alice.Get("/api/workspaces/acme/project-identifiers/?name=plx")

		// Archive.
		carol.Post(ps+pl+"/archive/", nil)
		alice.Post(ps+pl+"/archive/", nil, Mask("archived_at"))
		alice.Get(ps + pl + "/")
		alice.Patch(ps+pl+"/", map[string]any{"name": "Archived"})
		alice.Get(ps, SortBy("", "identifier"))
		alice.Delete(ps + pl + "/archive/")
		alice.Get(ps+pl+"/", projMask)

		// Delete.
		carol.Delete(ps + secret + "/")
		bob.Delete(ps + secret + "/")
		alice.Delete(ps + secret + "/")
		alice.Delete(ps + secret + "/")
		alice.Get(ps, SortBy("", "identifier"))
		s.DBRows("deleted project", `SELECT p.deleted_at IS NOT NULL AS deleted, p.updated_by_id,
				(SELECT count(*) FROM states WHERE project_id = p.id AND deleted_at IS NULL) AS live_states,
				(SELECT count(*) FROM project_members WHERE project_id = p.id AND deleted_at IS NULL) AS live_members,
				(SELECT count(*) FROM project_user_properties WHERE project_id = p.id AND deleted_at IS NULL) AS live_properties,
				(SELECT count(*) FROM project_identifiers WHERE project_id = p.id AND deleted_at IS NULL) AS live_identifiers,
				(SELECT count(*) FROM user_recent_visits WHERE project_id = p.id AND deleted_at IS NULL) AS live_visits
			FROM projects p WHERE p.id = $1`, secret)
		alice.Get("/api/workspaces/acme/project-identifiers/?name=sec")
		alice.Post(ps, map[string]any{"name": "Secret", "identifier": "SEC"}, projMask)

		// description_html (a JSONField) is cleaned as str(value).
		for _, html := range []any{
			`<p onclick="x()">Hi <script>x()</script><a href="javascript:y()">there</a> &amp; <b>bold</b></p>`,
			map[string]any{"html": "<i>x</i>"}, "", nil, 0,
		} {
			alice.Patch(ps+pl+"/", map[string]any{"description_html": html}, projMask)
			s.DBRows("project html", `SELECT description_html FROM projects WHERE id = $1`, pl)
		}
		alice.Post(ps, map[string]any{"name": "Html", "identifier": "HTML", "description_html": "<p>a<style>b</style></p>"}, projMask)
		s.DBRows("created html", `SELECT description_html FROM projects WHERE identifier = 'HTML'`)
	})
}

func TestProjectMembers(t *testing.T) {
	Run(t, "project_members", func(s *Scenario) {
		alice := s.ClientFrom("alice", "10.0.13.1")
		signUp(alice, "alice@example.com", strongPassword)
		alice.Patch("/api/users/me/", map[string]any{"first_name": "Alice"}, Mask("token"))
		createWorkspace(alice, "Acme", "acme")
		var clients []*Client
		var emails []string
		names := []string{"bob", "carol", "dan", "erin"}
		for i, n := range names {
			c := s.ClientFrom(n, "10.0.13."+string(rune('2'+i)))
			signUp(c, n+"@example.com", strongPassword)
			clients = append(clients, c)
			emails = append(emails, n+"@example.com")
		}
		bob, carol, dan, erin := clients[0], clients[1], clients[2], clients[3]
		addToWorkspace(s, alice, "acme", clients, emails, []int{15, 5, 15, 20})
		outsider := s.ClientFrom("outsider", "10.0.13.9")
		signUp(outsider, "outsider@example.com", strongPassword)
		ids := map[string]string{}
		for i, c := range clients {
			ids[names[i]] = userID(c)
		}
		ids["outsider"] = userID(outsider)

		pl := alice.Post("/api/workspaces/acme/projects/", map[string]any{"name": "Plane Lite", "identifier": "PL"}, projMask).String("id")
		secret := alice.Post("/api/workspaces/acme/projects/", map[string]any{"name": "Secret", "identifier": "SEC", "network": 0}, projMask).String("id")
		p := "/api/workspaces/acme/projects/" + pl + "/"

		alice.Get(p + "members/")
		alice.Post(p+"members/", map[string]any{})
		alice.Post(p+"members/", map[string]any{"members": []any{map[string]any{"member_id": ids["carol"], "role": 15}}})
		alice.Post(p+"members/", map[string]any{"members": []any{map[string]any{"member_id": ids["erin"], "role": 15}}})
		alice.Post(p+"members/", map[string]any{"members": []any{map[string]any{"member_id": ids["outsider"], "role": 15}}})
		bob.Post(p+"members/", map[string]any{"members": []any{map[string]any{"member_id": ids["bob"], "role": 20}}})
		added := alice.Post(p+"members/", map[string]any{"members": []any{
			map[string]any{"member_id": ids["bob"], "role": 15},
			map[string]any{"member_id": ids["carol"], "role": 5},
			map[string]any{"member_id": ids["erin"], "role": 20},
		}})
		s.LatestEmail("bob@example.com")
		_ = added

		members := alice.Get(p + "members/")
		alice.Get(p + "members/?search=bo")
		bobPM := idOf(findBy(members, "member", ids["bob"]))
		carolPM := idOf(findBy(members, "member", ids["carol"]))
		erinPM := idOf(findBy(members, "member", ids["erin"]))
		var alicePM string
		for _, e := range members.Data.([]any) {
			m := e.(map[string]any)
			if m["member"] != ids["bob"] && m["member"] != ids["carol"] && m["member"] != ids["erin"] {
				alicePM, _ = m["id"].(string)
			}
		}
		alice.Get(p + "members/" + bobPM + "/")
		carol.Get(p + "members/" + bobPM + "/")
		alice.Get(p + "members/00000000-0000-4000-8000-00000000dead/")
		dan.Get(p + "members/")
		outsider.Get(p + "members/")

		// Role changes.
		carol.Patch(p+"members/"+bobPM+"/", map[string]any{"role": 5})
		bob.Patch(p+"members/"+bobPM+"/", map[string]any{"role": 20})
		bob.Patch(p+"members/"+carolPM+"/", map[string]any{"role": 15})
		alice.Patch(p+"members/"+carolPM+"/", map[string]any{"role": 15})
		alice.Patch(p+"members/"+carolPM+"/", map[string]any{"role": "x"})
		alice.Patch(p+"members/"+bobPM+"/", map[string]any{"role": 7})
		alice.Patch(p+"members/"+bobPM+"/", map[string]any{"role": 20, "comment": "lead", "sort_order": 3.5, "member": ids["dan"], "project": secret, "preferences": map[string]any{"pages": map[string]any{"block_display": false}}})
		carol.Patch(p+"members/"+erinPM+"/", map[string]any{"is_active": false})
		// bob is now a project admin but only a workspace member.
		bob.Patch(p+"members/"+erinPM+"/", map[string]any{"role": 15})
		bob.Patch(p+"members/"+carolPM+"/", map[string]any{"role": 20})
		bob.Patch(p+"members/"+erinPM+"/", map[string]any{"is_active": true})
		bob.Patch(p+"members/"+carolPM+"/", map[string]any{"role": 5, "sort_order": "x", "view_props": nil, "is_active": "maybe", "comment": 5})
		alice.Patch(p+"members/"+alicePM+"/", map[string]any{"role": 15})
		alice.Patch(p+"members/"+alicePM+"/", map[string]any{"role": 20})

		alice.Get(p + "project-members/me/")
		carol.Get(p + "project-members/me/")
		dan.Get(p + "project-members/me/")
		alice.Get("/api/users/me/workspaces/acme/project-roles/")
		carol.Get("/api/users/me/workspaces/acme/project-roles/")
		outsider.Get("/api/users/me/workspaces/acme/project-roles/")
		alice.Get("/api/workspaces/acme/project-members/")
		carol.Get("/api/workspaces/acme/project-members/")

		// Per-user project settings.
		carol.Get(p + "user-properties/")
		carol.Patch(p+"user-properties/", map[string]any{
			"filters":            map[string]any{"priority": []any{"urgent"}},
			"display_properties": map[string]any{"key": false},
			"sort_order":         12.5,
			"preferences":        map[string]any{"pages": map[string]any{"block_display": false}},
			"user":               ids["bob"],
		})
		carol.Patch(p+"user-properties/", map[string]any{"display_filters": nil, "sort_order": "x"})
		dan.Get(p + "user-properties/")
		s.DBRows("carol's PL properties", `SELECT user_id, project_id, workspace_id, created_by_id, updated_by_id, sort_order
			FROM project_user_properties WHERE project_id = $1 AND user_id = $2`, pl, ids["carol"])

		// Self-join from the projects page.
		const join = "/api/users/me/workspaces/acme/projects/invitations/"
		dan.Post(join, map[string]any{"project_ids": []any{secret}})
		dan.Post(join, map[string]any{"project_ids": []any{pl, "00000000-0000-4000-8000-00000000dead"}})
		carol.Post(join, map[string]any{"project_ids": []any{pl}})
		erin.Post(join, map[string]any{"project_ids": []any{secret}})
		dan.Get("/api/users/me/workspaces/acme/project-roles/")
		dan.Get(p + "project-members/me/")
		erin.Get("/api/workspaces/acme/projects/" + secret + "/user-properties/")
		s.DBRows("self-joined memberships", `SELECT pm.member_id, pm.project_id, pm.role, pm.is_active, pm.created_by_id,
				pm.updated_by_id, pm.sort_order, pup.sort_order AS property_sort_order, pup.created_by_id AS property_created_by
			FROM project_members pm LEFT JOIN project_user_properties pup
				ON pup.project_id = pm.project_id AND pup.user_id = pm.member_id AND pup.deleted_at IS NULL
				JOIN users u ON u.id = pm.member_id JOIN projects p ON p.id = pm.project_id
			WHERE pm.member_id IN ($1, $2) ORDER BY u.email, p.identifier`, ids["dan"], ids["erin"])

		// Removal and leaving.
		alice.Delete(p + "members/" + alicePM + "/")
		bob.Delete(p + "members/" + erinPM + "/")
		alice.Delete(p + "members/" + carolPM + "/")
		alice.Delete(p + "members/" + carolPM + "/")
		carol.Get(p + "project-members/me/")
		bob.Post(p+"members/leave/", nil)
		bob.Post(p+"members/leave/", nil)
		alice.Post("/api/workspaces/acme/projects/"+secret+"/members/leave/", nil)
		alice.Get(p + "members/")
		// Re-adding a removed member reactivates the row.
		alice.Post(p+"members/", map[string]any{"members": []any{map[string]any{"member_id": ids["carol"], "role": 5}}})
		alice.Get(p + "members/")
		s.DBRows("PL memberships", `SELECT pm.member_id, pm.role, pm.is_active, pm.created_by_id, pm.updated_by_id,
				pm.deleted_at IS NULL AS live
			FROM project_members pm JOIN users u ON u.id = pm.member_id WHERE pm.project_id = $1 ORDER BY u.email`, pl)
	})
}
