package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/sanitize"
)

// PagesDescriptionViewSet (P/pages/<page_id>/description/), the endpoint
// apps/live loads and stores the collaborative document through, and
// PageVersionEndpoint (P/pages/<page_id>/versions/[<pk>/]).

// pageOwnedOrPublic is the Q(owned_by=user) | Q(access=0) filter.
const pageOwnedOrPublic = ` AND (pg.owned_by_id = $4 OR pg.access = 0)`

// getPageDescription ports PagesDescriptionViewSet.retrieve: the stored
// Yjs state (description_binary) as an octet-stream download, empty when
// there is none.
func (a *API) getPageDescription(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, pageID, _ := pageParamIDs(c)
	var binary []byte
	err := a.db.QueryRow(ctx, `SELECT pg.description_binary
		FROM pages pg JOIN project_pages pp ON pp.page_id = pg.id JOIN workspaces w ON w.id = pg.workspace_id
		WHERE pg.deleted_at IS NULL AND pg.id = $1 AND pp.deleted_at IS NULL AND pp.project_id = $2 AND w.slug = $3`+
		pageOwnedOrPublic, *pageID, projectID, c.Param("slug"), c.User.ID).Scan(&binary)
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "application/octet-stream")
	c.W.Header().Set("Content-Disposition", `attachment; filename="page_description.bin"`)
	c.W.WriteHeader(http.StatusOK)
	_, err = c.W.Write(binary)
	return err
}

// pageError is the error_code body of the description endpoint.
func pageError(code int, message string) error {
	return httpx.Body(http.StatusBadRequest, map[string]any{"error_code": code, "error_message": message})
}

// updatePageDescription ports PagesDescriptionViewSet.partial_update,
// which apps/live calls on every store: PageBinaryUpdateSerializer
// (base64 binary checked by validate_binary_data, html through nh3, any
// JSON), save(), then page_transaction and track_page_version.
func (a *API) updatePageDescription(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, pageID, _ := pageParamIDs(c)
	page, err := a.loadPageObject(ctx, c.Param("slug"), projectID, *pageID, pageOwnedOrPublic, c.User.ID)
	if err != nil {
		return err
	}
	if page.locked {
		return pageError(4701, "PAGE_LOCKED")
	}
	if page.archived != nil {
		return pageError(4702, "PAGE_ARCHIVED")
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	var set setList
	blankBinary := false
	if s, ok := v.Char("description_binary", drf.CharField{AllowBlank: true}); ok {
		switch {
		case *s == "":
			blankBinary = true
		default:
			if b, ok := pageB64Decode(*s); !ok {
				v.Add("description_binary", "Failed to decode base64 data")
			} else if msg := pageValidateBinary(b); msg != "" {
				v.Add("description_binary", "Invalid binary data: "+msg)
			} else {
				set.add("description_binary", b)
			}
		}
	}
	html := page.html
	if s, ok := v.Char("description_html", drf.CharField{AllowBlank: true}); ok {
		html = *s
		if html != "" {
			clean, _, err := sanitize.HTML(html)
			switch {
			case errors.Is(err, sanitize.ErrTooLarge):
				v.Add("description_html", "HTML content exceeds maximum size limit (10MB)")
			case err != nil:
				v.Add("description_html", "Failed to sanitize HTML")
			default:
				html = clean
			}
		}
		set.add("description_html", html)
	}
	if raw, ok := v.JSON("description_json", true); ok {
		if raw.Kind() == 'n' {
			set.addCast("description_json", nil, "::jsonb")
		} else {
			set.addCast("description_json", string(raw), "::jsonb")
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	if blankBinary {
		return errViewCrash // a str in a BinaryField: "bytes or buffer expected"
	}
	if err := a.pageSave(ctx, a.db, page.id, html, &c.User.ID, set); err != nil {
		return err
	}
	if v, ok := data.Get("description_html"); ok && drf.PyTruthy(v) {
		old := page.html
		a.enqueuePageTransaction(ctx, pageTransactionJob{NewHTML: json.RawMessage(v.Raw()), OldHTML: &old, PageID: page.id})
	}
	existing, _ := json.Marshal(map[string]string{"description_html": page.html})
	if err := jobs.Enqueue(ctx, a.jobs, pageVersionJob{PageID: page.id, ExistingInstance: string(existing),
		UserID: c.User.ID}); err != nil {
		a.log.Error("enqueue track_page_version", "err", err)
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Updated successfully"})
}

// pageB64Decode is base64.b64decode(s) (validate=False): ASCII only;
// characters outside the alphabet are skipped, decoding stops at complete
// padding, and a dangling quantum is an error.
func pageB64Decode(s string) ([]byte, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return nil, false
		}
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var (
		out        []byte
		quad, pads int
		leftchar   byte
	)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '=' {
			if quad >= 2 {
				pads++
				if quad+pads >= 4 {
					return out, true
				}
			}
			continue
		}
		v := strings.IndexByte(alphabet, ch)
		if v < 0 {
			continue
		}
		pads = 0
		b := byte(v)
		switch quad {
		case 0:
			quad, leftchar = 1, b
		case 1:
			out = append(out, leftchar<<2|b>>4)
			quad, leftchar = 2, b&0x0f
		case 2:
			out = append(out, leftchar<<4|b>>2)
			quad, leftchar = 3, b&0x03
		case 3:
			out = append(out, leftchar<<6|b)
			quad, leftchar = 0, 0
		}
	}
	if quad != 0 {
		return nil, false
	}
	if out == nil {
		out = []byte{}
	}
	return out, true
}

// pageSuspiciousBinary is content_validator.SUSPICIOUS_BINARY_PATTERNS.
var pageSuspiciousBinary = []string{"<html", "<!doctype", "<script", "javascript:", "data:", "<iframe"}

// pageValidateBinary is validate_binary_data on decoded bytes: the error
// message, or "".
func pageValidateBinary(b []byte) string {
	switch {
	case len(b) == 0:
		return ""
	case len(b) > sanitize.MaxSize:
		return "Binary data exceeds maximum size limit (10MB)"
	case len(b) < 4:
		return "Binary data too short to be valid document format"
	}
	// decode("utf-8", errors="ignore")[:200].lower()
	var text strings.Builder
	for n := 0; len(b) > 0 && n < 200; {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		if r == utf8.RuneError && size <= 1 {
			continue
		}
		text.WriteRune(r)
		n++
	}
	lower := pageLower(text.String())
	for _, p := range pageSuspiciousBinary {
		if strings.Contains(lower, p) {
			return "Binary data contains suspicious content patterns"
		}
	}
	return ""
}

// pageVersionItem is PageVersionSerializer.
type pageVersionItem struct {
	ID          uuid.UUID  `json:"id"`
	Workspace   uuid.UUID  `json:"workspace"`
	Page        uuid.UUID  `json:"page"`
	LastSavedAt time.Time  `json:"last_saved_at"`
	OwnedBy     uuid.UUID  `json:"owned_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CreatedBy   *uuid.UUID `json:"created_by"`
	UpdatedBy   *uuid.UUID `json:"updated_by"`
}

// pageVersionDetail is PageVersionDetailSerializer; description_binary
// goes through ModelField.value_to_string (base64).
type pageVersionDetail struct {
	ID                uuid.UUID      `json:"id"`
	Workspace         uuid.UUID      `json:"workspace"`
	Page              uuid.UUID      `json:"page"`
	LastSavedAt       time.Time      `json:"last_saved_at"`
	DescriptionBinary *string        `json:"description_binary"`
	DescriptionHTML   string         `json:"description_html"`
	DescriptionJSON   jsontext.Value `json:"description_json"`
	OwnedBy           uuid.UUID      `json:"owned_by"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	CreatedBy         *uuid.UUID     `json:"created_by"`
	UpdatedBy         *uuid.UUID     `json:"updated_by"`
}

// pageVersions ports PageVersionEndpoint.get: the page's versions (newest
// first) or one of them, through a live link to the URL project.
func (a *API) pageVersions(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, pageID, _ := pageParamIDs(c)
	const scope = `FROM page_versions v JOIN pages pg ON pg.id = v.page_id JOIN project_pages pp ON pp.page_id = pg.id
		JOIN workspaces w ON w.id = v.workspace_id
		WHERE v.deleted_at IS NULL AND pp.deleted_at IS NULL AND pp.project_id = $1 AND v.page_id = $2 AND w.slug = $3`
	args := []any{projectID, *pageID, c.Param("slug")}
	if c.Param("pk") != "" {
		pk, _ := c.UUIDParam("pk")
		var (
			d      pageVersionDetail
			binary []byte
			doc    string
		)
		if err := a.db.QueryRow(ctx, `SELECT DISTINCT v.id, v.workspace_id, v.page_id, v.last_saved_at,
				v.description_binary, v.description_html, v.description_json::text, v.owned_by_id, v.created_at,
				v.updated_at, v.created_by_id, v.updated_by_id `+scope+` AND v.id = $4`, append(args, pk)...).Scan(
			&d.ID, &d.Workspace, &d.Page, &d.LastSavedAt, &binary, &d.DescriptionHTML, &doc, &d.OwnedBy, &d.CreatedAt,
			&d.UpdatedAt, &d.CreatedBy, &d.UpdatedBy); err != nil {
			return err
		}
		if binary != nil {
			s := base64.StdEncoding.EncodeToString(binary)
			d.DescriptionBinary = &s
		}
		d.DescriptionJSON = jsontext.Value(doc)
		return c.JSON(http.StatusOK, d)
	}
	rows, err := a.db.Query(ctx, `SELECT v.id, v.workspace_id, v.page_id, v.last_saved_at, v.owned_by_id, v.created_at,
		v.updated_at, v.created_by_id, v.updated_by_id `+scope+` ORDER BY v.created_at DESC`, args...)
	if err != nil {
		return err
	}
	versions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pageVersionItem, error) {
		var p pageVersionItem
		err := row.Scan(&p.ID, &p.Workspace, &p.Page, &p.LastSavedAt, &p.OwnedBy, &p.CreatedAt, &p.UpdatedAt,
			&p.CreatedBy, &p.UpdatedBy)
		return p, err
	})
	if err != nil {
		return err
	}
	if versions == nil {
		versions = []pageVersionItem{}
	}
	return c.JSON(http.StatusOK, versions)
}
