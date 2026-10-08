package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 8. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerCycleJobs() {}

func (a *API) registerCycleRoutes(rt *httpx.Router) {
	// plane/app/urls/cycle.py. Django resolves <uuid:...> segments before
	// any permission check, so a bad one is a 404 first.
	rt.Handle(projectPrefix+"cycles/", httpx.Methods{
		"GET":  a.allowProject(anyRole, a.listCycles),
		"POST": a.allowProject(adminMember, a.createCycle),
	})
	rt.Handle(projectPrefix+"cycles/{pk}/", cycleUUIDs(httpx.Methods{
		"GET":    a.allowProject(adminMember, a.getCycle),
		"PATCH":  a.allowProject(adminMember, a.updateCycle),
		"DELETE": a.allowCycleCreator(adminOnly, a.deleteCycle),
	}, "pk"))
	rt.Handle(projectPrefix+"cycles/{cycle_id}/cycle-issues/", cycleUUIDs(httpx.Methods{
		"GET":  a.allowProject(adminMember, a.listCycleIssues),
		"POST": a.allowProject(adminMember, a.addCycleIssues),
	}, "cycle_id"))
	rt.Handle(projectPrefix+"cycles/{cycle_id}/cycle-issues/{issue_id}/", cycleUUIDs(httpx.Methods{
		"DELETE": a.allowProject(adminMember, a.removeCycleIssue),
	}, "cycle_id", "issue_id"))
	rt.Handle(projectPrefix+"cycles/date-check/", httpx.Methods{
		"POST": a.allowProject(adminMember, a.cycleDateCheck),
	})
	rt.Handle(projectPrefix+"cycles/{cycle_id}/transfer-issues/", cycleUUIDs(httpx.Methods{
		"POST": a.allowProject(adminMember, a.transferCycleIssues),
	}, "cycle_id"))
	rt.Handle(projectPrefix+"cycles/{cycle_id}/user-properties/", cycleUUIDs(httpx.Methods{
		"GET":   a.allowProject(anyRole, a.getCycleUserProperties),
		"PATCH": a.allowProject(anyRole, a.patchCycleUserProperties),
	}, "cycle_id"))
	rt.Handle(projectPrefix+"cycles/{cycle_id}/archive/", cycleUUIDs(httpx.Methods{
		"POST":   a.allowProject(adminMember, a.archiveCycle),
		"DELETE": a.allowProject(adminMember, a.unarchiveCycle),
	}, "cycle_id"))
	rt.Handle(projectPrefix+"archived-cycles/", httpx.Methods{
		"GET": a.allowProject(adminMember, a.archivedCycles),
	})
	rt.Handle(projectPrefix+"archived-cycles/{pk}/", cycleUUIDs(httpx.Methods{
		"GET": a.allowProject(adminMember, a.archivedCycles),
	}, "pk"))
	rt.Handle(projectPrefix+"cycles/{cycle_id}/progress/", cycleUUIDs(httpx.Methods{
		"GET": a.allowProject(anyRole, a.cycleProgress),
	}, "cycle_id"))
	rt.Handle(projectPrefix+"cycles/{cycle_id}/analytics/", cycleUUIDs(httpx.Methods{
		"GET": a.allowProject(anyRole, a.cycleAnalytics),
	}, "cycle_id"))

	// plane/app/urls/workspace.py
	rt.Handle(wsPrefix+"cycles/", httpx.Methods{
		"GET": a.workspacePerm(anyRole, a.workspaceCycles),
	})
}

// cycleUUIDs checks the route's <uuid:...> segments before any handler.
func cycleUUIDs(m httpx.Methods, names ...string) httpx.Methods {
	out := httpx.Methods{}
	for method, h := range m {
		out[method] = func(c *httpx.Ctx) error {
			for _, n := range names {
				if _, err := c.UUIDParam(n); err != nil {
					return err
				}
			}
			return h(c)
		}
	}
	return out
}
