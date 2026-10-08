package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/httpx"
)

// The pre-v2 asset endpoints the web still calls for old editor images
// (app/views/asset/base.py): FileAssetEndpoint.delete,
// FileAssetViewSet.restore and UserAssetsEndpoint.delete. They only flip
// is_deleted (deleted_at is left alone). Their GET and POST have no caller.

// legacyWorkspaceAsset is WorkspaceMemberPermission (an active membership
// of the workspace in the URL) then FileAsset.objects.get(asset=key),
// setting is_deleted.
func (a *API) legacyWorkspaceAsset(deleted bool) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		ctx := c.Context()
		wsID, err := c.UUIDParam("workspace_id")
		if err != nil {
			return err
		}
		var member bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workspace_members WHERE workspace_id = $1
			AND member_id = $2 AND is_active AND deleted_at IS NULL)`, wsID, c.User.ID).Scan(&member); err != nil {
			return err
		}
		if !member {
			return httpx.ErrForbidden
		}
		return a.legacySetDeleted(c, `asset = $1`, deleted, wsID.String()+"/"+c.Param("asset_key"))
	}
}

// deleteLegacyUserAsset ports UserAssetsEndpoint.delete.
func (a *API) deleteLegacyUserAsset(c *httpx.Ctx) error {
	return a.legacySetDeleted(c, `asset = $1 AND created_by_id = $2`, true, c.Param("asset_key"), c.User.ID)
}

// legacySetDeleted is FileAsset.objects.get(...) then
// save(update_fields=["is_deleted"]); several matches raise.
func (a *API) legacySetDeleted(c *httpx.Ctx, where string, deleted bool, args ...any) error {
	ctx := c.Context()
	rows, err := a.db.Query(ctx, `SELECT id::text FROM file_assets WHERE deleted_at IS NULL AND `+where+` LIMIT 2`, args...)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	switch len(ids) {
	case 0:
		return pgx.ErrNoRows
	case 2:
		return errViewCrash // MultipleObjectsReturned
	}
	if _, err := a.db.Exec(ctx, `UPDATE file_assets SET is_deleted = $2 WHERE id = $1`, ids[0], deleted); err != nil {
		return err
	}
	return c.NoContent()
}

// assetSubroute serves URLs whose Django patterns the Go mux can't hold
// side by side: they overlap only on values Django's converters reject
// (e.g. "<uuid:entity_id>/bulk/" and "download/<uuid:asset_id>/"), which
// ServeMux sees as conflicting wildcards. resolve picks the view's methods
// for the request, or nil for Django's 404; the rest is what Router does
// (401 before 405, Allow on 405).
func assetSubroute(public bool, resolve func(c *httpx.Ctx) httpx.Methods) httpx.Methods {
	h := func(c *httpx.Ctx) error {
		m := resolve(c)
		if m == nil {
			return httpx.ErrPageNotFound
		}
		if !public && c.User == nil {
			return httpx.ErrNotAuthenticated
		}
		method := c.R.Method
		if method == http.MethodHead {
			method = http.MethodGet
		}
		fn, ok := m[method]
		if !ok {
			allowed := []string{http.MethodOptions}
			for k := range m {
				allowed = append(allowed, k)
				if k == http.MethodGet {
					allowed = append(allowed, http.MethodHead)
				}
			}
			slices.Sort(allowed)
			c.W.Header().Set("Allow", strings.Join(allowed, ", "))
			return httpx.Detail(http.StatusMethodNotAllowed, `Method "`+c.R.Method+`" not allowed.`)
		}
		return fn(c)
	}
	out := httpx.Methods{}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodOptions} {
		out[m] = h
	}
	return out
}

// isUUIDParam is Django's <uuid:...> converter on a path segment.
func isUUIDParam(c *httpx.Ctx, name string) bool {
	_, err := c.UUIDParam(name)
	return err == nil
}
