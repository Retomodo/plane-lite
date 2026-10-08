package contract

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// File assets (PORTING.md section 17 and the v2 issue attachments).
//
// The full upload flow runs end to end: POST returns upload_data, the
// "browser" posts the file to upload_data.url with upload_data.fields, PATCH
// confirms it, and a GET redirects to a presigned URL that is then fetched
// from the bucket. Recording, Django's upload_data is a MinIO presigned POST
// and its redirects point at MinIO; verifying, upload_data.url is the Go
// server's own upload endpoint (R2 has no presigned POST, see DEVIATIONS.md)
// and the redirects point at the same MinIO.

// requireStorage skips an asset scenario when there is no bucket: recording
// needs the slot's MinIO, which the reference stores assets in
// (PLANE_DEV_S3=1); verifying needs any bucket (PLANE_DEV_S3=1 or the
// CONTRACT_S3_* variables, see env.go).
func requireStorage(t *testing.T) {
	t.Helper()
	e := current(t).env
	if e.record && e.minioURL == "" {
		t.Skip("recording file assets needs MinIO: PLANE_DEV_S3=1 scripts/devstack.sh N up, then export PLANE_DEV_S3=1")
	}
	if !e.s3.configured() {
		t.Skip("no object storage for file assets: start MinIO with PLANE_DEV_S3=1 scripts/devstack.sh N up and " +
			"export PLANE_DEV_S3=1, or point CONTRACT_S3_ENDPOINT (and _ACCESS_KEY, _SECRET_KEY, _BUCKET, _REGION) at a bucket")
	}
}

// assetUploadMask hides what differs between a presigned POST and Go's
// upload endpoint (the URL), what changes with the clock (the signed policy,
// its date and credential scope) and the key, which embeds uuid4().hex. The
// key's shape is checked through assetRows.
var assetUploadMask = Mask("upload_data.url", "upload_data.fields.policy", "upload_data.fields.x-amz-credential",
	"upload_data.fields.x-amz-date", "upload_data.fields.x-amz-signature", "upload_data.fields.key")

var (
	s3CodeRe = regexp.MustCompile(`<Code>([^<]*)</Code>`)
	hexInRe  = regexp.MustCompile(`[0-9a-f]{32}`)
)

// s3Request sends a request to a URL the API handed out (upload_data.url
// or a presigned GET). The reference names MinIO by its compose hostname,
// unreachable from here: the request goes to MinIO's published port with
// the original Host, which presigned signatures cover.
func s3Request(s *Scenario, method, raw string, body io.Reader, contentType string) (*http.Response, []byte) {
	s.t.Helper()
	target, host := raw, ""
	if rest, ok := strings.CutPrefix(raw, S3InternalURL); ok {
		target, host = s.tgt.env.minioURL+rest, strings.TrimPrefix(S3InternalURL, "http://")
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		s.t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, raw, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// s3Code is the error code of an S3 XML error body, or nil.
func s3Code(b []byte) any {
	if m := s3CodeRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return nil
}

// assetUpload posts content the way the web app does
// (generateFileUploadPayload: every upload_data field, then "file") and
// records the status and S3 error code. override replaces fields, to
// tamper with the signed ones.
func assetUpload(s *Scenario, label string, r *Response, content []byte, override map[string]string) {
	s.t.Helper()
	data, _ := r.Get("upload_data").(map[string]any)
	if data == nil {
		s.t.Fatalf("%s: no upload_data in %s", label, r.Raw)
	}
	fields := map[string]string{}
	for k, v := range data["fields"].(map[string]any) {
		fields[k], _ = v.(string)
	}
	for k, v := range override {
		fields[k] = v
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, k := range keys {
		_ = w.WriteField(k, fields[k])
	}
	fw, _ := w.CreateFormFile("file", "upload.bin")
	_, _ = fw.Write(content)
	_ = w.Close()
	resp, b := s3Request(s, http.MethodPost, data["url"].(string), &buf, w.FormDataContentType())
	s.steps = append(s.steps, Step{Actor: "browser", Method: "UPLOAD", Path: label, Status: resp.StatusCode,
		Body: map[string]any{"code": s3Code(b)}})
}

// assetFetch follows a presigned redirect to the bucket and records what
// the browser gets: the status, the object's type, disposition and bytes.
func assetFetch(s *Scenario, label string, r *Response) {
	s.t.Helper()
	loc := r.Header.Get("Location")
	if loc == "" {
		s.t.Fatalf("%s: no redirect", label)
	}
	resp, b := s3Request(s, http.MethodGet, loc, nil, "")
	body := map[string]any{"code": s3Code(b)}
	if resp.StatusCode == http.StatusOK {
		body = map[string]any{"content": string(b),
			"content_disposition": hexInRe.ReplaceAllString(resp.Header.Get("Content-Disposition"), "<hex32>")}
	}
	s.steps = append(s.steps, Step{Actor: "browser", Method: "GET", Path: label, Status: resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"), Body: body})
}

// assetGet is a GET that may redirect to a presigned URL. The redirect's
// host and bucket (MinIO's compose name for the reference, whatever bucket
// Go is given) and its signature change between runs, so the step keeps
// the key (its hex normalized), the expiry and the
// response-content-disposition, whose filename can be uuid4().hex.
func assetGet(c *Client, path string, opts ...Opt) *Response {
	c.s.t.Helper()
	r := c.Get(path, opts...)
	if loc := r.Header.Get("Location"); r.Status == http.StatusFound && loc != "" {
		u, err := url.Parse(loc)
		if err != nil {
			c.s.t.Fatal(err)
		}
		q := u.Query()
		_, key, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/") // path-style: /<bucket>/<key>
		norm := "<s3>/<bucket>/" + hexInRe.ReplaceAllString(c.s.ids.replace(key), "<hex32>") +
			"?X-Amz-Expires=" + q.Get("X-Amz-Expires") +
			"&response-content-disposition=" + hexInRe.ReplaceAllString(q.Get("response-content-disposition"), "<hex32>")
		if q.Get("X-Amz-Signature") != "" {
			norm += "&<signed>"
		}
		c.s.steps[len(c.s.steps)-1].Location = norm
	}
	return r
}

// assetRows records file_assets by names rather than ids. Keys show their
// shape (uuid4().hex replaced); LastModified only whether it is set the way
// get_object_metadata writes it.
func assetRows(s *Scenario) {
	s.DBRows("file_assets", `SELECT regexp_replace(replace(f.asset, coalesce(f.workspace_id::text, ''), '<ws>'),
			'[0-9a-f]{32}', '<hex>', 'g') AS asset, f.attributes, f.size, f.entity_type, f.entity_identifier,
			u.email AS "user", w.slug AS workspace, p.identifier AS project, i.name AS issue,
			c.comment_html AS comment, pg.name AS page, f.draft_issue_id IS NOT NULL AS has_draft,
			f.is_uploaded, f.is_deleted, f.deleted_at IS NOT NULL AS deleted, f.is_archived,
			f.external_id, f.external_source,
			f.storage_metadata - 'LastModified' AS storage_metadata,
			f.storage_metadata->>'LastModified' ~ '^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\+00:00$' AS last_modified_ok,
			cb.email AS created_by, ub.email AS updated_by
		FROM file_assets f LEFT JOIN users u ON u.id = f.user_id LEFT JOIN workspaces w ON w.id = f.workspace_id
		LEFT JOIN projects p ON p.id = f.project_id LEFT JOIN issues i ON i.id = f.issue_id
		LEFT JOIN issue_comments c ON c.id = f.comment_id LEFT JOIN pages pg ON pg.id = f.page_id
		LEFT JOIN users cb ON cb.id = f.created_by_id LEFT JOIN users ub ON ub.id = f.updated_by_id
		ORDER BY f.created_at, f.asset`)
}

func TestUserAssets(t *testing.T) {
	requireStorage(t)
	Run(t, "asset_user", func(s *Scenario) {
		alice := s.ClientFrom("alice", "10.0.40.1")
		signUp(alice, "alice@example.com", strongPassword)
		bob := s.ClientFrom("bob", "10.0.40.2")
		signUp(bob, "bob@example.com", strongPassword)
		anon := s.Client("anon")
		const ua = "/api/assets/v2/user-assets/"
		const static = "/api/assets/v2/static/"
		userRows := func() {
			s.DBRows("users", `SELECT u.email, u.avatar, u.cover_image,
					regexp_replace(av.asset, '[0-9a-f]{32}', '<hex>') AS avatar_asset,
					regexp_replace(cv.asset, '[0-9a-f]{32}', '<hex>') AS cover_asset
				FROM users u LEFT JOIN file_assets av ON av.id = u.avatar_asset_id
				LEFT JOIN file_assets cv ON cv.id = u.cover_image_asset_id ORDER BY u.email`)
		}

		// Validation, in the view's order: int(size) comes first and crashes.
		anon.Post(ua, map[string]any{"entity_type": "USER_AVATAR"})
		alice.Post(ua, map[string]any{"name": "a.png", "type": "image/png", "size": 10})
		alice.Post(ua, map[string]any{"entity_type": "WORKSPACE_LOGO", "type": "image/png"})
		alice.Post(ua, map[string]any{"entity_type": "", "type": "image/png"})
		alice.Post(ua, map[string]any{"entity_type": "USER_AVATAR", "type": "image/svg+xml"})
		alice.Post(ua, map[string]any{"entity_type": "USER_AVATAR", "type": ""})
		alice.Post(ua, map[string]any{"entity_type": "USER_AVATAR", "type": []any{"image/png"}})
		alice.Post(ua, map[string]any{"entity_type": "WORKSPACE_LOGO", "size": "big"})
		alice.Post(ua, map[string]any{"entity_type": "USER_AVATAR", "size": nil})
		alice.Post(ua, []any{"USER_AVATAR"})

		// Defaults: "unnamed", image/jpeg and FILE_SIZE_LIMIT; never uploaded.
		pending := alice.Post(ua, map[string]any{"entity_type": "USER_COVER"}, assetUploadMask)
		// A name is sanitized; a size is capped at FILE_SIZE_LIMIT; "7" and
		// 7.9 are int()ed.
		alice.Post(ua, map[string]any{"entity_type": "USER_COVER", "name": "../x\\..\x01 ..me .png", "size": 99999999},
			assetUploadMask)
		alice.Post(ua, map[string]any{"entity_type": "USER_COVER", "name": 12, "size": "7"}, assetUploadMask)
		alice.Post(ua, map[string]any{"entity_type": "USER_COVER", "name": " .. ", "size": 7.9}, assetUploadMask)

		// The avatar flow: create, upload, confirm, read back.
		avatar := alice.Post(ua, map[string]any{"entity_type": "USER_AVATAR", "name": "me.png", "type": "image/png",
			"size": 8}, assetUploadMask)
		avatarID := avatar.String("asset_id")
		assetUpload(s, "avatar: one byte too many", avatar, []byte("123456789"), nil)
		assetUpload(s, "avatar: empty", avatar, []byte{}, nil)
		assetUpload(s, "avatar: another type", avatar, []byte("12345678"), map[string]string{"Content-Type": "image/gif"})
		assetUpload(s, "avatar: another key", avatar, []byte("12345678"), map[string]string{"key": "elsewhere.png"})
		assetUpload(s, "avatar", avatar, []byte("PNG-DATA"), nil)
		// Not confirmed yet: the static URL 404s.
		assetGet(anon, static+avatarID+"/")
		bob.Patch(ua+avatarID+"/", nil)
		bob.Delete(ua + avatarID + "/")
		alice.Patch(ua+dead+"/", nil)
		alice.Patch(ua+avatarID+"/", nil)
		alice.Get("/api/users/me/", Mask("token"))
		r := assetGet(anon, static+avatarID+"/")
		assetFetch(s, "avatar", r)
		assetRows(s)
		userRows()

		// A second avatar replaces (and deletes) the first; attributes come
		// from the PATCH body when it has them.
		second := alice.Post(ua, map[string]any{"entity_type": "USER_AVATAR", "name": "me2.png", "type": "image/png",
			"size": 100}, assetUploadMask)
		assetUpload(s, "second avatar", second, []byte("SECOND"), nil)
		alice.Patch(ua+second.String("asset_id")+"/", map[string]any{"attributes": map[string]any{"name": "me2.svg",
			"type": "image/svg+xml", "size": 6}})
		// Script-capable types are served as attachments.
		r = assetGet(alice, static+second.String("asset_id")+"/")
		assetFetch(s, "second avatar", r)
		assetGet(anon, static+avatarID+"/")
		assetGet(anon, static+pending.String("asset_id")+"/")
		assetGet(anon, static+dead+"/")

		// The cover, then deleting both.
		cover := alice.Post(ua, map[string]any{"entity_type": "USER_COVER", "name": "cover.jpg", "size": 100},
			assetUploadMask)
		assetUpload(s, "cover", cover, []byte("JPEG"), nil)
		alice.Patch(ua+cover.String("asset_id")+"/", map[string]any{"attributes": "not a dict"})
		alice.Get("/api/users/me/", Mask("token"))
		userRows()
		alice.Patch(ua+cover.String("asset_id")+"/", map[string]any{"attributes": nil})
		alice.Delete(ua + cover.String("asset_id") + "/")
		alice.Delete(ua + second.String("asset_id") + "/")
		alice.Delete(ua + second.String("asset_id") + "/")
		alice.Get("/api/users/me/", Mask("token"))
		assetRows(s)
		userRows()
	})
}

// assetEntityRows records what the asset views link assets to: workspace
// logos and project covers.
func assetEntityRows(s *Scenario) {
	s.DBRows("workspaces", `SELECT w.slug, w.logo, regexp_replace(f.asset, '[0-9a-f]{32}', '<hex>', 'g') AS logo_asset,
			ub.email AS updated_by
		FROM workspaces w LEFT JOIN file_assets f ON f.id = w.logo_asset_id LEFT JOIN users ub ON ub.id = w.updated_by_id
		ORDER BY w.slug`)
	s.DBRows("projects", `SELECT p.identifier, p.cover_image, regexp_replace(f.asset, '[0-9a-f]{32}', '<hex>', 'g') AS cover_asset,
			ub.email AS updated_by
		FROM projects p LEFT JOIN file_assets f ON f.id = p.cover_image_asset_id LEFT JOIN users ub ON ub.id = p.updated_by_id
		ORDER BY p.identifier`)
}

func TestWorkspaceAssets(t *testing.T) {
	requireStorage(t)
	Run(t, "asset_workspace", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "41")
		anon := s.Client("anon")
		const ws = "/api/workspaces/acme/"
		const aw = "/api/assets/v2/workspaces/acme/"
		const static = "/api/assets/v2/static/"
		wsID := s.DBStrings(`SELECT id::text FROM workspaces WHERE slug = 'acme'`)[0]
		issue := alice.Post(ws+"projects/"+pl+"/issues/", map[string]any{"name": "Bug"},
			Unordered("label_ids", "assignee_ids", "module_ids")).String("id")
		logo := func(c *Client, body map[string]any) *Response {
			body["entity_type"] = "WORKSPACE_LOGO"
			return c.Post(aw, body, assetUploadMask)
		}

		// Who may create, then validation in the view's order.
		anon.Post(aw, map[string]any{"entity_type": "WORKSPACE_LOGO"})
		outsider.Post(aw, map[string]any{"entity_type": "WORKSPACE_LOGO"})
		alice.Post("/api/assets/v2/workspaces/nowhere/", map[string]any{"entity_type": "WORKSPACE_LOGO"})
		alice.Post(aw, map[string]any{"type": "image/png"})
		alice.Post(aw, map[string]any{"entity_type": "LOGO"})
		alice.Post(aw, map[string]any{"entity_type": "LOGO", "size": "x"})
		alice.Post(aw, []any{})
		logo(bob, map[string]any{"entity_identifier": wsID})
		logo(carol, map[string]any{"entity_identifier": wsID})
		logo(alice, map[string]any{"entity_identifier": wsID, "type": "image/svg+xml"})
		// entity_identifier becomes the entity's foreign key: missing is
		// False (UUID 0, no such row), "" is NULL, junk isn't a UUID.
		logo(alice, map[string]any{})
		logo(alice, map[string]any{"entity_identifier": "junk"})
		logo(alice, map[string]any{"entity_identifier": dead})
		logo(alice, map[string]any{"entity_identifier": 7})
		orphan := logo(alice, map[string]any{"entity_identifier": "", "name": "orphan.png"})
		bob.Post(aw, map[string]any{"entity_type": "USER_AVATAR", "entity_identifier": userID(bob), "name": "bob.png"},
			assetUploadMask)
		bob.Post(aw, map[string]any{"entity_type": "ISSUE_DESCRIPTION", "entity_identifier": issue, "name": "i.png"},
			assetUploadMask)
		bob.Post(aw, map[string]any{"entity_type": "DRAFT_ISSUE_ATTACHMENT", "name": "d.png"}, assetUploadMask)
		bob.Post(aw, map[string]any{"entity_type": "PAGE_DESCRIPTION", "entity_identifier": dead}, assetUploadMask)

		// The logo: upload, confirm, read through every URL.
		lg := logo(alice, map[string]any{"entity_identifier": wsID, "name": "logo one.png", "type": "image/png", "size": 64})
		lgID := lg.String("asset_id")
		assetGet(alice, aw+lgID+"/")
		assetUpload(s, "logo", lg, []byte("LOGO"), nil)
		outsider.Patch(aw+lgID+"/", nil)
		alice.Patch(aw+dead+"/", nil)
		alice.Patch("/api/assets/v2/workspaces/nowhere/"+lgID+"/", nil)
		carol.Patch(aw+lgID+"/", nil)
		alice.Get(ws, wsMask)
		r := assetGet(anon, static+lgID+"/")
		assetFetch(s, "logo, static", r)
		r = assetGet(dan, aw+lgID+"/")
		assetFetch(s, "logo, workspace", r)
		outsider.Get(aw + lgID + "/")
		r = assetGet(dan, aw+"download/"+lgID+"/")
		assetFetch(s, "logo, download", r)
		alice.Get(aw + "check/" + lgID + "/")
		outsider.Get(aw + "check/" + lgID + "/")
		assetRows(s)
		assetEntityRows(s)

		// Confirming the logo again deletes it: it is its own predecessor.
		alice.Patch(aw+lgID+"/", map[string]any{"attributes": map[string]any{"name": "", "type": "image/png"}})
		alice.Get(aw + lgID + "/")
		alice.Get(aw + "check/" + lgID + "/")
		outsider.Post(aw+"restore/"+lgID+"/", nil)
		alice.Post(aw+"restore/"+dead+"/", nil)
		bob.Post(aw+"restore/"+lgID+"/", nil)
		alice.Get(aw + "check/" + lgID + "/")
		alice.Get(aw + "check/" + dead + "/")
		// An empty name downloads without a filename.
		assetGet(alice, aw+lgID+"/")
		assetGet(alice, aw+"download/"+lgID+"/")
		// The orphan has no workspace: no workspace URL reaches it.
		assetUpload(s, "orphan", orphan, []byte("ORPHAN"), nil)
		alice.Patch(aw+orphan.String("asset_id")+"/", nil)
		alice.Get(aw + "check/" + orphan.String("asset_id") + "/")

		// A project cover through the workspace URL: project-bound, so only
		// the project's members reach it.
		cover := alice.Post(aw, map[string]any{"entity_type": "PROJECT_COVER", "entity_identifier": pl, "name": "c.jpg",
			"size": 10}, assetUploadMask)
		coverID := cover.String("asset_id")
		assetUpload(s, "cover", cover, []byte("COVER"), nil)
		dan.Patch(aw+coverID+"/", nil)
		bob.Patch(aw+coverID+"/", map[string]any{"attributes": map[string]any{"name": "cover.jpg"}})
		alice.Get(ws+"projects/"+pl+"/", projMask)
		dan.Get(aw + coverID + "/")
		dan.Delete(aw + coverID + "/")
		r = assetGet(carol, aw+coverID+"/")
		assetFetch(s, "cover", r)
		assetGet(dan, aw+"download/"+coverID+"/")
		assetGet(dan, aw+"download/"+orphan.String("asset_id")+"/")
		alice.Get(aw + "download/" + dead + "/")
		assetEntityRows(s)

		// Not uploaded yet.
		pending := alice.Post(aw, map[string]any{"entity_type": "PROJECT_COVER", "entity_identifier": pl}, assetUploadMask)
		alice.Get(aw + pending.String("asset_id") + "/")
		alice.Get(aw + "download/" + pending.String("asset_id") + "/")

		// Duplicates: same workspace, uploaded sources only.
		dup := aw + "duplicate-assets/" + coverID + "/"
		outsider.Post(dup, map[string]any{"entity_type": "ISSUE_DESCRIPTION"})
		alice.Post(dup, map[string]any{})
		alice.Post(dup, map[string]any{"entity_type": "COVER"})
		alice.Post(dup, map[string]any{"entity_type": "ISSUE_DESCRIPTION", "project_id": dead})
		alice.Post(aw+"duplicate-assets/"+pending.String("asset_id")+"/", map[string]any{"entity_type": "ISSUE_DESCRIPTION"})
		alice.Post(aw+"duplicate-assets/"+pending.String("asset_id")+"/", map[string]any{"entity_type": "ISSUE_DESCRIPTION",
			"project_id": "junk"})
		copied := bob.Post(dup, map[string]any{"entity_type": "ISSUE_DESCRIPTION", "entity_id": issue, "project_id": pl})
		r = assetGet(bob, aw+copied.String("asset_id")+"/")
		assetFetch(s, "copied cover", r)
		// The sixth call on one asset within a minute is throttled.
		alice.Post(dup, map[string]any{"entity_type": "WORKSPACE_LOGO"})
		alice.Post(dup, map[string]any{"entity_type": "PROJECT_COVER", "entity_id": pl})
		alice.Post(aw+"duplicate-assets/"+lgID+"/", map[string]any{"entity_type": "USER_COVER", "entity_id": userID(alice)})
		// PROJECT_COVER passes project_id twice (a TypeError); a logo's
		// entity_id (None here) overrides its workspace.
		alice.Post(aw+"duplicate-assets/"+lgID+"/", map[string]any{"entity_type": "PROJECT_COVER", "entity_id": pl})
		alice.Post(aw+"duplicate-assets/"+lgID+"/", map[string]any{"entity_type": "WORKSPACE_LOGO"})
		alice.Post(aw+"duplicate-assets/"+dead+"/", map[string]any{"entity_type": "USER_COVER"})
		assetRows(s)

		// Deleting unlinks the logo and the cover; check and restore see it.
		outsider.Delete(aw + lgID + "/")
		alice.Delete(aw + lgID + "/")
		alice.Delete(aw + lgID + "/")
		alice.Get(aw + "check/" + lgID + "/")
		bob.Delete(aw + coverID + "/")
		alice.Delete(aw + dead + "/")
		alice.Get(ws, wsMask)
		assetEntityRows(s)
		alice.Post(aw+"restore/"+lgID+"/", nil)
		alice.Get(aw + "check/" + lgID + "/")
		assetRows(s)
		assetEntityRows(s)
	})
}

func TestProjectAssets(t *testing.T) {
	requireStorage(t)
	Run(t, "asset_project", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "42")
		const ws = "/api/workspaces/acme/"
		const aw = "/api/assets/v2/workspaces/acme/"
		ap := aw + "projects/" + pl + "/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		issue := alice.Post(p+"issues/", map[string]any{"name": "Bug"}, ids).String("id")
		comment := alice.Post(p+"issues/"+issue+"/comments/", map[string]any{"comment_html": "<p>hi</p>"}).String("id")
		page := alice.Post(p+"pages/", map[string]any{"name": "Doc"}, pageIDs).String("id")
		ot := alice.Post(ws+"projects/", map[string]any{"name": "Other", "identifier": "OT"}, projMask).String("id")
		desc := func(c *Client, entity, identifier, name string) *Response {
			body := map[string]any{"entity_type": entity, "name": name, "type": "image/png", "size": 32}
			if identifier != "" {
				body["entity_identifier"] = identifier
			}
			return c.Post(ap, body, assetUploadMask)
		}

		// Who may create, then validation.
		dan.Post(ap, map[string]any{"entity_type": "ISSUE_DESCRIPTION"})
		outsider.Post(ap, map[string]any{"entity_type": "ISSUE_DESCRIPTION"})
		alice.Post(aw+"projects/"+dead+"/", map[string]any{"entity_type": "ISSUE_DESCRIPTION"})
		alice.Post(ap, map[string]any{})
		alice.Post(ap, map[string]any{"entity_type": "ISSUE_DESCRIPTION", "type": "text/plain"})
		alice.Post(ap, map[string]any{"entity_type": "ISSUE_DESCRIPTION", "size": []any{}})
		// PROJECT_COVER passes project_id twice: a TypeError.
		alice.Post(ap, map[string]any{"entity_type": "PROJECT_COVER", "entity_identifier": pl})
		desc(alice, "COMMENT_DESCRIPTION", dead, "c.png")
		desc(alice, "DRAFT_ISSUE_DESCRIPTION", dead, "d.png")
		desc(alice, "ISSUE_DESCRIPTION", "junk", "i.png")
		desc(carol, "DRAFT_ISSUE_DESCRIPTION", "", "draft.png")
		desc(carol, "WORKSPACE_LOGO", "", "logo.png")

		// An issue description image: upload, confirm, read.
		img := desc(bob, "ISSUE_DESCRIPTION", issue, "shot.png")
		imgID := img.String("asset_id")
		alice.Get(ap + imgID + "/")
		alice.Get(aw + "projects/" + pl + "/download/" + imgID + "/")
		assetUpload(s, "issue image", img, []byte("SHOT"), nil)
		dan.Patch(ap+imgID+"/", nil)
		alice.Patch(aw+"projects/"+ot+"/"+imgID+"/", nil)
		alice.Patch(ap+dead+"/", nil)
		carol.Patch(ap+imgID+"/", map[string]any{"attributes": map[string]any{"name": "shot 1.png", "type": "image/png"}})
		r := assetGet(carol, ap+imgID+"/")
		assetFetch(s, "issue image", r)
		r = assetGet(bob, aw+"projects/"+pl+"/download/"+imgID+"/")
		assetFetch(s, "issue image, download", r)
		dan.Get(aw + "projects/" + pl + "/download/" + imgID + "/")
		dan.Get(ap + imgID + "/")
		alice.Get(ap + dead + "/")
		// Description images aren't static.
		assetGet(s.Client("anon"), "/api/assets/v2/static/"+imgID+"/")

		// Bulk attaches the requester's own uploads to an entity.
		bulk := func(entity string) string { return ap + entity + "/bulk/" }
		loose := desc(bob, "ISSUE_DESCRIPTION", "", "loose.png").String("asset_id")
		dan.Post(bulk(issue), map[string]any{"asset_ids": []any{loose}})
		bob.Post(bulk(issue), map[string]any{})
		bob.Post(bulk(issue), map[string]any{"asset_ids": []any{}})
		bob.Post(bulk(issue), map[string]any{"asset_ids": []any{dead}})
		bob.Post(bulk(issue), map[string]any{"asset_ids": []any{"junk"}})
		bob.Post(bulk(issue), map[string]any{"asset_ids": 5})
		alice.Post(bulk(issue), map[string]any{"asset_ids": []any{loose}})
		// An entity that doesn't exist: the IntegrityError is swallowed.
		bob.Post(bulk(dead), map[string]any{"asset_ids": []any{loose}})
		bob.Post(bulk(issue), map[string]any{"asset_ids": []any{loose, imgID}})
		cdesc := desc(alice, "COMMENT_DESCRIPTION", "", "comment.png").String("asset_id")
		alice.Post(bulk(comment), map[string]any{"asset_ids": []any{cdesc}})
		pdesc := desc(alice, "PAGE_DESCRIPTION", "", "page.png").String("asset_id")
		alice.Post(bulk(dead), map[string]any{"asset_ids": []any{pdesc}})
		alice.Post(bulk(page), map[string]any{"asset_ids": []any{pdesc}})
		// Project covers uploaded before the project is known (project_id
		// NULL) are claimed by the bulk call; the oldest ends up the cover.
		c1 := alice.Post(aw, map[string]any{"entity_type": "PROJECT_COVER", "entity_identifier": "", "name": "c1.jpg"},
			assetUploadMask).String("asset_id")
		c2 := alice.Post(aw, map[string]any{"entity_type": "PROJECT_COVER", "entity_identifier": "", "name": "c2.jpg"},
			assetUploadMask).String("asset_id")
		alice.Post(aw+"projects/"+ot+"/"+ot+"/bulk/", map[string]any{"asset_ids": []any{imgID}})
		alice.Post(bulk(pl), map[string]any{"asset_ids": []any{c2, c1}})
		alice.Get(p, projMask)
		assetRows(s)
		assetEntityRows(s)

		// Duplicating a page copies the images its description uses (and
		// only those in the page's project), rewriting their ids.
		pimg := desc(alice, "PAGE_DESCRIPTION", page, "diagram.png")
		assetUpload(s, "page image", pimg, []byte("DIAGRAM"), nil)
		alice.Patch(ap+pimg.String("asset_id")+"/", nil)
		withImages := alice.Post(p+"pages/", map[string]any{"name": "Pictures", "description_html": `<p>a</p>` +
			`<image-component src="` + pimg.String("asset_id") + `"></image-component>` +
			`<image-component src="` + dead + `"></image-component>`}, pageIDs).String("id")
		copyID := alice.Post(p+"pages/"+withImages+"/duplicate/", nil, pageIDs).String("id")
		s.DBRows("pages", `SELECT name, description_html FROM pages ORDER BY name`)
		copied := s.DBStrings(`SELECT id::text FROM file_assets WHERE page_id = $1`, copyID)
		for _, id := range copied {
			r := assetGet(alice, ap+id+"/")
			assetFetch(s, "copied page image", r)
		}
		assetRows(s)
	})
}

func TestIssueAttachments(t *testing.T) {
	requireStorage(t)
	Run(t, "asset_attachments", func(s *Scenario) {
		alice, bob, carol, dan, outsider, pl := projectTeam(s, "43")
		const ws = "/api/workspaces/acme/"
		const aw = "/api/assets/v2/workspaces/acme/"
		p := ws + "projects/" + pl + "/"
		ids := Unordered("label_ids", "assignee_ids", "module_ids")
		issue := alice.Post(p+"issues/", map[string]any{"name": "Bug", "assignee_ids": []any{userID(alice)}}, ids).String("id")
		att := aw + "projects/" + pl + "/issues/" + issue + "/attachments/"
		// The attachment's asset is its key, which embeds uuid4().hex.
		attMask := Mask("upload_data.url", "upload_data.fields.policy", "upload_data.fields.x-amz-credential",
			"upload_data.fields.x-amz-date", "upload_data.fields.x-amz-signature", "upload_data.fields.key",
			"attachment.asset", "[].asset", "issue_attachments[].asset")

		// Who may create, then validation.
		dan.Post(att, map[string]any{"type": "text/plain"})
		outsider.Post(att, map[string]any{"type": "text/plain"})
		alice.Post(att, map[string]any{"name": "a.txt"})
		alice.Post(att, map[string]any{"type": "application/x-msdownload"})
		alice.Post(att, map[string]any{"type": "text/plain", "size": "big"})
		alice.Post(aw+"projects/"+pl+"/issues/"+dead+"/attachments/", map[string]any{"type": "text/plain"})

		// Bob attaches notes: create, upload, confirm (once: one activity).
		notes := bob.Post(att, map[string]any{"name": "notes.txt", "type": "text/plain", "size": 5}, attMask)
		nid := notes.String("asset_id")
		assetKeyStep(s, "attachment.asset", notes, "attachment.asset", nid)
		assetKeyStep(s, "upload_data.fields.key", notes, "upload_data.fields.key", nid)
		alice.Get(att)
		alice.Get(att + nid + "/")
		r := alice.Get(p+"issues/"+issue+"/?expand=issue_attachments", ids, attMask)
		assetKeyStep(s, "issue_attachments[0].asset", r, "issue_attachments.0.asset", nid)
		assetUpload(s, "notes", notes, []byte("hello"), nil)
		dan.Patch(att+nid+"/", nil)
		alice.Patch(att+dead+"/", nil)
		bob.Patch(att+nid+"/", nil)
		alice.Patch(att+nid+"/", nil)
		r = alice.Get(att, attMask)
		assetKeyStep(s, "[0].asset", r, "0.asset", nid)
		r = assetGet(carol, att+nid+"/")
		assetFetch(s, "notes", r)
		r = assetGet(carol, aw+"projects/"+pl+"/download/"+nid+"/")
		assetFetch(s, "notes, download", r)
		dan.Get(att + nid + "/")
		alice.Get(p+"issues/"+issue+"/?expand=issue_attachments", ids, attMask)
		// epoch is the wall clock; an attachment's new_value is its key.
		alice.Get(p+"issues/"+issue+"/history/", Mask("[].epoch", "[].new_value"))

		// A guest attaches a PDF; deleting is for its creator or an admin.
		pdf := carol.Post(att, map[string]any{"name": "spec.pdf", "type": "application/pdf", "size": 100}, attMask)
		pid := pdf.String("asset_id")
		assetUpload(s, "spec", pdf, []byte("%PDF-1.4"), nil)
		carol.Patch(att+pid+"/", nil)
		bob.Delete(att + pid + "/")
		dan.Delete(att + pid + "/")
		carol.Delete(att + nid + "/")
		alice.Delete(att + dead + "/")
		bob.Delete(att + nid + "/")
		bob.Delete(att + nid + "/")
		alice.Delete(att + pid + "/")
		alice.Get(att)
		alice.Get(p+"issues/"+issue+"/?expand=issue_attachments", ids, attMask)
		assetRows(s)
		attachmentRows(s)
	})
}

// assetKeyStep records whether a masked asset field (the stored key, which
// embeds uuid4().hex) is exactly the asset's key in the database.
func assetKeyStep(s *Scenario, label string, r *Response, path, id string) {
	s.t.Helper()
	key := s.DBStrings(`SELECT asset FROM file_assets WHERE id = $1`, id)[0]
	s.steps = append(s.steps, Step{Actor: "check", Method: "KEY", Path: label,
		Body: map[string]any{"is_stored_key": r.String(path) == key}})
}

// attachmentRows records the attachment activity and what it notifies.
// new_value is the attachment's key, its uuid4().hex replaced.
func attachmentRows(s *Scenario) {
	const nv = `regexp_replace(%s, '[0-9a-f]{32}', '<hex>', 'g')`
	s.DBRows("issue_activities", `SELECT i.name AS issue, a.verb, a.field, a.comment, a.old_value,
			`+fmt.Sprintf(nv, "a.new_value")+` AS new_value, a.old_identifier, a.new_identifier, u.email AS actor,
			a.epoch IS NOT NULL AS has_epoch, cb.email AS created_by, a.deleted_at IS NULL AS live
		FROM issue_activities a LEFT JOIN issues i ON i.id = a.issue_id LEFT JOIN users u ON u.id = a.actor_id
		LEFT JOIN users cb ON cb.id = a.created_by_id
		ORDER BY a.created_at`)
	s.DBRows("issue_subscribers", `SELECT i.name, u.email, cb.email AS created_by, ub.email AS updated_by
		FROM issue_subscribers x JOIN issues i ON i.id = x.issue_id JOIN users u ON u.id = x.subscriber_id
		LEFT JOIN users cb ON cb.id = x.created_by_id LEFT JOIN users ub ON ub.id = x.updated_by_id
		ORDER BY i.name, u.email`)
	s.DBRows("notifications", `SELECT r.email AS receiver, t.email AS triggered_by, n.sender, n.title, n.message,
			n.entity_name, i.name AS issue, n.data->'issue' AS issue_data,
			n.data->'issue_activity'->>'verb' AS verb, n.data->'issue_activity'->>'field' AS field,
			n.data->'issue_activity'->>'old_value' AS old_value,
			`+fmt.Sprintf(nv, "n.data->'issue_activity'->>'new_value'")+` AS new_value,
			n.data->'issue_activity'->>'new_identifier' AS new_identifier, n.message_html, n.read_at IS NULL AS unread
		FROM notifications n JOIN users r ON r.id = n.receiver_id LEFT JOIN users t ON t.id = n.triggered_by_id
		LEFT JOIN issues i ON i.id = n.entity_identifier
		ORDER BY r.email, n.sender, n.title, verb, field, new_value, t.email`)
	s.DBRows("email_notification_logs", `SELECT r.email AS receiver, t.email AS triggered_by, e.entity_name, e.entity,
			e.data->'issue' AS issue_data, e.data->'issue_activity'->>'verb' AS verb,
			e.data->'issue_activity'->>'field' AS field, e.data->'issue_activity'->>'old_value' AS old_value,
			`+fmt.Sprintf(nv, "e.data->'issue_activity'->>'new_value'")+` AS new_value,
			e.data->'issue_activity' ? 'activity_time' AS has_time, e.processed_at IS NULL AS pending
		FROM email_notification_logs e JOIN users r ON r.id = e.receiver_id LEFT JOIN users t ON t.id = e.triggered_by_id
		ORDER BY r.email, verb, field, new_value, t.email`)
}

func TestLegacyAssets(t *testing.T) {
	requireStorage(t)
	Run(t, "asset_legacy", func(s *Scenario) {
		alice, bob, _, _, outsider, _ := projectTeam(s, "44")
		anon := s.Client("anon")
		wsID := s.DBStrings(`SELECT id::text FROM workspaces WHERE slug = 'acme'`)[0]
		// Pre-v2 assets, keyed by "<workspace_id>/<name>" or a user key; no
		// endpoint creates them any more.
		s.DBStrings(`INSERT INTO file_assets (id, created_at, updated_at, attributes, asset, workspace_id, is_deleted,
				is_archived, size, is_uploaded, storage_metadata, created_by_id)
			SELECT gen_random_uuid(), now(), now(), '{}'::jsonb, $1 || '/old-logo.png', $1::uuid, false, false, 0, true, '{}'::jsonb,
				u.id FROM users u WHERE u.email = 'alice@example.com'
			UNION ALL
			SELECT gen_random_uuid(), now(), now(), '{}'::jsonb, 'user-old.png', NULL, false, false, 0, true, '{}'::jsonb, u.id
				FROM users u WHERE u.email = 'alice@example.com'
			RETURNING id::text`, wsID)
		legacy := "/api/workspaces/file-assets/" + wsID + "/"

		anon.Delete(legacy + "old-logo.png/")
		outsider.Delete(legacy + "old-logo.png/")
		bob.Delete(legacy + "missing.png/")
		bob.Delete(legacy + "old-logo.png/")
		bob.Delete(legacy + "old-logo.png/")
		bob.Delete("/api/workspaces/file-assets/" + dead + "/old-logo.png/")
		bob.Delete("/api/workspaces/file-assets/not-a-uuid/old-logo.png/")
		assetRows(s)
		outsider.Post(legacy+"old-logo.png/restore/", nil)
		alice.Post(legacy+"missing.png/restore/", nil)
		alice.Post(legacy+"old-logo.png/restore/", nil)

		anon.Delete("/api/users/file-assets/user-old.png/")
		bob.Delete("/api/users/file-assets/user-old.png/")
		alice.Delete("/api/users/file-assets/missing.png/")
		alice.Delete("/api/users/file-assets/user-old.png/")
		assetRows(s)
	})
}
