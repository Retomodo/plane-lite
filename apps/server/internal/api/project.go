package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/sanitize"
	"plane-lite/server/internal/softdelete"
)

// project is ProjectListSerializer over ProjectViewSet.get_queryset(): every
// Project column plus the is_favorite / sort_order / member_role / anchor
// annotations and the computed members, cover_image_url, inbox_view and
// next_work_item_sequence.
type project struct {
	ID                    uuid.UUID      `json:"id"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	DeletedAt             *time.Time     `json:"deleted_at"`
	CreatedBy             *uuid.UUID     `json:"created_by"`
	UpdatedBy             *uuid.UUID     `json:"updated_by"`
	Name                  string         `json:"name"`
	Description           string         `json:"description"`
	DescriptionText       jsontext.Value `json:"description_text"`
	DescriptionHTML       jsontext.Value `json:"description_html"`
	Network               int            `json:"network"`
	Workspace             uuid.UUID      `json:"workspace"`
	Identifier            string         `json:"identifier"`
	DefaultAssignee       *uuid.UUID     `json:"default_assignee"`
	ProjectLead           *uuid.UUID     `json:"project_lead"`
	Emoji                 *string        `json:"emoji"`
	IconProp              jsontext.Value `json:"icon_prop"`
	ModuleView            bool           `json:"module_view"`
	CycleView             bool           `json:"cycle_view"`
	IssueViewsView        bool           `json:"issue_views_view"`
	PageView              bool           `json:"page_view"`
	IntakeView            bool           `json:"intake_view"`
	IsTimeTrackingEnabled bool           `json:"is_time_tracking_enabled"`
	IsIssueTypeEnabled    bool           `json:"is_issue_type_enabled"`
	GuestViewAllFeatures  bool           `json:"guest_view_all_features"`
	CoverImage            *string        `json:"cover_image"`
	CoverImageAsset       *uuid.UUID     `json:"cover_image_asset"`
	Estimate              *uuid.UUID     `json:"estimate"`
	ArchiveIn             int            `json:"archive_in"`
	CloseIn               int            `json:"close_in"`
	LogoProps             jsontext.Value `json:"logo_props"`
	DefaultState          *uuid.UUID     `json:"default_state"`
	ArchivedAt            *time.Time     `json:"archived_at"`
	Timezone              string         `json:"timezone"`
	ExternalSource        *string        `json:"external_source"`
	ExternalID            *string        `json:"external_id"`

	IsFavorite           bool        `json:"is_favorite"`
	SortOrder            *float64    `json:"sort_order"`
	MemberRole           *int        `json:"member_role"`
	Anchor               *string     `json:"anchor"`
	Members              []uuid.UUID `json:"members"`
	CoverImageURL        *string     `json:"cover_image_url"`
	InboxView            bool        `json:"inbox_view"`
	NextWorkItemSequence int64       `json:"next_work_item_sequence"`
}

// projectSelect reads a project with ProjectViewSet.get_queryset()'s
// annotations for user $1 in the workspace with slug $2. anchor comes from
// deploy boards (publishing is cut) and is always null.
const projectSelect = `
	SELECT p.id, p.created_at, p.updated_at, p.deleted_at, p.created_by_id, p.updated_by_id,
		p.name, p.description, p.description_text::text, p.description_html::text, p.network, p.workspace_id,
		p.identifier, p.default_assignee_id, p.project_lead_id, p.emoji, p.icon_prop::text, p.module_view,
		p.cycle_view, p.issue_views_view, p.page_view, p.intake_view, p.is_time_tracking_enabled,
		p.is_issue_type_enabled, p.guest_view_all_features, p.cover_image, p.cover_image_asset_id,
		p.estimate_id, p.archive_in, p.close_in, p.logo_props::text, p.default_state_id, p.archived_at,
		p.timezone, p.external_source, p.external_id, ca.entity_type,
		EXISTS (SELECT 1 FROM user_favorites f WHERE f.user_id = $1 AND f.entity_identifier = p.id
			AND f.entity_type = 'project' AND f.project_id = p.id AND f.deleted_at IS NULL) AS is_favorite,
		(SELECT pup.sort_order FROM project_user_properties pup WHERE pup.user_id = $1 AND pup.project_id = p.id
			AND pup.workspace_id = w.id AND pup.deleted_at IS NULL) AS sort_order,
		(SELECT pm.role FROM project_members pm WHERE pm.project_id = p.id AND pm.member_id = $1
			AND pm.is_active AND pm.deleted_at IS NULL) AS member_role,
		ARRAY(SELECT pm.member_id FROM project_members pm JOIN users u ON u.id = pm.member_id
			WHERE pm.project_id = p.id AND pm.workspace_id = w.id AND pm.is_active AND pm.deleted_at IS NULL
				AND NOT u.is_bot
			ORDER BY pm.created_at DESC) AS members,
		(SELECT coalesce(nullif(max(s.sequence), 0) + 1, 1) FROM issue_sequences s
			WHERE s.project_id = p.id AND s.deleted_at IS NULL) AS next_work_item_sequence
	FROM projects p
	JOIN workspaces w ON w.id = p.workspace_id
	LEFT JOIN file_assets ca ON ca.id = p.cover_image_asset_id
	WHERE w.slug = $2 AND p.deleted_at IS NULL`

func scanProject(row pgx.Row) (*project, error) {
	var (
		p                                  project
		descText, descHTML, iconProp, logo *string
		coverType                          *string
	)
	if err := row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &p.CreatedBy, &p.UpdatedBy,
		&p.Name, &p.Description, &descText, &descHTML, &p.Network, &p.Workspace,
		&p.Identifier, &p.DefaultAssignee, &p.ProjectLead, &p.Emoji, &iconProp, &p.ModuleView,
		&p.CycleView, &p.IssueViewsView, &p.PageView, &p.IntakeView, &p.IsTimeTrackingEnabled,
		&p.IsIssueTypeEnabled, &p.GuestViewAllFeatures, &p.CoverImage, &p.CoverImageAsset,
		&p.Estimate, &p.ArchiveIn, &p.CloseIn, &logo, &p.DefaultState, &p.ArchivedAt,
		&p.Timezone, &p.ExternalSource, &p.ExternalID, &coverType,
		&p.IsFavorite, &p.SortOrder, &p.MemberRole, &p.Members, &p.NextWorkItemSequence); err != nil {
		return nil, err
	}
	p.DescriptionText, p.DescriptionHTML, p.IconProp, p.LogoProps = jsonValue(descText), jsonValue(descHTML), jsonValue(iconProp), jsonValue(logo)
	p.CoverImageURL = imageURL(p.CoverImageAsset, coverType, p.CoverImage)
	p.InboxView = p.IntakeView
	return &p, nil
}

// projectVisibility is the list filters: guests see projects they belong
// to, members also see public ones. The membership join has no deleted_at
// check, as in Django.
func projectVisibility(wsRole int) string {
	joined := `EXISTS (SELECT 1 FROM project_members x WHERE x.project_id = p.id AND x.member_id = $1 AND x.is_active)`
	switch wsRole {
	case roleGuest:
		return " AND " + joined
	case roleMember:
		return " AND (" + joined + " OR p.network = 2)"
	}
	return ""
}

func (a *API) queryProjects(ctx context.Context, sql string, args ...any) ([]*project, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// loadProject is get_queryset().filter(pk=id).first() (archived included).
func (a *API) loadProject(ctx context.Context, slug string, user, id uuid.UUID) (*project, error) {
	return scanProject(a.db.QueryRow(ctx, projectSelect+" AND p.id = $3", user, slug, id))
}

// utcTime is a timestamp read through .values(): DRF's JSON encoder prints
// it as stored (UTC) rather than in the user's timezone.
type utcTime time.Time

func (t utcTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + httpx.FormatDateTime(time.Time(t), time.UTC) + `"`), nil
}

// projectListItem is one row of ProjectViewSet.list's .values().
type projectListItem struct {
	ID                   uuid.UUID      `json:"id"`
	Name                 string         `json:"name"`
	Identifier           string         `json:"identifier"`
	SortOrder            *float64       `json:"sort_order"`
	LogoProps            jsontext.Value `json:"logo_props"`
	MemberRole           *int           `json:"member_role"`
	IntakeCount          int            `json:"intake_count"`
	ArchivedAt           *utcTime       `json:"archived_at"`
	Workspace            uuid.UUID      `json:"workspace"`
	CycleView            bool           `json:"cycle_view"`
	IssueViewsView       bool           `json:"issue_views_view"`
	ModuleView           bool           `json:"module_view"`
	PageView             bool           `json:"page_view"`
	InboxView            bool           `json:"inbox_view"`
	GuestViewAllFeatures bool           `json:"guest_view_all_features"`
	ProjectLead          *uuid.UUID     `json:"project_lead"`
	Network              int            `json:"network"`
	CreatedAt            utcTime        `json:"created_at"`
	UpdatedAt            utcTime        `json:"updated_at"`
	CreatedBy            *uuid.UUID     `json:"created_by"`
	UpdatedBy            *uuid.UUID     `json:"updated_by"`
}

// listProjects ports ProjectViewSet.list, the boot-time project list.
// intake_count counts pending intake issues; intake is cut, so it is 0.
func (a *API) listProjects(c *httpx.Ctx) error {
	ctx := c.Context()
	slug := c.Param("slug")
	wsRole, err := a.workspaceRole(ctx, slug, c.User.ID)
	if err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `
		SELECT p.id, p.name, p.identifier,
			(SELECT pup.sort_order FROM project_user_properties pup WHERE pup.user_id = $1 AND pup.project_id = p.id
				AND pup.workspace_id = w.id AND pup.deleted_at IS NULL),
			p.logo_props::text,
			(SELECT pm.role FROM project_members pm WHERE pm.project_id = p.id AND pm.member_id = $1
				AND pm.is_active AND pm.deleted_at IS NULL),
			p.archived_at, p.workspace_id, p.cycle_view, p.issue_views_view, p.module_view, p.page_view,
			p.intake_view, p.guest_view_all_features, p.project_lead_id, p.network, p.created_at,
			p.updated_at, p.created_by_id, p.updated_by_id
		FROM projects p JOIN workspaces w ON w.id = p.workspace_id
		WHERE w.slug = $2 AND p.deleted_at IS NULL`+projectVisibility(wsRole)+`
		ORDER BY p.created_at DESC`, c.User.ID, slug)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []projectListItem{}
	for rows.Next() {
		var (
			p                projectListItem
			logo             string
			archived         *time.Time
			created, updated time.Time
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Identifier, &p.SortOrder, &logo, &p.MemberRole, &archived,
			&p.Workspace, &p.CycleView, &p.IssueViewsView, &p.ModuleView, &p.PageView, &p.InboxView,
			&p.GuestViewAllFeatures, &p.ProjectLead, &p.Network, &created, &updated, &p.CreatedBy,
			&p.UpdatedBy); err != nil {
			return err
		}
		p.LogoProps, p.CreatedAt, p.UpdatedAt = jsontext.Value(logo), utcTime(created), utcTime(updated)
		if archived != nil {
			t := utcTime(*archived)
			p.ArchivedAt = &t
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// projectOrderBy maps PROJECT_ORDER_BY_ALLOWLIST to SQL.
var projectOrderBy = map[string]string{
	"created_at": "p.created_at",
	"updated_at": "p.updated_at",
	"name":       "p.name",
	"network":    "p.network",
	"sort_order": "sort_order", // the annotation's output column
}

// listProjectDetails ports ProjectViewSet.list_detail. ?fields= is ignored
// (DynamicBaseSerializer drops it); results are paginated only when both
// ?per_page= and ?cursor= are given.
func (a *API) listProjectDetails(c *httpx.Ctx) error {
	ctx := c.Context()
	slug := c.Param("slug")
	wsRole, err := a.workspaceRole(ctx, slug, c.User.ID)
	if err != nil {
		return err
	}
	base := projectSelect + projectVisibility(wsRole)
	if c.Query("per_page") == "" || c.Query("cursor") == "" {
		projects, err := a.queryProjects(ctx, base+` ORDER BY sort_order, p.name`, c.User.ID, slug)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, projects)
	}

	field, desc := sanitizeOrderBy(c.Query("order_by"), []string{"created_at", "updated_at", "name", "network", "sort_order"}, "-created_at")
	page, err := parseOffsetPage(c)
	if err != nil {
		return err
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	projects, err := a.queryProjects(ctx, fmt.Sprintf(`%s ORDER BY %s %s NULLS LAST, p.created_at DESC LIMIT $3 OFFSET $4`,
		base, projectOrderBy[field], dir), c.User.ID, slug, page.limit+1, page.offset)
	if err != nil {
		return err
	}
	var total int64
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM (`+base+`) p`, c.User.ID, slug).Scan(&total); err != nil {
		return err
	}
	n := len(projects)
	return c.JSON(http.StatusOK, page.response(projects[:min(int64(n), page.limit)], n, total))
}

// getProject ports ProjectViewSet.retrieve.
func (a *API) getProject(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	p, err := scanProject(a.db.QueryRow(ctx, projectSelect+" AND p.archived_at IS NULL AND p.id = $3", c.User.ID, slug, pk))
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "Project does not exist")
	}
	if err != nil {
		return err
	}
	// members_list (bots included) must hold the requester.
	var member bool
	if err := a.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE pm.project_id = $1 AND w.slug = $2 AND pm.member_id = $3 AND pm.is_active AND pm.deleted_at IS NULL)`,
		p.ID, slug, c.User.ID).Scan(&member); err != nil {
		return err
	}
	if !member {
		if p.Network == 0 {
			return httpx.Err(http.StatusForbidden, "You do not have permission")
		}
		return httpx.Err(http.StatusConflict, "You are not a member of this project")
	}
	a.recordVisit(ctx, slug, "project", p.ID, c.User.ID, &p.ID)
	return c.JSON(http.StatusOK, p)
}

// recordVisit is bgtasks.recent_visited_task.recent_visited_task, run
// inline; like the task it logs failures instead of returning them.
func (a *API) recordVisit(ctx context.Context, slug, entity string, entityID, user uuid.UUID, projectID *uuid.UUID) {
	if err := a.recordVisitErr(ctx, slug, entity, entityID, user, projectID); err != nil {
		a.log.Error("recent_visited_task", "err", err)
	}
}

func (a *API) recordVisitErr(ctx context.Context, slug, entity string, entityID, user uuid.UUID, projectID *uuid.UUID) error {
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, slug).Scan(&workspaceID); err != nil {
		return err
	}
	tag, err := a.db.Exec(ctx, `
		UPDATE user_recent_visits SET visited_at = now() WHERE id = (
			SELECT id FROM user_recent_visits
			WHERE entity_name = $1 AND entity_identifier = $2 AND user_id = $3
				AND project_id IS NOT DISTINCT FROM $4 AND workspace_id = $5 AND deleted_at IS NULL
			ORDER BY created_at DESC LIMIT 1)`, entity, entityID, user, projectID, workspaceID)
	if err != nil || tag.RowsAffected() > 0 {
		return err
	}
	// The user keeps at most 20 visits per workspace: at exactly 20 the
	// oldest is soft-deleted first.
	var count int
	var oldest uuid.UUID
	if err := a.db.QueryRow(ctx, `
		SELECT count(*), (array_agg(id ORDER BY created_at))[1] FROM user_recent_visits
		WHERE user_id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, user, workspaceID).Scan(&count, &oldest); err != nil {
		return err
	}
	if count == 20 {
		if err := softdelete.Row(ctx, a.db, "user_recent_visits", oldest, user); err != nil {
			return err
		}
	}
	_, err = a.db.Exec(ctx, `
		INSERT INTO user_recent_visits (entity_name, entity_identifier, user_id, visited_at, project_id, workspace_id,
			created_by_id, updated_by_id)
		VALUES ($1, $2, $3, now(), $4, $5, $3, $3)`, entity, entityID, user, projectID, workspaceID)
	return err
}

// forbiddenIdentifierChars is Project.FORBIDDEN_IDENTIFIER_CHARS_PATTERN.
var forbiddenIdentifierChars = regexp.MustCompile(`^.*[&+,:;$^}{*=?@#|'<>.()%!-].*$`)

// validateProject runs ProjectSerializer over request.data. current is the
// project being updated (nil on create).
func (a *API) validateProject(ctx context.Context, v *drf.Validator, workspaceID uuid.UUID, current *uuid.UUID, partial bool) (*setList, error) {
	var set setList
	if !partial {
		v.Require("name", "identifier")
	}
	// validate_name / validate_identifier: no forbidden characters, and
	// unique (as given, case-sensitively) among the workspace's live projects.
	unique := func(field, col, value, special, taken string) error {
		if forbiddenIdentifierChars.MatchString(value) {
			v.Add(field, special)
			return nil
		}
		var exists bool
		if err := a.db.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM projects
			WHERE %s = $1 AND workspace_id = $2 AND deleted_at IS NULL AND id IS DISTINCT FROM $3)`, col),
			value, workspaceID, current).Scan(&exists); err != nil {
			return err
		}
		if exists {
			v.Add(field, taken)
			return nil
		}
		set.add(col, value)
		return nil
	}
	if name, ok := v.Char("name", drf.CharField{MaxLength: 255}); ok {
		if err := unique("name", "name", *name, "PROJECT_NAME_CANNOT_CONTAIN_SPECIAL_CHARACTERS", "PROJECT_NAME_ALREADY_EXIST"); err != nil {
			return nil, err
		}
	}
	if s, ok := v.Char("description", drf.CharField{AllowBlank: true}); ok {
		set.add("description", *s)
	}
	for _, col := range []string{"description_text", "description_html", "icon_prop"} {
		if raw, ok := v.JSON(col, true); ok {
			set.addCast(col, string(raw), "::jsonb")
		}
	}
	if s, ok := v.Choice("network", []string{"0", "2"}, drf.ChoiceField{}); ok {
		set.add("network", atoi(*s))
	}
	if id, ok := v.Char("identifier", drf.CharField{MaxLength: 12}); ok {
		if err := unique("identifier", "identifier", *id, "PROJECT_IDENTIFIER_CANNOT_CONTAIN_SPECIAL_CHARACTERS", "PROJECT_IDENTIFIER_ALREADY_EXIST"); err != nil {
			return nil, err
		}
	}
	userExists := func(id uuid.UUID) (bool, error) {
		var ok bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&ok)
		return ok, err
	}
	pks := []struct {
		field, col string
		exists     func(uuid.UUID) (bool, error)
	}{
		{"default_assignee", "default_assignee_id", userExists},
		{"project_lead", "project_lead_id", userExists},
		{"cover_image_asset", "cover_image_asset_id", a.liveRowExists(ctx, "file_assets")},
		{"estimate", "estimate_id", a.liveRowExists(ctx, "estimates")},
		// State's default manager hides triage states.
		{"default_state", "default_state_id", func(id uuid.UUID) (bool, error) {
			var ok bool
			err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM states
				WHERE id = $1 AND deleted_at IS NULL AND "group" <> 'triage')`, id).Scan(&ok)
			return ok, err
		}},
	}
	for _, f := range pks {
		id, ok, err := v.PK(f.field, true, f.exists)
		if err != nil {
			return nil, err
		}
		if ok {
			set.add(f.col, id)
		}
	}
	for _, f := range []string{"emoji", "external_source", "external_id"} {
		if s, ok := v.Char(f, drf.CharField{MaxLength: 255, AllowBlank: true, AllowNull: true}); ok {
			set.add(f, s)
		}
	}
	if s, ok := v.Char("cover_image", drf.CharField{AllowBlank: true, AllowNull: true}); ok {
		set.add("cover_image", s)
	}
	for _, f := range []string{"module_view", "cycle_view", "issue_views_view", "page_view", "intake_view",
		"is_time_tracking_enabled", "is_issue_type_enabled", "guest_view_all_features"} {
		if b, ok := v.Bool(f); ok {
			set.add(f, b)
		}
	}
	for _, f := range []string{"archive_in", "close_in"} {
		if n, ok := v.Int(f, 0, 12); ok {
			set.add(f, n)
		}
	}
	if raw, ok := v.JSON("logo_props", false); ok {
		set.addCast("logo_props", string(raw), "::jsonb")
	}
	if t, ok := v.DateTime("archived_at", true); ok {
		set.add("archived_at", t)
	}
	if s, ok := v.Choice("timezone", userTimezones, drf.ChoiceField{}); ok {
		set.add("timezone", *s)
	}
	if err := a.auditFields(ctx, v, &set); err != nil {
		return nil, err
	}
	// validate(): a truthy description_html (a JSONField) is replaced by
	// nh3.clean(str(value)).
	if v.Valid() {
		if i := slices.Index(set.cols, "description_html"); i >= 0 {
			if raw := drf.JSONValue(jsontext.Value(set.args[i].(string))); drf.PyTruthy(raw) {
				clean, _, err := sanitize.HTML(drf.PyStr(raw))
				if err != nil {
					return nil, httpx.Body(http.StatusBadRequest, map[string]any{"error": []string{"html content is not valid"}})
				}
				enc, err := json.Marshal(clean)
				if err != nil {
					return nil, err
				}
				set.args[i] = string(enc)
			}
		}
	}
	return &set, nil
}

// identifierCol is Project.save()'s identifier.strip().upper().
func identifierCol(set *setList) {
	if i := slices.Index(set.cols, "identifier"); i >= 0 {
		set.args[i] = drf.PyUpper(drf.PyStrip(set.args[i].(string)))
	}
}

// defaultStates is plane.db.models.state.DEFAULT_STATES.
var defaultStates = []struct {
	name, color, group string
	sequence           float64
	isDefault          bool
}{
	{"Backlog", "#60646C", "backlog", 15000, true},
	{"Todo", "#60646C", "unstarted", 25000, false},
	{"In Progress", "#F59E0B", "started", 35000, false},
	{"Done", "#46A758", "completed", 45000, false},
	{"Cancelled", "#9AA4BC", "cancelled", 55000, false},
	{"Triage", "#4E5355", "triage", 65000, false},
}

// createProject ports ProjectViewSet.create. Like Django it runs without a
// transaction. The model_activity task (webhooks) is not ported.
func (a *API) createProject(c *httpx.Ctx) error {
	ctx := c.Context()
	slug := c.Param("slug")
	var workspaceID uuid.UUID
	var workspaceTZ string
	if err := a.db.QueryRow(ctx, `SELECT id, timezone FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, slug).
		Scan(&workspaceID, &workspaceTZ); err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // {**request.data}
	}
	v := drf.NewValidator(data, c.Loc())
	set, err := a.validateProject(ctx, v, workspaceID, nil, false)
	if err != nil {
		return err
	}
	if err := v.Err(); err != nil {
		return err
	}
	identifierCol(set)
	if !slices.Contains(set.cols, "timezone") {
		set.add("timezone", workspaceTZ) // Project.save() inherits the workspace's
	}
	set.add("workspace_id", workspaceID)
	set.drop("created_by_id") // save() records the requester, whatever the body named
	set.add("created_by_id", c.User.ID)
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, set.insertSQL("projects"), set.args...).Scan(&id); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `
		INSERT INTO project_identifiers (name, project_id, workspace_id) SELECT identifier, id, workspace_id FROM projects WHERE id = $1`,
		id); err != nil {
		return err
	}
	var lead *uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT project_lead_id FROM projects WHERE id = $1`, id).Scan(&lead); err != nil {
		return err
	}
	if err := a.addProjectMember(ctx, id, workspaceID, c.User.ID, roleAdmin, c.User.ID); err != nil {
		return err
	}
	if lead != nil && *lead != c.User.ID {
		if err := a.addProjectMember(ctx, id, workspaceID, *lead, roleAdmin, c.User.ID); err != nil {
			return err
		}
	}
	// bulk_create: no State.save(), so slug stays blank.
	for _, s := range defaultStates {
		if _, err := a.db.Exec(ctx, `
			INSERT INTO states (name, color, sequence, "group", "default", project_id, workspace_id, created_by_id,
				created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, clock_timestamp(), clock_timestamp())`,
			s.name, s.color, s.sequence, s.group, s.isDefault, id, workspaceID, c.User.ID); err != nil {
			return err
		}
	}
	p, err := a.loadProject(ctx, slug, c.User.ID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, p)
}

// addProjectMember is ProjectMember.objects.create(): save() first creates
// the member's ProjectUserProperty, sorted above their other projects in
// the workspace. actor is the requester, recorded as created_by.
func (a *API) addProjectMember(ctx context.Context, projectID, workspaceID, member uuid.UUID, role int, actor uuid.UUID) error {
	if _, err := a.db.Exec(ctx, `
		INSERT INTO project_user_properties (workspace_id, project_id, user_id, sort_order, created_by_id)
		SELECT $1, $2, $3, coalesce(min(sort_order) - 10000, 65535), $4 FROM project_user_properties
		WHERE workspace_id = $1 AND user_id = $3 AND deleted_at IS NULL`, workspaceID, projectID, member, actor); err != nil {
		return err
	}
	_, err := a.db.Exec(ctx, `
		INSERT INTO project_members (project_id, workspace_id, member_id, role, created_by_id) VALUES ($1, $2, $3, $4, $5)`,
		projectID, workspaceID, member, role, actor)
	return err
}

// isProjectAdmin is the inline check of partial_update and destroy: a
// workspace admin, or an admin of the project.
func (a *API) isProjectAdmin(ctx context.Context, slug string, projectID, user uuid.UUID) (bool, error) {
	wsRole, err := a.workspaceRole(ctx, slug, user)
	if err != nil || wsRole == roleAdmin {
		return wsRole == roleAdmin, err
	}
	role, err := a.projectRole(ctx, slug, projectID, user)
	return role == roleAdmin, err
}

// projectObject is Project.objects.get(pk=pk, workspace__slug=slug).
func (a *API) projectObject(ctx context.Context, slug string, pk uuid.UUID) (id, workspaceID uuid.UUID, archivedAt *time.Time, intakeView bool, err error) {
	err = a.db.QueryRow(ctx, `
		SELECT p.id, p.workspace_id, p.archived_at, p.intake_view FROM projects p JOIN workspaces w ON w.id = p.workspace_id
		WHERE p.id = $1 AND w.slug = $2 AND p.deleted_at IS NULL`, pk, slug).Scan(&id, &workspaceID, &archivedAt, &intakeView)
	return
}

// updateProject ports ProjectViewSet.partial_update. Turning intake on
// would also create a default Intake; intake is cut, so only the flag is
// stored.
func (a *API) updateProject(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	ok, err := a.isProjectAdmin(ctx, slug, pk, c.User.ID)
	if err != nil {
		return err
	}
	if !ok {
		return errNoRole
	}
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, slug).Scan(&workspaceID); err != nil {
		return err
	}
	id, _, archivedAt, intakeView, err := a.projectObject(ctx, slug, pk)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get
	}
	intake, ok := data.Get("inbox_view")
	if !ok {
		intake = drf.JSONValue(jsontext.Value(fmt.Sprint(intakeView)))
	}
	if archivedAt != nil {
		return httpx.Err(http.StatusBadRequest, "Archived projects cannot be updated")
	}
	data.Set("intake_view", intake)
	v := drf.NewValidator(data, c.Loc())
	set, err := a.validateProject(ctx, v, workspaceID, &id, true)
	if err != nil {
		return err
	}
	if err := v.Err(); err != nil {
		return err
	}
	identifierCol(set)
	set.add("updated_at", time.Now())
	set.add("updated_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, `UPDATE projects SET `+set.sql()+` WHERE id = $1`, append([]any{id}, set.args...)...); err != nil {
		return err
	}
	p, err := a.loadProject(ctx, slug, c.User.ID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

// deleteProject ports ProjectViewSet.destroy: soft delete with cascade,
// then every user's favorite of the project. (Deploy boards go with the
// cascade; the webhook_activity task is not ported.)
func (a *API) deleteProject(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	ok, err := a.isProjectAdmin(ctx, slug, pk, c.User.ID)
	if err != nil {
		return err
	}
	if !ok {
		return errNoRole
	}
	id, workspaceID, _, _, err := a.projectObject(ctx, slug, pk)
	if err != nil {
		return err
	}
	if err := softdelete.Row(ctx, a.db, "projects", id, c.User.ID); err != nil {
		return err
	}
	if err := a.unfavoriteProject(ctx, workspaceID, id); err != nil {
		return err
	}
	return c.NoContent()
}

// unfavoriteProject is UserFavorite.objects.filter(project=...).delete(), a
// plain soft-delete UPDATE.
func (a *API) unfavoriteProject(ctx context.Context, workspaceID, projectID uuid.UUID) error {
	_, err := a.db.Exec(ctx, `UPDATE user_favorites SET deleted_at = now()
		WHERE project_id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, projectID, workspaceID)
	return err
}

// archiveProject ports ProjectArchiveUnarchiveEndpoint.post.
func (a *API) archiveProject(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	id, workspaceID, _, _, err := a.projectObject(ctx, c.Param("slug"), pk)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := a.db.Exec(ctx, `UPDATE projects SET archived_at = $2, updated_at = now(), updated_by_id = $3 WHERE id = $1`,
		id, now, c.User.ID); err != nil {
		return err
	}
	if err := a.unfavoriteProject(ctx, workspaceID, id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"archived_at": pyDateTimeStr(now)})
}

// pyDateTimeStr is str() of an aware UTC datetime.
func pyDateTimeStr(t time.Time) string {
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02 15:04:05-07:00")
	}
	return t.Format("2006-01-02 15:04:05.000000-07:00")
}

// unarchiveProject ports ProjectArchiveUnarchiveEndpoint.delete.
func (a *API) unarchiveProject(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	id, _, _, _, err := a.projectObject(ctx, c.Param("slug"), pk)
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE projects SET archived_at = NULL, updated_at = now(), updated_by_id = $2 WHERE id = $1`,
		id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// projectIdentifiers ports ProjectIdentifierEndpoint.get: whether an
// identifier is taken in the workspace.
func (a *API) projectIdentifiers(c *httpx.Ctx) error {
	name := drf.PyUpper(drf.PyStrip(c.Query("name")))
	if name == "" {
		return httpx.Err(http.StatusBadRequest, "Name is required")
	}
	rows, err := a.db.Query(c.Context(), `
		SELECT i.id, i.name, i.project_id FROM project_identifiers i JOIN workspaces w ON w.id = i.workspace_id
		WHERE i.name = $1 AND w.slug = $2 AND i.deleted_at IS NULL ORDER BY i.created_at DESC`, name, c.Param("slug"))
	if err != nil {
		return err
	}
	type identifier struct {
		ID      int64     `json:"id"`
		Name    string    `json:"name"`
		Project uuid.UUID `json:"project"`
	}
	ids, err := pgx.CollectRows(rows, pgx.RowToStructByPos[identifier])
	if err != nil {
		return err
	}
	if ids == nil {
		ids = []identifier{}
	}
	return c.JSON(http.StatusOK, map[string]any{"exists": len(ids), "identifiers": ids})
}

// joinProjects ports UserProjectInvitationsViewset.create: a workspace
// admin or member joins projects from the projects page, taking their
// workspace role. Secret projects are for workspace admins.
func (a *API) joinProjects(c *httpx.Ctx) error {
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	var requested []uuid.UUID
	if v, ok := data.Get("project_ids"); ok {
		if requested, err = uuidList(v); err != nil {
			return err
		}
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var (
		workspaceID uuid.UUID
		wsRole      int
	)
	if err := a.db.QueryRow(ctx, `
		SELECT wm.workspace_id, wm.role FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
		WHERE wm.member_id = $1 AND w.slug = $2 AND wm.is_active AND wm.deleted_at IS NULL`, c.User.ID, slug).
		Scan(&workspaceID, &wsRole); err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `
		SELECT p.id, p.network FROM projects p JOIN workspaces w ON w.id = p.workspace_id
		WHERE p.id = ANY($1) AND w.slug = $2 AND p.deleted_at IS NULL ORDER BY p.created_at DESC`, requested, slug)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var (
			id      uuid.UUID
			network int
		)
		if err := rows.Scan(&id, &network); err != nil {
			rows.Close()
			return err
		}
		if network == 0 && wsRole != roleAdmin {
			rows.Close()
			return httpx.Err(http.StatusForbidden, "Only workspace admins can join private project")
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `
		UPDATE project_members SET is_active = true
		WHERE workspace_id = $1 AND project_id = ANY($2) AND member_id = $3 AND deleted_at IS NULL`,
		workspaceID, ids, c.User.ID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `
		INSERT INTO project_members (project_id, member_id, role, workspace_id, created_by_id, created_at, updated_at)
		SELECT id, $2, $3, $4, $2, clock_timestamp(), clock_timestamp() FROM unnest($1::uuid[]) AS id
		ON CONFLICT DO NOTHING`, ids, c.User.ID, wsRole, workspaceID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `
		INSERT INTO project_user_properties (project_id, user_id, workspace_id, created_by_id, created_at, updated_at)
		SELECT id, $2, $3, $2, clock_timestamp(), clock_timestamp() FROM unnest($1::uuid[]) AS id
		ON CONFLICT DO NOTHING`, ids, c.User.ID, workspaceID); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]any{"message": "Projects joined successfully"})
}

// uuidList is the value of an id__in lookup: each element through
// UUIDField.to_python (a bad one is a ValidationError, BaseAPIView's 400),
// nulls dropped. Strings iterate as characters and dicts as keys; anything
// else is not iterable.
func uuidList(v drf.Value) ([]uuid.UUID, error) {
	var elems []drf.Value
	switch v.Kind() {
	case '[':
		elems, _ = v.Elems()
	case '"':
		for _, r := range v.Str() {
			elems = append(elems, drf.JSONValue(jsonString(string(r))))
		}
	case '{':
		var m map[string]jsontext.Value
		if err := drf.Decode(v.Raw(), &m); err != nil {
			return nil, err
		}
		for k := range m {
			elems = append(elems, drf.JSONValue(jsonString(k)))
		}
	default:
		return nil, errViewCrash
	}
	out := []uuid.UUID{}
	for _, e := range elems {
		if e.IsNull() {
			continue
		}
		id, ok := drf.UUIDValue(e)
		if !ok {
			if e.Kind() == '[' || e.Kind() == '{' {
				return nil, errViewCrash // unhashable in the lookup
			}
			return nil, errInvalidDetail
		}
		out = append(out, id)
	}
	return out, nil
}
