package api

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// PageViewSet (P/pages/...): list, create, retrieve, partial_update and
// destroy. The other page views are in page_action.go and
// page_description.go; the page tasks in page_task.go.

// Page.access values.
const (
	pagePublic  = 0
	pagePrivate = 1
)

// pageParamIDs parses the URL's <uuid:...> segments up front: Django's
// resolver answers a malformed one with a 404 before any permission runs.
func pageParamIDs(c *httpx.Ctx) (project uuid.UUID, page *uuid.UUID, err error) {
	if project, err = c.UUIDParam("project_id"); err != nil {
		return
	}
	for _, name := range []string{"page_id", "pk"} {
		if c.Param(name) == "" {
			continue
		}
		id, perr := c.UUIDParam(name)
		if perr != nil {
			return project, nil, perr
		}
		if name == "page_id" {
			page = &id
		}
	}
	return
}

// pagePerm is ProjectPagePermission: an active project member; for a URL
// page (live, linked to this project) its owner passes and only its owner
// passes a private one; otherwise the role must allow the method (POST,
// PUT and PATCH admins and members, DELETE admins, reads anyone).
func (a *API) pagePerm(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		projectID, pageID, err := pageParamIDs(c)
		if err != nil {
			return err
		}
		ctx := c.Context()
		slug := c.Param("slug")
		role, err := a.projectRole(ctx, slug, projectID, c.User.ID)
		if err != nil {
			return err
		}
		if role == 0 {
			return httpx.ErrForbidden
		}
		if pageID != nil {
			var (
				owner  uuid.UUID
				access int
			)
			err := a.db.QueryRow(ctx, `SELECT pg.owned_by_id, pg.access FROM pages pg
				JOIN project_pages pp ON pp.page_id = pg.id JOIN workspaces w ON w.id = pg.workspace_id
				WHERE pg.deleted_at IS NULL AND pg.id = $1 AND pp.deleted_at IS NULL AND pp.project_id = $2 AND w.slug = $3
				ORDER BY pg.created_at DESC LIMIT 1`, *pageID, projectID, slug).Scan(&owner, &access)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrForbidden
			}
			if err != nil {
				return err
			}
			if owner == c.User.ID {
				return h(c)
			}
			if access == pagePrivate {
				return httpx.ErrForbidden
			}
		}
		var allowed []int
		switch c.R.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			allowed = anyRole
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			allowed = adminMember
		case http.MethodDelete:
			allowed = adminOnly
		}
		if !slices.Contains(allowed, role) {
			return httpx.ErrForbidden
		}
		return h(c)
	}
}

// pageItem is PageSerializer; with Description set, PageDetailSerializer.
// Fields the instance lacks are left out, as DRF skips a non-required
// field whose attribute is missing: is_favorite and label_ids exist only
// on the annotated queryset, project_ids on it and on duplicate's.
type pageItem struct {
	ID          uuid.UUID       `json:"id"`
	Name        string          `json:"name"`
	OwnedBy     uuid.UUID       `json:"owned_by"`
	Access      int             `json:"access"`
	Color       string          `json:"color"`
	Parent      *uuid.UUID      `json:"parent"`
	IsFavorite  *bool           `json:"is_favorite,omitzero"`
	IsLocked    bool            `json:"is_locked"`
	ArchivedAt  *httpx.Date     `json:"archived_at"`
	Workspace   uuid.UUID       `json:"workspace"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	CreatedBy   *uuid.UUID      `json:"created_by"`
	UpdatedBy   *uuid.UUID      `json:"updated_by"`
	ViewProps   jsontext.Value  `json:"view_props"`
	LogoProps   jsontext.Value  `json:"logo_props"`
	LabelIDs    *[]uuid.UUID    `json:"label_ids,omitzero"`
	ProjectIDs  *[]uuid.UUID    `json:"project_ids,omitzero"`
	Description *string         `json:"description_html,omitzero"`
	IssueIDs    *[]*uuid.UUID   `json:"issue_ids,omitzero"`
	extra       pageItemPrivate `json:"-"`
}

// pageItemPrivate holds the loaded row's other columns, for save().
type pageItemPrivate struct {
	html string
}

// pageItemCols are the columns scanPageItem reads, from pages pg.
const pageItemCols = `pg.id, pg.name, pg.owned_by_id, pg.access, pg.color, pg.parent_id, pg.is_locked, pg.archived_at,
	pg.workspace_id, pg.created_at, pg.updated_at, pg.created_by_id, pg.updated_by_id, pg.view_props::text,
	pg.logo_props::text, pg.description_html`

func scanPageItem(row pgx.Row, extra ...any) (*pageItem, error) {
	var (
		p           pageItem
		archived    *time.Time
		view, logo  string
		description string
	)
	dest := []any{&p.ID, &p.Name, &p.OwnedBy, &p.Access, &p.Color, &p.Parent, &p.IsLocked, &archived, &p.Workspace,
		&p.CreatedAt, &p.UpdatedAt, &p.CreatedBy, &p.UpdatedBy, &view, &logo, &description}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	if archived != nil {
		p.ArchivedAt = &httpx.Date{Time: *archived}
	}
	p.ViewProps, p.LogoProps = jsontext.Value(view), jsontext.Value(logo)
	p.extra.html = description
	return &p, nil
}

// pageQuery is PageViewSet.get_queryset (with SearchFilter): top-level
// pages of the URL project that the user owns or that are public, reached
// through a project the user is an active member of. The aggregates run
// over the joins the filters made, so project_ids only lists such
// projects and label_ids keeps soft-deleted page labels.
type pageQuery struct {
	slug      string
	project   uuid.UUID
	user      uuid.UUID
	search    []string
	ownedOnly bool       // the guest restriction of list()
	pk        *uuid.UUID // .filter(pk=...) / .get(pk=...)
	orderBy   string     // the sanitized ?order_by=
}

func (a *API) pageQuerySet(ctx context.Context, q pageQuery) ([]*pageItem, error) {
	args := []any{q.slug, q.user, q.project}
	where := ""
	for _, term := range q.search {
		args = append(args, "%"+escapeLike(term)+"%")
		where += ` AND UPPER(pg.name::text) LIKE UPPER($` + itoa(len(args)) + `)`
	}
	if q.ownedOnly {
		where += ` AND pg.owned_by_id = $2`
	}
	if q.pk != nil {
		args = append(args, *q.pk)
		where += ` AND pg.id = $` + itoa(len(args))
	}
	field, desc := sanitizeOrderBy(q.orderBy, []string{"created_at", "updated_at", "name", "sort_order"}, "-created_at")
	order := "pg." + field
	if desc {
		order += " DESC"
	}
	rows, err := a.db.Query(ctx, `SELECT `+pageItemCols+`,
			EXISTS (SELECT 1 FROM user_favorites f JOIN workspaces fw ON fw.id = f.workspace_id
				WHERE f.deleted_at IS NULL AND f.entity_identifier = pg.id AND f.entity_type = 'page'
					AND f.user_id = $2 AND fw.slug = $1) AS is_favorite,
			COALESCE(array_agg(DISTINCT pl.label_id) FILTER (WHERE pl.label_id IS NOT NULL), '{}') AS label_ids,
			COALESCE(array_agg(DISTINCT pp.project_id) FILTER (WHERE NOT (pp.project_id = '00000000-0000-0000-0000-000000000001')), '{}') AS project_ids
		FROM pages pg JOIN workspaces w ON w.id = pg.workspace_id
		JOIN project_pages pp ON pp.page_id = pg.id JOIN projects pr ON pr.id = pp.project_id
		JOIN project_members pm ON pm.project_id = pr.id
		LEFT JOIN page_labels pl ON pl.page_id = pg.id
		WHERE pg.deleted_at IS NULL AND w.slug = $1 AND pr.archived_at IS NULL AND pm.is_active AND pm.member_id = $2
			AND pg.parent_id IS NULL AND (pg.owned_by_id = $2 OR pg.access = 0)
			AND EXISTS (SELECT 1 FROM project_pages x WHERE x.deleted_at IS NULL AND x.page_id = pg.id AND x.project_id = $3)`+where+`
		GROUP BY pg.id
		ORDER BY is_favorite DESC, `+order+`, pg.id`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*pageItem, error) {
		var (
			fav                bool
			labels, projectIDs []uuid.UUID
		)
		p, err := scanPageItem(row, &fav, &labels, &projectIDs)
		if err != nil {
			return nil, err
		}
		p.IsFavorite, p.LabelIDs, p.ProjectIDs = &fav, &labels, &projectIDs
		return p, nil
	})
}

// pageGuestRestricted is list()/retrieve()'s guest check: an active guest
// of a project without guest_view_all_features.
func (a *API) pageGuestRestricted(ctx context.Context, slug string, projectID, user uuid.UUID) (bool, error) {
	var guestViewAll bool
	if err := a.db.QueryRow(ctx, `SELECT guest_view_all_features FROM projects WHERE id = $1 AND deleted_at IS NULL`,
		projectID).Scan(&guestViewAll); err != nil {
		return false, err
	}
	var guest bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE pm.deleted_at IS NULL AND pm.is_active AND pm.member_id = $1 AND pm.project_id = $2 AND pm.role = 5
			AND w.slug = $3)`, user, projectID, slug).Scan(&guest)
	return guest && !guestViewAll, err
}

// listPages ports PageViewSet.list.
func (a *API) listPages(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, _, _ := pageParamIDs(c)
	search, err := drf.SearchTerms(c.Query("search"))
	if err != nil {
		return err
	}
	q := pageQuery{slug: c.Param("slug"), project: projectID, user: c.User.ID, search: search, orderBy: c.Query("order_by")}
	if q.ownedOnly, err = a.pageGuestRestricted(ctx, q.slug, projectID, c.User.ID); err != nil {
		return err
	}
	pages, err := a.pageQuerySet(ctx, q)
	if err != nil {
		return err
	}
	if pages == nil {
		pages = []*pageItem{}
	}
	return c.JSON(http.StatusOK, pages)
}

// getPage ports PageViewSet.retrieve; apps/live calls it too.
func (a *API) getPage(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, pageID, _ := pageParamIDs(c)
	slug := c.Param("slug")
	search, err := drf.SearchTerms(c.Query("search"))
	if err != nil {
		return err
	}
	pages, err := a.pageQuerySet(ctx, pageQuery{slug: slug, project: projectID, user: c.User.ID, search: search,
		pk: pageID, orderBy: c.Query("order_by")})
	if err != nil {
		return err
	}
	restricted, err := a.pageGuestRestricted(ctx, slug, projectID, c.User.ID)
	if err != nil {
		return err
	}
	if restricted {
		if len(pages) == 0 {
			return errViewCrash // page.owned_by on None
		}
		if pages[0].OwnedBy != c.User.ID {
			return httpx.Err(http.StatusBadRequest, "You are not allowed to view this page")
		}
	}
	if len(pages) == 0 {
		return httpx.Err(http.StatusNotFound, "Page not found")
	}
	p := pages[0]
	p.Description = &p.extra.html
	rows, err := a.db.Query(ctx, `SELECT entity_identifier FROM page_logs
		WHERE deleted_at IS NULL AND entity_name = 'issue' AND page_id = $1 ORDER BY created_at DESC`, *pageID)
	if err != nil {
		return err
	}
	issueIDs, err := pgx.CollectRows(rows, pgx.RowTo[*uuid.UUID])
	if err != nil {
		return err
	}
	if issueIDs == nil {
		issueIDs = []*uuid.UUID{}
	}
	p.IssueIDs = &issueIDs
	track := c.Query("track_visit")
	if !c.HasQuery("track_visit") {
		track = "true"
	}
	if strings.ToLower(track) == "true" {
		a.recordVisit(ctx, slug, "page", *pageID, c.User.ID, &projectID)
	}
	return c.JSON(http.StatusOK, p)
}

// pageInput is a validated PageSerializer / PageDetailSerializer.
type pageInput struct {
	set        setList
	labels     *[]uuid.UUID // the write-only labels
	labelIDs   *[]uuid.UUID // label_ids and project_ids: plain list fields
	projectIDs *[]uuid.UUID
	html       *string // PageDetailSerializer's description_html
}

// validatePage runs PageSerializer (detail: PageDetailSerializer) over
// request.data, absent fields skipped.
func (a *API) validatePage(ctx context.Context, c *httpx.Ctx, data *drf.Data, detail bool) (*pageInput, error) {
	v := drf.NewValidator(data, c.Loc())
	in := &pageInput{}
	if s, ok := v.Char("name", drf.CharField{AllowBlank: true}); ok {
		in.set.add("name", *s)
	}
	if s, ok := v.Choice("access", []string{"0", "1"}, drf.ChoiceField{}); ok {
		in.set.add("access", map[string]int{"0": 0, "1": 1}[*s])
	}
	if s, ok := v.Char("color", drf.CharField{MaxLength: 255, AllowBlank: true}); ok {
		in.set.add("color", *s)
	}
	labels, ok, err := v.PKList("labels", func(id uuid.UUID) (bool, error) {
		var ok bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM labels WHERE id = $1 AND deleted_at IS NULL)`, id).Scan(&ok)
		return ok, err
	})
	if err != nil {
		return nil, err
	}
	if ok {
		in.labels = &labels
	}
	parent, ok, err := v.PK("parent", true, func(id uuid.UUID) (bool, error) {
		var ok bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pages WHERE id = $1 AND deleted_at IS NULL)`, id).Scan(&ok)
		return ok, err
	})
	if err != nil {
		return nil, err
	}
	if ok {
		in.set.add("parent_id", parent)
	}
	if b, ok := v.Bool("is_locked"); ok {
		in.set.add("is_locked", b)
	}
	if d, ok := v.Date("archived_at", true); ok {
		in.set.add("archived_at", d)
	}
	if err := a.auditFields(ctx, v, &in.set); err != nil {
		return nil, err
	}
	if raw, ok := v.JSON("view_props", false); ok {
		in.set.addCast("view_props", string(raw), "::jsonb")
	}
	if raw, ok := v.JSON("logo_props", false); ok {
		in.set.addCast("logo_props", string(raw), "::jsonb")
	}
	if ids, ok := v.UUIDList("label_ids"); ok {
		in.labelIDs = &ids
	}
	if ids, ok := v.UUIDList("project_ids"); ok {
		in.projectIDs = &ids
	}
	if detail {
		if s, ok := v.Char("description_html", drf.CharField{}); ok {
			in.html = s
			in.set.add("description_html", *s)
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// pageStripped is Page.save()'s description_stripped for html; a parser
// failure is the view's 500.
func pageStripped(html string) (*string, error) {
	s, err := pageStripTags(html)
	if err != nil {
		return nil, errViewCrash
	}
	return s, nil
}

// createPage ports PageViewSet.create: PageSerializer.create (the Page,
// its ProjectPage and PageLabels), page_transaction, then the page read
// back through get_queryset.
func (a *API) createPage(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, _, _ := pageParamIDs(c)
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get on a list
	}
	in, err := a.validatePage(ctx, c, data, false)
	if err != nil {
		return err
	}
	if in.labelIDs != nil || in.projectIDs != nil {
		return errViewCrash // Page(label_ids=...): an unexpected keyword
	}
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT workspace_id FROM projects WHERE id = $1 AND deleted_at IS NULL`,
		projectID).Scan(&workspaceID); err != nil {
		return err
	}
	// The description comes from request.data unvalidated.
	html := "<p></p>"
	var htmlArg any = html
	if v, ok := data.Get("description_html"); ok {
		switch {
		case v.IsNull():
			htmlArg = nil
			html = ""
		case v.IsString():
			html = v.Str()
			htmlArg = html
		default:
			return errViewCrash // strip_tags of a non-string
		}
	}
	if v, ok := data.Get("description_binary"); ok && !v.IsNull() {
		return errViewCrash // BinaryField: "bytes or buffer expected"
	}
	descJSON := "{}"
	var jsonArg any = descJSON
	if v, ok := data.Get("description_json"); ok {
		if v.IsNull() {
			jsonArg = nil
		} else {
			jsonArg = string(v.Raw())
		}
	}
	stripped, err := pageStripped(html)
	if err != nil {
		return err
	}
	set := in.set
	set.drop("created_by_id") // save() sets created_by to the requester
	set.drop("updated_by_id")
	set.addCast("description_json", jsonArg, "::jsonb")
	set.add("description_html", htmlArg)
	set.add("description_stripped", stripped)
	set.add("owned_by_id", c.User.ID)
	set.add("workspace_id", workspaceID)
	set.add("created_by_id", c.User.ID)
	var id uuid.UUID
	err = pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, set.insertSQL("pages"), set.args...).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_pages (workspace_id, project_id, page_id, created_by_id)
			VALUES ($1, $2, $3, $4)`, workspaceID, projectID, id, c.User.ID); err != nil {
			return err
		}
		if in.labels != nil {
			return a.insertPageLabels(ctx, tx, id, *in.labels, workspaceID, &c.User.ID, nil)
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.enqueuePageTransaction(ctx, pageTransactionJob{NewHTML: json.RawMessage(pageHTMLArg(data, "<p></p>")), PageID: id})
	search, err := drf.SearchTerms(c.Query("search"))
	if err != nil {
		return err
	}
	pages, err := a.pageQuerySet(ctx, pageQuery{slug: c.Param("slug"), project: projectID, user: c.User.ID,
		search: search, pk: &id, orderBy: c.Query("order_by")})
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		return pgx.ErrNoRows
	}
	pages[0].Description = &pages[0].extra.html
	return c.JSON(http.StatusCreated, pages[0])
}

// insertPageLabels is PageLabel.objects.bulk_create: one statement, the
// audit columns as given.
func (a *API) insertPageLabels(ctx context.Context, tx pgx.Tx, pageID uuid.UUID, labels []uuid.UUID, workspaceID uuid.UUID,
	createdBy, updatedBy *uuid.UUID) error {
	if len(labels) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO page_labels (label_id, page_id, workspace_id, created_by_id, updated_by_id,
			created_at, updated_at)
		SELECT l, $2, $3, $4, $5, clock_timestamp(), clock_timestamp() FROM unnest($1::uuid[]) WITH ORDINALITY AS t(l, n)
		ORDER BY n`, labels, pageID, workspaceID, createdBy, updatedBy)
	return err
}

// pageHTMLArg is request.data.get("description_html", def) as the task
// receives it: any JSON value.
func pageHTMLArg(data *drf.Data, def string) jsontext.Value {
	if v, ok := data.Get("description_html"); ok {
		return v.Raw()
	}
	return jsontext.Value(jsonString(def))
}

// pageObject is Page.objects.get(pk=page_id, workspace__slug=slug,
// projects__id=project_id, project_pages__deleted_at__isnull=True).
type pageObject struct {
	id, workspace, owner uuid.UUID
	access               int
	locked               bool
	archived             *time.Time
	parent               *uuid.UUID
	html                 string
	createdBy, updatedBy *uuid.UUID
}

func (a *API) loadPageObject(ctx context.Context, slug string, projectID, pageID uuid.UUID, extraWhere string,
	extraArgs ...any) (*pageObject, error) {
	var p pageObject
	err := a.db.QueryRow(ctx, `SELECT pg.id, pg.workspace_id, pg.owned_by_id, pg.access, pg.is_locked, pg.archived_at,
			pg.parent_id, pg.description_html, pg.created_by_id, pg.updated_by_id
		FROM pages pg JOIN project_pages pp ON pp.page_id = pg.id JOIN workspaces w ON w.id = pg.workspace_id
		WHERE pg.deleted_at IS NULL AND pg.id = $1 AND pp.deleted_at IS NULL AND pp.project_id = $2 AND w.slug = $3`+extraWhere,
		append([]any{pageID, projectID, slug}, extraArgs...)...).Scan(&p.id, &p.workspace, &p.owner, &p.access, &p.locked,
		&p.archived, &p.parent, &p.html, &p.createdBy, &p.updatedBy)
	return &p, err
}

func (a *API) pageFromURL(c *httpx.Ctx) (*pageObject, error) {
	projectID, pageID, _ := pageParamIDs(c)
	return a.loadPageObject(c.Context(), c.Param("slug"), projectID, *pageID, "")
}

// pageSave is Page.save() on a loaded page: the given columns plus
// auto_now, the requester as updated_by and description_stripped
// recomputed from the (new) html.
func (a *API) pageSave(ctx context.Context, q db.Querier, id uuid.UUID, html string, user *uuid.UUID, set setList) error {
	stripped, err := pageStripped(html)
	if err != nil {
		return err
	}
	set.add("description_stripped", stripped)
	set.add("updated_by_id", user)
	_, err = q.Exec(ctx, `UPDATE pages SET `+set.sql()+`, updated_at = now() WHERE id = $1`, append([]any{id}, set.args...)...)
	return err
}

// pyEqualsInt is Python's n == value for a JSON value.
func pyEqualsInt(n int, v drf.Value) bool {
	switch v.Kind() {
	case 't':
		return n == 1
	case 'f':
		return n == 0
	case '0':
		f, ok := new(big.Float).SetString(string(v.Raw()))
		return ok && f.Cmp(big.NewFloat(float64(n))) == 0
	}
	return false
}

// updatePage ports PageViewSet.partial_update; apps/live renames pages
// through it.
func (a *API) updatePage(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, pageID, _ := pageParamIDs(c)
	slug := c.Param("slug")
	ownerErr := httpx.Err(http.StatusBadRequest, "Access cannot be updated since this page is owned by someone else")
	page, err := a.loadPageObject(ctx, slug, projectID, *pageID, "")
	if errors.Is(err, pgx.ErrNoRows) {
		return ownerErr
	}
	if err != nil {
		return err
	}
	if page.locked {
		return httpx.Err(http.StatusBadRequest, "Page is locked")
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	if v, ok := data.Get("parent"); ok && drf.PyTruthy(v) {
		id, ok := drf.UUIDValue(v)
		if !ok {
			return errFilterDetail
		}
		if _, err := a.loadPageObject(ctx, slug, projectID, id, ""); errors.Is(err, pgx.ErrNoRows) {
			return ownerErr
		} else if err != nil {
			return err
		}
	}
	if v, ok := data.Get("access"); ok && !pyEqualsInt(page.access, v) && page.owner != c.User.ID {
		return ownerErr
	}
	in, err := a.validatePage(ctx, c, data, true)
	if err != nil {
		return err
	}
	html := page.html
	if in.html != nil {
		html = *in.html
	}
	set := in.set
	set.drop("updated_by_id")
	err = pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		if in.labels != nil {
			if _, err := tx.Exec(ctx, `UPDATE page_labels SET deleted_at = now() WHERE page_id = $1 AND deleted_at IS NULL`,
				page.id); err != nil {
				return err
			}
			if err := a.insertPageLabels(ctx, tx, page.id, *in.labels, page.workspace, page.createdBy, page.updatedBy); err != nil {
				return err
			}
		}
		return a.pageSave(ctx, tx, page.id, html, &c.User.ID, set)
	})
	if err != nil {
		return err
	}
	if v, ok := data.Get("description_html"); ok && drf.PyTruthy(v) {
		old := page.html
		a.enqueuePageTransaction(ctx, pageTransactionJob{NewHTML: json.RawMessage(v.Raw()), OldHTML: &old, PageID: page.id})
	}
	out, err := scanPageItem(a.db.QueryRow(ctx, `SELECT `+pageItemCols+` FROM pages pg WHERE pg.id = $1`, page.id))
	if err != nil {
		return err
	}
	out.Description = &out.extra.html
	out.LabelIDs, out.ProjectIDs = in.labelIDs, in.projectIDs
	return c.JSON(http.StatusOK, out)
}

// deletePage ports PageViewSet.destroy: archived pages only, by their
// owner or a project admin. Children in the project are cut loose first.
func (a *API) deletePage(c *httpx.Ctx) error {
	ctx := c.Context()
	projectID, _, _ := pageParamIDs(c)
	slug := c.Param("slug")
	page, err := a.pageFromURL(c)
	if err != nil {
		return err
	}
	if page.archived == nil {
		return httpx.Err(http.StatusBadRequest, "The page should be archived before deleting")
	}
	if page.owner != c.User.ID {
		var admin bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
			WHERE pm.deleted_at IS NULL AND w.slug = $1 AND pm.member_id = $2 AND pm.role = 20 AND pm.project_id = $3
				AND pm.is_active)`, slug, c.User.ID, projectID).Scan(&admin); err != nil {
			return err
		}
		if !admin {
			return httpx.Err(http.StatusForbidden, "Only admin or owner can delete the page")
		}
	}
	err = pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pages SET parent_id = NULL WHERE id IN (
				SELECT pg.id FROM pages pg JOIN project_pages pp ON pp.page_id = pg.id JOIN workspaces w ON w.id = pg.workspace_id
				WHERE pg.deleted_at IS NULL AND pg.parent_id = $1 AND pp.project_id = $2 AND w.slug = $3
					AND pp.deleted_at IS NULL)`, page.id, projectID, slug); err != nil {
			return err
		}
		// page.delete(): save(), then the soft-delete cascade.
		if err := softdelete.Row(ctx, tx, "pages", page.id, c.User.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE user_favorites f SET deleted_at = now() FROM workspaces w
			WHERE w.id = f.workspace_id AND f.deleted_at IS NULL AND f.project_id = $1 AND w.slug = $2
				AND f.entity_identifier = $3 AND f.entity_type = 'page'`, projectID, slug, page.id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM user_recent_visits v USING workspaces w
			WHERE w.id = v.workspace_id AND v.deleted_at IS NULL AND v.project_id = $1 AND w.slug = $2
				AND v.entity_identifier = $3 AND v.entity_name = 'page'`, projectID, slug, page.id)
		return err
	})
	if err != nil {
		return err
	}
	return c.NoContent()
}
