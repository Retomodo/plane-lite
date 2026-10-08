package api

import (
	"context"
	"errors"
	"math"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/storage"
)

// Shared pieces of the file asset views (app/views/asset/v2.py,
// issue/attachment.py): the FileAsset model's entity types and asset_url,
// request parsing, row creation and the presigned redirects.

// FileAsset.EntityTypeContext.
const (
	assetIssueAttachment       = "ISSUE_ATTACHMENT"
	assetIssueDescription      = "ISSUE_DESCRIPTION"
	assetCommentDescription    = "COMMENT_DESCRIPTION"
	assetPageDescription       = "PAGE_DESCRIPTION"
	assetUserCover             = "USER_COVER"
	assetUserAvatar            = "USER_AVATAR"
	assetWorkspaceLogo         = "WORKSPACE_LOGO"
	assetProjectCover          = "PROJECT_COVER"
	assetDraftIssueAttachment  = "DRAFT_ISSUE_ATTACHMENT"
	assetDraftIssueDescription = "DRAFT_ISSUE_DESCRIPTION"
)

var assetEntityTypes = []string{assetIssueAttachment, assetIssueDescription, assetCommentDescription,
	assetPageDescription, assetUserCover, assetUserAvatar, assetWorkspaceLogo, assetProjectCover,
	assetDraftIssueAttachment, assetDraftIssueDescription}

// assetImageTypes are the types the image upload views accept.
var assetImageTypes = []string{"image/jpeg", "image/png", "image/webp", "image/jpg", "image/gif"}

// assetAttachmentTypes is settings.ATTACHMENT_MIME_TYPES.
var assetAttachmentTypes = []string{
	"image/jpeg", "image/png", "image/gif", "image/svg+xml", "image/webp", "image/tiff", "image/bmp",
	"application/pdf", "application/msword",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.ms-excel",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/vnd.ms-powerpoint",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation", "text/plain", "text/markdown",
	"application/rtf", "application/vnd.oasis.opendocument.spreadsheet", "application/vnd.oasis.opendocument.text",
	"application/vnd.oasis.opendocument.presentation", "application/vnd.oasis.opendocument.graphics",
	"application/vnd.visio", "image/x-portable-graymap", "image/x-portable-bitmap", "image/x-portable-pixmap",
	"application/vnd.oasis.opendocument.database",
	"audio/mpeg", "audio/wav", "audio/ogg", "audio/midi", "audio/x-midi", "audio/aac", "audio/flac", "audio/x-m4a",
	"video/mp4", "video/mpeg", "video/ogg", "video/webm", "video/quicktime", "video/x-msvideo", "video/x-ms-wmv",
	"application/zip", "application/x-rar", "application/x-rar-compressed", "application/x-tar", "application/gzip",
	"application/x-zip", "application/x-zip-compressed", "application/x-7z-compressed", "application/x-compressed",
	"application/x-compressed-tar", "application/x-compressed-tar-gz", "application/x-compressed-tar-bz2",
	"application/x-compressed-tar-zip", "application/x-compressed-tar-7z", "application/x-compressed-tar-rar",
	"model/gltf-binary", "model/gltf+json", "application/octet-stream",
	"font/ttf", "font/otf", "font/woff", "font/woff2",
	"text/css", "text/javascript", "application/json", "text/xml", "text/csv", "application/xml",
	"application/x-sql", "application/x-gzip",
}

// assetScriptTypes is settings.SCRIPT_CAPABLE_MIME_TYPES: always served as
// attachments.
var assetScriptTypes = []string{"image/svg+xml", "text/javascript", "application/javascript", "text/html",
	"application/xhtml+xml", "text/xml", "application/xml"}

var (
	// errAssetStorage answers what needs the bucket when none is configured.
	errAssetStorage    = httpx.Err(http.StatusServiceUnavailable, "File storage is not configured.")
	errAssetEntityType = httpx.Body(http.StatusBadRequest, map[string]any{"error": "Invalid entity type.", "status": false})
	errAssetImageType  = httpx.Body(http.StatusBadRequest, map[string]any{
		"error": "Invalid file type. Only JPEG, PNG, WebP, JPG and GIF files are allowed.", "status": false})
	errAssetNotFound = httpx.Err(http.StatusNotFound, "The requested asset could not be found.")
	errAssetNoAccess = httpx.Err(http.StatusForbidden, "You don't have access to this asset.")
)

// assetIn is `value in choices` for a request value: only an equal string
// matches.
func assetIn(v drf.Value, choices []string) bool {
	return v.IsString() && slices.Contains(choices, v.Str())
}

// assetSanitizeFilename ports plane.utils.path_validator.sanitize_filename;
// "" stands for None (non-strings and names that sanitize to nothing).
func assetSanitizeFilename(v drf.Value, ok bool) string {
	if !ok || !v.IsString() {
		return ""
	}
	name := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, v.Str())
	name = strings.ReplaceAll(name, `\`, "/")
	name = name[strings.LastIndex(name, "/")+1:] // os.path.basename
	name = strings.ReplaceAll(name, "..", "")
	name = drf.PyStrip(name)
	name = strings.TrimLeft(name, ".")
	return drf.PyStrip(name)
}

// assetHex is uuid.uuid4().hex.
func assetHex() string { return strings.ReplaceAll(uuid.NewString(), "-", "") }

// assetUploadRequest is what the upload views read from request.data, in
// their order: sanitize_filename(name) or "unnamed", the type (with the
// view's default), then int(size) (default FILE_SIZE_LIMIT), which raises
// for anything int() refuses.
type assetUploadRequest struct {
	data *drf.Data
	name string
	typ  drf.Value
	// limit is the size the upload may have: min(size, FILE_SIZE_LIMIT).
	limit *big.Int
}

func (a *API) assetParseUpload(c *httpx.Ctx, typeDefault string) (*assetUploadRequest, error) {
	d, err := drf.Parse(c.R)
	if err != nil {
		return nil, err
	}
	if !d.IsDict() {
		return nil, errViewCrash // request.data.get on a list or scalar
	}
	r := &assetUploadRequest{data: d}
	if r.name = assetSanitizeFilename(d.Get("name")); r.name == "" {
		r.name = "unnamed"
	}
	var ok bool
	if r.typ, ok = d.Get("type"); !ok {
		r.typ = drf.JSONValue(jsontext.Value(typeDefault))
	}
	limit := big.NewInt(a.cfg.FileSizeLimit)
	r.limit = limit
	if v, ok := d.Get("size"); ok {
		n, ok := drf.PyInt(v)
		if !ok {
			return nil, errViewCrash // int() raised
		}
		if n.Cmp(limit) < 0 {
			r.limit = n
		}
	}
	return r, nil
}

// attributes is {"name": name, "type": type, "size": size_limit}.
func (r *assetUploadRequest) attributes() jsontext.Value {
	return jsontext.Value(`{"name":` + mustJSON(r.name) + `,"type":` + string(r.typ.Raw()) + `,"size":` +
		r.limit.String() + `}`)
}

// sizeColumn is the FloatField value of size_limit; float() of an int too
// large for a double raises OverflowError.
func (r *assetUploadRequest) sizeColumn() (float64, error) {
	f, _ := new(big.Float).SetInt(r.limit).Float64()
	if math.IsInf(f, 0) {
		return 0, errViewCrash
	}
	return f, nil
}

// postLimit is the content-length-range maximum of the upload policy.
func (r *assetUploadRequest) postLimit() int64 {
	if r.limit.IsInt64() {
		return r.limit.Int64()
	}
	return math.MinInt64
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// assetFK is a request value assigned to a FileAsset foreign key and saved:
// None and "" are NULL, anything else goes through UUIDField.to_python,
// whose ValidationError the view answers with "Please provide valid
// detail". A UUID that matches no row fails the INSERT's constraint.
func assetFK(v drf.Value) (*uuid.UUID, error) {
	if v.IsNull() || v.IsString() && v.Str() == "" {
		return nil, nil
	}
	id, ok := drf.UUIDValue(v)
	if !ok {
		return nil, errFilterDetail
	}
	return &id, nil
}

// assetEntityColumn is get_entity_id_field: the FileAsset column the
// entity's id goes in. withDraft is ProjectAssetEndpoint's (and the copy
// task's) version, which also maps draft issue descriptions.
func assetEntityColumn(entityType string, withDraft bool) string {
	switch entityType {
	case assetWorkspaceLogo:
		return "workspace_id"
	case assetProjectCover:
		return "project_id"
	case assetUserAvatar, assetUserCover:
		return "user_id"
	case assetIssueAttachment, assetIssueDescription:
		return "issue_id"
	case assetPageDescription:
		return "page_id"
	case assetCommentDescription:
		return "comment_id"
	case assetDraftIssueDescription:
		if withDraft {
			return "draft_issue_id"
		}
	}
	return ""
}

// assetRow is a FileAsset to create.
type assetRow struct {
	attributes jsontext.Value
	key        string
	size       float64
	entityType *string
	// fks are the foreign key columns set (user_id, workspace_id, ...).
	fks             map[string]*uuid.UUID
	storageMetadata jsontext.Value // nil for the default {}
	metadataNull    bool           // storage_metadata=None: SQL NULL
	createdBy       uuid.UUID
}

func (r *assetRow) set(col string, id *uuid.UUID) {
	if r.fks == nil {
		r.fks = map[string]*uuid.UUID{}
	}
	r.fks[col] = id
}

var assetFKColumns = []string{"user_id", "workspace_id", "draft_issue_id", "project_id", "issue_id", "comment_id", "page_id"}

// insert is FileAsset.objects.create.
func (r *assetRow) insert(ctx context.Context, q db.Querier) (uuid.UUID, error) {
	meta := r.storageMetadata
	if meta == nil {
		meta = jsontext.Value("{}")
	}
	var metaArg any = []byte(meta)
	if r.metadataNull {
		metaArg = nil
	}
	args := []any{[]byte(r.attributes), r.key, r.size, r.entityType, metaArg, r.createdBy}
	cols := []string{"attributes", "asset", "size", "entity_type", "storage_metadata", "created_by_id"}
	vals := []string{"$1", "$2", "$3", "$4", "$5", "$6"}
	for _, c := range assetFKColumns {
		if id, ok := r.fks[c]; ok {
			args = append(args, id)
			cols = append(cols, c)
			vals = append(vals, "$"+itoa(len(args)))
		}
	}
	var id uuid.UUID
	err := q.QueryRow(ctx, `INSERT INTO file_assets (id, created_at, updated_at, is_deleted, is_archived, is_uploaded, `+
		strings.Join(cols, ", ")+`) VALUES (gen_random_uuid(), now(), now(), false, false, false, `+
		strings.Join(vals, ", ")+`) RETURNING id`, args...).Scan(&id)
	return id, err
}

// assetURL is FileAsset.asset_url of a stored asset.
func (a *API) assetURL(ctx context.Context, id uuid.UUID, entityType *string) (*string, error) {
	var (
		slug             *string
		project, issueID *uuid.UUID
	)
	if err := a.db.QueryRow(ctx, `SELECT w.slug, f.project_id, f.issue_id FROM file_assets f
		LEFT JOIN workspaces w ON w.id = f.workspace_id WHERE f.id = $1`, id).Scan(&slug, &project, &issueID); err != nil {
		return nil, err
	}
	return assetURLFor(entityType, id, slug, project, issueID)
}

// assetURLFor is FileAsset.asset_url: static URLs for logos, avatars and
// covers, project URLs for attachments and description images. The latter
// need the workspace's slug; an asset without a workspace raises there.
func assetURLFor(entityType *string, id uuid.UUID, slug *string, project, issueID *uuid.UUID) (*string, error) {
	if entityType == nil {
		return nil, nil
	}
	switch *entityType {
	case assetWorkspaceLogo, assetUserAvatar, assetUserCover, assetProjectCover:
		return strp("/api/assets/v2/static/" + id.String() + "/"), nil
	case assetIssueAttachment, assetIssueDescription, assetCommentDescription, assetPageDescription,
		assetDraftIssueDescription:
	default:
		return nil, nil
	}
	if slug == nil {
		return nil, errViewCrash // self.workspace is None
	}
	none := func(u *uuid.UUID) string {
		if u == nil {
			return "None"
		}
		return u.String()
	}
	base := "/api/assets/v2/workspaces/" + *slug + "/projects/" + none(project) + "/"
	if *entityType == assetIssueAttachment {
		return strp(base + "issues/" + none(issueID) + "/attachments/" + id.String() + "/"), nil
	}
	return strp(base + id.String() + "/"), nil
}

// assetUploadURL is where Go's upload_data sends the browser: the upload
// endpoint on this server, as the request reached it (R2 has no presigned
// POST; see the storage package).
func assetUploadURL(c *httpx.Ctx, id uuid.UUID) string {
	scheme := "http"
	if c.R.TLS != nil {
		scheme = "https"
	}
	if p := c.R.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	return scheme + "://" + c.R.Host + "/api/assets/v2/upload/" + id.String() + "/"
}

// assetCreated answers an upload POST: the presigned form (upload_data),
// asset_id and asset_url, plus extra members.
func (a *API) assetCreated(c *httpx.Ctx, id uuid.UUID, r *assetUploadRequest, key string, entityType *string,
	extra map[string]any) error {
	form, err := a.storage.NewPost(assetUploadURL(c, id), key, r.typ.Str(), r.postLimit(), time.Now())
	if err != nil {
		return err
	}
	assetURL, err := a.assetURL(c.Context(), id, entityType)
	if err != nil {
		return err
	}
	out := map[string]any{"upload_data": form, "asset_id": id.String(), "asset_url": assetURL}
	for k, v := range extra {
		out[k] = v
	}
	return c.JSON(http.StatusOK, out)
}

// pyQuote is urllib.parse.quote(s): UTF-8 bytes percent-encoded except
// letters, digits, "_.-~" and "/".
func pyQuote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.IndexByte("_.-~/", ch) >= 0 {
			b.WriteByte(ch)
			continue
		}
		b.WriteString("%" + strings.ToUpper(hexByte(ch)))
	}
	return b.String()
}

func hexByte(c byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[c>>4], digits[c&15]})
}

// assetAttr is attributes.get(name) on a FileAsset's attributes: ok=false
// when the key is missing; a non-dict attributes raises (errViewCrash).
func assetAttr(attrs jsontext.Value, name string) (drf.Value, bool, error) {
	if attrs.Kind() != '{' {
		return drf.Value{}, false, errViewCrash
	}
	v, ok := drf.JSONValue(attrs).Member(name)
	return v, ok, nil
}

// assetFilename is the filename generate_presigned_url gets from
// attributes.get("name"): nil (None, so a uuid4().hex) when missing or null.
func assetFilename(attrs jsontext.Value) (*drf.Value, error) {
	v, ok, err := assetAttr(attrs, "name")
	if err != nil || !ok || v.IsNull() {
		return nil, err
	}
	return &v, nil
}

// assetDisposition is S3Storage._get_content_disposition.
func assetDisposition(disposition string, filename *drf.Value) (string, error) {
	if filename == nil {
		return disposition + "; filename*=UTF-8''" + assetHex(), nil
	}
	if !drf.PyTruthy(*filename) {
		return disposition, nil
	}
	if !filename.IsString() {
		return "", errViewCrash // quote() of a non-str
	}
	return disposition + "; filename*=UTF-8''" + pyQuote(filename.Str()), nil
}

// assetRedirect is HttpResponseRedirect(storage.generate_presigned_url(...)).
func (a *API) assetRedirect(c *httpx.Ctx, key, disposition string, filename *drf.Value) error {
	cd, err := assetDisposition(disposition, filename)
	if err != nil {
		return err
	}
	u, err := a.storage.PresignGet(c.Context(), key, cd)
	if errors.Is(err, storage.ErrNotConfigured) {
		return errAssetStorage
	}
	if err != nil {
		return err
	}
	return c.Redirect(u)
}

// fileAsset is the FileAsset columns the views read.
type fileAsset struct {
	id              uuid.UUID
	key             string
	attributes      jsontext.Value
	entityType      *string
	userID          *uuid.UUID
	workspaceID     *uuid.UUID
	projectID       *uuid.UUID
	issueID         *uuid.UUID
	isUploaded      bool
	storageMetadata jsontext.Value
}

const fileAssetCols = `f.id, f.asset, f.attributes, f.entity_type, f.user_id, f.workspace_id, f.project_id, f.issue_id,
	f.is_uploaded, f.storage_metadata`

func scanFileAsset(row pgx.Row) (*fileAsset, error) {
	var f fileAsset
	err := row.Scan(&f.id, &f.key, &f.attributes, &f.entityType, &f.userID, &f.workspaceID, &f.projectID, &f.issueID,
		&f.isUploaded, &f.storageMetadata)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// getAsset is FileAsset.objects.get(<where>): live rows only. where uses
// the alias f and w (the asset's workspace, for workspace__slug).
func (a *API) getAsset(ctx context.Context, where string, args ...any) (*fileAsset, error) {
	return scanFileAsset(a.db.QueryRow(ctx, `SELECT `+fileAssetCols+` FROM file_assets f
		LEFT JOIN workspaces w ON w.id = f.workspace_id WHERE f.deleted_at IS NULL AND `+where+` LIMIT 1`, args...))
}

// hasMetadata is `if asset.storage_metadata` (NULL, {} and other falsy
// JSON values are empty).
func (f *fileAsset) hasMetadata() bool {
	return f.storageMetadata != nil && drf.PyTruthy(drf.JSONValue(f.storageMetadata))
}

// assetSoftDelete is the views' asset_delete: the live asset, if any,
// flagged is_deleted with deleted_at set (save(update_fields=...)).
func (a *API) assetSoftDelete(ctx context.Context, id uuid.UUID) error {
	_, err := a.db.Exec(ctx, `UPDATE file_assets SET is_deleted = true, deleted_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id)
	return err
}

// assetConfirm is the end of the PATCH views: is_uploaded set and
// attributes taken from request.data["attributes"] when present, saved
// with update_fields (no updated_at). request.data is read only here, as
// the view first touches it after its other writes.
func (a *API) assetConfirm(c *httpx.Ctx, f *fileAsset) error {
	d, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !d.IsDict() {
		return errViewCrash
	}
	var attrs any = []byte(f.attributes)
	if v, ok := d.Get("attributes"); ok {
		attrs = []byte(v.Raw())
		if v.IsNull() {
			attrs = nil // NOT NULL violation
		}
	}
	_, err = a.db.Exec(c.Context(), `UPDATE file_assets SET is_uploaded = true, attributes = $2 WHERE id = $1`, f.id, attrs)
	return err
}

// assetMetadataJob is bgtasks.storage_metadata_task.get_asset_object_metadata.
type assetMetadataJob struct {
	AssetID uuid.UUID `json:"asset_id"`
}

func (assetMetadataJob) Kind() string { return "get_asset_object_metadata" }

func (a *API) enqueueAssetMetadata(ctx context.Context, id uuid.UUID) {
	if err := jobs.Enqueue(ctx, a.jobs, assetMetadataJob{AssetID: id}); err != nil {
		a.log.Error("enqueue get_asset_object_metadata", "err", err)
	}
}

// assetObjectMetadata stores the object's HEAD as storage_metadata: NULL
// when the bucket answers with an error (a missing object included);
// nothing when the bucket can't be reached.
func (a *API) assetObjectMetadata(ctx context.Context, j assetMetadataJob) error {
	var key string
	err := a.db.QueryRow(ctx, `SELECT asset FROM file_assets WHERE id = $1 AND deleted_at IS NULL`, j.AssetID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var meta any
	m, err := a.storage.Head(ctx, key)
	switch {
	case err == nil:
		var lastModified *string
		if m.LastModified != nil {
			lastModified = strp(m.LastModified.UTC().Format("2006-01-02T15:04:05+00:00"))
		}
		meta = map[string]any{"ContentType": m.ContentType, "ContentLength": m.ContentLength,
			"LastModified": lastModified, "ETag": m.ETag, "Metadata": m.Metadata}
	case errors.Is(err, storage.ErrNotFound) || storage.IsClientError(err):
		meta = nil
	default:
		return err
	}
	var raw any
	if meta != nil {
		raw, _ = json.Marshal(meta)
	}
	_, err = a.db.Exec(ctx, `UPDATE file_assets SET storage_metadata = $2 WHERE id = $1`, j.AssetID, raw)
	return err
}
