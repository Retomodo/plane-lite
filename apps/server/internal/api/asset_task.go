package api

import (
	"context"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/storage"
)

// unuploadedAssetsJob is bgtasks.file_asset_task.delete_unuploaded_file_asset
// (Celery beat, daily at 02:00). Scheduling is wired with the other
// periodic jobs.
type unuploadedAssetsJob struct{}

func (unuploadedAssetsJob) Kind() string { return "delete_unuploaded_file_asset" }

// deleteUnuploadedAssets soft-deletes the assets whose upload never
// completed (is_uploaded still false) created more than days ago: a
// queryset delete(), so only deleted_at is set. It returns how many.
func deleteUnuploadedAssets(ctx context.Context, q db.Querier, days int) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE file_assets SET deleted_at = now()
		WHERE created_at < now() - make_interval(days => $1) AND NOT is_uploaded AND deleted_at IS NULL`, days)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// assetCopy is one duplicated asset of copy_assets.
type assetCopy struct{ old, new uuid.UUID }

// copyEntityAssets is copy_s3_object.copy_assets for a page: each live
// asset of the page's workspace and the project among ids (newest first)
// is copied in the bucket to a new FileAsset of the duplicate, then all
// copies are marked uploaded. The new key takes the name unsanitized
// (str() of it, "None" when missing).
func (a *API) copyEntityAssets(ctx context.Context, pageID, projectID uuid.UUID, ids []uuid.UUID,
	user uuid.UUID) ([]assetCopy, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var wsID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT workspace_id FROM pages WHERE id = $1`, pageID).Scan(&wsID); err != nil {
		return nil, err
	}
	rows, err := a.db.Query(ctx, `SELECT id, asset, attributes, size, entity_type, storage_metadata FROM file_assets
		WHERE workspace_id = $1 AND project_id = $2 AND id = ANY($3) AND deleted_at IS NULL ORDER BY created_at DESC`,
		wsID, projectID, ids)
	if err != nil {
		return nil, err
	}
	type original struct {
		id         uuid.UUID
		key        string
		attributes jsontext.Value
		size       float64
		entityType *string
		meta       jsontext.Value
	}
	originals, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (original, error) {
		var o original
		return o, r.Scan(&o.id, &o.key, &o.attributes, &o.size, &o.entityType, &o.meta)
	})
	if err != nil {
		return nil, err
	}
	var copies []assetCopy
	for _, o := range originals {
		attrs, err := assetCopiedAttributes(o.attributes)
		if err != nil {
			return copies, err
		}
		name := "None"
		if v, ok, _ := assetAttr(o.attributes, "name"); ok && !v.IsNull() {
			name = drf.PyStr(v)
		}
		row := assetRow{attributes: attrs, key: wsID.String() + "/" + assetHex() + "-" + name, size: o.size,
			entityType: o.entityType, storageMetadata: o.meta, metadataNull: o.meta == nil, createdBy: user}
		row.set("workspace_id", &wsID)
		row.set("project_id", &projectID)
		if o.entityType != nil {
			if col := assetEntityColumn(*o.entityType, true); col != "" {
				if col == "project_id" {
					return copies, abort("create() got project_id twice")
				}
				row.set(col, &pageID)
			}
		}
		id, err := row.insert(ctx, a.db)
		if err != nil {
			return copies, err
		}
		if a.storage == nil {
			return copies, storage.ErrNotConfigured
		}
		if err := a.assetCopyObject(ctx, o.key, row.key); err != nil {
			return copies, err
		}
		copies = append(copies, assetCopy{old: o.id, new: id})
	}
	if len(copies) > 0 {
		newIDs := make([]uuid.UUID, len(copies))
		for i, cp := range copies {
			newIDs[i] = cp.new
		}
		if _, err := a.db.Exec(ctx, `UPDATE file_assets SET is_uploaded = true WHERE id = ANY($1) AND deleted_at IS NULL`,
			newIDs); err != nil {
			return copies, err
		}
	}
	return copies, nil
}

// assetSetAttr is tag[name] = value on a parsed description.
func assetSetAttr(n *pageSoupNode, name, value string) {
	for i := range n.attrs {
		if n.attrs[i].name == name {
			n.attrs[i].value = value
			return
		}
	}
	n.attrs = append(n.attrs, pageSoupAttr{name: name, value: value})
}
