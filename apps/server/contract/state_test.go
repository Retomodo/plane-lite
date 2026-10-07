package contract

import (
	"strings"
	"testing"
)

// projectTeam sets up workspace "acme" with project PL: alice admin, bob a
// project member, carol a project guest, dan a workspace member outside the
// project, and outsider with no membership.
func projectTeam(s *Scenario, subnet string) (alice, bob, carol, dan, outsider *Client, pl string) {
	alice = s.ClientFrom("alice", "10.0."+subnet+".1")
	signUp(alice, "alice@example.com", strongPassword)
	createWorkspace(alice, "Acme", "acme")
	var team []*Client
	var emails []string
	for i, n := range []string{"bob", "carol", "dan"} {
		c := s.ClientFrom(n, "10.0."+subnet+"."+string(rune('2'+i)))
		signUp(c, n+"@example.com", strongPassword)
		team = append(team, c)
		emails = append(emails, n+"@example.com")
	}
	bob, carol, dan = team[0], team[1], team[2]
	addToWorkspace(s, alice, "acme", team, emails, []int{15, 5, 15})
	outsider = s.ClientFrom("outsider", "10.0."+subnet+".9")
	signUp(outsider, "outsider@example.com", strongPassword)
	pl = alice.Post("/api/workspaces/acme/projects/", map[string]any{"name": "Plane Lite", "identifier": "PL"}, projMask).String("id")
	alice.Post("/api/workspaces/acme/projects/"+pl+"/members/", map[string]any{"members": []any{
		map[string]any{"member_id": userID(bob), "role": 15},
		map[string]any{"member_id": userID(carol), "role": 5},
	}})
	return
}

func TestStates(t *testing.T) {
	Run(t, "states", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "14")
		p := "/api/workspaces/acme/projects/" + pl + "/"

		states := alice.Get(p + "states/")
		alice.Get(p + "states/?grouped=true")
		carol.Get(p + "states/")
		dan.Get(p + "states/")
		outsider.Get(p + "states/")
		alice.Get("/api/workspaces/acme/states/")
		dan.Get("/api/workspaces/acme/states/")
		outsider.Get("/api/workspaces/acme/states/")
		alice.Get(p + "intake-state/")
		dan.Get(p + "intake-state/")
		backlog := idOf(findBy(states, "name", "Backlog"))
		todo := idOf(findBy(states, "name", "Todo"))
		triage := s.DBStrings(`SELECT id::text FROM states WHERE "group" = 'triage'`)[0]

		// Create: admins only; the sequence is always max + 15000.
		bob.Post(p+"states/", map[string]any{"name": "Review", "color": "#ff0000", "group": "started"})
		alice.Post(p+"states/", map[string]any{})
		alice.Post(p+"states/", []any{"x"})
		alice.Post(p+"states/", map[string]any{"name": strings.Repeat("x", 256), "color": "", "group": "nope", "default": "maybe", "sequence": "x", "description": nil})
		alice.Post(p+"states/", map[string]any{"name": "Waiting", "color": "#000", "group": "triage"})
		alice.Post(p+"states/", map[string]any{"name": "Todo", "color": "#000"})
		alice.Post(p+"states/", map[string]any{"name": "Triage", "color": "#000"})
		alice.Post(p+"states/", map[string]any{"name": "Ordered", "color": "#000", "order": 2})
		review := alice.Post(p+"states/", map[string]any{
			"name": "In Review!", "color": "#ff0000", "group": "started", "sequence": 1, "description": "Waiting on review",
			"id": "00000000-0000-4000-8000-00000000dead", "project_id": "00000000-0000-4000-8000-00000000dead",
		}).String("id")
		alice.Post(p+"states/", map[string]any{"name": "QA", "color": "#00ff00", "group": "started", "default": true})

		alice.Get(p + "states/" + review + "/")
		dan.Get(p + "states/" + review + "/")
		alice.Get(p + "states/" + triage + "/")
		alice.Get(p + "states/00000000-0000-4000-8000-00000000dead/")

		// Update: any project member.
		carol.Patch(p+"states/"+review+"/", map[string]any{"name": "Under Review", "color": "#123456"})
		dan.Patch(p+"states/"+review+"/", map[string]any{"name": "Nope"})
		alice.Patch(p+"states/"+review+"/", map[string]any{"name": "Todo"})
		alice.Patch(p+"states/"+review+"/", map[string]any{"group": "triage"})
		alice.Patch(p+"states/"+review+"/", map[string]any{"sequence": 99.5, "order": 2, "default": false})
		alice.Patch(p+"states/"+review+"/", map[string]any{"name": ""})
		alice.Patch(p+"states/"+triage+"/", map[string]any{"name": "Inbox"})
		alice.Patch(p+"states/00000000-0000-4000-8000-00000000dead/", map[string]any{"name": "X"})

		// Default state.
		bob.Post(p+"states/"+review+"/mark-default/", nil)
		alice.Post(p+"states/"+review+"/mark-default/", nil)
		alice.Get(p + "states/")

		// Delete: not the default, not one with issues.
		alice.Delete(p + "states/" + review + "/")
		// Marking the Triage state (or nothing) clears every default.
		alice.Post(p+"states/"+triage+"/mark-default/", nil)
		alice.Post(p+"states/00000000-0000-4000-8000-00000000dead/mark-default/", nil)
		alice.Get(p + "states/")
		alice.Post(p+"states/"+backlog+"/mark-default/", nil)
		bob.Delete(p + "states/" + review + "/")
		alice.Delete(p + "states/" + triage + "/")
		alice.Delete(p + "states/" + review + "/")
		alice.Delete(p + "states/" + review + "/")
		alice.Patch(p+"states/"+todo+"/", map[string]any{"name": "Under Review"})
		s.DBRows("states", `SELECT name, slug, color, "group", "default", sequence, description, is_triage,
				created_by_id, updated_by_id, deleted_at IS NULL AS live
			FROM states ORDER BY created_at, sequence`)
		alice.Get("/api/workspaces/acme/states/")

		// Archived projects drop out of the lists.
		alice.Post(p+"archive/", nil, Mask("archived_at"))
		alice.Get(p + "states/")
		alice.Get("/api/workspaces/acme/states/")
		alice.Get(p + "states/" + todo + "/")
		alice.Get(p + "intake-state/")
	})
}

func TestLabels(t *testing.T) {
	Run(t, "labels", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "15")
		p := "/api/workspaces/acme/projects/" + pl + "/"
		const wl = "/api/workspaces/acme/labels/"

		alice.Get(p + "issue-labels/")
		dan.Get(p + "issue-labels/")
		outsider.Get(p + "issue-labels/")
		alice.Get(wl)

		// Create: project admins.
		bob.Post(p+"issue-labels/", map[string]any{"name": "Bug"})
		carol.Post(p+"issue-labels/", map[string]any{"name": "Bug"})
		alice.Post(p+"issue-labels/", map[string]any{})
		alice.Post(p+"issue-labels/", map[string]any{"name": strings.Repeat("x", 256), "color": nil, "sort_order": "x", "parent": "nope"})
		alice.Post(p+"issue-labels/", map[string]any{"name": "Child", "parent": "00000000-0000-4000-8000-00000000dead"})
		bug := alice.Post(p+"issue-labels/", map[string]any{"name": "Bug", "color": "#ff0000", "description": "ignored"}).String("id")
		alice.Post(p+"issue-labels/", map[string]any{"name": "bug"})
		feature := alice.Post(p+"issue-labels/", map[string]any{"name": "Feature", "sort_order": 5, "parent": bug, "project_id": "00000000-0000-4000-8000-00000000dead"}).String("id")
		alice.Post(p+"issue-labels/", map[string]any{"name": "Chore", "color": ""})
		alice.Post(p+"issue-labels/", []any{"x"})

		alice.Get(p + "issue-labels/")
		carol.Get(p + "issue-labels/")
		dan.Get(p + "issue-labels/")
		alice.Get(wl)
		bob.Get(wl)
		dan.Get(wl)
		outsider.Get(wl)

		// Update.
		bob.Patch(p+"issue-labels/"+bug+"/", map[string]any{"color": "#000000"})
		alice.Patch(p+"issue-labels/"+bug+"/", map[string]any{"name": "Feature"})
		alice.Patch(p+"issue-labels/"+bug+"/", map[string]any{"name": "feature"})
		alice.Patch(p+"issue-labels/"+bug+"/", map[string]any{"name": "Defect", "color": "#000000", "sort_order": 1.5, "parent": nil})
		alice.Patch(p+"issue-labels/"+feature+"/", map[string]any{"parent": feature, "name": ""})
		alice.Patch(p+"issue-labels/00000000-0000-4000-8000-00000000dead/", map[string]any{"color": "#fff"})
		alice.Patch(p+"issue-labels/"+bug+"/", []any{"name"})
		alice.Get(p + "issue-labels/")

		// Delete cascades to child labels.
		bob.Delete(p + "issue-labels/" + bug + "/")
		alice.Delete(p + "issue-labels/" + bug + "/")
		alice.Delete(p + "issue-labels/" + bug + "/")
		alice.Get(p + "issue-labels/")
		s.DBRows("labels", `SELECT name, color, description, sort_order, parent_id, project_id, workspace_id,
				created_by_id, updated_by_id, deleted_at IS NULL AS live
			FROM labels ORDER BY created_at`)
		alice.Post(p+"issue-labels/", map[string]any{"name": "Defect"})
		alice.Post(p+"issue-labels/", map[string]any{"name": "Bug"})

		// Archived projects drop out of the workspace list (fetched fresh:
		// the create above invalidated the cache).
		alice.Post(p+"archive/", nil, Mask("archived_at"))
		alice.Post("/api/workspaces/acme/projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask)
		bob.Get(wl)
	})
}
