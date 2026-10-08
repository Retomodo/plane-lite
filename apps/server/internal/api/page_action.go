package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
)

// PageViewSet's archive, unarchive, lock, unlock and access actions, and
// PageDuplicateEndpoint.

// pageSetArchived is unarchive_archive_page_and_descendants: the page and
// every page below it (deleted ones too), by raw SQL.
func (a *API) pageSetArchived(ctx context.Context, id uuid.UUID, archive bool) error {
	_, err := a.db.Exec(ctx, `WITH RECURSIVE descendants AS (
			SELECT id FROM pages WHERE id = $1
			UNION ALL
			SELECT pages.id FROM pages, descendants WHERE pages.parent_id = descendants.id
		)
		UPDATE pages SET archived_at = CASE WHEN $2 THEN (now() AT TIME ZONE 'UTC')::date END
		WHERE id IN (SELECT id FROM descendants)`, id, archive)
	return err
}

// pageNotOwnerOrAdmin is archive()'s check: an active project member at
// most a member (role <= 15) who does not own the page.
func (a *API) pageNotOwnerOrAdmin(ctx context.Context, projectID uuid.UUID, page *pageObject, user uuid.UUID) (bool, error) {
	if page.owner == user {
		return false, nil
	}
	var low bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE deleted_at IS NULL AND project_id = $1
		AND member_id = $2 AND is_active AND role <= 15)`, projectID, user).Scan(&low)
	return low, err
}

// pagePyNow is str(datetime.now()) in the (UTC) server.
func pagePyNow() string {
	now := time.Now().UTC()
	if now.Nanosecond()/1000 == 0 {
		return now.Format(time.DateTime)
	}
	return now.Format("2006-01-02 15:04:05.000000")
}

// archivePage ports PageViewSet.archive.
func (a *API) archivePage(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, _, _ := pageParamIDs(c)
	page, err := a.pageFromURL(c)
	if err != nil {
		return err
	}
	if denied, err := a.pageNotOwnerOrAdmin(ctx, projectID, page, c.User.ID); err != nil {
		return err
	} else if denied {
		return httpx.Err(http.StatusBadRequest, "Only the owner or admin can archive the page")
	}
	if _, err := a.db.Exec(ctx, `UPDATE user_favorites f SET deleted_at = now() FROM workspaces w
		WHERE w.id = f.workspace_id AND f.deleted_at IS NULL AND f.entity_type = 'page' AND f.entity_identifier = $1
			AND f.project_id = $2 AND w.slug = $3`, page.id, projectID, c.Param("slug")); err != nil {
		return err
	}
	if err := a.pageSetArchived(ctx, page.id, true); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"archived_at": pagePyNow()})
}

// unarchivePage ports PageViewSet.unarchive: a page whose parent is still
// archived is detached from it first.
func (a *API) unarchivePage(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, _, _ := pageParamIDs(c)
	page, err := a.pageFromURL(c)
	if err != nil {
		return err
	}
	if denied, err := a.pageNotOwnerOrAdmin(ctx, projectID, page, c.User.ID); err != nil {
		return err
	} else if denied {
		return httpx.Err(http.StatusBadRequest, "Only the owner or admin can un archive the page")
	}
	if page.parent != nil {
		// page.parent goes through the base manager: deleted parents count.
		if _, err := a.db.Exec(ctx, `UPDATE pages SET parent_id = NULL WHERE id = $1
			AND EXISTS (SELECT 1 FROM pages p WHERE p.id = $2 AND p.archived_at IS NOT NULL)`, page.id, *page.parent); err != nil {
			return err
		}
	}
	if err := a.pageSetArchived(ctx, page.id, false); err != nil {
		return err
	}
	return c.NoContent()
}

// lockPage and unlockPage port PageViewSet.lock and unlock.
func (a *API) lockPage(c *httpx.Ctx) error   { return a.pageSetLocked(c, true) }
func (a *API) unlockPage(c *httpx.Ctx) error { return a.pageSetLocked(c, false) }

func (a *API) pageSetLocked(c *httpx.Ctx, locked bool) error {
	page, err := a.pageFromURL(c)
	if err != nil {
		return err
	}
	var set setList
	set.add("is_locked", locked)
	if err := a.pageSave(c.Context(), a.db, page.id, page.html, &c.User.ID, set); err != nil {
		return err
	}
	return c.NoContent()
}

// pageAccess ports PageViewSet.access. The value is stored as given:
// SmallIntegerField's int() conversion (failing with a 500), then the
// column's range and its access >= 0 check.
func (a *API) pageAccess(c *httpx.Ctx) error {
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get on a list
	}
	page, err := a.pageFromURL(c)
	if err != nil {
		return err
	}
	v, given := data.Get("access")
	if given && !pyEqualsInt(page.access, v) && page.owner != c.User.ID {
		return httpx.Err(http.StatusBadRequest, "Access cannot be updated since this page is owned by someone else")
	}
	var access any = 0
	if given && !v.IsNull() {
		if v.Kind() != 't' && v.Kind() != 'f' && v.Kind() != '0' && !v.IsString() {
			return errViewCrash // int() of a list or dict
		}
		n, ok := drf.PyInt(v)
		if !ok || !n.IsInt64() || n.Int64() < -32768 || n.Int64() > 32767 {
			return errViewCrash // ValueError, or smallint out of range (a DataError)
		}
		access = n.Int64()
	} else if given {
		access = nil // NOT NULL: an IntegrityError
	}
	var set setList
	set.add("access", access)
	if err := a.pageSave(ctx, a.db, page.id, page.html, &c.User.ID, set); err != nil {
		return err
	}
	return c.NoContent()
}

// duplicatePage ports PageDuplicateEndpoint.post: a copy of the page
// (description_binary dropped) owned by the requester, linked to every
// project the original is, then page_transaction and the description
// copy task.
func (a *API) duplicatePage(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, _, _ := pageParamIDs(c)
	page, err := a.pageFromURL(c)
	if err != nil {
		return err
	}
	if page.access == pagePrivate && page.owner != c.User.ID {
		return httpx.Err(http.StatusForbidden, "Permission denied")
	}
	rows, err := a.db.Query(ctx, `SELECT project_id FROM project_pages WHERE page_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`, page.id)
	if err != nil {
		return err
	}
	projectIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	stripped, err := pageStripped(page.html)
	if err != nil {
		return err
	}
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `INSERT INTO pages (id, created_at, updated_at, name, description_json, description_html,
			description_stripped, access, created_by_id, owned_by_id, updated_by_id, workspace_id, color, archived_at,
			is_locked, parent_id, view_props, logo_props, description_binary, is_global, deleted_at, moved_to_page,
			moved_to_project, external_id, external_source, sort_order)
		SELECT gen_random_uuid(), now(), now(), name || ' (Copy)', description_json, description_html, $2, access, $3, $3,
			$3, workspace_id, color, archived_at, is_locked, parent_id, view_props, logo_props, NULL, is_global, deleted_at,
			moved_to_page, moved_to_project, external_id, external_source, sort_order
		FROM pages WHERE id = $1 RETURNING id`, page.id, stripped, c.User.ID).Scan(&id); err != nil {
		return err
	}
	jobProject := projectID // the view's loop variable outlives the loop
	for _, p := range projectIDs {
		if _, err := a.db.Exec(ctx, `INSERT INTO project_pages (workspace_id, project_id, page_id, created_by_id)
			VALUES ($1, $2, $3, $4)`, page.workspace, p, id, c.User.ID); err != nil {
			return err
		}
		jobProject = p
	}
	html, _ := json.Marshal(page.html)
	a.enqueuePageTransaction(ctx, pageTransactionJob{NewHTML: html, PageID: id})
	if err := jobs.Enqueue(ctx, a.jobs, pageCopyJob{PageID: id, ProjectID: jobProject, Slug: c.Param("slug"),
		UserID: c.User.ID}); err != nil {
		a.log.Error("enqueue copy_s3_objects_of_description_and_assets", "err", err)
	}
	// project_ids: every link, deleted ones included.
	var ids []uuid.UUID
	out, err := scanPageItem(a.db.QueryRow(ctx, `SELECT `+pageItemCols+`,
			COALESCE((SELECT array_agg(DISTINCT pp.project_id) FROM project_pages pp WHERE pp.page_id = pg.id
				AND NOT (pp.project_id = '00000000-0000-0000-0000-000000000001')), '{}')
		FROM pages pg WHERE pg.id = $1 AND pg.deleted_at IS NULL`, id), &ids)
	if errors.Is(err, pgx.ErrNoRows) {
		return errViewCrash // .first() is None
	}
	if err != nil {
		return err
	}
	out.ProjectIDs = &ids
	out.Description = &out.extra.html
	return c.JSON(http.StatusCreated, out)
}
