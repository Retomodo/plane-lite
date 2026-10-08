package api

import (
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// createUserAsset ports UserAssetsV2Endpoint.post: a profile avatar or
// cover, not uploaded yet, and the form to upload it with.
func (a *API) createUserAsset(c *httpx.Ctx) error {
	r, err := a.assetParseUpload(c, `"image/jpeg"`)
	if err != nil {
		return err
	}
	entity, ok := r.data.Get("entity_type")
	if !ok || !drf.PyTruthy(entity) || !assetIn(entity, []string{assetUserAvatar, assetUserCover}) {
		return errAssetEntityType
	}
	if !assetIn(r.typ, assetImageTypes) {
		return errAssetImageType
	}
	if a.storage == nil {
		return errAssetStorage
	}
	size, err := r.sizeColumn()
	if err != nil {
		return err
	}
	key := assetHex() + "-" + r.name
	entityType := entity.Str()
	row := assetRow{attributes: r.attributes(), key: key, size: size, entityType: &entityType, createdBy: c.User.ID}
	row.set("user_id", &c.User.ID)
	id, err := row.insert(c.Context(), a.db)
	if err != nil {
		return err
	}
	return a.assetCreated(c, id, r, key, &entityType, nil)
}

// userAsset is FileAsset.objects.get(id=asset_id, user_id=request.user.id).
func (a *API) userAsset(c *httpx.Ctx) (*fileAsset, error) {
	id, err := c.UUIDParam("asset_id")
	if err != nil {
		return nil, err
	}
	return a.getAsset(c.Context(), `f.id = $1 AND f.user_id = $2`, id, c.User.ID)
}

// patchUserAsset ports UserAssetsV2Endpoint.patch: the upload is confirmed,
// its metadata fetched, and the user's avatar or cover pointed at it.
func (a *API) patchUserAsset(c *httpx.Ctx) error {
	f, err := a.userAsset(c)
	if err != nil {
		return err
	}
	if !f.hasMetadata() {
		a.enqueueAssetMetadata(c.Context(), f.id)
	}
	if err := a.saveUserAssetEntity(c, f); err != nil {
		return err
	}
	if err := a.assetConfirm(c, f); err != nil {
		return err
	}
	return c.NoContent()
}

// saveUserAssetEntity is UserAssetsV2Endpoint.entity_asset_save: the
// previous avatar (or cover) is deleted, even when it is this very asset,
// and User.save() records the new one.
func (a *API) saveUserAssetEntity(c *httpx.Ctx, f *fileAsset) error {
	if f.entityType == nil || (*f.entityType != assetUserAvatar && *f.entityType != assetUserCover) {
		return nil
	}
	ctx := c.Context()
	col, image := "avatar_asset_id", "avatar = ''"
	if *f.entityType == assetUserCover {
		col, image = "cover_image_asset_id", "cover_image = NULL"
	}
	var previous *uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT `+col+` FROM users WHERE id = $1`, f.userID).Scan(&previous); err != nil {
		return err
	}
	if previous != nil {
		if err := a.assetSoftDelete(ctx, *previous); err != nil {
			return err
		}
	}
	_, err := a.db.Exec(ctx, `UPDATE users SET `+image+`, `+col+` = $2, `+userSaveSQL+` WHERE id = $1`, f.userID, f.id)
	return err
}

// deleteUserAsset ports UserAssetsV2Endpoint.delete: the user stops
// pointing at the asset and it is soft-deleted.
func (a *API) deleteUserAsset(c *httpx.Ctx) error {
	f, err := a.userAsset(c)
	if err != nil {
		return err
	}
	ctx := c.Context()
	if f.entityType != nil && (*f.entityType == assetUserAvatar || *f.entityType == assetUserCover) {
		col := "avatar_asset_id"
		if *f.entityType == assetUserCover {
			col = "cover_image_asset_id"
		}
		tag, err := a.db.Exec(ctx, `UPDATE users SET `+col+` = NULL, `+userSaveSQL+` WHERE id = $1`, f.userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows // User.objects.get raised
		}
	}
	if err := a.assetSoftDelete(ctx, f.id); err != nil {
		return err
	}
	return c.NoContent()
}

// staticAsset ports StaticFileAssetEndpoint.get (AllowAny): a redirect to
// a presigned URL for logos, avatars and covers. Script-capable types are
// served as attachments so they can't run on the app's origin.
func (a *API) staticAsset(c *httpx.Ctx) error {
	id, err := c.UUIDParam("asset_id")
	if err != nil {
		return err
	}
	f, err := a.getAsset(c.Context(), `f.id = $1`, id)
	if err != nil {
		return err
	}
	if !f.isUploaded {
		return errAssetNotFound
	}
	switch {
	case f.entityType == nil:
		return errAssetEntityType
	case *f.entityType == assetUserAvatar, *f.entityType == assetUserCover, *f.entityType == assetWorkspaceLogo,
		*f.entityType == assetProjectCover:
	default:
		return errAssetEntityType
	}
	typ, ok, err := assetAttr(f.attributes, "type")
	if err != nil {
		return err
	}
	mime := ""
	if ok && drf.PyTruthy(typ) {
		if !typ.IsString() {
			return errViewCrash // .split() of a non-str
		}
		mime = strings.ToLower(drf.PyStrip(strings.SplitN(typ.Str(), ";", 2)[0]))
	}
	disposition := "inline"
	if slices.Contains(assetScriptTypes, mime) {
		disposition = "attachment"
	}
	return a.assetRedirect(c, f.key, disposition, nil)
}
