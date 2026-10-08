package contract

import (
	"strconv"
	"strings"
	"testing"
)

func TestEstimates(t *testing.T) {
	Run(t, "estimates", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "20")
		const ws = "/api/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		est := p + "estimates/"
		// Django caches W/estimates/ per user and full path for two hours and
		// nothing invalidates it; Go doesn't cache (DEVIATIONS.md). A fresh
		// query string per read keeps every Django read uncached.
		reads := 0
		we := func() string { reads++; return ws + "estimates/?read=" + strconv.Itoa(reads) }
		dead := "00000000-0000-4000-8000-00000000dead"

		// Reads: any project member.
		alice.Get(est)
		carol.Get(est)
		dan.Get(est)
		outsider.Get(est)
		alice.Get(we())
		dan.Get(we())
		outsider.Get(we())

		// Create: project admins and members. The estimate is written before
		// its points are validated, so a rejected body still leaves one behind.
		carol.Post(est, map[string]any{"estimate": map[string]any{"name": "Guest"}, "estimate_points": []any{}})
		dan.Post(est, map[string]any{"estimate": map[string]any{"name": "Dan"}, "estimate_points": []any{}})
		outsider.Post(est, map[string]any{"estimate": map[string]any{"name": "Out"}, "estimate_points": []any{}})
		alice.Post(est, nil)
		alice.Post(est, []any{"x"})
		alice.Post(est, map[string]any{"estimate": "text"})
		alice.Post(est, map[string]any{"estimate": []any{}})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": "NoPoints"}})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": "NoPoints"}, "estimate_points": []any{}})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": "NotList"}, "estimate_points": "x"})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": "DictPoints"}, "estimate_points": map[string]any{"key": 1}})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": "BadPoints"}, "estimate_points": []any{
			map[string]any{"key": 1, "value": strings.Repeat("x", 21)},
			map[string]any{"key": 2},
			map[string]any{"key": -1, "value": "neg"},
			map[string]any{"key": "a", "value": "text", "description": nil},
			map[string]any{"key": 1.5, "value": []any{}},
			"text",
			map[string]any{},
			map[string]any{"value": "ok", "deleted_at": "nope", "created_by": dead},
			map[string]any{"key": 1, "value": "fine"},
		}})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": "NullName", "type": "points"}, "estimate_points": nil})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": nil}, "estimate_points": []any{}})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": "BadFlag", "last_used": "maybe"}, "estimate_points": []any{}})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": "Numeric Name", "type": "weird", "last_used": "t"}, "estimate_points": []any{}})
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": 7, "last_used": 1}, "estimate_points": []any{}})
		tshirt := alice.Post(est, map[string]any{
			"estimate": map[string]any{"name": "T-shirt", "type": "categories", "ignored": true},
			"estimate_points": []any{
				map[string]any{"key": 1, "value": "S", "description": "small", "extra": 1},
				map[string]any{"key": "2", "value": "M"},
				map[string]any{"key": 3, "value": 3, "created_by": userID(carol)},
				map[string]any{"value": "XL"},
			},
		}).String("id")
		alice.Post(est, map[string]any{"estimate": map[string]any{"name": "T-shirt"}, "estimate_points": []any{}})
		fib := bob.Post(est, map[string]any{
			"estimate":        map[string]any{"name": "Fibonacci", "type": "points", "last_used": true},
			"estimate_points": []any{map[string]any{"key": 1, "value": "1"}, map[string]any{"key": 2, "value": "2"}, map[string]any{"key": 3, "value": "3"}},
		}).String("id")

		alice.Get(est)
		bob.Get(est)
		carol.Get(est)
		dan.Get(est)

		// The workspace list is empty until a project uses an estimate.
		bob.Get(we())
		alice.Patch(p, map[string]any{"estimate": tshirt}, projMask)
		bob.Get(we())
		alice.Get(we())
		dan.Get(we())
		carol.Get(we())
		alice.Get(we())

		// Estimate points: project admins and members.
		pts := est + tshirt + "/estimate-points/"
		carol.Post(pts, map[string]any{"key": 4, "value": "XXL"})
		dan.Post(pts, map[string]any{"key": 4, "value": "XXL"})
		alice.Post(pts, nil)
		alice.Post(pts, []any{"x"})
		alice.Post(pts, map[string]any{"key": 4})
		alice.Post(pts, map[string]any{"value": "XXL"})
		alice.Post(pts, map[string]any{"key": 0, "value": "XXL"})
		alice.Post(pts, map[string]any{"key": 4, "value": ""})
		alice.Post(est+dead+"/estimate-points/", map[string]any{"key": 4, "value": "XXL"})
		alice.Post(est+fib+"/estimate-points/", map[string]any{"key": 4, "value": "5", "description": "ignored"})
		alice.Post(pts, map[string]any{"key": "abc", "value": "bad"})
		alice.Post(pts, map[string]any{"key": []any{1}, "value": "bad"})
		alice.Post(pts, map[string]any{"key": 99999999999, "value": "bad"})
		alice.Post(pts, map[string]any{"key": 1.9, "value": strings.Repeat("y", 256)})
		alice.Post(pts, map[string]any{"key": true, "value": []any{"a"}})
		xxl := bob.Post(pts, map[string]any{"key": "4", "value": "XXL", "description": "ignored"}).String("id")
		alice.Post(pts, map[string]any{"key": 5, "value": 10})
		alice.Post(pts, map[string]any{"key": -3, "value": "negative"})
		alice.Get(est)

		pt := pts + xxl + "/"
		carol.Patch(pt, map[string]any{"value": "XXXL"})
		dan.Patch(pt, map[string]any{"value": "XXXL"})
		alice.Patch(pt, map[string]any{})
		alice.Patch(pt, []any{"x"})
		alice.Patch(pt, map[string]any{"value": strings.Repeat("x", 21)})
		alice.Patch(pt, map[string]any{"value": strings.Repeat("x", 256), "key": -1})
		alice.Patch(pt, map[string]any{"key": 2147483648, "description": nil, "value": nil})
		alice.Patch(pt, map[string]any{"key": "x", "value": []any{}, "created_by": dead, "updated_by": dead, "deleted_at": "nope"})
		alice.Patch(pt, map[string]any{"value": "XXXL", "key": 6, "description": "bigger", "estimate": fib, "project": dead})
		bob.Patch(pt, map[string]any{"description": "changed", "created_by": userID(carol), "updated_by": userID(dan)})
		alice.Patch(pt, map[string]any{"deleted_at": "2030-01-01T00:00:00Z"})
		alice.Patch(pts+dead+"/", map[string]any{"value": "X"})
		alice.Patch(pts+"not-a-uuid/", map[string]any{"value": "X"})
		alice.Patch(est+fib+"/estimate-points/"+xxl+"/", map[string]any{"value": "X"})
		alice.Patch(est+dead+"/estimate-points/"+xxl+"/", map[string]any{"value": "X"})
		alice.Get(est)

		// Delete: any project admin or member. Projects using the estimate
		// lose it.
		carol.Delete(est + fib + "/")
		dan.Delete(est + fib + "/")
		alice.Delete(est + dead + "/")
		alice.Delete(est + fib + "/")
		alice.Delete(est + fib + "/")
		bob.Delete(est + tshirt + "/")
		alice.Post(pts, map[string]any{"key": 4, "value": "late"})
		alice.Get(est)
		alice.Get(p)
		alice.Get(we())
		bob.Get(we())

		s.DBRows("estimates", `SELECT e.name, e.type, e.last_used, e.description, e.deleted_at IS NULL AS live,
				cu.email AS created_by, uu.email AS updated_by, e.workspace_id = p.workspace_id AS in_workspace
			FROM estimates e JOIN projects p ON p.id = e.project_id
			LEFT JOIN users cu ON cu.id = e.created_by_id LEFT JOIN users uu ON uu.id = e.updated_by_id
			ORDER BY e.name`)
		s.DBRows("estimate_points", `SELECT e.name AS estimate, ep.key, ep.value, ep.description, ep.deleted_at IS NULL AS live,
				cu.email AS created_by, uu.email AS updated_by, ep.workspace_id = e.workspace_id AS in_workspace,
				ep.project_id = e.project_id AS in_project, ep.updated_at > ep.created_at AS touched
			FROM estimate_points ep JOIN estimates e ON e.id = ep.estimate_id
			LEFT JOIN users cu ON cu.id = ep.created_by_id LEFT JOIN users uu ON uu.id = ep.updated_by_id
			ORDER BY e.name, ep.key, ep.value`)
	})
}
