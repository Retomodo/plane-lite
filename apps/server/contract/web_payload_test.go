package contract

import "testing"

// TestIssueWebPayload replays the create-work-item body the web app sends
// (captured from the browser), with an empty state_id when no state is
// picked. DRF's RelatedField turns "" into None before validating.
func TestIssueWebPayload(t *testing.T) {
	Run(t, "issue_web_payload", func(s *Scenario) {
		alice := s.ClientFrom("alice", "10.0.170.1")
		signUp(alice, "alice@example.com", strongPassword)
		createWorkspace(alice, "Acme", "acme")
		pl := alice.Post("/api/workspaces/acme/projects/", map[string]any{"name": "Plane Lite", "identifier": "PL"}, projMask).String("id")
		p := "/api/workspaces/acme/projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")

		web := func(name string, extra map[string]any) map[string]any {
			body := map[string]any{
				"project_id": pl, "type_id": nil, "name": name, "description_html": "<p></p>",
				"estimate_point": nil, "state_id": "", "parent_id": nil, "priority": "none",
				"assignee_ids": []any{}, "label_ids": []any{}, "cycle_id": nil, "module_ids": nil,
				"start_date": nil, "target_date": nil,
			}
			for k, v := range extra {
				body[k] = v
			}
			return body
		}
		first := alice.Post(p+"issues/", web("Ship plane-lite", nil), ids).String("id")
		alice.Post(p+"issues/", web("Empty parent", map[string]any{"parent_id": ""}), ids)
		alice.Post(p+"issues/", web("Blank state and parent", map[string]any{"state_id": " ", "parent_id": ""}), ids)
		alice.Patch(p+"issues/"+first+"/", map[string]any{"state_id": ""}, ids)
		alice.Patch(p+"issues/"+first+"/", map[string]any{"parent_id": ""}, ids)
		s.DBRows("issues", `SELECT i.name, s.name AS state, i.parent_id IS NULL AS no_parent
			FROM issues i JOIN states s ON s.id = i.state_id WHERE i.project_id = '`+pl+`' ORDER BY i.sequence_id`)
	})
}
