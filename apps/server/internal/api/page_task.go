package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	jsonv2 "github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/jobs"
)

// The page Celery tasks: page_transaction (mentions and embeds into
// page_logs), track_page_version and copy_s3_objects_of_description_and_assets
// (for page duplicates). Each swallows its own exceptions, as the tasks do.

func (a *API) registerPageTaskJobs() {
	jobs.Register(a.jobs, func(ctx context.Context, j pageTransactionJob) error {
		if err := a.pageTransaction(ctx, j); err != nil {
			a.log.Error("page_transaction", "page", j.PageID, "err", err)
		}
		return nil
	})
	jobs.Register(a.jobs, func(ctx context.Context, j pageVersionJob) error {
		if err := a.trackPageVersion(ctx, j); err != nil {
			a.log.Error("track_page_version", "page", j.PageID, "err", err)
		}
		return nil
	})
	jobs.Register(a.jobs, func(ctx context.Context, j pageCopyJob) error {
		if err := a.copyPageDescription(ctx, j); err != nil {
			a.log.Error("copy_s3_objects_of_description_and_assets", "page", j.PageID, "err", err)
		}
		return nil
	})
}

// pageTransactionJob is page_transaction's arguments. NewHTML is whatever
// JSON value the request carried (the views pass request.data through).
type pageTransactionJob struct {
	NewHTML json.RawMessage `json:"new_description_html"`
	OldHTML *string         `json:"old_description_html"`
	PageID  uuid.UUID       `json:"page_id"`
}

func (pageTransactionJob) Kind() string { return "page_transaction" }

func (a *API) enqueuePageTransaction(ctx context.Context, j pageTransactionJob) {
	if err := jobs.Enqueue(ctx, a.jobs, j); err != nil {
		a.log.Error("enqueue page_transaction", "err", err)
	}
}

// pageComponent is one extracted editor component: its attributes as
// tag.get() returns them (nil when absent).
type pageComponent map[string]*string

// pageComponentNames is COMPONENT_MAP's order.
var pageComponentNames = []string{"mention-component", "image-component"}

// pageExtractComponents is extract_all_components: every mention and
// image component, none when the html is empty or fails to parse.
func pageExtractComponents(raw json.RawMessage) map[string][]pageComponent {
	out := map[string][]pageComponent{}
	v := drf.JSONValue(jsontext.Value(raw))
	if len(raw) == 0 || !drf.PyTruthy(v) || !v.IsString() {
		return out // empty, or BeautifulSoup raising on a non-string
	}
	soup, err := pageSoup(v.Str())
	if err != nil {
		return out
	}
	for _, name := range pageComponentNames {
		for _, tag := range soup.findAll(name) {
			c := pageComponent{}
			for _, attr := range []string{"id", "entity_identifier", "entity_name", "src"} {
				if v, ok := tag.get(attr); ok {
					c[attr] = &v
				}
			}
			out[name] = append(out[name], c)
		}
	}
	return out
}

func (c pageComponent) str(k string) string {
	if v := c[k]; v != nil {
		return *v
	}
	return ""
}

// pageTransaction ports page_transaction: log the components new to the
// html, soft-delete the logs of removed ones (whatever page holds them).
func (a *API) pageTransaction(ctx context.Context, j pageTransactionJob) error {
	var workspaceID uuid.UUID
	err := a.db.QueryRow(ctx, `SELECT workspace_id FROM pages WHERE id = $1 AND deleted_at IS NULL`, j.PageID).Scan(&workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var hasLogs bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM page_logs WHERE page_id = $1 AND deleted_at IS NULL)`,
		j.PageID).Scan(&hasLogs); err != nil {
		return err
	}
	var oldRaw json.RawMessage
	if j.OldHTML != nil {
		oldRaw = json.RawMessage(jsonString(*j.OldHTML))
	}
	oldC, newC := pageExtractComponents(oldRaw), pageExtractComponents(j.NewHTML)
	type logRow struct {
		transaction, name string
		identifier        *string
	}
	var (
		rows    []logRow
		deleted []string
		seen    = map[string]bool{}
	)
	for _, comp := range pageComponentNames {
		oldIDs, newIDs := map[string]bool{}, map[string]bool{}
		for _, m := range oldC[comp] {
			if id := m.str("id"); id != "" {
				oldIDs[id] = true
			}
		}
		for _, m := range newC[comp] {
			if id := m.str("id"); id != "" {
				newIDs[id] = true
			}
		}
		for id := range oldIDs {
			if !newIDs[id] && !seen[id] {
				seen[id] = true
				deleted = append(deleted, id)
			}
		}
		for _, m := range newC[comp] {
			id := m.str("id")
			if id == "" || (oldIDs[id] && hasLogs) {
				continue
			}
			row := logRow{transaction: id}
			if comp == "image-component" {
				row.name, row.identifier = "image", m["src"]
			} else {
				if m["entity_name"] == nil {
					return abort("entity_name is None") // NOT NULL
				}
				row.name, row.identifier = *m["entity_name"], m["entity_identifier"]
			}
			rows = append(rows, row)
		}
	}
	if len(rows) > 0 {
		// bulk_create(ignore_conflicts=True): the UUIDField values must
		// convert, or nothing is written.
		var (
			txs, names []string
			idents     []*uuid.UUID
		)
		for _, r := range rows {
			tx, ok := drf.ParseUUID(r.transaction)
			if !ok {
				return abort("transaction %q is not a UUID", r.transaction)
			}
			var ident *uuid.UUID
			if r.identifier != nil {
				id, ok := drf.ParseUUID(*r.identifier)
				if !ok {
					return abort("entity_identifier %q is not a UUID", *r.identifier)
				}
				ident = &id
			}
			txs, names, idents = append(txs, tx.String()), append(names, r.name), append(idents, ident)
		}
		if _, err := a.db.Exec(ctx, `INSERT INTO page_logs (transaction, page_id, entity_identifier, entity_name,
				entity_type, workspace_id, created_at, updated_at)
			SELECT t.tx::uuid, $4, t.ident, t.name, NULL, $5, clock_timestamp(), clock_timestamp()
			FROM unnest($1::text[], $2::uuid[], $3::text[]) WITH ORDINALITY AS t(tx, ident, name, n)
			ORDER BY t.n
			ON CONFLICT (page_id, transaction) DO NOTHING`, txs, idents, names, j.PageID, workspaceID); err != nil {
			return err
		}
	}
	if len(deleted) > 0 {
		ids := make([]uuid.UUID, 0, len(deleted))
		for _, d := range deleted {
			id, ok := drf.ParseUUID(d)
			if !ok {
				return abort("transaction %q is not a UUID", d)
			}
			ids = append(ids, id)
		}
		if _, err := a.db.Exec(ctx, `UPDATE page_logs SET deleted_at = now() WHERE deleted_at IS NULL AND transaction = ANY($1)`,
			ids); err != nil {
			return err
		}
	}
	return nil
}

// pageVersionJob is track_page_version's arguments.
type pageVersionJob struct {
	PageID           uuid.UUID `json:"page_id"`
	ExistingInstance string    `json:"existing_instance"`
	UserID           uuid.UUID `json:"user_id"`
}

func (pageVersionJob) Kind() string { return "track_page_version" }

// trackPageVersion ports track_page_version, which never writes: when the
// html changed it reads page.description, an attribute Page lacks, and the
// AttributeError ends the task (logged, swallowed). So no PageVersion is
// ever created or updated.
func (a *API) trackPageVersion(ctx context.Context, j pageVersionJob) error {
	var html string
	err := a.db.QueryRow(ctx, `SELECT description_html FROM pages WHERE id = $1 AND deleted_at IS NULL`, j.PageID).Scan(&html)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	cur := loadDict(&j.ExistingInstance)
	if v, ok := cur.get("description_html"); ok && v.IsString() && v.Str() == html {
		return nil
	}
	a.log.Debug("track_page_version: 'Page' object has no attribute 'description'", "page", j.PageID)
	return nil
}

// pageCopyJob is copy_s3_objects_of_description_and_assets for a PAGE.
// UserID stands for the request user that the eager task's save() records.
type pageCopyJob struct {
	PageID    uuid.UUID `json:"entity_identifier"`
	ProjectID uuid.UUID `json:"project_id"`
	Slug      string    `json:"slug"`
	UserID    uuid.UUID `json:"user_id"`
}

func (pageCopyJob) Kind() string { return "copy_s3_objects_of_description_and_assets" }

// copyPageDescription ports copy_s3_objects_of_description_and_assets for
// a duplicated page: the image-component sources must be asset ids (the
// FileAsset lookup fails otherwise); the page's project's assets among
// them are copied (copy_assets, asset_task.go) and their sources rewritten;
// the html is re-serialized by BeautifulSoup and saved, then apps/live
// converts it to the binary and JSON forms.
func (a *API) copyPageDescription(ctx context.Context, j pageCopyJob) error {
	var html string
	err := a.db.QueryRow(ctx, `SELECT description_html FROM pages WHERE id = $1 AND deleted_at IS NULL`, j.PageID).Scan(&html)
	if err != nil {
		return err
	}
	soup, soupErr := pageSoup(html)
	if soupErr == nil {
		var ids []uuid.UUID
		tags := soup.findAll("image-component")
		for _, tag := range tags {
			src, _ := tag.get("src")
			if src == "" {
				continue
			}
			id, ok := drf.ParseUUID(src)
			if !ok {
				return abort("asset id %q is not a UUID", src)
			}
			ids = append(ids, id)
		}
		copies, err := a.copyEntityAssets(ctx, j.PageID, j.ProjectID, ids, j.UserID)
		if err != nil {
			return err
		}
		// replace_asset_ids: a source equal to a copied asset's old id.
		for _, tag := range tags {
			src, _ := tag.get("src")
			for _, cp := range copies {
				if src == cp.old.String() {
					assetSetAttr(tag, "src", cp.new.String())
				}
			}
		}
		if soup.surrogate {
			return abort("lone surrogate in the description") // the save's UnicodeEncodeError
		}
		html = soup.String()
	}
	if err := a.pageSave(ctx, a.db, j.PageID, html, &j.UserID, setList{cols: []string{"description_html"},
		casts: []string{""}, args: []any{html}}); err != nil {
		return err
	}
	data, err := a.convertDocument(ctx, html)
	if err != nil || data == nil {
		return err
	}
	return a.pageSave(ctx, a.db, j.PageID, html, &j.UserID, *data)
}

// convertDocument is sync_with_external_service: apps/live's
// POST /convert-document/ for a page ("rich" variant). Request failures
// and non-200 answers mean no conversion; the result is the columns to
// save.
func (a *API) convertDocument(ctx context.Context, html string) (*setList, error) {
	base, err := url.Parse(a.cfg.LiveBaseURL)
	if a.cfg.LiveBaseURL == "" || err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, nil
	}
	target := url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/live/convert-document/"}
	body, _ := json.Marshal(map[string]string{"description_html": html, "variant": "rich"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var out map[string]jsontext.Value
	if err := jsonv2.Unmarshal(raw, &out); err != nil {
		var val jsontext.Value
		if jsonv2.Unmarshal(raw, &val) != nil || !drf.PyTruthy(drf.JSONValue(val)) {
			return nil, nil // invalid JSON is a RequestException; a falsy value skips the update
		}
		return nil, abort("convert-document returned a non-object")
	}
	if len(out) == 0 {
		return nil, nil
	}
	set := setList{}
	doc, ok := out["description_json"]
	if !ok || doc.Kind() == 'n' {
		set.addCast("description_json", nil, "::jsonb")
	} else {
		set.addCast("description_json", string(doc), "::jsonb")
	}
	encoded, ok := out["description_binary"]
	if !ok || encoded.Kind() != '"' {
		return nil, abort("description_binary is not a string")
	}
	binary, ok := pageB64Decode(drf.JSONValue(encoded).Str())
	if !ok {
		return nil, abort("description_binary is not base64")
	}
	set.add("description_binary", binary)
	return &set, nil
}
