package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// ModuleViewSet (P/modules/) and WorkspaceModulesEndpoint (W/modules/).

// moduleStateGroups are the state groups the module annotations count, in
// the order of the *_issues / *_estimate_points annotations.
var moduleStateGroups = []string{"backlog", "unstarted", "started", "cancelled", "completed"}

// moduleIssueCountSQL is one of get_queryset's issue counts for the module
// row m: Coalesce(Subquery(Issue.issue_objects.filter(state__group=group,
// issue_module__module_id=OuterRef("pk"), issue_module__deleted_at__isnull=
// True) ... Count("pk")), 0). An empty group counts every state.
func moduleIssueCountSQL(group string) string {
	return `COALESCE((SELECT count(mi.id) FROM issues mi JOIN module_issues mmi ON mmi.issue_id = mi.id
		WHERE ` + issueObjects("mi") + ` AND mmi.deleted_at IS NULL AND mmi.module_id = m.id` + moduleGroupCond(group) + `), 0)`
}

// moduleEstimateSQL is one of get_queryset's estimate sums: the points of
// the module's issues whose estimate is of type points (the joins to
// estimate_points and estimates ignore deleted_at).
func moduleEstimateSQL(group string) string {
	return `COALESCE((SELECT sum(mep.value::double precision) FROM issues mi
		JOIN module_issues mmi ON mmi.issue_id = mi.id
		JOIN estimate_points mep ON mep.id = mi.estimate_point_id JOIN estimates mes ON mes.id = mep.estimate_id
		WHERE ` + issueObjects("mi") + ` AND mes.type = 'points' AND mmi.deleted_at IS NULL AND mmi.module_id = m.id` +
		moduleGroupCond(group) + `), 0)`
}

func moduleGroupCond(group string) string {
	if group == "" {
		return ""
	}
	return ` AND EXISTS (SELECT 1 FROM states mst WHERE mst.id = mi.state_id AND mst."group" = '` + group + `')`
}

// moduleMemberIDsSQL is the member_ids ArrayAgg over the members join.
// ModuleViewSet skips soft-deleted ModuleMember rows; the archive view's
// copy of the queryset does not.
func moduleMemberIDsSQL(liveOnly bool) string {
	filter := ""
	if liveOnly {
		filter = " FILTER (WHERE mm.deleted_at IS NULL)"
	}
	return `COALESCE((SELECT array_agg(DISTINCT mm.member_id)` + filter + ` FROM module_members mm
		WHERE mm.module_id = m.id), '{}')`
}

// moduleAnnotatedSQL is ModuleViewSet.get_queryset (archived=false) or
// ModuleArchiveUnarchiveEndpoint.get_queryset (archived=true), parameters
// $1 project, $2 user, $3 slug; extra conditions take $4 onward. Every
// annotation is computed: the views pick from them.
func moduleAnnotatedSQL(archived bool, extra string) string {
	var b strings.Builder
	b.WriteString(`SELECT m.id, m.workspace_id, m.project_id, m.name, m.description, m.description_text::text,
		m.description_html::text, m.start_date, m.target_date, m.status, m.lead_id, `)
	b.WriteString(moduleMemberIDsSQL(!archived))
	b.WriteString(`, m.view_props::text, m.sort_order, m.external_source, m.external_id, m.logo_props::text,
		m.archived_at, m.created_at, m.updated_at,
		EXISTS (SELECT 1 FROM user_favorites f JOIN workspaces fw ON fw.id = f.workspace_id
			WHERE f.deleted_at IS NULL AND f.entity_identifier = m.id AND f.entity_type = 'module' AND f.project_id = $1
				AND f.user_id = $2 AND fw.slug = $3) AS is_favorite`)
	for _, g := range append(moduleStateGroups, "") {
		b.WriteString(", " + moduleIssueCountSQL(g))
	}
	for _, g := range append(moduleStateGroups, "") {
		b.WriteString(", " + moduleEstimateSQL(g))
	}
	b.WriteString(`
		FROM modules m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.deleted_at IS NULL AND m.project_id = $1 AND w.slug = $3`)
	if archived {
		b.WriteString(` AND m.archived_at IS NOT NULL`)
	}
	b.WriteString(extra)
	b.WriteString(` ORDER BY is_favorite DESC, m.created_at DESC`)
	return b.String()
}

// moduleRow is one annotated module.
type moduleRow struct {
	ID, WorkspaceID, ProjectID       uuid.UUID
	Name, Description, Status        string
	DescriptionText, DescriptionHTML jsontext.Value
	StartDate, TargetDate            *httpx.Date
	LeadID                           *uuid.UUID
	MemberIDs                        []uuid.UUID
	ViewProps, LogoProps             jsontext.Value
	SortOrder                        float64
	ExternalSource, ExternalID       *string
	ArchivedAt                       *time.Time
	CreatedAt, UpdatedAt             time.Time
	IsFavorite                       bool
	// Issues and Estimates are indexed like moduleStateGroups, with the
	// total last.
	Issues    [6]int
	Estimates [6]float64
}

func scanModuleRow(row pgx.Row) (*moduleRow, error) {
	var (
		m             moduleRow
		text, html    *string
		view, logo    string
		start, target *time.Time
		issues        [6]*int
		estimates     [6]*float64
	)
	dest := []any{&m.ID, &m.WorkspaceID, &m.ProjectID, &m.Name, &m.Description, &text, &html, &start, &target,
		&m.Status, &m.LeadID, &m.MemberIDs, &view, &m.SortOrder, &m.ExternalSource, &m.ExternalID, &logo,
		&m.ArchivedAt, &m.CreatedAt, &m.UpdatedAt, &m.IsFavorite}
	for i := range issues {
		dest = append(dest, &issues[i])
	}
	for i := range estimates {
		dest = append(dest, &estimates[i])
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	m.DescriptionText, m.DescriptionHTML = jsonOrNull(text), jsonOrNull(html)
	m.ViewProps, m.LogoProps = jsontext.Value(view), jsontext.Value(logo)
	m.StartDate, m.TargetDate = dateOrNil(start), dateOrNil(target)
	for i := range issues {
		if issues[i] != nil {
			m.Issues[i] = *issues[i]
		}
		if estimates[i] != nil {
			m.Estimates[i] = *estimates[i]
		}
	}
	if m.MemberIDs == nil {
		m.MemberIDs = []uuid.UUID{}
	}
	return &m, nil
}

func jsonOrNull(s *string) jsontext.Value {
	if s == nil {
		return jsontext.Value("null")
	}
	return jsontext.Value(*s)
}

func dateOrNil(t *time.Time) *httpx.Date {
	if t == nil {
		return nil
	}
	return &httpx.Date{Time: *t}
}

// modules runs the annotated queryset.
func (a *API) modules(ctx context.Context, archived bool, slug string, projectID, user uuid.UUID, extra string, args ...any) ([]*moduleRow, error) {
	rows, err := a.db.Query(ctx, moduleAnnotatedSQL(archived, extra), append([]any{projectID, user, slug}, args...)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*moduleRow, error) { return scanModuleRow(row) })
}

// moduleByID is get_queryset().filter(pk=id).first(), or nil.
func (a *API) moduleByID(ctx context.Context, archived bool, slug string, projectID, user, id uuid.UUID, extra string) (*moduleRow, error) {
	ms, err := a.modules(ctx, archived, slug, projectID, user, ` AND m.id = $4`+extra, id)
	if err != nil || len(ms) == 0 {
		return nil, err
	}
	return ms[0], nil
}

// moduleCore holds the fields every module shape shares.
type moduleCore struct {
	ID              uuid.UUID      `json:"id"`
	WorkspaceID     uuid.UUID      `json:"workspace_id"`
	ProjectID       uuid.UUID      `json:"project_id"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	DescriptionText jsontext.Value `json:"description_text"`
	DescriptionHTML jsontext.Value `json:"description_html"`
	StartDate       *httpx.Date    `json:"start_date"`
	TargetDate      *httpx.Date    `json:"target_date"`
	Status          string         `json:"status"`
	LeadID          *uuid.UUID     `json:"lead_id"`
	MemberIDs       []uuid.UUID    `json:"member_ids"`
	ViewProps       jsontext.Value `json:"view_props"`
	SortOrder       float64        `json:"sort_order"`
	ExternalSource  *string        `json:"external_source"`
	ExternalID      *string        `json:"external_id"`
	TotalIssues     int            `json:"total_issues"`
	CancelledIssues int            `json:"cancelled_issues"`
	CompletedIssues int            `json:"completed_issues"`
	StartedIssues   int            `json:"started_issues"`
	UnstartedIssues int            `json:"unstarted_issues"`
	BacklogIssues   int            `json:"backlog_issues"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

func (m *moduleRow) core() moduleCore {
	return moduleCore{
		ID: m.ID, WorkspaceID: m.WorkspaceID, ProjectID: m.ProjectID, Name: m.Name, Description: m.Description,
		DescriptionText: m.DescriptionText, DescriptionHTML: m.DescriptionHTML, StartDate: m.StartDate,
		TargetDate: m.TargetDate, Status: m.Status, LeadID: m.LeadID, MemberIDs: m.MemberIDs, ViewProps: m.ViewProps,
		SortOrder: m.SortOrder, ExternalSource: m.ExternalSource, ExternalID: m.ExternalID, TotalIssues: m.Issues[5],
		CancelledIssues: m.Issues[3], CompletedIssues: m.Issues[4], StartedIssues: m.Issues[2],
		UnstartedIssues: m.Issues[1], BacklogIssues: m.Issues[0], CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// moduleValues is the .values() dict ModuleViewSet's list, create and
// partial_update answer with (created_at/updated_at in the user's
// timezone through user_timezone_converter).
type moduleValues struct {
	moduleCore              `json:",inline"`
	LogoProps               jsontext.Value `json:"logo_props"`
	IsFavorite              bool           `json:"is_favorite"`
	CompletedEstimatePoints float64        `json:"completed_estimate_points"`
	TotalEstimatePoints     float64        `json:"total_estimate_points"`
}

func (m *moduleRow) values() moduleValues {
	return moduleValues{moduleCore: m.core(), LogoProps: m.LogoProps, IsFavorite: m.IsFavorite,
		CompletedEstimatePoints: m.Estimates[4], TotalEstimatePoints: m.Estimates[5]}
}

// moduleSerialized is ModuleSerializer on an annotated module.
type moduleSerialized struct {
	moduleValues `json:",inline"`
	ArchivedAt   *time.Time `json:"archived_at"`
}

func (m *moduleRow) serialized() moduleSerialized {
	return moduleSerialized{moduleValues: m.values(), ArchivedAt: m.ArchivedAt}
}

// moduleDetail is ModuleDetailSerializer.
type moduleDetail struct {
	moduleSerialized        `json:",inline"`
	LinkModule              []*moduleLink `json:"link_module"`
	SubIssues               int           `json:"sub_issues"`
	BacklogEstimatePoints   float64       `json:"backlog_estimate_points"`
	UnstartedEstimatePoints float64       `json:"unstarted_estimate_points"`
	StartedEstimatePoints   float64       `json:"started_estimate_points"`
	CancelledEstimatePoints float64       `json:"cancelled_estimate_points"`
}

// listModules ports ModuleViewSet.list. With ?fields= the rows go through
// ModuleSerializer (DynamicBaseSerializer ignores the field list).
func (a *API) listModules(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ms, err := a.modules(c.Context(), false, c.Param("slug"), projectID, c.User.ID, ` AND m.archived_at IS NULL`)
	if err != nil {
		return err
	}
	if moduleFieldsGiven(c) {
		out := make([]moduleSerialized, len(ms))
		for i, m := range ms {
			out[i] = m.serialized()
		}
		return c.JSON(http.StatusOK, out)
	}
	out := make([]moduleValues, len(ms))
	for i, m := range ms {
		out[i] = m.values()
	}
	return c.JSON(http.StatusOK, out)
}

// moduleFieldsGiven is BaseViewSet.fields: a non-empty ?fields= entry.
func moduleFieldsGiven(c *httpx.Ctx) bool {
	for _, f := range strings.Split(c.Query("fields"), ",") {
		if f != "" {
			return true
		}
	}
	return false
}

// moduleStatuses are Module.status's choices.
var moduleStatuses = []string{"backlog", "planned", "in-progress", "paused", "completed", "cancelled"}

// moduleWrite is ModuleWriteSerializer's validated data.
type moduleWrite struct {
	set               setList
	name              *string
	members           []uuid.UUID
	membersGiven      bool
	startGiven        bool
	targetGiven       bool
	startDate, target *time.Time
}

var (
	errModuleNameTaken = httpx.Err(http.StatusBadRequest, "Module with this name already exists")
	errModuleNotFound  = httpx.Err(http.StatusNotFound, "Module not found")
)

// validateModuleWrite runs ModuleWriteSerializer (fields "__all__" plus
// lead_id and member_ids) and its validate() over request.data.
func (a *API) validateModuleWrite(ctx context.Context, data *drf.Data, loc *time.Location, partial bool) (*moduleWrite, error) {
	v := drf.NewValidator(data, loc)
	w := &moduleWrite{}
	if !partial {
		v.Require("name")
	}
	userExists := func(id uuid.UUID) (bool, error) {
		var ok bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&ok)
		return ok, err
	}
	// lead_id and lead share the source "lead"; lead comes later and wins.
	for _, f := range []string{"lead_id", "lead"} {
		id, ok, err := v.PK(f, true, userExists)
		if err != nil {
			return nil, err
		}
		if ok {
			w.set.drop("lead_id")
			w.set.add("lead_id", id)
		}
	}
	members, ok, err := v.PKList("member_ids", userExists)
	if err != nil {
		return nil, err
	}
	if ok {
		w.members, w.membersGiven = members, true
	}
	if s, ok := v.Char("name", drf.CharField{MaxLength: 255}); ok {
		w.name = s
		w.set.add("name", *s)
	}
	if s, ok := v.Char("description", drf.CharField{AllowBlank: true}); ok {
		w.set.add("description", *s)
	}
	for _, col := range []string{"description_text", "description_html"} {
		if raw, ok := v.JSON(col, true); ok {
			w.set.addCast(col, string(raw), "::jsonb")
		}
	}
	if t, ok := v.Date("start_date", true); ok {
		w.startGiven, w.startDate = true, t
		w.set.add("start_date", t)
	}
	if t, ok := v.Date("target_date", true); ok {
		w.targetGiven, w.target = true, t
		w.set.add("target_date", t)
	}
	if s, ok := v.Choice("status", moduleStatuses, drf.ChoiceField{}); ok {
		w.set.add("status", *s)
	}
	for _, col := range []string{"view_props", "logo_props"} {
		if raw, ok := v.JSON(col, false); ok {
			w.set.addCast(col, string(raw), "::jsonb")
		}
	}
	if f, ok := v.Float("sort_order"); ok {
		w.set.add("sort_order", f)
	}
	for _, col := range []string{"external_source", "external_id"} {
		if s, ok := v.Char(col, drf.CharField{MaxLength: 255, AllowBlank: true, AllowNull: true}); ok {
			w.set.add(col, s)
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	// validate(): both dates given and out of order.
	if w.startDate != nil && w.target != nil && w.startDate.After(*w.target) {
		return nil, httpx.Body(http.StatusBadRequest, map[string][]string{
			"non_field_errors": {"Start date cannot exceed target date"}})
	}
	return w, nil
}

// moduleNameTaken is the serializers' lookup for a live module of the
// project with this name, other than except.
func (a *API) moduleNameTaken(ctx context.Context, name string, projectID uuid.UUID, except *uuid.UUID) (bool, error) {
	var taken bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM modules WHERE name = $1 AND project_id = $2
		AND deleted_at IS NULL AND id IS DISTINCT FROM $3)`, name, projectID, except).Scan(&taken)
	return taken, err
}

// addModuleMembers is ModuleMember.objects.bulk_create(..., ignore_conflicts=True).
func (a *API) addModuleMembers(ctx context.Context, moduleID, projectID, workspaceID uuid.UUID, members []uuid.UUID, createdBy, updatedBy *uuid.UUID) error {
	if len(members) == 0 {
		return nil
	}
	_, err := a.db.Exec(ctx, `INSERT INTO module_members (id, created_at, updated_at, module_id, member_id, project_id,
			workspace_id, created_by_id, updated_by_id)
		SELECT gen_random_uuid(), now(), now(), $1, u, $2, $3, $4, $5 FROM unnest($6::uuid[]) AS u
		ON CONFLICT DO NOTHING`, moduleID, projectID, workspaceID, createdBy, updatedBy, members)
	return err
}

// createModule ports ModuleViewSet.create.
func (a *API) createModule(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT p.workspace_id FROM projects p JOIN workspaces w ON w.id = p.workspace_id
		WHERE w.slug = $1 AND p.id = $2 AND p.deleted_at IS NULL`, slug, projectID).Scan(&workspaceID); err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	w, err := a.validateModuleWrite(ctx, data, c.Loc(), false)
	if err != nil {
		return err
	}
	if taken, err := a.moduleNameTaken(ctx, *w.name, projectID, nil); err != nil {
		return err
	} else if taken {
		return errModuleNameTaken
	}
	// Module.save(): a new module goes 10000 below the project's lowest.
	var smallest *float64
	if err := a.db.QueryRow(ctx, `SELECT min(sort_order) FROM modules WHERE project_id = $1 AND deleted_at IS NULL`,
		projectID).Scan(&smallest); err != nil {
		return err
	}
	if smallest != nil {
		w.set.drop("sort_order")
		w.set.add("sort_order", *smallest-10000)
	}
	id := uuid.New()
	w.set.add("id", id)
	w.set.add("project_id", projectID)
	w.set.add("workspace_id", workspaceID)
	w.set.add("created_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, w.set.insertSQL("modules"), w.set.args...); err != nil {
		return err
	}
	if w.membersGiven {
		if err := a.addModuleMembers(ctx, id, projectID, workspaceID, w.members, &c.User.ID, nil); err != nil {
			return err
		}
	}
	m, err := a.moduleByID(ctx, false, slug, projectID, c.User.ID, id, "")
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, m.values())
}

// updateModule ports ModuleViewSet.partial_update.
func (a *API) updateModule(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	cur, err := a.moduleByID(ctx, false, slug, projectID, c.User.ID, pk, "")
	if err != nil {
		return err
	}
	if cur == nil {
		return errModuleNotFound
	}
	if cur.ArchivedAt != nil {
		return httpx.Err(http.StatusBadRequest, "Archived module cannot be updated")
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	w, err := a.validateModuleWrite(ctx, data, c.Loc(), true)
	if err != nil {
		return err
	}
	// ModuleWriteSerializer.update
	if w.name != nil && *w.name != "" {
		if taken, err := a.moduleNameTaken(ctx, *w.name, projectID, &cur.ID); err != nil {
			return err
		} else if taken {
			return errModuleNameTaken
		}
	}
	if w.membersGiven {
		var createdBy, updatedBy *uuid.UUID
		if err := a.db.QueryRow(ctx, `SELECT created_by_id, updated_by_id FROM modules WHERE id = $1`, cur.ID).
			Scan(&createdBy, &updatedBy); err != nil {
			return err
		}
		if _, err := a.db.Exec(ctx, `UPDATE module_members SET deleted_at = now() WHERE module_id = $1
			AND deleted_at IS NULL`, cur.ID); err != nil {
			return err
		}
		if err := a.addModuleMembers(ctx, cur.ID, cur.ProjectID, cur.WorkspaceID, w.members, createdBy, updatedBy); err != nil {
			return err
		}
	}
	w.set.add("updated_at", time.Now())
	w.set.add("updated_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, `UPDATE modules SET `+w.set.sql()+` WHERE id = $1`,
		append([]any{cur.ID}, w.set.args...)...); err != nil {
		return err
	}
	m, err := a.moduleByID(ctx, false, slug, projectID, c.User.ID, pk, "")
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m.values())
}

// allowModuleCreator is @allow_permission(roles, creator=True, model=Module):
// a workspace member who created the module passes, others need the role.
func (a *API) allowModuleCreator(roles []int, h httpx.HandlerFunc) httpx.HandlerFunc {
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
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM modules
			WHERE id = $1 AND created_by_id = $2 AND deleted_at IS NULL)`, pk, c.User.ID).Scan(&creator); err != nil {
			return err
		}
		if creator {
			return h(c)
		}
		return a.allowProject(roles, h)(c)
	}
}

// moduleActivity sends issue_activity for a module-issue change;
// moduleIDJSON is the JSON of requested_data's module_id.
func (a *API) moduleActivity(ctx context.Context, c *httpx.Ctx, typ string, moduleIDJSON string, current *string, issueID string, projectID uuid.UUID) {
	requested := `{"module_id": ` + moduleIDJSON + `}`
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: typ, RequestedData: &requested, CurrentInstance: current, IssueID: issueID,
		ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
		Subscriber: true, Notification: true,
	})
}

// moduleNameJSON is json.dumps({"module_name": name}).
func moduleNameJSON(name *string) *string {
	s := `{"module_name": null}`
	if name != nil {
		s = `{"module_name": ` + string(jsonString(*name)) + `}`
	}
	return &s
}

// deleteModule ports ModuleViewSet.destroy.
func (a *API) deleteModule(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var (
		id   uuid.UUID
		name string
	)
	if err := a.db.QueryRow(ctx, `SELECT m.id, m.name FROM modules m JOIN workspaces w ON w.id = m.workspace_id
		WHERE w.slug = $1 AND m.project_id = $2 AND m.id = $3 AND m.deleted_at IS NULL`, slug, projectID, pk).
		Scan(&id, &name); err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `SELECT issue_id FROM module_issues WHERE module_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`, id)
	if err != nil {
		return err
	}
	issues, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, issue := range issues {
		a.moduleActivity(ctx, c, "module.activity.deleted", string(jsonString(id.String())), moduleNameJSON(&name), issue.String(), projectID)
	}
	if err := softdelete.Row(ctx, a.db, "modules", id, c.User.ID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE module_issues SET deleted_at = now() WHERE module_id = $1 AND project_id = $2
		AND deleted_at IS NULL`, id, projectID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE user_favorites SET deleted_at = now() WHERE user_id = $1
		AND entity_type = 'module' AND entity_identifier = $2 AND project_id = $3 AND deleted_at IS NULL`,
		c.User.ID, id, projectID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `DELETE FROM user_recent_visits v USING workspaces w WHERE w.id = v.workspace_id
		AND v.project_id = $1 AND w.slug = $2 AND v.entity_identifier = $3 AND v.entity_name = 'module'
		AND v.deleted_at IS NULL`, projectID, slug, id); err != nil {
		return err
	}
	return c.NoContent()
}

// retrieveModule ports ModuleViewSet.retrieve.
func (a *API) retrieveModule(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	m, err := a.moduleByID(ctx, false, slug, projectID, c.User.ID, pk, ` AND m.archived_at IS NULL`)
	if err != nil {
		return err
	}
	if m == nil {
		return errModuleNotFound
	}
	body, err := a.moduleDetailBody(ctx, c, slug, projectID, pk, m, false)
	if err != nil {
		return err
	}
	a.recordVisit(ctx, slug, "module", pk, c.User.ID, &projectID)
	return c.JSON(http.StatusOK, body)
}

// moduleWorkspaceItem is ModuleSerializer on WorkspaceModulesEndpoint's
// queryset, which lacks is_favorite and the estimate annotations: DRF
// skips those read-only fields.
type moduleWorkspaceItem struct {
	moduleCore `json:",inline"`
	LogoProps  jsontext.Value `json:"logo_props"`
	ArchivedAt *time.Time     `json:"archived_at"`
}

// workspaceModules ports WorkspaceModulesEndpoint.get: the live modules of
// unarchived projects the user has an active membership row in. The issue
// counts read module_issues joined to issues and states (no IssueManager).
func (a *API) workspaceModules(c *httpx.Ctx) error {
	count := func(group string) string {
		cond := ""
		if group != "" {
			cond = ` AND s."group" = '` + group + `'`
		}
		return `count(DISTINCT mi.id) FILTER (WHERE mi.deleted_at IS NULL AND i.archived_at IS NULL AND NOT i.is_draft` + cond + `)`
	}
	rows, err := a.db.Query(c.Context(), `SELECT m.id, m.workspace_id, m.project_id, m.name, m.description,
			m.description_text::text, m.description_html::text, m.start_date, m.target_date, m.status, m.lead_id,
			COALESCE(array_agg(DISTINCT mm.member_id) FILTER (WHERE mm.member_id IS NOT NULL AND mm.deleted_at IS NULL), '{}'),
			m.view_props::text, m.sort_order, m.external_source, m.external_id, m.logo_props::text, m.archived_at,
			m.created_at, m.updated_at, `+count("")+`, `+count("cancelled")+`, `+count("completed")+`, `+
		count("started")+`, `+count("unstarted")+`, `+count("backlog")+`
		FROM modules m JOIN projects p ON p.id = m.project_id JOIN project_members pm ON pm.project_id = p.id
			JOIN workspaces w ON w.id = m.workspace_id
			LEFT JOIN module_issues mi ON mi.module_id = m.id LEFT JOIN issues i ON i.id = mi.issue_id
			LEFT JOIN states s ON s.id = i.state_id LEFT JOIN module_members mm ON mm.module_id = m.id
		WHERE m.deleted_at IS NULL AND p.archived_at IS NULL AND pm.is_active AND pm.member_id = $2 AND w.slug = $1
			AND m.archived_at IS NULL
		GROUP BY m.id ORDER BY m.created_at DESC`, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []moduleWorkspaceItem{}
	for rows.Next() {
		var (
			it            moduleWorkspaceItem
			text, html    *string
			view, logo    string
			start, target *time.Time
			mc            = &it.moduleCore
		)
		if err := rows.Scan(&mc.ID, &mc.WorkspaceID, &mc.ProjectID, &mc.Name, &mc.Description, &text, &html, &start,
			&target, &mc.Status, &mc.LeadID, &mc.MemberIDs, &view, &mc.SortOrder, &mc.ExternalSource, &mc.ExternalID,
			&logo, &it.ArchivedAt, &mc.CreatedAt, &mc.UpdatedAt, &mc.TotalIssues, &mc.CancelledIssues,
			&mc.CompletedIssues, &mc.StartedIssues, &mc.UnstartedIssues, &mc.BacklogIssues); err != nil {
			return err
		}
		mc.DescriptionText, mc.DescriptionHTML = jsonOrNull(text), jsonOrNull(html)
		mc.ViewProps, it.LogoProps = jsontext.Value(view), jsontext.Value(logo)
		mc.StartDate, mc.TargetDate = dateOrNil(start), dateOrNil(target)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}
