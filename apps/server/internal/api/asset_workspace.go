package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/storage"
	"plane-lite/server/internal/throttle"
)

// The workspace- and project-scoped asset views of app/views/asset/v2.py:
// WorkspaceFileAssetEndpoint, AssetRestoreEndpoint, ProjectAssetEndpoint,
// ProjectBulkAssetEndpoint, AssetCheckEndpoint, DuplicateAssetEndpoint and
// the two download endpoints.

// assetDuplicateRate is DEFAULT_THROTTLE_RATES["asset_id"]
// (AssetRateThrottle, per asset).
var assetDuplicateRate = throttle.MustRate("5/minute")

// workspaceBySlug is Workspace.objects.get(slug=slug).
func (a *API) workspaceBySlug(ctx context.Context, slug string) (uuid.UUID, error) {
	var id uuid.UUID
	err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, slug).Scan(&id)
	return id, err
}

// createWorkspaceAsset ports WorkspaceFileAssetEndpoint.post. The entity's
// id (entity_identifier, False when missing) goes into the column its type
// names, overriding the workspace for a logo.
func (a *API) createWorkspaceAsset(c *httpx.Ctx) error {
	r, err := a.assetParseUpload(c, `"image/jpeg"`)
	if err != nil {
		return err
	}
	entity, _ := r.data.Get("entity_type")
	if !assetIn(entity, assetEntityTypes) {
		return errAssetEntityType
	}
	identifier, ok := r.data.Get("entity_identifier")
	if !ok {
		identifier = drf.JSONValue(jsontext.Value("false"))
	}
	entityType := entity.Str()
	ctx := c.Context()
	if entityType == assetWorkspaceLogo {
		role, err := a.workspaceRole(ctx, c.Param("slug"), c.User.ID)
		if err != nil {
			return err
		}
		if role != roleAdmin {
			return httpx.Err(http.StatusForbidden, "Only workspace admins can upload a workspace logo.")
		}
	}
	if !assetIn(r.typ, assetImageTypes) {
		return errAssetImageType
	}
	return a.createScopedAsset(c, r, entityType, identifier, nil, false)
}

// createScopedAsset is the shared end of the workspace and project create
// views: a FileAsset keyed under the workspace, with the entity's id in
// its column, and the upload form.
func (a *API) createScopedAsset(c *httpx.Ctx, r *assetUploadRequest, entityType string, identifier drf.Value,
	projectID *uuid.UUID, withDraft bool) error {
	ctx := c.Context()
	wsID, err := a.workspaceBySlug(ctx, c.Param("slug"))
	if err != nil {
		return err
	}
	if a.storage == nil {
		return errAssetStorage
	}
	size, err := r.sizeColumn()
	if err != nil {
		return err
	}
	key := wsID.String() + "/" + assetHex() + "-" + r.name
	row := assetRow{attributes: r.attributes(), key: key, size: size, entityType: &entityType, createdBy: c.User.ID}
	row.set("workspace_id", &wsID)
	if projectID != nil {
		row.set("project_id", projectID)
	}
	if col := assetEntityColumn(entityType, withDraft); col != "" {
		if projectID != nil && col == "project_id" {
			return errViewCrash // create() got project_id twice: TypeError
		}
		fk, err := assetFK(identifier)
		if err != nil {
			return err
		}
		row.set(col, fk)
	}
	id, err := row.insert(ctx, a.db)
	if err != nil {
		return err
	}
	return a.assetCreated(c, id, r, key, &entityType, nil)
}

// workspaceAsset is FileAsset.objects.get(id=asset_id, workspace__slug=slug)
// plus has_project_asset_access: a project-bound asset needs an active
// membership of its project.
func (a *API) workspaceAsset(c *httpx.Ctx) (*fileAsset, error) {
	id, err := c.UUIDParam("asset_id")
	if err != nil {
		return nil, err
	}
	f, err := a.getAsset(c.Context(), `f.id = $1 AND w.slug = $2`, id, c.Param("slug"))
	if err != nil {
		return nil, err
	}
	if f.projectID != nil {
		var ok bool
		if err := a.db.QueryRow(c.Context(), `SELECT EXISTS (SELECT 1 FROM project_members WHERE member_id = $1
			AND workspace_id IS NOT DISTINCT FROM $2 AND project_id = $3 AND is_active AND deleted_at IS NULL)`,
			c.User.ID, f.workspaceID, f.projectID).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, errAssetNoAccess
		}
	}
	return f, nil
}

// patchWorkspaceAsset ports WorkspaceFileAssetEndpoint.patch.
func (a *API) patchWorkspaceAsset(c *httpx.Ctx) error {
	f, err := a.workspaceAsset(c)
	if err != nil {
		return err
	}
	if !f.hasMetadata() {
		a.enqueueAssetMetadata(c.Context(), f.id)
	}
	if err := a.saveWorkspaceAssetEntity(c, f); err != nil {
		return err
	}
	if err := a.assetConfirm(c, f); err != nil {
		return err
	}
	return c.NoContent()
}

// saveWorkspaceAssetEntity is WorkspaceFileAssetEndpoint.entity_asset_save:
// the workspace's logo or the project's cover becomes this asset (the
// previous one is deleted, even when it is this very asset), saved with
// the request user as updated_by.
func (a *API) saveWorkspaceAssetEntity(c *httpx.Ctx, f *fileAsset) error {
	if f.entityType == nil {
		return nil
	}
	var table, col, image string
	var owner *uuid.UUID
	switch *f.entityType {
	case assetWorkspaceLogo:
		table, col, image, owner = "workspaces", "logo_asset_id", "logo = ''", f.workspaceID
	case assetProjectCover:
		table, col, image, owner = "projects", "cover_image_asset_id", "cover_image = ''", f.projectID
	default:
		return nil
	}
	ctx := c.Context()
	var previous *uuid.UUID
	err := a.db.QueryRow(ctx, `SELECT `+col+` FROM `+table+` WHERE id = $1 AND deleted_at IS NULL`, owner).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // .filter(...).first() is None
	}
	if err != nil {
		return err
	}
	if previous != nil {
		if err := a.assetSoftDelete(ctx, *previous); err != nil {
			return err
		}
	}
	_, err = a.db.Exec(ctx, `UPDATE `+table+` SET `+image+`, `+col+` = $2, updated_at = now(), updated_by_id = $3
		WHERE id = $1`, owner, f.id, c.User.ID)
	return err
}

// deleteWorkspaceAsset ports WorkspaceFileAssetEndpoint.delete: the logo
// or cover is unlinked and the asset soft-deleted.
func (a *API) deleteWorkspaceAsset(c *httpx.Ctx) error {
	f, err := a.workspaceAsset(c)
	if err != nil {
		return err
	}
	ctx := c.Context()
	if f.entityType != nil {
		switch *f.entityType {
		case assetWorkspaceLogo:
			// Workspace.objects.get: a missing workspace raises.
			tag, err := a.db.Exec(ctx, `UPDATE workspaces SET logo_asset_id = NULL, updated_at = now(), updated_by_id = $2
				WHERE id = $1 AND deleted_at IS NULL`, f.workspaceID, c.User.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return pgx.ErrNoRows
			}
		case assetProjectCover:
			if _, err := a.db.Exec(ctx, `UPDATE projects SET cover_image_asset_id = NULL, updated_at = now(),
				updated_by_id = $2 WHERE id = $1 AND deleted_at IS NULL`, f.projectID, c.User.ID); err != nil {
				return err
			}
		}
	}
	if err := a.assetSoftDelete(ctx, f.id); err != nil {
		return err
	}
	return c.NoContent()
}

// getWorkspaceAsset ports WorkspaceFileAssetEndpoint.get: a redirect to
// download the asset.
func (a *API) getWorkspaceAsset(c *httpx.Ctx) error {
	f, err := a.workspaceAsset(c)
	if err != nil {
		return err
	}
	if !f.isUploaded {
		return errAssetNotFound
	}
	return a.assetDownload(c, f)
}

// assetDownload redirects to the object with an attachment disposition
// named after attributes["name"].
func (a *API) assetDownload(c *httpx.Ctx, f *fileAsset) error {
	name, err := assetFilename(f.attributes)
	if err != nil {
		return err
	}
	return a.assetRedirect(c, f.key, "attachment", name)
}

// restoreAsset ports AssetRestoreEndpoint.post: FileAsset.all_objects, so
// deleted assets too.
func (a *API) restoreAsset(c *httpx.Ctx) error {
	id, err := c.UUIDParam("asset_id")
	if err != nil {
		return err
	}
	tag, err := a.db.Exec(c.Context(), `UPDATE file_assets f SET is_deleted = false, deleted_at = NULL
		FROM workspaces w WHERE w.id = f.workspace_id AND f.id = $1 AND w.slug = $2`, id, c.Param("slug"))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return c.NoContent()
}

// checkAsset ports AssetCheckEndpoint.get.
func (a *API) checkAsset(c *httpx.Ctx) error {
	id, err := c.UUIDParam("asset_id")
	if err != nil {
		return err
	}
	var exists bool
	if err := a.db.QueryRow(c.Context(), `SELECT EXISTS (SELECT 1 FROM file_assets f JOIN workspaces w
		ON w.id = f.workspace_id WHERE f.id = $1 AND w.slug = $2 AND f.deleted_at IS NULL)`, id, c.Param("slug")).
		Scan(&exists); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"exists": exists})
}

// downloadWorkspaceAsset ports WorkspaceAssetDownloadEndpoint.get (no
// project access check, unlike the asset's own URL).
func (a *API) downloadWorkspaceAsset(c *httpx.Ctx) error {
	id, err := c.UUIDParam("asset_id")
	if err != nil {
		return err
	}
	f, err := a.getAsset(c.Context(), `f.id = $1 AND w.slug = $2 AND f.is_uploaded`, id, c.Param("slug"))
	if errors.Is(err, pgx.ErrNoRows) {
		return errAssetNotFound
	}
	if err != nil {
		return err
	}
	return a.assetDownload(c, f)
}

// throttleAsset is AssetRateThrottle: requests per asset id, whoever makes
// them (it runs before the view's role check).
func (a *API) throttleAsset(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		id, err := c.UUIDParam("asset_id")
		if err != nil {
			return err
		}
		ok, wait, err := a.limiter.Allow(c.Context(), "asset_id", id.String(), assetDuplicateRate)
		if err != nil {
			return err
		}
		if !ok {
			return a.rateLimited(c, wait)
		}
		return h(c)
	}
}

// duplicateAsset ports DuplicateAssetEndpoint.post: a server-side copy of
// an uploaded asset of the workspace, attached to another entity.
func (a *API) duplicateAsset(c *httpx.Ctx) error {
	ctx := c.Context()
	assetID, err := c.UUIDParam("asset_id")
	if err != nil {
		return err
	}
	d, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !d.IsDict() {
		return errViewCrash
	}
	null := drf.JSONValue(jsontext.Value("null"))
	get := func(name string) drf.Value {
		if v, ok := d.Get(name); ok {
			return v
		}
		return null
	}
	projectVal, entityID, entity := get("project_id"), get("entity_id"), get("entity_type")
	if !drf.PyTruthy(entity) || !assetIn(entity, assetEntityTypes) {
		return httpx.Err(http.StatusBadRequest, "Invalid entity type or entity id")
	}
	wsID, err := a.workspaceBySlug(ctx, c.Param("slug"))
	if err != nil {
		return err
	}
	var projectID *uuid.UUID
	if drf.PyTruthy(projectVal) {
		id, ok := drf.UUIDValue(projectVal)
		if !ok {
			return errFilterDetail
		}
		var exists bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND workspace_id = $2
			AND deleted_at IS NULL)`, id, wsID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.Err(http.StatusNotFound, "Project not found")
		}
		projectID = &id
	}
	if a.storage == nil {
		return errAssetStorage
	}
	var (
		original fileAsset
		size     float64
	)
	err = a.db.QueryRow(ctx, `SELECT asset, attributes, storage_metadata, size FROM file_assets
		WHERE id = $1 AND is_uploaded AND workspace_id = $2 AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1`,
		assetID, wsID).Scan(&original.key, &original.attributes, &original.storageMetadata, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "Asset not found")
	}
	if err != nil {
		return err
	}
	attrs, err := assetCopiedAttributes(original.attributes)
	if err != nil {
		return err
	}
	name, _, _ := assetAttr(original.attributes, "name")
	sanitized := assetSanitizeFilename(name, true)
	if sanitized == "" {
		sanitized = "unnamed"
	}
	entityType := entity.Str()
	row := assetRow{attributes: attrs, key: wsID.String() + "/" + assetHex() + "-" + sanitized, size: size,
		entityType: &entityType, storageMetadata: original.storageMetadata, createdBy: c.User.ID}
	row.set("workspace_id", &wsID)
	row.set("project_id", projectID)
	if col := assetEntityColumn(entityType, false); col != "" {
		if col == "project_id" {
			return errViewCrash // create() got project_id twice: TypeError
		}
		fk, err := assetFK(entityID)
		if err != nil {
			return err
		}
		row.set(col, fk)
	}
	row.metadataNull = original.storageMetadata == nil
	id, err := row.insert(ctx, a.db)
	if err != nil {
		return err
	}
	if err := a.assetCopyObject(ctx, original.key, row.key); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE file_assets SET is_uploaded = true WHERE id = $1 AND deleted_at IS NULL`, id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"asset_id": id.String()})
}

// assetCopiedAttributes is the attributes of a copied asset: name, type
// and size taken over (None when missing).
func assetCopiedAttributes(attrs jsontext.Value) (jsontext.Value, error) {
	parts := make([]string, 0, 3)
	for _, k := range []string{"name", "type", "size"} {
		v, ok, err := assetAttr(attrs, k)
		if err != nil {
			return nil, err
		}
		raw := "null"
		if ok {
			raw = string(v.Raw())
		}
		parts = append(parts, `"`+k+`":`+raw)
	}
	return jsontext.Value("{" + strings.Join(parts, ",") + "}"), nil
}

// assetCopyObject is S3Storage.copy_object: an error answer from the bucket
// is logged and ignored (the copy row stays), anything else raises.
func (a *API) assetCopyObject(ctx context.Context, src, dst string) error {
	err := a.storage.Copy(ctx, src, dst)
	if err != nil && storage.IsClientError(err) {
		a.log.Error("copy_object", "src", src, "dst", dst, "err", err)
		return nil
	}
	return err
}

// --- Project-scoped assets (ProjectAssetEndpoint and friends).

// createProjectAsset ports ProjectAssetEndpoint.post.
func (a *API) createProjectAsset(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	r, err := a.assetParseUpload(c, `"image/jpeg"`)
	if err != nil {
		return err
	}
	entity, ok := r.data.Get("entity_type")
	if !ok {
		entity = drf.JSONValue(jsontext.Value(`""`))
	}
	if !assetIn(entity, assetEntityTypes) {
		return errAssetEntityType
	}
	identifier, ok := r.data.Get("entity_identifier")
	if !ok {
		identifier = drf.JSONValue(jsontext.Value("null"))
	}
	if !assetIn(r.typ, assetImageTypes) {
		return errAssetImageType
	}
	return a.createScopedAsset(c, r, entity.Str(), identifier, &projectID, true)
}

// projectAsset is FileAsset.objects.get(id=pk, workspace__slug=slug,
// project_id=project_id); extra adds conditions.
func (a *API) projectAsset(c *httpx.Ctx, param, extra string) (*fileAsset, error) {
	id, err := c.UUIDParam(param)
	if err != nil {
		return nil, err
	}
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return nil, err
	}
	return a.getAsset(c.Context(), `f.id = $1 AND w.slug = $2 AND f.project_id = $3`+extra, id, c.Param("slug"), projectID)
}

// patchProjectAsset ports ProjectAssetEndpoint.patch.
func (a *API) patchProjectAsset(c *httpx.Ctx) error {
	f, err := a.projectAsset(c, "pk", "")
	if err != nil {
		return err
	}
	if !f.hasMetadata() {
		a.enqueueAssetMetadata(c.Context(), f.id)
	}
	if err := a.assetConfirm(c, f); err != nil {
		return err
	}
	return c.NoContent()
}

// getProjectAsset ports ProjectAssetEndpoint.get.
func (a *API) getProjectAsset(c *httpx.Ctx) error {
	f, err := a.projectAsset(c, "pk", "")
	if err != nil {
		return err
	}
	if !f.isUploaded {
		return errAssetNotFound
	}
	return a.assetDownload(c, f)
}

// downloadProjectAsset ports ProjectAssetDownloadEndpoint.get.
func (a *API) downloadProjectAsset(c *httpx.Ctx) error {
	f, err := a.projectAsset(c, "asset_id", " AND f.is_uploaded")
	if errors.Is(err, pgx.ErrNoRows) {
		return errAssetNotFound
	}
	if err != nil {
		return err
	}
	return a.assetDownload(c, f)
}

// bulkAssets ports ProjectBulkAssetEndpoint.post: the requester's own
// uploads in the workspace (unattached or in this project) are attached to
// the entity, by the newest one's entity type.
func (a *API) bulkAssets(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	entityID, err := c.UUIDParam("entity_id")
	if err != nil {
		return err
	}
	d, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !d.IsDict() {
		return errViewCrash
	}
	v, ok := d.Get("asset_ids")
	if !ok || !drf.PyTruthy(v) {
		return httpx.Err(http.StatusBadRequest, "No asset ids provided.")
	}
	ids, err := assetIDList(v)
	if err != nil {
		return err
	}
	const filter = ` FROM file_assets f JOIN workspaces w ON w.id = f.workspace_id
		WHERE f.id = ANY($1) AND w.slug = $2 AND f.created_by_id = $3 AND (f.project_id = $4 OR f.project_id IS NULL)
			AND f.deleted_at IS NULL`
	args := []any{ids, c.Param("slug"), c.User.ID, projectID}
	rows, err := a.db.Query(ctx, `SELECT f.id, f.entity_type`+filter+` ORDER BY f.created_at DESC`, args...)
	if err != nil {
		return err
	}
	type found struct {
		id         uuid.UUID
		entityType *string
	}
	assets, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (found, error) {
		var f found
		return f, r.Scan(&f.id, &f.entityType)
	})
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		return errAssetNotFound
	}
	update := func(set string, extra ...any) error {
		_, err := a.db.Exec(ctx, `UPDATE file_assets f SET `+set+` FROM workspaces w WHERE w.id = f.workspace_id
			AND f.id = ANY($1) AND w.slug = $2 AND f.created_by_id = $3 AND (f.project_id = $4 OR f.project_id IS NULL)
			AND f.deleted_at IS NULL`, append(args, extra...)...)
		return err
	}
	// swallow is the views' `except IntegrityError: pass`.
	swallow := func(err error) error {
		if db.IsIntegrityError(err) {
			return nil
		}
		return err
	}
	var first string
	if assets[0].entityType != nil {
		first = *assets[0].entityType
	}
	switch first {
	case assetProjectCover:
		if err := update(`project_id = $4`); err != nil {
			return err
		}
		// save_project_cover per asset, newest first: the oldest wins.
		for _, f := range assets {
			tag, err := a.db.Exec(ctx, `UPDATE projects SET cover_image_asset_id = $2, updated_at = now(),
				updated_by_id = $3 WHERE id = $1 AND deleted_at IS NULL`, projectID, f.id, c.User.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return pgx.ErrNoRows
			}
		}
	case assetIssueDescription:
		err = swallow(update(`issue_id = $5, project_id = $4`, entityID))
	case assetCommentDescription:
		err = swallow(update(`comment_id = $5`, entityID))
	case assetPageDescription:
		err = update(`page_id = $5`, entityID)
	case assetDraftIssueDescription:
		err = swallow(update(`draft_issue_id = $5`, entityID))
	}
	if err != nil {
		return err
	}
	return c.NoContent()
}

// assetIDList is the ids an id__in lookup gets: a list's members (a string
// iterates its characters), each a UUID or a ValidationError; None is
// dropped. Anything not iterable raises.
func assetIDList(v drf.Value) ([]uuid.UUID, error) {
	var elems []drf.Value
	switch {
	case v.IsString():
		for _, r := range v.Str() {
			elems = append(elems, drf.JSONValue(jsontext.Value(mustJSON(string(r)))))
		}
	case v.Kind() == '[':
		elems, _ = v.Elems()
	case v.Kind() == '{':
		var m map[string]jsontext.Value
		if err := json.Unmarshal(v.Raw(), &m); err != nil {
			return nil, err
		}
		for k := range m {
			elems = append(elems, drf.JSONValue(jsontext.Value(mustJSON(k))))
		}
	default:
		return nil, errViewCrash
	}
	ids := make([]uuid.UUID, 0, len(elems))
	for _, e := range elems {
		if e.IsNull() {
			continue
		}
		id, ok := drf.UUIDValue(e)
		if !ok {
			return nil, errFilterDetail
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
