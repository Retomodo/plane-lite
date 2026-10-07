package contract

import (
	"strings"
	"testing"
)

// wsMask hides the random background_color of WorkSpaceSerializer bodies.
var wsMask = Mask("background_color", "[].background_color")

func createWorkspace(c *Client, name, slug string) *Response {
	return c.Post("/api/workspaces/", map[string]any{"name": name, "slug": slug}, wsMask)
}

// findBy returns the object in the top-level array of r whose value at
// path (dotted) equals want.
func findBy(r *Response, path, want string) map[string]any {
	list, _ := r.Data.([]any)
	for _, e := range list {
		cur := e
		for _, part := range strings.Split(path, ".") {
			m, _ := cur.(map[string]any)
			cur = m[part]
		}
		if cur == want {
			return e.(map[string]any)
		}
	}
	return nil
}

func idOf(m map[string]any) string {
	if m == nil {
		return "00000000-0000-4000-8000-00000000dead"
	}
	s, _ := m["id"].(string)
	return s
}

// aliasInviteTokens maps every invitation token (JWTs, unique per invite)
// to a readable placeholder.
func aliasInviteTokens(s *Scenario) {
	for _, row := range s.DBStrings(`SELECT email || ' ' || token FROM workspace_member_invites`) {
		email, token, _ := strings.Cut(row, " ")
		s.Alias(token, "<invite-token:"+email+">")
	}
}

func inviteToken(s *Scenario, id string) string {
	t := s.DBStrings(`SELECT token FROM workspace_member_invites WHERE id = $1`, id)
	if len(t) == 0 {
		return ""
	}
	return t[0]
}

func TestWorkspaceCreate(t *testing.T) {
	Run(t, "workspace_create", func(s *Scenario) {
		anon := s.ClientFrom("anon", "10.0.9.1")
		anon.Get("/api/users/me/workspaces/")
		anon.Get("/api/workspace-slug-check/?slug=acme")
		anon.Post("/api/workspaces/", map[string]any{"name": "Acme", "slug": "acme"})

		alice := s.ClientFrom("alice", "10.0.9.2")
		signUp(alice, "alice@example.com", strongPassword)
		alice.Get("/api/users/me/workspaces/")

		alice.Get("/api/workspace-slug-check/")
		alice.Get("/api/workspace-slug-check/?slug=")
		alice.Get("/api/workspace-slug-check/?slug=api")
		alice.Get("/api/workspace-slug-check/?slug=Acme")

		// Plane's own checks, then serializer validation.
		alice.Post("/api/workspaces/", map[string]any{})
		alice.Post("/api/workspaces/", map[string]any{"name": "Acme"})
		alice.Post("/api/workspaces/", map[string]any{"name": "", "slug": "acme"})
		alice.Post("/api/workspaces/", map[string]any{"name": strings.Repeat("a", 81), "slug": "acme"})
		alice.Post("/api/workspaces/", map[string]any{"name": "Acme", "slug": strings.Repeat("a", 49)})
		alice.Post("/api/workspaces/", map[string]any{"name": "visit acme.com", "slug": "acme"})
		alice.Post("/api/workspaces/", map[string]any{"name": "-____-", "slug": "acme"})
		alice.Post("/api/workspaces/", map[string]any{"name": "Acme", "slug": "acme corp"})
		alice.Post("/api/workspaces/", map[string]any{"name": "Acme", "slug": "api"})
		alice.Post("/api/workspaces/", map[string]any{"name": "Acme", "slug": "acme", "timezone": "Mars/Base"})
		alice.Post("/api/workspaces/", map[string]any{"name": "Acme", "slug": "acme", "organization_size": strings.Repeat("9", 21)})
		alice.Post("/api/workspaces/", map[string]any{"name": "-_-", "slug": "a b", "timezone": "nope"})

		alice.Post("/api/workspaces/", map[string]any{
			"name":              " Acme ",
			"slug":              "acme",
			"organization_size": "2-10",
			"timezone":          "Asia/Kolkata",
			"company_role":      "CTO",
			"owner":             "00000000-0000-4000-8000-000000000001", // read-only
			"id":                "00000000-0000-4000-8000-000000000002",
			"logo":              "https://example.com/logo.png",
		}, wsMask)
		alice.Post("/api/workspaces/", map[string]any{"name": "Again", "slug": "acme"})
		alice.Get("/api/workspace-slug-check/?slug=acme")
		createWorkspace(alice, "Beta", "beta")

		alice.Get("/api/users/me/workspaces/", wsMask)
		alice.Get("/api/users/me/workspaces/?search=bet", wsMask)
		alice.Get("/api/users/me/workspaces/?fields=id,name", wsMask)
		aliceID := alice.Get("/api/users/me/").String("id")
		alice.Get("/api/users/me/workspaces/?owner="+aliceID+"&search=a,c", wsMask)
		alice.Get("/api/users/me/workspaces/?owner=00000000-0000-4000-8000-00000000dead", wsMask)
		alice.Get("/api/users/me/workspaces/?owner=nope", wsMask)
		alice.Get("/api/users/me/workspaces/?owner=", wsMask)
		alice.Get("/api/workspaces/", wsMask)
		alice.Get("/api/workspaces/?search=acm", wsMask)
		alice.Get("/api/workspaces/acme/", wsMask)
		alice.Get("/api/workspaces/missing/", wsMask)
		alice.Get("/api/users/me/settings/")

		alice.Patch("/api/workspaces/acme/", map[string]any{
			"name":              "Acme Inc",
			"organization_size": "11-50",
			"timezone":          "Europe/Berlin",
			"owner":             "00000000-0000-4000-8000-000000000001",
		}, wsMask)
		alice.Patch("/api/workspaces/acme/", map[string]any{"name": "see https://x.io", "timezone": "Mars/Base"})
		alice.Patch("/api/workspaces/acme/", map[string]any{"name": "", "slug": "two words"})
		alice.Patch("/api/workspaces/acme/", map[string]any{"slug": "beta"})
		alice.Patch("/api/workspaces/acme/", map[string]any{"slug": " two words and then some more words to pass the limit ", "name": strings.Repeat("x", 76) + ".com"})
		alice.Patch("/api/workspaces/acme/", map[string]any{"slug": "a\x00b", "background_color": "", "logo_asset": "nope", "deleted_at": "soon", "organization_size": nil})
		alice.Patch("/api/workspaces/acme/", map[string]any{"name": nil, "logo": nil}, wsMask)
		alice.Patch("/api/workspaces/missing/", map[string]any{"name": "X"})
		alice.Put("/api/workspaces/acme/", map[string]any{"name": "Acme", "slug": "acme"}, wsMask)

		bob := s.ClientFrom("bob", "10.0.9.3")
		signUp(bob, "bob@example.com", strongPassword)
		bob.Get("/api/users/me/workspaces/")
		bob.Get("/api/workspaces/")
		bob.Get("/api/workspaces/acme/")
		bob.Patch("/api/workspaces/acme/", map[string]any{"name": "Mine"})
		bob.Delete("/api/workspaces/acme/")

		alice.Post("/api/workspaces/beta/invitations/", map[string]any{"emails": []any{map[string]any{"email": "bob@example.com"}}})
		aliasInviteTokens(s)
		bob.Get("/api/users/me/settings/")

		// Odd payloads.
		alice.Post("/api/workspaces/", map[string]any{"name": 5, "slug": "five"})
		alice.Post("/api/workspaces/", map[string]any{"name": "Five", "slug": []any{"x"}})
		alice.Post("/api/workspaces/", map[string]any{"name": "Five", "slug": 0})
		alice.Post("/api/workspaces/", []any{"x"})

		// Deleting frees the slug, clears last_workspace_id and cascades to
		// the workspace's invitations.
		alice.Patch("/api/users/me/profile/", map[string]any{"last_workspace_id": idOf(findBy(alice.Get("/api/users/me/workspaces/", wsMask), "slug", "beta"))}, Mask("background_color"))
		alice.Delete("/api/workspaces/beta/")
		alice.Delete("/api/workspaces/beta/")
		alice.Get("/api/users/me/profile/", Mask("background_color"))
		alice.Get("/api/users/me/settings/")
		alice.Get("/api/workspace-slug-check/?slug=beta")
		alice.Get("/api/users/me/workspaces/", wsMask)
		bob.Get("/api/users/me/settings/")
		bob.Get("/api/users/me/workspaces/invitations/")
		createWorkspace(alice, "Beta again", "beta")
		alice.Get("/api/users/me/workspaces/", SortBy("", "slug"), wsMask)
	})
}

func TestWorkspaceInvitations(t *testing.T) {
	Run(t, "workspace_invitations", func(s *Scenario) {
		alice := s.ClientFrom("alice", "10.0.10.1")
		signUp(alice, "alice@example.com", strongPassword)
		alice.Patch("/api/users/me/", map[string]any{"first_name": "Alice"}, Mask("token"))
		createWorkspace(alice, "Acme", "acme")
		bob := s.ClientFrom("bob", "10.0.10.2")
		signUp(bob, "bob@example.com", strongPassword)
		carol := s.ClientFrom("carol", "10.0.10.3")
		signUp(carol, "carol@example.com", strongPassword)
		anon := s.ClientFrom("anon", "10.0.10.4")

		const inv = "/api/workspaces/acme/invitations/"
		alice.Post(inv, map[string]any{})
		alice.Post(inv, map[string]any{"emails": []any{}})
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": "x@example.com", "role": 25}}})
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": "not-an-email", "role": 5}}})
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": " bob@example.com"}}})
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": "alice@example.com", "role": 15}}})
		alice.Post(inv, map[string]any{"emails": []any{
			map[string]any{"email": "Bob@Example.com", "role": 15},
			map[string]any{"email": "carol@example.com", "role": "5"},
			map[string]any{"email": "dave@example.com"},
		}})
		aliasInviteTokens(s)
		s.LatestEmail("bob@example.com")
		s.LatestEmail("dave@example.com")
		// Already invited: bulk_create(ignore_conflicts) skips it silently.
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": "dave@example.com", "role": 15}}})

		list := alice.Get(inv, SortBy("", "email"))
		bobInv := idOf(findBy(list, "email", "bob@example.com"))
		carolInv := idOf(findBy(list, "email", "carol@example.com"))
		daveInv := idOf(findBy(list, "email", "dave@example.com"))
		alice.Get(inv + bobInv + "/")
		alice.Get(inv + "00000000-0000-4000-8000-00000000dead/")
		alice.Patch(inv+daveInv+"/", map[string]any{"role": 15, "email": "eve@example.com", "token": "x", "message": "hi"})
		alice.Patch(inv+daveInv+"/", map[string]any{"role": 7, "accepted": "maybe"})
		alice.Get(inv+"?search=dave", SortBy("", "email"))

		// Outsiders.
		bob.Get(inv)
		bob.Post(inv, map[string]any{"emails": []any{map[string]any{"email": "z@example.com"}}})
		anon.Get(inv)

		// The invitee's view.
		bob.Get("/api/users/me/workspaces/invitations/")
		bob.Get("/api/users/me/settings/")
		anon.Get(inv + bobInv + "/join/")
		bob.Get(inv + bobInv + "/join/")
		anon.Get("/api/workspaces/other/invitations/" + bobInv + "/join/")

		// Joining by link.
		anon.Post(inv+bobInv+"/join/", map[string]any{"token": "wrong"})
		anon.Post(inv+bobInv+"/join/", map[string]any{})
		anon.Post(inv+bobInv+"/join/", map[string]any{"token": inviteToken(s, bobInv), "accepted": true})
		carol.Post(inv+bobInv+"/join/", map[string]any{"token": inviteToken(s, bobInv), "accepted": true})
		bob.Post(inv+bobInv+"/join/", map[string]any{"token": inviteToken(s, bobInv), "accepted": true})
		bob.Post(inv+bobInv+"/join/", map[string]any{"token": "x", "accepted": true})
		bob.Get("/api/users/me/workspaces/", wsMask)
		bob.Get("/api/users/me/profile/", Mask("background_color"))

		carol.Post(inv+carolInv+"/join/", map[string]any{"token": inviteToken(s, carolInv), "accepted": false})
		carol.Post(inv+carolInv+"/join/", map[string]any{"token": inviteToken(s, carolInv), "accepted": true})
		carol.Get("/api/users/me/workspaces/invitations/")

		// Bulk accept from the invitations page (a responded invite too).
		createWorkspace(alice, "Beta", "beta")
		alice.Post("/api/workspaces/beta/invitations/", map[string]any{"emails": []any{map[string]any{"email": "carol@example.com", "role": 20}}})
		aliasInviteTokens(s)
		mine := carol.Get("/api/users/me/workspaces/invitations/", SortBy("", "workspace.slug"))
		ids := []any{}
		for _, e := range mine.Data.([]any) {
			ids = append(ids, e.(map[string]any)["id"])
		}
		carol.Post("/api/users/me/workspaces/invitations/", map[string]any{"invitations": append(ids, "00000000-0000-4000-8000-00000000dead")})
		carol.Get("/api/users/me/workspaces/invitations/")
		carol.Get("/api/users/me/workspaces/", SortBy("", "slug"), wsMask)
		carol.Post("/api/users/me/workspaces/invitations/", map[string]any{})

		// The raw "accepted" value decides the branch, its BooleanField
		// conversion is what gets stored.
		dave := s.ClientFrom("dave", "10.0.10.5")
		signUp(dave, "dave@example.com", strongPassword)
		dave.Post(inv+daveInv+"/join/", map[string]any{"token": inviteToken(s, daveInv), "accepted": "maybe"})
		dave.Post(inv+daveInv+"/join/", map[string]any{"token": inviteToken(s, daveInv), "accepted": nil})
		dave.Post(inv+daveInv+"/join/", map[string]any{"token": inviteToken(s, daveInv), "accepted": "False"})
		dave.Get("/api/users/me/workspaces/", wsMask)
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": "erin@example.com", "role": -5}}})
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": "erin@example.com", "role": "x"}}})
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": 0}}})
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": 7}}})
		alice.Post(inv, map[string]any{"emails": "erin@example.com"})
		alice.Post(inv, map[string]any{"emails": []any{map[string]any{"email": "erin@example.com", "role": 5.9}}})
		aliasInviteTokens(s)
		erinInv := idOf(findBy(alice.Get(inv), "email", "erin@example.com"))
		erin := s.ClientFrom("erin", "10.0.10.6")
		signUp(erin, "erin@example.com", strongPassword)
		erin.Post("/api/users/me/workspaces/invitations/", map[string]any{"invitations": []any{"nope"}})
		erin.Post("/api/users/me/workspaces/invitations/", map[string]any{"invitations": nil})
		erin.Post("/api/users/me/workspaces/invitations/", map[string]any{"invitations": []any{nil, strings.ToUpper(erinInv)}})
		erin.Get("/api/workspaces/acme/workspace-members/me/")

		alice.Delete(inv + daveInv + "/")
		alice.Delete(inv + daveInv + "/")
		alice.Get(inv)
		alice.Get("/api/workspaces/acme/members/", SortBy("", "member.email"))
	})
}

func TestWorkspaceMembers(t *testing.T) {
	Run(t, "workspace_members", func(s *Scenario) {
		alice := s.ClientFrom("alice", "10.0.11.1")
		signUp(alice, "alice@example.com", strongPassword)
		createWorkspace(alice, "Acme", "acme")
		users := map[string]*Client{}
		for i, name := range []string{"bob", "carol", "erin", "dan", "fay"} {
			c := s.ClientFrom(name, "10.0.11."+string(rune('2'+i)))
			signUp(c, name+"@example.com", strongPassword)
			users[name] = c
		}
		bob, carol, erin, dan, fay := users["bob"], users["carol"], users["erin"], users["dan"], users["fay"]
		alice.Post("/api/workspaces/acme/invitations/", map[string]any{"emails": []any{
			map[string]any{"email": "bob@example.com", "role": 15},
			map[string]any{"email": "carol@example.com", "role": 5},
			map[string]any{"email": "erin@example.com", "role": 15},
			map[string]any{"email": "fay@example.com", "role": 15},
		}})
		aliasInviteTokens(s)
		for _, c := range []*Client{bob, carol, erin, fay} {
			r := c.Get("/api/users/me/workspaces/invitations/", Mask("[].token", "[].invite_link"))
			c.Post("/api/users/me/workspaces/invitations/", map[string]any{"invitations": []any{idOf(findBy(r, "workspace.slug", "acme"))}})
		}

		const ws = "/api/workspaces/acme/"
		members := alice.Get(ws+"members/", SortBy("", "member.email"))
		bobM := idOf(findBy(members, "member.email", "bob@example.com"))
		carolM := idOf(findBy(members, "member.email", "carol@example.com"))
		erinM := idOf(findBy(members, "member.email", "erin@example.com"))
		aliceM := idOf(findBy(members, "member.email", "alice@example.com"))
		fayM := idOf(findBy(members, "member.email", "fay@example.com"))
		carol.Get(ws+"members/", SortBy("", "member.display_name"))
		alice.Get(ws+"members/?search=bo", SortBy("", "member.email"))
		alice.Get(ws + "members/" + bobM + "/")
		carol.Get(ws + "members/" + bobM + "/")
		alice.Get(ws + "members/00000000-0000-4000-8000-00000000dead/")
		dan.Get(ws + "members/")
		dan.Get(ws + "members/" + bobM + "/")

		alice.Get(ws + "workspace-members/me/")
		carol.Get(ws + "workspace-members/me/")
		dan.Get(ws + "workspace-members/me/")
		alice.Get("/api/workspaces/missing/workspace-members/me/")

		// Role changes.
		bob.Patch(ws+"members/"+carolM+"/", map[string]any{"role": 15})
		alice.Patch(ws+"members/"+aliceM+"/", map[string]any{"role": 5})
		alice.Patch(ws+"members/"+bobM+"/", map[string]any{"role": 7})
		alice.Patch(ws+"members/"+bobM+"/", map[string]any{"workspace": aliceM, "role": "x"})
		alice.Patch(ws+"members/"+bobM+"/", map[string]any{"role": "20", "company_role": "Lead", "member": aliceM})
		alice.Patch(ws+"members/"+erinM+"/", map[string]any{"role": 5, "is_active": false})
		erin.Get(ws + "members/")
		erin.Get("/api/users/me/workspaces/", wsMask)
		alice.Patch(ws+"members/00000000-0000-4000-8000-00000000dead/", map[string]any{"role": 5})

		// Removal and leaving.
		alice.Delete(ws + "members/" + aliceM + "/")
		carol.Delete(ws + "members/" + fayM + "/")
		bob.Delete(ws + "members/" + fayM + "/")
		bob.Delete(ws + "members/" + fayM + "/")
		fay.Get(ws + "members/")
		fay.Get("/api/users/me/workspaces/", wsMask)
		bob.Delete(ws + "members/" + erinM + "/")
		carol.Post(ws+"members/leave/", nil)
		carol.Post(ws+"members/leave/", nil)
		dan.Post(ws+"members/leave/", nil)
		createWorkspace(alice, "Solo", "solo")
		alice.Post("/api/workspaces/solo/members/leave/", nil)
		alice.Get(ws+"members/", SortBy("", "member.email"))

		// Per-user workspace settings.
		bob.Get(ws + "user-properties/")
		bob.Patch(ws+"user-properties/", map[string]any{
			"navigation_project_limit":      5,
			"navigation_control_preference": "TABBED",
			"filters":                       map[string]any{"priority": []any{"high"}},
			"rich_filters":                  map[string]any{"and": []any{}},
			"user":                          aliceM,
		})
		bob.Patch(ws+"user-properties/", map[string]any{"navigation_project_limit": "many", "navigation_control_preference": "GRID", "display_filters": nil})
		bob.Get(ws + "user-properties/")
		dan.Get(ws + "user-properties/")

		bob.Get(ws + "sidebar-preferences/")
		bob.Patch(ws+"sidebar-preferences/", []any{
			map[string]any{"key": "views", "is_pinned": true, "sort_order": 1.5},
			map[string]any{"key": "drafts", "is_pinned": false},
			map[string]any{"key": "unknown", "is_pinned": true},
			map[string]any{"is_pinned": true},
		})
		bob.Get(ws + "sidebar-preferences/")
		bob.Patch(ws+"sidebar-preferences/", []any{map[string]any{"key": "views", "is_pinned": "maybe"}})
		bob.Patch(ws+"sidebar-preferences/", []any{map[string]any{"key": "views", "is_pinned": nil}})
		bob.Patch(ws+"sidebar-preferences/", []any{map[string]any{"key": "analytics", "is_pinned": "t", "sort_order": "2.5"}, map[string]any{"key": "views", "sort_order": "abc"}})
		bob.Patch(ws+"sidebar-preferences/", map[string]any{"key": "views"})
		bob.Patch(ws+"sidebar-preferences/", map[string]any{})
		bob.Get(ws + "sidebar-preferences/")
		dan.Get(ws + "sidebar-preferences/")
		bob.Patch(ws+"user-properties/", map[string]any{"navigation_project_limit": "7.00"})
		bob.Patch(ws+"user-properties/", map[string]any{"navigation_project_limit": true})
		bob.Patch(ws+"user-properties/", map[string]any{"navigation_project_limit": 2147483648})

		bob.Post(ws+"workspace-views/", map[string]any{"view_props": nil})
		bob.Post(ws+"workspace-views/", map[string]any{"view_props": map[string]any{"layout": "board"}})
		bob.Get(ws + "workspace-members/me/")
		dan.Post(ws+"workspace-views/", map[string]any{"view_props": map[string]any{}})
	})
}
