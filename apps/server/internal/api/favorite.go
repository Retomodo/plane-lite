package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// favoriteProjectLite is ProjectFavoriteLiteSerializer.
type favoriteProjectLite struct {
	ID        uuid.UUID      `json:"id"`
	Name      string         `json:"name"`
	LogoProps jsontext.Value `json:"logo_props"`
}

// favoriteEntityLite is the Page, Cycle, Module and View lite serializers
// (a page's project_id is its first project, if any).
type favoriteEntityLite struct {
	ID        uuid.UUID      `json:"id"`
	Name      string         `json:"name"`
	LogoProps jsontext.Value `json:"logo_props"`
	ProjectID *uuid.UUID     `json:"project_id"`
}

// favoriteEntityData ports get_entity_model_and_serializer plus the
// serializers' get_entity_data: the entity's lite form, or nil when the type
// has no serializer, the id is NULL or the row is gone (objects.get skips
// soft-deleted rows, archived ones are found).
func (a *API) favoriteEntityData(ctx context.Context, entityType string, id *uuid.UUID) (any, error) {
	if id == nil {
		return nil, nil
	}
	var (
		table string
		lite  favoriteEntityLite
		logo  string
		err   error
	)
	switch entityType {
	case "project":
		var p favoriteProjectLite
		err = a.db.QueryRow(ctx, `SELECT id, name, logo_props::text FROM projects WHERE id = $1 AND deleted_at IS NULL`, *id).
			Scan(&p.ID, &p.Name, &logo)
		p.LogoProps = jsontext.Value(logo)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return p, nil
	case "cycle":
		table = "cycles"
	case "module":
		table = "modules"
	case "view":
		table = "issue_views"
	case "page":
		err = a.db.QueryRow(ctx, `SELECT id, name, logo_props::text FROM pages WHERE id = $1 AND deleted_at IS NULL`, *id).
			Scan(&lite.ID, &lite.Name, &logo)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		// obj.projects.first(): the newest live project among the page's
		// project_pages rows, whatever their own deleted_at.
		err = a.db.QueryRow(ctx, `SELECT p.id FROM projects p JOIN project_pages pp ON pp.project_id = p.id
			WHERE p.deleted_at IS NULL AND pp.page_id = $1 ORDER BY p.created_at DESC LIMIT 1`, *id).Scan(&lite.ProjectID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		lite.LogoProps = jsontext.Value(logo)
		return lite, nil
	default:
		return nil, nil
	}
	err = a.db.QueryRow(ctx, `SELECT id, name, logo_props::text, project_id FROM `+table+` WHERE id = $1 AND deleted_at IS NULL`, *id).
		Scan(&lite.ID, &lite.Name, &logo, &lite.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lite.LogoProps = jsontext.Value(logo)
	return lite, nil
}

// favoriteRow is a user_favorites row.
type favoriteRow struct {
	ID               uuid.UUID
	EntityType       string
	EntityIdentifier *uuid.UUID
	Name             *string
	IsFolder         bool
	Sequence         float64
	Parent           *uuid.UUID
	WorkspaceID      uuid.UUID
	ProjectID        *uuid.UUID
}

const favoriteCols = `f.id, f.entity_type, f.entity_identifier, f.name, f.is_folder, f.sequence, f.parent_id, f.workspace_id, f.project_id`

func scanFavorite(row pgx.Row) (*favoriteRow, error) {
	var r favoriteRow
	err := row.Scan(&r.ID, &r.EntityType, &r.EntityIdentifier, &r.Name, &r.IsFolder, &r.Sequence, &r.Parent, &r.WorkspaceID, &r.ProjectID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// favoriteOut is UserFavoriteSerializer's output. project_id is a plain
// read-only attribute, so a favorite just created echoes the raw value the
// request sent (projectEcho).
type favoriteOut struct {
	ID               uuid.UUID  `json:"id"`
	EntityType       string     `json:"entity_type"`
	EntityIdentifier *uuid.UUID `json:"entity_identifier"`
	EntityData       any        `json:"entity_data"`
	Name             *string    `json:"name"`
	IsFolder         bool       `json:"is_folder"`
	Sequence         float64    `json:"sequence"`
	Parent           *uuid.UUID `json:"parent"`
	WorkspaceID      uuid.UUID  `json:"workspace_id"`
	ProjectID        any        `json:"project_id"`
}

func (a *API) favoriteOut(ctx context.Context, r *favoriteRow, projectEcho any) (favoriteOut, error) {
	data, err := a.favoriteEntityData(ctx, r.EntityType, r.EntityIdentifier)
	if err != nil {
		return favoriteOut{}, err
	}
	out := favoriteOut{ID: r.ID, EntityType: r.EntityType, EntityIdentifier: r.EntityIdentifier, EntityData: data,
		Name: r.Name, IsFolder: r.IsFolder, Sequence: r.Sequence, Parent: r.Parent, WorkspaceID: r.WorkspaceID}
	switch {
	case projectEcho != nil:
		out.ProjectID = projectEcho
	case r.ProjectID != nil:
		out.ProjectID = *r.ProjectID
	}
	return out, nil
}

func (a *API) favoriteOuts(ctx context.Context, rows []*favoriteRow) ([]favoriteOut, error) {
	outs := make([]favoriteOut, 0, len(rows))
	for _, r := range rows {
		o, err := a.favoriteOut(ctx, r, nil)
		if err != nil {
			return nil, err
		}
		outs = append(outs, o)
	}
	return outs, nil
}

func (a *API) collectFavorites(rows pgx.Rows, err error) ([]*favoriteRow, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*favoriteRow, error) { return scanFavorite(row) })
}

// favoriteMemberJoin is the part of the favorites querysets that joins the
// project's members. It is a plain join, so every membership row counts,
// soft-deleted ones included, and a favorite is repeated once per match.
const favoriteMemberJoin = `
	JOIN workspaces w ON w.id = f.workspace_id
	LEFT JOIN projects p ON p.id = f.project_id
	LEFT JOIN project_members pm ON pm.project_id = p.id`

// listFavorites ports WorkspaceFavoriteEndpoint.get.
func (a *API) listFavorites(c *httpx.Ctx) error {
	ctx := c.Context()
	rows, err := a.collectFavorites(a.db.Query(ctx, `SELECT `+favoriteCols+` FROM user_favorites f`+favoriteMemberJoin+`
		WHERE f.deleted_at IS NULL AND f.parent_id IS NULL AND f.user_id = $1 AND w.slug = $2
			AND ((f.project_id IS NULL AND NOT (f.entity_type = 'page'))
				OR (f.project_id IS NOT NULL AND pm.member_id = $1 AND pm.is_active))
		ORDER BY f.created_at DESC`, c.User.ID, c.Param("slug")))
	if err != nil {
		return err
	}
	outs, err := a.favoriteOuts(ctx, rows)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, outs)
}

// groupFavorites ports WorkspaceFavoriteGroupEndpoint.get.
func (a *API) groupFavorites(c *httpx.Ctx) error {
	ctx := c.Context()
	parent, err := c.UUIDParam("favorite_id")
	if err != nil {
		return err
	}
	rows, err := a.collectFavorites(a.db.Query(ctx, `SELECT `+favoriteCols+` FROM user_favorites f`+favoriteMemberJoin+`
		WHERE f.deleted_at IS NULL AND f.parent_id = $3 AND f.user_id = $1 AND w.slug = $2
			AND (f.project_id IS NULL OR (f.project_id IS NOT NULL AND pm.member_id = $1 AND pm.is_active))
		ORDER BY f.created_at DESC`, c.User.ID, c.Param("slug"), parent))
	if err != nil {
		return err
	}
	outs, err := a.favoriteOuts(ctx, rows)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, outs)
}

// favoriteNew is what UserFavorite(...) is built from. A UUID model field
// converts what it is given only when it is saved, so the raw request values
// travel along (ProjectRaw, EntityRaw) and fail there, not before.
type favoriteNew struct {
	EntityType  string
	EntityRaw   *drf.Value // request value of entity_identifier; nil when EntityID is used
	EntityID    *uuid.UUID
	Name        *string
	IsFolder    bool
	Sequence    float64
	Parent      *uuid.UUID
	ProjectID   *uuid.UUID // a project id that needs no conversion
	ProjectRaw  *drf.Value // request value of project_id (replaces ProjectID when set)
	WorkspaceID *uuid.UUID // the workspace set on the instance; nil when none was
}

// favoriteUUID is a UUIDField's to_python on a raw request value: NULL for
// None, a ValidationError (400 "Please provide valid detail") otherwise.
func favoriteUUID(v *drf.Value) (*uuid.UUID, error) {
	if v == nil || v.IsNull() {
		return nil, nil
	}
	id, ok := drf.UUIDValue(*v)
	if !ok {
		return nil, errFilterDetail
	}
	return &id, nil
}

// insertFavorite ports UserFavorite.save() for a new favorite (and
// WorkspaceBaseModel.save): the workspace comes from the project; the
// sequence is 10000 past the largest one among the workspace's live
// favorites, of every user. It returns the stored row and, for a project
// given as a string, that string.
func (a *API) insertFavorite(ctx context.Context, user uuid.UUID, in favoriteNew) (*favoriteRow, error) {
	projectID := in.ProjectID
	var err error
	if in.ProjectRaw != nil {
		if projectID, err = favoriteUUID(in.ProjectRaw); err != nil {
			return nil, err
		}
	}
	workspaceID := in.WorkspaceID
	if projectID != nil {
		// self.project goes through the base manager: soft-deleted projects
		// are found.
		var ws uuid.UUID
		if err := a.db.QueryRow(ctx, `SELECT workspace_id FROM projects WHERE id = $1`, *projectID).Scan(&ws); err != nil {
			return nil, err
		}
		workspaceID = &ws
	}
	if workspaceID == nil {
		return nil, pgx.ErrNoRows // RelatedObjectDoesNotExist: no workspace set
	}
	var largest *float64
	if err := a.db.QueryRow(ctx, `SELECT max(sequence) FROM user_favorites WHERE deleted_at IS NULL AND workspace_id = $1`,
		*workspaceID).Scan(&largest); err != nil {
		return nil, err
	}
	sequence := in.Sequence
	if largest != nil {
		sequence = *largest + 10000
	}
	entityID := in.EntityID
	if in.EntityRaw != nil {
		if entityID, err = favoriteUUID(in.EntityRaw); err != nil {
			return nil, err
		}
	}
	return scanFavorite(a.db.QueryRow(ctx, `
		WITH f AS (
			INSERT INTO user_favorites (id, created_at, updated_at, created_by_id, user_id, workspace_id, project_id, entity_type,
				entity_identifier, name, is_folder, sequence, parent_id)
			VALUES (gen_random_uuid(), now(), now(), $1, $1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING *)
		SELECT `+favoriteCols+` FROM f`, user, *workspaceID, projectID, in.EntityType, entityID, in.Name, in.IsFolder, sequence, in.Parent))
}

// favoriteParentExists is the parent field's queryset: UserFavorite.objects,
// any user's live favorite.
func (a *API) favoriteParentExists(ctx context.Context) func(uuid.UUID) (bool, error) {
	return func(id uuid.UUID) (bool, error) {
		var ok bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_favorites WHERE id = $1 AND deleted_at IS NULL)`, id).Scan(&ok)
		return ok, err
	}
}

// favoriteFields are the writable UserFavoriteSerializer fields. All are
// optional on a partial update; on create entity_type is required.
type favoriteFields struct {
	entityType    *string
	entityTypeSet bool
	entityID      *uuid.UUID
	entityIDSet   bool
	name          *string
	nameSet       bool
	isFolder      bool
	isFolderSet   bool
	sequence      float64
	sequenceSet   bool
	parent        *uuid.UUID
	parentSet     bool
}

func (a *API) validateFavorite(ctx context.Context, c *httpx.Ctx, data *drf.Data, partial bool) (*favoriteFields, error) {
	v := drf.NewValidator(data, c.Loc())
	var f favoriteFields
	f.entityType, f.entityTypeSet = v.Char("entity_type", drf.CharField{MaxLength: 100})
	f.entityID, f.entityIDSet = v.UUID("entity_identifier", true)
	f.name, f.nameSet = v.Char("name", drf.CharField{MaxLength: 255, AllowBlank: true, AllowNull: true})
	f.isFolder, f.isFolderSet = v.Bool("is_folder")
	f.sequence, f.sequenceSet = v.Float("sequence")
	var err error
	f.parent, f.parentSet, err = v.PK("parent", true, a.favoriteParentExists(ctx))
	if err != nil {
		return nil, err
	}
	if !partial {
		v.Require("entity_type")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return &f, nil
}

// favoriteLookupType is the value of filter(entity_type=...): CharField
// stringifies what it is given, and None matches nothing.
func favoriteLookupType(v drf.Value, ok bool) (string, bool) {
	if !ok || v.IsNull() {
		return "", false
	}
	return drf.PyStr(v), true
}

// createFavorite ports WorkspaceFavoriteEndpoint.post.
func (a *API) createFavorite(c *httpx.Ctx) error {
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get on a list or string
	}
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, c.Param("slug")).Scan(&workspaceID); err != nil {
		return err
	}
	// An existing favorite of the same entity is returned as it is.
	if ev, ok := data.Get("entity_identifier"); ok && drf.PyTruthy(ev) {
		id, valid := drf.UUIDValue(ev)
		if !valid {
			return errFilterDetail
		}
		tv, tok := data.Get("entity_type")
		if et, ok := favoriteLookupType(tv, tok); ok {
			existing, err := scanFavorite(a.db.QueryRow(ctx, `SELECT `+favoriteCols+` FROM user_favorites f
				WHERE f.workspace_id = $1 AND f.user_id = $2 AND f.entity_type = $3 AND f.entity_identifier = $4 AND f.deleted_at IS NULL
				ORDER BY f.created_at DESC LIMIT 1`, workspaceID, c.User.ID, et, id))
			if err == nil {
				out, err := a.favoriteOut(ctx, existing, nil)
				if err != nil {
					return err
				}
				return c.JSON(http.StatusOK, out)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
	}
	f, err := a.validateFavorite(ctx, c, data, false)
	if err != nil {
		return err
	}
	in := favoriteNew{EntityType: *f.entityType, EntityID: f.entityID, Name: f.name, IsFolder: f.isFolder, Sequence: 65535,
		Parent: f.parent, WorkspaceID: &workspaceID}
	if f.sequenceSet {
		in.Sequence = f.sequence
	}
	if pv, ok := data.Get("project_id"); ok {
		in.ProjectRaw = &pv
	}
	row, err := a.insertFavorite(ctx, c.User.ID, in)
	if db.IsIntegrityError(err) {
		return httpx.Err(http.StatusBadRequest, "Favorite already exists")
	}
	if err != nil {
		return err
	}
	var echo any
	if in.ProjectRaw != nil && in.ProjectRaw.IsString() {
		echo = in.ProjectRaw.Str() // instance.project_id keeps the string it was given
	}
	out, err := a.favoriteOut(ctx, row, echo)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// loadUserFavorite is UserFavorite.objects.get(user=..., workspace__slug=..., pk=...).
func (a *API) loadUserFavorite(ctx context.Context, c *httpx.Ctx) (*favoriteRow, error) {
	id, err := c.UUIDParam("favorite_id")
	if err != nil {
		return nil, err
	}
	return scanFavorite(a.db.QueryRow(ctx, `SELECT `+favoriteCols+` FROM user_favorites f JOIN workspaces w ON w.id = f.workspace_id
		WHERE f.deleted_at IS NULL AND f.user_id = $1 AND w.slug = $2 AND f.id = $3`, c.User.ID, c.Param("slug"), id))
}

// updateFavorite ports WorkspaceFavoriteEndpoint.patch.
func (a *API) updateFavorite(c *httpx.Ctx) error {
	ctx := c.Context()
	cur, err := a.loadUserFavorite(ctx, c)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	f, err := a.validateFavorite(ctx, c, data, true)
	if err != nil {
		return err
	}
	var set setList
	if f.entityTypeSet {
		set.add("entity_type", *f.entityType)
	}
	if f.entityIDSet {
		set.add("entity_identifier", f.entityID)
	}
	if f.nameSet {
		set.add("name", f.name)
	}
	if f.isFolderSet {
		set.add("is_folder", f.isFolder)
	}
	if f.sequenceSet {
		set.add("sequence", f.sequence)
	}
	if f.parentSet {
		set.add("parent_id", f.parent)
	}
	// instance.save(): updated_by is the requester, and a favorite with a
	// project takes that project's workspace.
	set.add("updated_by_id", c.User.ID)
	_, err = a.db.Exec(ctx, `UPDATE user_favorites SET `+set.sql()+`, updated_at = now(),
		workspace_id = coalesce((SELECT workspace_id FROM projects WHERE id = project_id), workspace_id)
		WHERE id = $1`, append([]any{cur.ID}, set.args...)...)
	if err != nil {
		if db.IsIntegrityError(err) {
			return httpx.Err(http.StatusBadRequest, "The payload is not valid")
		}
		return err
	}
	row, err := scanFavorite(a.db.QueryRow(ctx, `SELECT `+favoriteCols+` FROM user_favorites f WHERE f.id = $1`, cur.ID))
	if err != nil {
		return err
	}
	out, err := a.favoriteOut(ctx, row, nil)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// hardDeleteFavorite is favorite.delete(soft=False): the row goes for good,
// with the favorites below it (the parent foreign key cascades, soft-deleted
// children included).
func (a *API) hardDeleteFavorite(ctx context.Context, id uuid.UUID) error {
	_, err := a.db.Exec(ctx, `WITH RECURSIVE tree AS (
			SELECT id FROM user_favorites WHERE id = $1
			UNION SELECT f.id FROM user_favorites f JOIN tree ON f.parent_id = tree.id)
		DELETE FROM user_favorites WHERE id IN (SELECT id FROM tree)`, id)
	return err
}

// deleteFavorite ports WorkspaceFavoriteEndpoint.delete.
func (a *API) deleteFavorite(c *httpx.Ctx) error {
	cur, err := a.loadUserFavorite(c.Context(), c)
	if err != nil {
		return err
	}
	if err := a.hardDeleteFavorite(c.Context(), cur.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// favoriteRequestValue is request.data.get(key): a view that asks a list or
// string body crashes. A missing key is None (nil).
func favoriteRequestValue(c *httpx.Ctx, key string) (*drf.Value, error) {
	data, err := drf.Parse(c.R)
	if err != nil {
		return nil, err
	}
	if !data.IsDict() {
		return nil, errViewCrash
	}
	if v, ok := data.Get(key); ok {
		return &v, nil
	}
	return nil, nil
}

// createEntityFavorite is the create of the per-entity favorite viewsets:
// UserFavorite.objects.create(project_id=..., user=..., entity_type=...,
// entity_identifier=...) and a 204. An IntegrityError (a duplicate) is the
// generic 400.
func (a *API) createEntityFavorite(c *httpx.Ctx, in favoriteNew) error {
	_, err := a.insertFavorite(c.Context(), c.User.ID, in)
	if db.IsIntegrityError(err) {
		return httpx.Err(http.StatusBadRequest, "The payload is not valid")
	}
	if err != nil {
		return err
	}
	return c.NoContent()
}

// deleteEntityFavorite is the destroy of the per-entity viewsets: get the
// user's favorite of the entity in the workspace (and project) and hard
// delete it.
func (a *API) deleteEntityFavorite(c *httpx.Ctx, entityType string, entity, project uuid.UUID) error {
	ctx := c.Context()
	var id uuid.UUID
	err := a.db.QueryRow(ctx, `SELECT f.id FROM user_favorites f JOIN workspaces w ON w.id = f.workspace_id
		WHERE f.deleted_at IS NULL AND f.project_id = $1 AND f.entity_type = $2 AND f.user_id = $3 AND w.slug = $4
			AND f.entity_identifier = $5`, project, entityType, c.User.ID, c.Param("slug"), entity).Scan(&id)
	if err != nil {
		return err
	}
	if err := a.hardDeleteFavorite(ctx, id); err != nil {
		return err
	}
	return c.NoContent()
}

// favoriteURLIDs reads the project and entity of a project favorite URL.
func favoriteURLIDs(c *httpx.Ctx, entityParam string) (project, entity uuid.UUID, err error) {
	if project, err = c.UUIDParam("project_id"); err != nil {
		return
	}
	entity, err = c.UUIDParam(entityParam)
	return
}

// createCycleFavorite ports CycleFavoriteViewSet.create.
func (a *API) createCycleFavorite(c *httpx.Ctx) error {
	project, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	raw, err := favoriteRequestValue(c, "cycle")
	if err != nil {
		return err
	}
	return a.createEntityFavorite(c, favoriteNew{EntityType: "cycle", EntityRaw: raw, ProjectID: &project, Sequence: 65535})
}

// deleteCycleFavorite ports CycleFavoriteViewSet.destroy.
func (a *API) deleteCycleFavorite(c *httpx.Ctx) error {
	project, cycle, err := favoriteURLIDs(c, "cycle_id")
	if err != nil {
		return err
	}
	return a.deleteEntityFavorite(c, "cycle", cycle, project)
}

// createModuleFavorite ports ModuleFavoriteViewSet.create.
func (a *API) createModuleFavorite(c *httpx.Ctx) error {
	project, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	raw, err := favoriteRequestValue(c, "module")
	if err != nil {
		return err
	}
	return a.createEntityFavorite(c, favoriteNew{EntityType: "module", EntityRaw: raw, ProjectID: &project, Sequence: 65535})
}

// deleteModuleFavorite ports ModuleFavoriteViewSet.destroy.
func (a *API) deleteModuleFavorite(c *httpx.Ctx) error {
	project, module, err := favoriteURLIDs(c, "module_id")
	if err != nil {
		return err
	}
	return a.deleteEntityFavorite(c, "module", module, project)
}

// createViewFavorite ports IssueViewFavoriteViewSet.create.
func (a *API) createViewFavorite(c *httpx.Ctx) error {
	project, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	raw, err := favoriteRequestValue(c, "view")
	if err != nil {
		return err
	}
	return a.createEntityFavorite(c, favoriteNew{EntityType: "view", EntityRaw: raw, ProjectID: &project, Sequence: 65535})
}

// deleteViewFavorite ports IssueViewFavoriteViewSet.destroy.
func (a *API) deleteViewFavorite(c *httpx.Ctx) error {
	project, view, err := favoriteURLIDs(c, "view_id")
	if err != nil {
		return err
	}
	return a.deleteEntityFavorite(c, "view", view, project)
}

// createPageFavorite ports PageFavoriteViewSet.create (the page comes from
// the URL, the body is not read).
func (a *API) createPageFavorite(c *httpx.Ctx) error {
	project, page, err := favoriteURLIDs(c, "page_id")
	if err != nil {
		return err
	}
	return a.createEntityFavorite(c, favoriteNew{EntityType: "page", EntityID: &page, ProjectID: &project, Sequence: 65535})
}

// createProjectFavorite ports ProjectFavoritesViewSet.create. It reads the
// project from the body for both the entity and the project, and checks
// neither the workspace in the URL nor the user's membership.
func (a *API) createProjectFavorite(c *httpx.Ctx) error {
	raw, err := favoriteRequestValue(c, "project")
	if err != nil {
		return err
	}
	return a.createEntityFavorite(c, favoriteNew{EntityType: "project", EntityRaw: raw, ProjectRaw: raw, Sequence: 65535})
}

// deleteProjectFavorite ports ProjectFavoritesViewSet.destroy.
func (a *API) deleteProjectFavorite(c *httpx.Ctx) error {
	project, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	return a.deleteEntityFavorite(c, "project", project, project)
}

// recentVisit is WorkspaceRecentVisitSerializer.
type recentVisit struct {
	ID               uuid.UUID  `json:"id"`
	EntityName       string     `json:"entity_name"`
	EntityIdentifier *uuid.UUID `json:"entity_identifier"`
	EntityData       any        `json:"entity_data"`
	VisitedAt        time.Time  `json:"visited_at"`
}

// listRecentVisits ports UserRecentVisitViewSet.list: the user's latest 20
// issue, page and project visits (newest created first, not newest visited),
// optionally of one entity_name.
func (a *API) listRecentVisits(c *httpx.Ctx) error {
	ctx := c.Context()
	args := []any{c.Param("slug"), c.User.ID}
	filter := ""
	if name := c.Query("entity_name"); name != "" {
		filter = " AND v.entity_name = $3"
		args = append(args, name)
	}
	rows, err := a.db.Query(ctx, `SELECT v.id, v.entity_name, v.entity_identifier, v.visited_at
		FROM user_recent_visits v JOIN workspaces w ON w.id = v.workspace_id
		WHERE v.deleted_at IS NULL AND w.slug = $1 AND v.user_id = $2`+filter+` AND v.entity_name IN ('issue', 'page', 'project')
		ORDER BY v.created_at DESC LIMIT 20`, args...)
	if err != nil {
		return err
	}
	visits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (recentVisit, error) {
		var r recentVisit
		err := row.Scan(&r.ID, &r.EntityName, &r.EntityIdentifier, &r.VisitedAt)
		return r, err
	})
	if err != nil {
		return err
	}
	if visits == nil {
		visits = []recentVisit{}
	}
	for i := range visits {
		if visits[i].EntityData, err = a.recentEntityData(ctx, visits[i].EntityName, visits[i].EntityIdentifier); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, visits)
}

// recentIssue is IssueRecentVisitSerializer.
type recentIssue struct {
	ID                uuid.UUID   `json:"id"`
	Name              string      `json:"name"`
	State             *uuid.UUID  `json:"state"`
	Priority          string      `json:"priority"`
	Assignees         []uuid.UUID `json:"assignees"`
	Type              *uuid.UUID  `json:"type"`
	SequenceID        int         `json:"sequence_id"`
	ProjectID         uuid.UUID   `json:"project_id"`
	ProjectIdentifier string      `json:"project_identifier"`
}

// recentProject is ProjectRecentVisitSerializer.
type recentProject struct {
	ID             uuid.UUID      `json:"id"`
	Name           string         `json:"name"`
	LogoProps      jsontext.Value `json:"logo_props"`
	ProjectMembers []uuid.UUID    `json:"project_members"`
	Identifier     string         `json:"identifier"`
}

// recentPage is PageRecentVisitSerializer; the project is the page's newest
// one.
type recentPage struct {
	ID                uuid.UUID      `json:"id"`
	Name              string         `json:"name"`
	LogoProps         jsontext.Value `json:"logo_props"`
	ProjectID         *uuid.UUID     `json:"project_id"`
	OwnedBy           uuid.UUID      `json:"owned_by"`
	ProjectIdentifier *string        `json:"project_identifier"`
}

// recentEntityData ports WorkspaceRecentVisitSerializer.get_entity_data: the
// entity, if objects.get still finds it, else nil.
func (a *API) recentEntityData(ctx context.Context, entityType string, id *uuid.UUID) (any, error) {
	if id == nil {
		return nil, nil
	}
	var logo string
	switch entityType {
	case "issue":
		var i recentIssue
		err := a.db.QueryRow(ctx, `SELECT i.id, i.name, i.state_id, i.priority, i.type_id, i.sequence_id, i.project_id, p.identifier
			FROM issues i JOIN projects p ON p.id = i.project_id WHERE i.id = $1 AND i.deleted_at IS NULL`, *id).
			Scan(&i.ID, &i.Name, &i.State, &i.Priority, &i.Type, &i.SequenceID, &i.ProjectID, &i.ProjectIdentifier)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		rows, err := a.db.Query(ctx, `SELECT u.id FROM users u JOIN issue_assignees ia ON ia.assignee_id = u.id
			WHERE ia.issue_id = $1 AND ia.deleted_at IS NULL ORDER BY u.created_at DESC`, i.ID)
		if err != nil {
			return nil, err
		}
		if i.Assignees, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
			return nil, err
		}
		if i.Assignees == nil {
			i.Assignees = []uuid.UUID{}
		}
		return i, nil
	case "project":
		var p recentProject
		err := a.db.QueryRow(ctx, `SELECT id, name, logo_props::text, identifier FROM projects WHERE id = $1 AND deleted_at IS NULL`, *id).
			Scan(&p.ID, &p.Name, &logo, &p.Identifier)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		p.LogoProps = jsontext.Value(logo)
		rows, err := a.db.Query(ctx, `SELECT pm.member_id FROM project_members pm JOIN users u ON u.id = pm.member_id
			WHERE pm.deleted_at IS NULL AND pm.is_active AND NOT u.is_bot AND pm.project_id = $1 ORDER BY pm.created_at DESC`, p.ID)
		if err != nil {
			return nil, err
		}
		if p.ProjectMembers, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
			return nil, err
		}
		if p.ProjectMembers == nil {
			p.ProjectMembers = []uuid.UUID{}
		}
		return p, nil
	case "page":
		var pg recentPage
		err := a.db.QueryRow(ctx, `SELECT id, name, logo_props::text, owned_by_id FROM pages WHERE id = $1 AND deleted_at IS NULL`, *id).
			Scan(&pg.ID, &pg.Name, &logo, &pg.OwnedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		pg.LogoProps = jsontext.Value(logo)
		// obj.projects.first(), twice: the newest live project among the
		// page's project_pages rows, whatever their own deleted_at.
		err = a.db.QueryRow(ctx, `SELECT p.id, p.identifier FROM projects p JOIN project_pages pp ON pp.project_id = p.id
			WHERE p.deleted_at IS NULL AND pp.page_id = $1 ORDER BY p.created_at DESC LIMIT 1`, pg.ID).
			Scan(&pg.ProjectID, &pg.ProjectIdentifier)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return pg, nil
	}
	return nil, nil
}
