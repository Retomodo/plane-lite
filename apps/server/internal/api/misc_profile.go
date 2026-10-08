package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// The workspace profile pages (W/user-*/<user_id>/): a member's stats,
// projects, activity and issues as another member sees them. Every query
// joins the viewer's project memberships (project__project_projectmember__
// member=request.user) without collapsing the rows that join can repeat,
// and counts over multi-valued joins exactly as Django's do.

// profileIssueFrom is the Issue queryset's joins plus the viewer's project
// membership.
const profileIssueFrom = ` FROM issues i LEFT JOIN states s ON s.id = i.state_id JOIN projects p ON p.id = i.project_id
	JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = i.workspace_id`

// stateCount and priorityCount are the distribution rows (.values() dicts).
type stateCount struct {
	StateGroup *string `json:"state_group"`
	StateCount int64   `json:"state_count"`
}

type priorityCount struct {
	Priority      *string `json:"priority"`
	PriorityCount int64   `json:"priority_count"`
	PriorityOrder int     `json:"priority_order"`
}

type cycleRef struct {
	Name      string    `json:"cycle__name"`
	ID        uuid.UUID `json:"cycle__id"`
	ProjectID uuid.UUID `json:"cycle__project_id"`
}

type userStats struct {
	StateDistribution    []stateCount    `json:"state_distribution"`
	PriorityDistribution []priorityCount `json:"priority_distribution"`
	CreatedIssues        int64           `json:"created_issues"`
	AssignedIssues       int64           `json:"assigned_issues"`
	CompletedIssues      int64           `json:"completed_issues"`
	PendingIssues        int64           `json:"pending_issues"`
	SubscribedIssues     int64           `json:"subscribed_issues"`
	PresentCycles        []cycleRef      `json:"present_cycles"`
	UpcomingCycles       []cycleRef      `json:"upcoming_cycles"`
}

// statsQuery is one of the stats view's Issue querysets: its filter() call
// (the user as creator, or as a live assignee, plus extra), then
// .filter(**filters) with the legacy issue filters in a call of their own.
func (a *API) statsQuery(c *httpx.Ctx, user uuid.UUID, assigned bool, extra string) (*issueQuery, error) {
	q := &issueQuery{tz: c.Loc().String()}
	q.filter(issueBaseWhere)
	if assigned {
		ia := q.rel("issue_assignees", newScope())
		q.filter(ia + ".assignee_id = " + q.arg(user) + " AND " + ia + ".deleted_at IS NULL")
	} else {
		q.filter("i.created_by_id = " + q.arg(user))
	}
	q.filter(extra)
	q.filter("pm.is_active AND pm.member_id = " + q.arg(c.User.ID) + " AND w.slug = " + q.arg(c.Param("slug")))
	lookups, err := q.legacyFilters(c.QueryValues(), false)
	if err != nil {
		return nil, err
	}
	sc := newScope()
	var conds []string
	for _, l := range lookups {
		conds = append(conds, q.render(l, sc, false, false))
	}
	q.filter(strings.Join(conds, " AND "))
	return q, nil
}

func (a *API) countSQL(ctx context.Context, q *issueQuery) (int64, error) {
	var n int64
	err := a.db.QueryRow(ctx, "SELECT count(*)"+profileIssueFrom+q.joinSQL()+" WHERE "+q.whereSQL(), q.args...).Scan(&n)
	return n, err
}

// subscriberColumns maps the issue columns a legacy filter may name to
// IssueSubscriber's; the subscription count crashes (FieldError) on any
// other filter.
var subscriberColumns = map[string]string{"i.project_id": "x.project_id", "i.created_by_id": "x.created_by_id"}

// subscriberFilter renders the legacy filters on an IssueSubscriber
// queryset, or fails as Django does on a field it lacks.
func (q *issueQuery) subscriberFilter(params map[string][]string) (string, error) {
	lookups, err := q.legacyFilters(params, false)
	if err != nil {
		return "", err
	}
	var conds []string
	for _, l := range lookups {
		col, ok := subscriberColumns[l.col]
		for _, f := range []string{"created_at", "updated_at"} {
			if prefix := "(i." + f + " AT TIME ZONE "; strings.HasPrefix(l.col, prefix) {
				col, ok = "(x."+f+" AT TIME ZONE "+strings.TrimPrefix(l.col, prefix), true
			}
		}
		if l.rel != "" || !ok {
			return "", errViewCrash
		}
		conds = append(conds, l.cond(col))
	}
	return strings.Join(conds, " AND "), nil
}

// userProfileStats ports WorkspaceUserProfileStatsEndpoint.get. It has no
// permission class: anyone signed in gets counts over the projects they
// are a member of. The legacy filters also apply to the subscriptions,
// which crash on any filter that isn't a field of IssueSubscriber.
func (a *API) userProfileStats(c *httpx.Ctx) error {
	user, err := c.UUIDParam("user_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var out userStats

	type countSpec struct {
		dst      *int64
		assigned bool
		extra    string
	}
	for _, s := range []countSpec{
		{&out.CreatedIssues, false, ""},
		{&out.AssignedIssues, true, ""},
		{&out.PendingIssues, true, `NOT (s."group" IN ('completed', 'cancelled') AND s."group" IS NOT NULL)`},
		{&out.CompletedIssues, true, `s."group" = 'completed'`},
	} {
		q, err := a.statsQuery(c, user, s.assigned, s.extra)
		if err != nil {
			return err
		}
		if *s.dst, err = a.countSQL(ctx, q); err != nil {
			return err
		}
	}

	sq := &issueQuery{tz: c.Loc().String()}
	sq.filter("x.deleted_at IS NULL AND p.archived_at IS NULL AND pm.is_active AND pm.member_id = " + sq.arg(c.User.ID) +
		" AND x.subscriber_id = " + sq.arg(user) + " AND w.slug = " + sq.arg(slug))
	cond, err := sq.subscriberFilter(c.QueryValues())
	if err != nil {
		return err
	}
	sq.filter(cond)
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM issue_subscribers x JOIN projects p ON p.id = x.project_id
		JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = x.workspace_id
		WHERE `+sq.whereSQL(), sq.args...).Scan(&out.SubscribedIssues); err != nil {
		return err
	}

	q, err := a.statsQuery(c, user, true, "")
	if err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `SELECT s."group", count(s."group")`+profileIssueFrom+q.joinSQL()+" WHERE "+q.whereSQL()+
		" GROUP BY 1 ORDER BY 1 ASC", q.args...)
	if err != nil {
		return err
	}
	if out.StateDistribution, err = pgx.CollectRows(rows, pgx.RowToStructByPos[stateCount]); err != nil {
		return err
	}

	q, err = a.statsQuery(c, user, true, "")
	if err != nil {
		return err
	}
	order := "CASE"
	for i, p := range priorityOrder {
		order += fmt.Sprintf(" WHEN i.priority = '%s' THEN %d", p, i)
	}
	order += fmt.Sprintf(" ELSE %d END", len(priorityOrder))
	rows, err = a.db.Query(ctx, `SELECT i.priority, count(i.priority), `+order+profileIssueFrom+q.joinSQL()+
		" WHERE "+q.whereSQL()+" GROUP BY 1, 3 HAVING count(i.priority) >= 1 ORDER BY 3 ASC", q.args...)
	if err != nil {
		return err
	}
	if out.PriorityDistribution, err = pgx.CollectRows(rows, pgx.RowToStructByPos[priorityCount]); err != nil {
		return err
	}

	// The cycles ignore the filters and the viewer's memberships, and see
	// every assignee row of the issue, removed or not.
	cycles := func(when string) ([]cycleRef, error) {
		rows, err := a.db.Query(ctx, `SELECT c.name, ci.cycle_id, c.project_id FROM cycle_issues ci
			JOIN cycles c ON c.id = ci.cycle_id JOIN issues i ON i.id = ci.issue_id
			JOIN issue_assignees ia ON ia.issue_id = i.id JOIN workspaces w ON w.id = ci.workspace_id
			WHERE ci.deleted_at IS NULL AND `+when+` AND ia.assignee_id = $1 AND w.slug = $2
			ORDER BY ci.created_at DESC`, user, slug)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByPos[cycleRef])
	}
	if out.PresentCycles, err = cycles("c.end_date > now() AND c.start_date < now()"); err != nil {
		return err
	}
	if out.UpcomingCycles, err = cycles("c.start_date > now()"); err != nil {
		return err
	}
	for _, l := range []*[]cycleRef{&out.PresentCycles, &out.UpcomingCycles} {
		if *l == nil {
			*l = []cycleRef{}
		}
	}
	if out.StateDistribution == nil {
		out.StateDistribution = []stateCount{}
	}
	if out.PriorityDistribution == nil {
		out.PriorityDistribution = []priorityCount{}
	}
	return c.JSON(http.StatusOK, out)
}

// activeMemberRole is WorkspaceMember.objects.get(workspace__slug=slug,
// member=user, is_active=True).role.
func (a *API) activeMemberRole(ctx context.Context, slug string, user uuid.UUID) (int, error) {
	rows, err := a.db.Query(ctx, `SELECT wm.role FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
		WHERE wm.deleted_at IS NULL AND wm.is_active AND wm.member_id = $1 AND w.slug = $2 LIMIT 2`, user, slug)
	if err != nil {
		return 0, err
	}
	roles, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return 0, err
	}
	switch len(roles) {
	case 0:
		return 0, pgx.ErrNoRows
	case 1:
		return roles[0], nil
	}
	return 0, errViewCrash // MultipleObjectsReturned
}

type profileProject struct {
	ID              uuid.UUID      `json:"id"`
	LogoProps       jsontext.Value `json:"logo_props"`
	CreatedIssues   int64          `json:"created_issues"`
	AssignedIssues  int64          `json:"assigned_issues"`
	CompletedIssues int64          `json:"completed_issues"`
	PendingIssues   int64          `json:"pending_issues"`
}

type profileUser struct {
	Email         *string `json:"email"`
	FirstName     string  `json:"first_name"`
	LastName      string  `json:"last_name"`
	AvatarURL     *string `json:"avatar_url"`
	CoverImageURL *string `json:"cover_image_url"`
	DateJoined    utcTime `json:"date_joined"`
	UserTimezone  string  `json:"user_timezone"`
	DisplayName   string  `json:"display_name"`
}

// userProfile ports WorkspaceUserProfileEndpoint.get: the viewer and the
// user must both be active members; workspace admins and members also get
// per-project counts over the projects they belong to. Those counts join
// every issue (deleted ones too) and every assignee row of it.
func (a *API) userProfile(c *httpx.Ctx) error {
	user, err := c.UUIDParam("user_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	role, err := a.activeMemberRole(ctx, slug, c.User.ID)
	if err != nil {
		return err
	}
	if _, err := a.activeMemberRole(ctx, slug, user); err != nil {
		return err
	}
	var (
		u                     profileUser
		avatar                string
		cover                 *string
		avatarID, coverID     *uuid.UUID
		avatarType, coverType *string
		joined                time.Time
	)
	if err := a.db.QueryRow(ctx, `SELECT u.email, u.first_name, u.last_name, u.avatar, u.avatar_asset_id, aa.entity_type,
			u.cover_image, u.cover_image_asset_id, ca.entity_type, u.date_joined, u.user_timezone, u.display_name
		FROM users u LEFT JOIN file_assets aa ON aa.id = u.avatar_asset_id
		LEFT JOIN file_assets ca ON ca.id = u.cover_image_asset_id WHERE u.id = $1`, user).Scan(
		&u.Email, &u.FirstName, &u.LastName, &avatar, &avatarID, &avatarType, &cover, &coverID, &coverType, &joined,
		&u.UserTimezone, &u.DisplayName); err != nil {
		return err
	}
	u.AvatarURL = imageURL(avatarID, avatarType, &avatar)
	u.CoverImageURL = imageURL(coverID, coverType, cover)
	u.DateJoined = utcTime(joined)

	projects := []profileProject{}
	if role >= roleMember {
		filter := func(cond string) string {
			return "count(i.id) FILTER (WHERE i.archived_at IS NULL AND " + cond + " AND NOT i.is_draft)"
		}
		rows, err := a.db.Query(ctx, `SELECT p.id, p.logo_props::text, `+filter("i.created_by_id = $1")+`, `+
			filter("ia.assignee_id = $1")+`, `+filter("ia.assignee_id = $1 AND i.completed_at IS NOT NULL")+`, `+
			filter(`ia.assignee_id = $1 AND s."group" IN ('backlog', 'unstarted', 'started')`)+`
			FROM projects p JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = p.workspace_id
			LEFT JOIN issues i ON i.project_id = p.id LEFT JOIN issue_assignees ia ON ia.issue_id = i.id
			LEFT JOIN states s ON s.id = i.state_id
			WHERE p.deleted_at IS NULL AND p.archived_at IS NULL AND pm.is_active AND pm.member_id = $2 AND w.slug = $3
			GROUP BY p.id`, user, c.User.ID, slug)
		if err != nil {
			return err
		}
		projects, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (profileProject, error) {
			var p profileProject
			var logo string
			err := row.Scan(&p.ID, &logo, &p.CreatedIssues, &p.AssignedIssues, &p.CompletedIssues, &p.PendingIssues)
			p.LogoProps = jsontext.Value(logo)
			return p, err
		})
		if err != nil {
			return err
		}
		if projects == nil {
			projects = []profileProject{}
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"project_data": projects, "user_data": u})
}

// activityFrom is the IssueActivity queryset with its select_related joins
// and the viewer's project membership.
const activityFrom = ` FROM issue_activities a JOIN users u ON u.id = a.actor_id JOIN projects p ON p.id = a.project_id
	JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = a.workspace_id
	LEFT JOIN issues i ON i.id = a.issue_id`

// activityWhere is the shared filter() of the activity views ($1 the
// user, $2 the viewer, $3 the slug).
const activityWhere = ` WHERE a.deleted_at IS NULL
	AND NOT (a.field IN ('comment', 'vote', 'reaction', 'draft') AND a.field IS NOT NULL)
	AND a.actor_id = $1 AND pm.is_active AND pm.member_id = $2 AND w.slug = $3`

const activityCols = `a.id, a.created_at, a.updated_at, a.deleted_at, a.verb, a.field, a.old_value, a.new_value, a.comment,
	a.attachments, a.old_identifier, a.new_identifier, a.epoch, a.created_by_id, a.updated_by_id, a.project_id,
	a.workspace_id, a.issue_id, a.issue_comment_id, a.actor_id,
	u.id, u.first_name, u.last_name, u.avatar, u.is_bot, u.display_name, ua.id, ua.entity_type,
	p.id, p.identifier, p.name, p.cover_image, p.cover_image_asset_id, pa.entity_type, p.logo_props::text, p.description,
	w.id, w.name, w.slug, w.logo, w.logo_asset_id, wa.entity_type,
	i.id, i.name, i.description_json::text, i.description_html, i.priority, i.start_date, i.target_date, i.sequence_id,
	i.sort_order, i.is_draft`

const activityAssetJoins = ` LEFT JOIN file_assets ua ON ua.id = u.avatar_asset_id
	LEFT JOIN file_assets pa ON pa.id = p.cover_image_asset_id LEFT JOIN file_assets wa ON wa.id = w.logo_asset_id`

func scanIssueActivity(row pgx.CollectableRow) (*issueActivityOut, error) {
	x := issueActivityOut{ActorDetail: &userLite{}, ProjectDetail: &projectLite{}, WorkspaceDetail: &workspaceLite{}}
	var (
		avatarID, coverID, logoID           *uuid.UUID
		avatarType, coverType, logoType     *string
		logoProps                           string
		wsLogo                              *string
		issueID                             *uuid.UUID
		issueName, descJSON, descHTML, prio *string
		start, target                       *time.Time
		seq                                 *int
		sortOrder                           *float64
		draft                               *bool
	)
	if err := row.Scan(&x.ID, &x.CreatedAt, &x.UpdatedAt, &x.DeletedAt, &x.Verb, &x.Field, &x.OldValue, &x.NewValue,
		&x.Comment, &x.Attachments, &x.OldIdentifier, &x.NewIdentifier, &x.Epoch, &x.CreatedBy, &x.UpdatedBy,
		&x.Project, &x.Workspace, &x.Issue, &x.IssueComment, &x.Actor,
		&x.ActorDetail.ID, &x.ActorDetail.FirstName, &x.ActorDetail.LastName, &x.ActorDetail.Avatar, &x.ActorDetail.IsBot,
		&x.ActorDetail.DisplayName, &avatarID, &avatarType,
		&x.ProjectDetail.ID, &x.ProjectDetail.Identifier, &x.ProjectDetail.Name, &x.ProjectDetail.CoverImage, &coverID,
		&coverType, &logoProps, &x.ProjectDetail.Description,
		&x.WorkspaceDetail.ID, &x.WorkspaceDetail.Name, &x.WorkspaceDetail.Slug, &wsLogo, &logoID, &logoType,
		&issueID, &issueName, &descJSON, &descHTML, &prio, &start, &target, &seq, &sortOrder, &draft); err != nil {
		return nil, err
	}
	if x.Attachments == nil {
		x.Attachments = []string{}
	}
	x.ActorDetail.AvatarURL = imageURL(avatarID, avatarType, &x.ActorDetail.Avatar)
	x.ProjectDetail.CoverImageURL = imageURL(coverID, coverType, x.ProjectDetail.CoverImage)
	x.ProjectDetail.LogoProps = jsontext.Value(logoProps)
	x.WorkspaceDetail.LogoURL = imageURL(logoID, logoType, wsLogo)
	if issueID != nil {
		f := &issueFlat{ID: *issueID, Name: *issueName, DescriptionJSON: jsontext.Value(*descJSON),
			DescriptionHTML: *descHTML, Priority: *prio, SequenceID: *seq, SortOrder: *sortOrder, IsDraft: *draft}
		if start != nil {
			f.StartDate = &httpx.Date{Time: *start}
		}
		if target != nil {
			f.TargetDate = &httpx.Date{Time: *target}
		}
		x.IssueDetail = f
	}
	return &x, nil
}

// userActivity ports WorkspaceUserActivityEndpoint.get: the user's issue
// activity in the viewer's live projects, offset-paginated.
func (a *API) userActivity(c *httpx.Ctx) error {
	user, err := c.UUIDParam("user_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	args := []any{user, c.User.ID, c.Param("slug")}
	where := activityWhere + " AND p.archived_at IS NULL"
	if projects := c.QueryValues()["project"]; len(projects) > 0 {
		ids := make([]string, len(projects))
		for i, s := range projects {
			id, ok := drf.ParseUUID(s)
			if !ok {
				return errFilterDetail // UUIDField.to_python raises ValidationError
			}
			ids[i] = id.String()
		}
		args = append(args, ids)
		where += " AND a.project_id = ANY($4::uuid[])"
	}
	field, desc := sanitizeOrderBy(c.Query("order_by"), []string{"created_at", "updated_at"}, "-created_at")
	page, err := parseOffsetPage(c)
	if err != nil {
		return err
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	var total int64
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM issue_activities a JOIN projects p ON p.id = a.project_id
		JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = a.workspace_id`+where,
		args...).Scan(&total); err != nil {
		return err
	}
	n := len(args)
	rows, err := a.db.Query(ctx, fmt.Sprintf(`SELECT %s%s%s%s ORDER BY a.%s %s NULLS LAST, a.created_at DESC LIMIT $%d OFFSET $%d`,
		activityCols, activityFrom, activityAssetJoins, where, field, dir, n+1, n+2), append(args, page.limit+1, page.offset)...)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, scanIssueActivity)
	if err != nil {
		return err
	}
	count := len(items)
	if items == nil {
		items = []*issueActivityOut{}
	}
	return c.JSON(http.StatusOK, page.response(items[:min(int64(count), page.limit)], count, total))
}

// csvFormulaTriggers are the leading characters sanitize_csv_value quotes.
const csvFormulaTriggers = "=+-@\t\r\n"

// csvRow is csv.writer(quoting=QUOTE_ALL).writerow over sanitize_csv_row:
// None is an empty field.
func csvRow(b *strings.Builder, cells []*string) {
	for i, c := range cells {
		if i > 0 {
			b.WriteByte(',')
		}
		v := ""
		if c != nil {
			v = *c
			if v != "" && strings.ContainsRune(csvFormulaTriggers, rune(v[0])) {
				v = "'" + v
			}
		}
		b.WriteString(`"` + strings.ReplaceAll(v, `"`, `""`) + `"`)
	}
	b.WriteString("\r\n")
}

// pyDateTime is str() of an aware UTC datetime.
func pyDateTime(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 != 0 {
		return t.Format("2006-01-02 15:04:05.000000") + "+00:00"
	}
	return t.Format("2006-01-02 15:04:05") + "+00:00"
}

// exportUserActivity ports ExportWorkspaceUserActivityEndpoint.post: the
// user's activity on one date (in the viewer's time zone) as a CSV
// download, archived projects included.
func (a *API) exportUserActivity(c *httpx.Ctx) error {
	user, err := c.UUIDParam("user_id")
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get on a list
	}
	v, ok := data.Get("date")
	if !ok || !drf.PyTruthy(drf.JSONValue(v.Raw())) {
		return httpx.Err(http.StatusBadRequest, "Date is required")
	}
	if !v.IsString() {
		return errViewCrash // parse_date on a non-string: TypeError
	}
	day, ok := drf.ParseDate(v.Str())
	if !ok {
		return errFilterDetail
	}
	rows, err := a.db.Query(c.Context(), `SELECT u.display_name, p.identifier, i.sequence_id, p.name, a.created_at,
			a.updated_at, a.verb, a.field, a.old_value, a.new_value`+activityFrom+activityWhere+`
			AND (a.created_at AT TIME ZONE $4)::date = $5::date
		ORDER BY a.created_at DESC LIMIT 10000`, user, c.User.ID, c.Param("slug"), c.Loc().String(), day.Format(time.DateOnly))
	if err != nil {
		return err
	}
	var b strings.Builder
	header := []string{"Actor name", "Issue ID", "Project", "Created at", "Updated at", "Action", "Field", "Old value", "New value"}
	cells := make([]*string, len(header))
	for i := range header {
		cells[i] = &header[i]
	}
	csvRow(&b, cells)
	defer rows.Close()
	for rows.Next() {
		var (
			name, ident, project, verb string
			seq                        *int
			created, updated           time.Time
			field, oldValue, newValue  *string
		)
		if err := rows.Scan(&name, &ident, &seq, &project, &created, &updated, &verb, &field, &oldValue,
			&newValue); err != nil {
			return err
		}
		// f"{identifier} - {sequence_id if issue else ''}"
		issue := ident + " - "
		if seq != nil {
			issue += fmt.Sprint(*seq)
		}
		createdS, updatedS := pyDateTime(created), pyDateTime(updated)
		csvRow(&b, []*string{&name, &issue, &project, &createdS, &updatedS, &verb, field, oldValue, newValue})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	h := c.W.Header()
	h.Set("Content-Type", "text/csv")
	h.Set("Content-Disposition", `attachment; filename="workspace-user-activity.csv"`)
	c.W.WriteHeader(http.StatusOK)
	_, err = c.W.Write([]byte(b.String()))
	return err
}

// userProfileIssues ports WorkspaceUserProfileIssuesEndpoint.get: the
// issues the user is assigned to (removed assignments included), created
// or subscribed to, in the viewer's projects, through IssueViewSet.list's
// filters, ordering, grouping and paginators. Unlike that view it has no
// DISTINCT, its counts are 0 rather than null, and the legacy filters
// skip updated_at__gt.
func (a *API) userProfileIssues(c *httpx.Ctx) error {
	user, err := c.UUIDParam("user_id")
	if err != nil {
		return err
	}
	l := &issueList{a: a, c: c, slug: c.Param("slug"), from: profileIssueFrom, plainCounts: true, workspace: true,
		q: &issueQuery{tz: c.Loc().String()}}
	q := l.q
	q.filter(issueBaseWhere)
	u, slug := q.arg(user), q.arg(l.slug)
	q.filter(`i.id IN (SELECT u0.id FROM issues u0 LEFT JOIN states u1 ON u1.id = u0.state_id
			JOIN projects u2 ON u2.id = u0.project_id LEFT JOIN issue_assignees u3 ON u3.issue_id = u0.id
			LEFT JOIN issue_subscribers u6 ON u6.issue_id = u0.id JOIN workspaces u8 ON u8.id = u0.workspace_id
		WHERE u0.deleted_at IS NULL AND u1."group" IS DISTINCT FROM 'triage' AND u0.archived_at IS NULL
			AND u2.archived_at IS NULL AND NOT u0.is_draft
			AND (u3.assignee_id = ` + u + ` OR u0.created_by_id = ` + u + ` OR u6.subscriber_id = ` + u + `)
			AND u8.slug = ` + slug + `)`)
	q.filter("pm.is_active AND pm.member_id = " + q.arg(c.User.ID) + " AND w.slug = " + slug)
	if err := l.applyFilters(false); err != nil {
		return err
	}
	l.filtered = q.clone()
	l.prepare(c.Query("order_by"), c.Query("group_by"), c.Query("sub_group_by"))
	return l.paginate(c.Context())
}
