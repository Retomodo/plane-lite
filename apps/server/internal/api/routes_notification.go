package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 14. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerNotificationJobs() {}

func (a *API) registerNotificationRoutes(rt *httpx.Router) {
	n := wsPrefix + "users/notifications/"
	rt.Handle(n, httpx.Methods{"GET": a.allowWorkspace(anyRole, a.listNotifications)})
	// DELETE n+"{pk}/" (NotificationViewSet.destroy) is UNUSED and not ported.
	rt.Handle(n+"{pk}/", httpx.Methods{
		"GET":   a.retrieveNotification,
		"PATCH": a.allowWorkspace(anyRole, a.patchNotification),
	})
	rt.Handle(n+"{pk}/read/", httpx.Methods{
		"POST":   a.allowWorkspace(anyRole, a.notificationUpdate("read_at = now()")),
		"DELETE": a.allowWorkspace(anyRole, a.notificationUpdate("read_at = NULL")),
	})
	rt.Handle(n+"{pk}/archive/", httpx.Methods{
		"POST":   a.allowWorkspace(anyRole, a.notificationUpdate("archived_at = now()")),
		"DELETE": a.allowWorkspace(anyRole, a.notificationUpdate("archived_at = NULL")),
	})
	rt.Handle(n+"unread/", httpx.Methods{"GET": a.allowWorkspace(anyRole, a.unreadNotifications)})
	rt.Handle(n+"mark-all-read/", httpx.Methods{"POST": a.allowWorkspace(anyRole, a.markAllRead)})
	rt.Handle("/api/users/me/notification-preferences/", httpx.Methods{
		"GET":   a.getNotificationPreferences,
		"PATCH": a.patchNotificationPreferences,
	})
}
