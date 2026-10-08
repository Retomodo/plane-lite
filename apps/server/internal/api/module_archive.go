package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"plane-lite/server/internal/httpx"
)

// ModuleArchiveUnarchiveEndpoint (P/archived-modules/ and
// P/modules/<module_id>/archive/).

// moduleArchivedValues is the .values() dict of the archived list:
// created_at/updated_at in the user's timezone, archived_at left in UTC.
type moduleArchivedValues struct {
	moduleCore `json:",inline"`
	IsFavorite bool     `json:"is_favorite"`
	ArchivedAt *utcTime `json:"archived_at"`
}

// listArchivedModules ports ModuleArchiveUnarchiveEndpoint.get without pk.
func (a *API) listArchivedModules(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ms, err := a.modules(c.Context(), true, c.Param("slug"), projectID, c.User.ID, "")
	if err != nil {
		return err
	}
	out := make([]moduleArchivedValues, len(ms))
	for i, m := range ms {
		out[i] = moduleArchivedValues{moduleCore: m.core(), IsFavorite: m.IsFavorite}
		if m.ArchivedAt != nil {
			t := utcTime(*m.ArchivedAt)
			out[i].ArchivedAt = &t
		}
	}
	return c.JSON(http.StatusOK, out)
}

// getArchivedModule ports ModuleArchiveUnarchiveEndpoint.get with pk. A pk
// that is not an archived module still answers 200, with the serializer's
// empty data and the (empty) distributions.
func (a *API) getArchivedModule(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	m, err := a.moduleByID(ctx, true, slug, projectID, c.User.ID, pk, "")
	if err != nil {
		return err
	}
	body, err := a.moduleDetailBody(ctx, c, slug, projectID, pk, m, true)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, body)
}

// archiveModule ports ModuleArchiveUnarchiveEndpoint.post: completed or
// cancelled modules only; every user's favorite of it goes.
func (a *API) archiveModule(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	moduleID, err := c.UUIDParam("module_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var (
		id     uuid.UUID
		status string
	)
	if err := a.db.QueryRow(ctx, `SELECT m.id, m.status FROM modules m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.id = $1 AND m.project_id = $2 AND w.slug = $3 AND m.deleted_at IS NULL`, moduleID, projectID, slug).
		Scan(&id, &status); err != nil {
		return err
	}
	if status != "completed" && status != "cancelled" {
		return httpx.Err(http.StatusBadRequest, "Only completed or cancelled modules can be archived")
	}
	var archivedAt time.Time
	if err := a.db.QueryRow(ctx, `UPDATE modules SET archived_at = now(), updated_at = now(), updated_by_id = $2
		WHERE id = $1 RETURNING archived_at`, id, c.User.ID).Scan(&archivedAt); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE user_favorites f SET deleted_at = now() FROM workspaces w
		WHERE w.id = f.workspace_id AND f.entity_type = 'module' AND f.entity_identifier = $1 AND f.project_id = $2
			AND w.slug = $3 AND f.deleted_at IS NULL`, id, projectID, slug); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"archived_at": pyDateTimeStr(archivedAt.UTC())})
}

// unarchiveModule ports ModuleArchiveUnarchiveEndpoint.delete.
func (a *API) unarchiveModule(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	moduleID, err := c.UUIDParam("module_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	tag, err := a.db.Exec(ctx, `UPDATE modules m SET archived_at = NULL, updated_at = now(), updated_by_id = $4
		FROM workspaces w WHERE w.id = m.workspace_id AND m.id = $1 AND m.project_id = $2 AND w.slug = $3
			AND m.deleted_at IS NULL`, moduleID, projectID, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errModuleObjectMissing
	}
	return c.NoContent()
}

// errModuleObjectMissing is Module.objects.get's DoesNotExist.
var errModuleObjectMissing = httpx.Err(http.StatusNotFound, "The required object does not exist.")
