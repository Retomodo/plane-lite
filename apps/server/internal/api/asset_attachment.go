package api

import (
	"net/http"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/httpx"
)

// Issue attachments: IssueAttachmentV2Endpoint (app/views/issue/attachment.py),
// the v2 rows of PORTING.md section 7. Attachments are FileAssets of type
// ISSUE_ATTACHMENT, uploaded like any other asset.

// issueAttachment is IssueAttachmentSerializer: every FileAsset field (the
// asset as its key, which S3Storage.url returns as-is) plus asset_url.
type issueAttachment struct {
	ID               uuid.UUID      `json:"id"`
	AssetURL         *string        `json:"asset_url"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	DeletedAt        *time.Time     `json:"deleted_at"`
	Attributes       jsontext.Value `json:"attributes"`
	Asset            *string        `json:"asset"`
	EntityType       *string        `json:"entity_type"`
	EntityIdentifier *string        `json:"entity_identifier"`
	IsDeleted        bool           `json:"is_deleted"`
	IsArchived       bool           `json:"is_archived"`
	ExternalID       *string        `json:"external_id"`
	ExternalSource   *string        `json:"external_source"`
	Size             float64        `json:"size"`
	IsUploaded       bool           `json:"is_uploaded"`
	StorageMetadata  jsontext.Value `json:"storage_metadata"`
	CreatedBy        *uuid.UUID     `json:"created_by"`
	UpdatedBy        *uuid.UUID     `json:"updated_by"`
	User             *uuid.UUID     `json:"user"`
	Workspace        *uuid.UUID     `json:"workspace"`
	DraftIssue       *uuid.UUID     `json:"draft_issue"`
	Project          *uuid.UUID     `json:"project"`
	Issue            *uuid.UUID     `json:"issue"`
	Comment          *uuid.UUID     `json:"comment"`
	Page             *uuid.UUID     `json:"page"`
}

const issueAttachmentCols = `f.id, f.created_at, f.updated_at, f.deleted_at, f.attributes, f.asset, f.entity_type,
	f.entity_identifier, f.is_deleted, f.is_archived, f.external_id, f.external_source, f.size, f.is_uploaded,
	f.storage_metadata, f.created_by_id, f.updated_by_id, f.user_id, f.workspace_id, f.draft_issue_id, f.project_id,
	f.issue_id, f.comment_id, f.page_id, w.slug`

func scanIssueAttachment(row pgx.Row) (issueAttachment, error) {
	var (
		f    issueAttachment
		meta []byte
		slug *string
	)
	err := row.Scan(&f.ID, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt, &f.Attributes, &f.Asset, &f.EntityType,
		&f.EntityIdentifier, &f.IsDeleted, &f.IsArchived, &f.ExternalID, &f.ExternalSource, &f.Size, &f.IsUploaded,
		&meta, &f.CreatedBy, &f.UpdatedBy, &f.User, &f.Workspace, &f.DraftIssue, &f.Project, &f.Issue, &f.Comment,
		&f.Page, &slug)
	if err != nil {
		return f, err
	}
	f.StorageMetadata = jsontext.Value("null")
	if meta != nil {
		f.StorageMetadata = meta
	}
	if f.Asset != nil && *f.Asset == "" {
		f.Asset = nil // FileField: no file is None
	}
	f.AssetURL, err = assetURLFor(f.EntityType, f.ID, slug, f.Project, f.Issue)
	return f, err
}

func (a *API) loadIssueAttachment(c *httpx.Ctx, id uuid.UUID) (issueAttachment, error) {
	return scanIssueAttachment(a.db.QueryRow(c.Context(), `SELECT `+issueAttachmentCols+` FROM file_assets f
		LEFT JOIN workspaces w ON w.id = f.workspace_id WHERE f.id = $1`, id))
}

// createIssueAttachment ports IssueAttachmentV2Endpoint.post.
func (a *API) createIssueAttachment(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	r, err := a.assetParseUpload(c, `false`)
	if err != nil {
		return err
	}
	if !assetIn(r.typ, assetAttachmentTypes) {
		return httpx.Body(http.StatusBadRequest, map[string]any{"error": "Invalid file type.", "status": false})
	}
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
	entityType := assetIssueAttachment
	row := assetRow{attributes: r.attributes(), key: key, size: size, entityType: &entityType, createdBy: c.User.ID}
	row.set("workspace_id", &wsID)
	row.set("issue_id", &issueID)
	row.set("project_id", &projectID)
	id, err := row.insert(ctx, a.db)
	if err != nil {
		return err
	}
	out, err := a.loadIssueAttachment(c, id)
	if err != nil {
		return err
	}
	return a.assetCreated(c, id, r, key, &entityType, map[string]any{"attachment": out})
}

// issueAttachmentFor is FileAsset.objects.get(pk=pk, workspace__slug=slug,
// project_id=project_id, issue_id=issue_id).
func (a *API) issueAttachmentFor(c *httpx.Ctx) (*fileAsset, error) {
	id, err := c.UUIDParam("pk")
	if err != nil {
		return nil, err
	}
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return nil, err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return nil, err
	}
	return a.getAsset(c.Context(), `f.id = $1 AND w.slug = $2 AND f.project_id = $3 AND f.issue_id = $4`,
		id, c.Param("slug"), projectID, issueID)
}

// listIssueAttachments ports IssueAttachmentV2Endpoint.get without pk: the
// issue's uploaded attachments, newest first.
func (a *API) listIssueAttachments(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	rows, err := a.db.Query(c.Context(), `SELECT `+issueAttachmentCols+` FROM file_assets f
		JOIN workspaces w ON w.id = f.workspace_id
		WHERE f.issue_id = $1 AND f.entity_type = 'ISSUE_ATTACHMENT' AND w.slug = $2 AND f.project_id = $3
			AND f.is_uploaded AND f.deleted_at IS NULL
		ORDER BY f.created_at DESC`, issueID, c.Param("slug"), projectID)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (issueAttachment, error) { return scanIssueAttachment(r) })
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, list)
}

// getIssueAttachment ports IssueAttachmentV2Endpoint.get with pk: a
// redirect to download it.
func (a *API) getIssueAttachment(c *httpx.Ctx) error {
	f, err := a.issueAttachmentFor(c)
	if err != nil {
		return err
	}
	if !f.isUploaded {
		return httpx.Body(http.StatusBadRequest, map[string]any{"error": "The asset is not uploaded.", "status": false})
	}
	return a.assetDownload(c, f)
}

// patchIssueAttachment ports IssueAttachmentV2Endpoint.patch: the first
// confirmation records "created an attachment". The full save() writes
// back the storage_metadata the view loaded, so the metadata task's result
// is lost.
func (a *API) patchIssueAttachment(c *httpx.Ctx) error {
	ctx := c.Context()
	f, err := a.issueAttachmentFor(c)
	if err != nil {
		return err
	}
	if !f.isUploaded {
		out, err := a.loadIssueAttachment(c, f.id)
		if err != nil {
			return err
		}
		raw, err := httpx.Marshal(out, c.Loc())
		if err != nil {
			return err
		}
		current := string(raw)
		a.enqueueIssueActivity(ctx, issueActivityJob{Type: "attachment.activity.created", CurrentInstance: &current,
			IssueID: f.issueID.String(), ActorID: c.User.ID.String(), ProjectID: f.projectID.String(),
			Epoch: time.Now().Unix(), Subscriber: true, Notification: true})
	}
	if !f.hasMetadata() {
		a.enqueueAssetMetadata(ctx, f.id)
	}
	var meta any
	if f.storageMetadata != nil {
		meta = []byte(f.storageMetadata)
	}
	if _, err := a.db.Exec(ctx, `UPDATE file_assets SET is_uploaded = true, storage_metadata = $2, updated_at = now(),
		updated_by_id = $3 WHERE id = $1`, f.id, meta, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// deleteIssueAttachment ports IssueAttachmentV2Endpoint.delete (project
// admins, or the attachment's creator): a soft delete through a full save,
// then "deleted the attachment".
func (a *API) deleteIssueAttachment(c *httpx.Ctx) error {
	ctx := c.Context()
	f, err := a.issueAttachmentFor(c)
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE file_assets SET is_deleted = true, deleted_at = now(), updated_at = now(),
		updated_by_id = $2 WHERE id = $1`, f.id, c.User.ID); err != nil {
		return err
	}
	a.enqueueIssueActivity(ctx, issueActivityJob{Type: "attachment.activity.deleted", IssueID: c.Param("issue_id"),
		ActorID: c.User.ID.String(), ProjectID: c.Param("project_id"), Epoch: time.Now().Unix(),
		Subscriber: true, Notification: true})
	return c.NoContent()
}

// allowAttachmentDelete is allow_permission([ADMIN], creator=True,
// model=FileAsset): an active workspace member who created the (live)
// asset, or a project admin.
func (a *API) allowAttachmentDelete(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		ctx := c.Context()
		wsRole, err := a.workspaceRole(ctx, c.Param("slug"), c.User.ID)
		if err != nil {
			return err
		}
		if wsRole == 0 {
			return errNoRole
		}
		pk, err := c.UUIDParam("pk")
		if err != nil {
			return err
		}
		var creator bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM file_assets
			WHERE id = $1 AND created_by_id = $2 AND deleted_at IS NULL)`, pk, c.User.ID).Scan(&creator); err != nil {
			return err
		}
		if creator {
			return h(c)
		}
		return a.allowProject(adminOnly, h)(c)
	}
}
