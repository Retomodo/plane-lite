package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// SubIssuesEndpoint (P/issues/<issue_id>/sub-issues/).

// subIssueValues is one dict of the list's .values(); created_at and
// updated_at go through user_timezone_converter, completed_at stays UTC.
type subIssueValues struct {
	ID              uuid.UUID   `json:"id"`
	Name            string      `json:"name"`
	StateID         *uuid.UUID  `json:"state_id"`
	SortOrder       float64     `json:"sort_order"`
	CompletedAt     *time.Time  `json:"-"`
	CompletedAtUTC  *utcTime    `json:"completed_at"`
	EstimatePoint   *uuid.UUID  `json:"estimate_point"`
	Priority        string      `json:"priority"`
	StartDate       *httpx.Date `json:"start_date"`
	TargetDate      *httpx.Date `json:"target_date"`
	SequenceID      int         `json:"sequence_id"`
	ProjectID       uuid.UUID   `json:"project_id"`
	ParentID        *uuid.UUID  `json:"parent_id"`
	CycleID         *uuid.UUID  `json:"cycle_id"`
	ModuleIDs       []uuid.UUID `json:"module_ids"`
	LabelIDs        []uuid.UUID `json:"label_ids"`
	AssigneeIDs     []uuid.UUID `json:"assignee_ids"`
	SubIssuesCount  int         `json:"sub_issues_count"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	CreatedBy       *uuid.UUID  `json:"created_by"`
	UpdatedBy       *uuid.UUID  `json:"updated_by"`
	AttachmentCount int         `json:"attachment_count"`
	LinkCount       int         `json:"link_count"`
	IsDraft         bool        `json:"is_draft"`
	ArchivedAt      *httpx.Date `json:"archived_at"`
	StateGroup      *string     `json:"state_group"`
}

// groupKey is str(issue[field]) for group_by; ok is false for a KeyError.
func (v *subIssueValues) groupKey(field string, loc *time.Location) (string, bool) {
	uuidStr := func(id *uuid.UUID) string {
		if id == nil {
			return "None"
		}
		return id.String()
	}
	dateStr := func(d *httpx.Date) string {
		if d == nil {
			return "None"
		}
		return d.Format(time.DateOnly)
	}
	listStr := func(ids []uuid.UUID) string {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = "UUID('" + id.String() + "')"
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	switch field {
	case "id":
		return v.ID.String(), true
	case "name":
		return v.Name, true
	case "state_id":
		return uuidStr(v.StateID), true
	case "sort_order":
		return drf.PyFloatRepr(v.SortOrder), true
	case "completed_at":
		if v.CompletedAt == nil {
			return "None", true
		}
		return pyDateTimeStr(v.CompletedAt.UTC()), true
	case "estimate_point":
		return uuidStr(v.EstimatePoint), true
	case "priority":
		return v.Priority, true
	case "start_date":
		return dateStr(v.StartDate), true
	case "target_date":
		return dateStr(v.TargetDate), true
	case "sequence_id":
		return strconv.Itoa(v.SequenceID), true
	case "project_id":
		return v.ProjectID.String(), true
	case "parent_id":
		return uuidStr(v.ParentID), true
	case "cycle_id":
		return uuidStr(v.CycleID), true
	case "module_ids":
		return listStr(v.ModuleIDs), true
	case "label_ids":
		return listStr(v.LabelIDs), true
	case "assignee_ids":
		return listStr(v.AssigneeIDs), true
	case "sub_issues_count":
		return strconv.Itoa(v.SubIssuesCount), true
	case "created_at":
		return pyDateTimeStr(v.CreatedAt.In(loc)), true
	case "updated_at":
		return pyDateTimeStr(v.UpdatedAt.In(loc)), true
	case "created_by":
		return uuidStr(v.CreatedBy), true
	case "updated_by":
		return uuidStr(v.UpdatedBy), true
	case "attachment_count":
		return strconv.Itoa(v.AttachmentCount), true
	case "link_count":
		return strconv.Itoa(v.LinkCount), true
	case "is_draft":
		if v.IsDraft {
			return "True", true
		}
		return "False", true
	case "archived_at":
		return dateStr(v.ArchivedAt), true
	case "state_group":
		return pyStrOrNone(v.StateGroup), true
	}
	return "", false
}

// subIssueOrder is order_issue_queryset's ORDER BY (and the joins and GROUP
// BY its Min() annotations bring) for the sub-issue list.
func subIssueOrder(param string) (joins, groupBy, orderBy string) {
	field, desc := sanitizeOrderBy(param, issueOrderAllow, "-created_at")
	dir := ""
	if desc {
		dir = " DESC"
	}
	cases := func(col string, values []string, def string) string {
		var b strings.Builder
		b.WriteString("CASE")
		for i, v := range values {
			fmt.Fprintf(&b, " WHEN %s = '%s' THEN %d", col, v, i)
		}
		b.WriteString(" ELSE " + def + " END")
		return b.String()
	}
	const minGroup = ` GROUP BY i.id, s."group"`
	switch field {
	case "priority":
		// Always urgent first, whichever the direction.
		return "", "", cases("i.priority", priorityOrder, "NULL") + " ASC, i.created_at DESC"
	case "state__group":
		order := stateGroupOrder
		if desc {
			order = []string{"cancelled", "completed", "started", "unstarted", "backlog"}
		}
		return "", "", cases(`s."group"`, order, "5") + " ASC, i.created_at DESC"
	case "labels__name":
		return ` LEFT JOIN issue_labels xl ON xl.issue_id = i.id LEFT JOIN labels xn ON xn.id = xl.label_id`, minGroup,
			"MIN(xn.name)" + dir + ", i.created_at DESC"
	case "assignees__first_name":
		return ` LEFT JOIN issue_assignees xa ON xa.issue_id = i.id LEFT JOIN users xn ON xn.id = xa.assignee_id`, minGroup,
			"MIN(xn.first_name)" + dir + ", i.created_at DESC"
	case "issue_module__module__name":
		return ` LEFT JOIN module_issues xm ON xm.issue_id = i.id LEFT JOIN modules xn ON xn.id = xm.module_id`, minGroup,
			"MIN(xn.name)" + dir + ", i.created_at DESC"
	case "state__name":
		return "", "", "s.name" + dir + ", i.created_at DESC"
	case "created_at":
		return "", "", "i.created_at" + dir
	}
	return "", "", "i." + field + dir + ", i.created_at DESC"
}

// listSubIssues ports SubIssuesEndpoint.get.
func (a *API) listSubIssues(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	orderParam := "-created_at"
	if c.HasQuery("order_by") {
		orderParam = c.Query("order_by")
	}
	// An empty order_by skips order_issue_queryset, leaving Meta.ordering
	// (-created_at).
	joins, groupBy, orderBy := "", "", "i.created_at DESC"
	if orderParam != "" {
		joins, groupBy, orderBy = subIssueOrder(orderParam)
	}
	rows, err := a.db.Query(ctx, `SELECT i.id, i.name, i.state_id, i.sort_order, i.completed_at, i.estimate_point_id,
			i.priority, i.start_date, i.target_date, i.sequence_id, i.project_id, i.parent_id, `+issueCycleSQL+`,
			`+issueModuleIDsSQL+`, `+issueLabelIDsSQL+`, `+issueActiveAssigneeIDsSQL+`, `+issueSubIssuesCountSQL+`,
			i.created_at, i.updated_at, i.created_by_id, i.updated_by_id, `+issueAttachmentCountSQL+`,
			`+issueLinkCountSQL+`, i.is_draft, i.archived_at, s."group"
		`+issueFrom+joins+`
		WHERE `+issueBaseWhere+` AND i.parent_id = $1 AND i.project_id = $2 AND w.slug = $3`+groupBy+`
		ORDER BY `+orderBy, issueID, projectID, c.Param("slug"))
	if err != nil {
		return err
	}
	subs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*subIssueValues, error) {
		var (
			v                        subIssueValues
			start, target, archived  *time.Time
			subs, attachments, links int64
		)
		err := row.Scan(&v.ID, &v.Name, &v.StateID, &v.SortOrder, &v.CompletedAt, &v.EstimatePoint, &v.Priority,
			&start, &target, &v.SequenceID, &v.ProjectID, &v.ParentID, &v.CycleID, &v.ModuleIDs, &v.LabelIDs,
			&v.AssigneeIDs, &subs, &v.CreatedAt, &v.UpdatedAt, &v.CreatedBy, &v.UpdatedBy, &attachments, &links,
			&v.IsDraft, &archived, &v.StateGroup)
		v.CompletedAtUTC = (*utcTime)(v.CompletedAt)
		v.StartDate, v.TargetDate, v.ArchivedAt = dateOf(start), dateOf(target), dateOf(archived)
		v.SubIssuesCount, v.AttachmentCount, v.LinkCount = int(subs), int(attachments), int(links)
		return &v, err
	})
	if err != nil {
		return err
	}
	distribution := stateDistribution{}
	for _, s := range subs {
		distribution.add(s.StateGroup, s.ID)
	}
	group := c.Query("group_by")
	if group == "" {
		if subs == nil {
			subs = []*subIssueValues{}
		}
		return c.JSON(http.StatusOK, map[string]any{"sub_issues": subs, "state_distribution": distribution})
	}
	groups := map[string][]*subIssueValues{}
	for _, s := range subs {
		if group == "assignees__ids" {
			for _, id := range s.AssigneeIDs {
				groups[id.String()] = append(groups[id.String()], s)
			}
			if len(s.AssigneeIDs) == 0 {
				groups["None"] = append(groups["None"], s)
			}
			continue
		}
		key, ok := s.groupKey(group, c.Loc())
		if !ok {
			return errKeyMissing // issue[group_by]
		}
		groups[key] = append(groups[key], s)
	}
	return c.JSON(http.StatusOK, map[string]any{"sub_issues": groups, "state_distribution": distribution})
}

// stateDistribution is the views' {state group: [issue ids]}; a None
// group renders as the "null" key.
type stateDistribution map[string][]string

func (d stateDistribution) add(group *string, id uuid.UUID) {
	key := "null"
	if group != nil {
		key = *group
	}
	d[key] = append(d[key], id.String())
}

// subIssueOut is IssueSerializer on the re-parented issues, which carry no
// annotations: the count, cycle and id-list fields are skipped.
type subIssueOut struct {
	ID            uuid.UUID   `json:"id"`
	Name          string      `json:"name"`
	StateID       *uuid.UUID  `json:"state_id"`
	SortOrder     float64     `json:"sort_order"`
	CompletedAt   *time.Time  `json:"completed_at"`
	EstimatePoint *uuid.UUID  `json:"estimate_point"`
	Priority      string      `json:"priority"`
	StartDate     *httpx.Date `json:"start_date"`
	TargetDate    *httpx.Date `json:"target_date"`
	SequenceID    int         `json:"sequence_id"`
	ProjectID     uuid.UUID   `json:"project_id"`
	ParentID      *uuid.UUID  `json:"parent_id"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	CreatedBy     *uuid.UUID  `json:"created_by"`
	UpdatedBy     *uuid.UUID  `json:"updated_by"`
	IsDraft       bool        `json:"is_draft"`
	ArchivedAt    *httpx.Date `json:"archived_at"`

	stateGroup *string
}

var (
	errParentNotFound      = httpx.Err(http.StatusNotFound, "Parent issue not found")
	errSubIssueIDsRequired = httpx.Err(http.StatusBadRequest, "Sub Issue IDs are required")
)

// addSubIssues ports SubIssuesEndpoint.post: set the parent of the requested
// issues of the project (bulk_update, so neither updated_at nor updated_by),
// with one parent activity per issue.
func (a *API) addSubIssues(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var parent uuid.UUID
	err = a.db.QueryRow(ctx, `SELECT i.id FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE i.id = $1 AND w.slug = $2 AND i.project_id = $3 AND `+issueObjects("i"), issueID, slug, projectID).Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return errParentNotFound
	}
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
	var items []drf.Value
	if v, ok := data.Get("sub_issue_ids"); ok {
		switch v.Kind() {
		case '[', '"', '{':
			items, _ = pyIter(v)
		default:
			return errViewCrash // len() of a number, bool or None
		}
	}
	if len(items) == 0 {
		return errSubIssueIDsRequired
	}
	ids, err := modelUUIDs(items)
	if err != nil {
		return err
	}
	const scoped = `FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE i.id = ANY($1) AND w.slug = $2 AND i.project_id = $3 AND `
	rows, err := a.db.Query(ctx, `SELECT i.id `+scoped+issueObjects("i")+` ORDER BY i.created_at DESC`, ids, slug, projectID)
	if err != nil {
		return err
	}
	subIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	if len(subIDs) > 0 {
		if _, err := a.db.Exec(ctx, `UPDATE issues SET parent_id = $2 WHERE id = ANY($1)`, subIDs, parent); err != nil {
			return err
		}
	}
	for _, id := range subIDs {
		requested := `{"parent": "` + issueID.String() + `"}`
		current := `{"parent": "` + id.String() + `"}`
		a.enqueueIssueActivity(ctx, issueActivityJob{
			Type: "issue.activity.updated", RequestedData: &requested, CurrentInstance: &current, IssueID: id.String(),
			ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
			Subscriber: true, Notification: true,
		})
	}
	rows, err = a.db.Query(ctx, `SELECT i.id, i.name, i.state_id, i.sort_order, i.completed_at, i.estimate_point_id,
			i.priority, i.start_date, i.target_date, i.sequence_id, i.project_id, i.parent_id, i.created_at, i.updated_at,
			i.created_by_id, i.updated_by_id, i.is_draft, i.archived_at,
			(SELECT st."group" FROM states st WHERE st.id = i.state_id) `+scoped+issueObjects("i")+`
		ORDER BY i.created_at DESC`, subIDs, slug, projectID)
	if err != nil {
		return err
	}
	updated, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (subIssueOut, error) {
		var (
			v                       subIssueOut
			start, target, archived *time.Time
		)
		err := row.Scan(&v.ID, &v.Name, &v.StateID, &v.SortOrder, &v.CompletedAt, &v.EstimatePoint, &v.Priority,
			&start, &target, &v.SequenceID, &v.ProjectID, &v.ParentID, &v.CreatedAt, &v.UpdatedAt, &v.CreatedBy,
			&v.UpdatedBy, &v.IsDraft, &archived, &v.stateGroup)
		v.StartDate, v.TargetDate, v.ArchivedAt = dateOf(start), dateOf(target), dateOf(archived)
		return v, err
	})
	if err != nil {
		return err
	}
	if updated == nil {
		updated = []subIssueOut{}
	}
	distribution := stateDistribution{}
	for _, v := range updated {
		distribution.add(v.stateGroup, v.ID)
	}
	return c.JSON(http.StatusOK, map[string]any{"sub_issues": updated, "state_distribution": distribution})
}
