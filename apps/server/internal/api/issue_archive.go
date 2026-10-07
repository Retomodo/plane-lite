package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// projectEntityPerm is the ProjectEntityPermission class: reads need an
// active project membership, writes an active project admin or member.
func (a *API) projectEntityPerm(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		project, err := c.UUIDParam("project_id")
		if err != nil {
			return err
		}
		roles := []int{roleAdmin, roleMember, roleGuest}
		switch c.R.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			roles = []int{roleAdmin, roleMember}
		}
		var ok bool
		if err := a.db.QueryRow(c.Context(), `SELECT EXISTS (SELECT 1 FROM project_members pm
			JOIN workspaces w ON w.id = pm.workspace_id
			WHERE w.slug = $1 AND pm.project_id = $2 AND pm.member_id = $3 AND pm.is_active AND pm.deleted_at IS NULL
				AND pm.role = ANY($4))`, c.Param("slug"), project, c.User.ID, roles).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return httpx.ErrForbidden
		}
		return h(c)
	}
}

// today is timezone.now().date(): the UTC date.
func today() string { return time.Now().UTC().Format(time.DateOnly) }

// archivedAtJSON is the archived_at of IssueSerializer(issue).data, the
// only member of current_instance the archive activity reads (the
// serializer has no description_html, so it brings no mentions).
func archivedAtJSON(t *time.Time) string {
	if t == nil {
		return `{"archived_at": null}`
	}
	return `{"archived_at": "` + t.Format(time.DateOnly) + `"}`
}

func (a *API) archiveActivity(ctx context.Context, c *httpx.Ctx, issue, project uuid.UUID, requested, current string) {
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "issue.activity.updated", RequestedData: &requested, CurrentInstance: &current, IssueID: issue.String(),
		ActorID: c.User.ID.String(), ProjectID: project.String(), Epoch: time.Now().Unix(), Subscriber: true,
		Notification: true,
	})
}

// archiveIssue ports IssueArchiveViewSet.archive.
func (a *API) archiveIssue(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	var group *string
	err = a.db.QueryRow(ctx, `SELECT s."group" FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		JOIN projects p ON p.id = i.project_id LEFT JOIN states s ON s.id = i.state_id
		WHERE w.slug = $1 AND i.project_id = $2 AND i.id = $3 AND `+issueBaseWhere, c.Param("slug"), projectID, pk).Scan(&group)
	if err != nil {
		return err
	}
	if group == nil {
		return errViewCrash // issue.state.group on None
	}
	if *group != "completed" && *group != "cancelled" {
		return httpx.Err(http.StatusBadRequest, "Can only archive completed or cancelled state group issue")
	}
	day := today()
	a.archiveActivity(ctx, c, pk, projectID, `{"archived_at": "`+day+`", "automation": false}`, archivedAtJSON(nil))
	if err := a.saveIssueFields(ctx, pk, c.User.ID, "archived_at = "+"'"+day+"'::date"); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"archived_at": day})
}

// saveIssueFields is issue.save() after changing the given columns: the
// default state if it has none, description_stripped and the audit fields.
func (a *API) saveIssueFields(ctx context.Context, id, actor uuid.UUID, set string) error {
	var (
		project uuid.UUID
		state   *uuid.UUID
		html    string
	)
	if err := a.db.QueryRow(ctx, `SELECT project_id, state_id, description_html FROM issues WHERE id = $1`, id).
		Scan(&project, &state, &html); err != nil {
		return err
	}
	if state == nil {
		def, err := defaultState(ctx, a.db, project)
		if err != nil {
			return err
		}
		state = def
	}
	_, err := a.db.Exec(ctx, `UPDATE issues SET `+set+`, state_id = $2, description_stripped = $3, updated_at = now(),
		updated_by_id = $4 WHERE id = $1`, id, state, stripTags(html), actor)
	return err
}

// unarchiveIssue ports IssueArchiveViewSet.unarchive.
func (a *API) unarchiveIssue(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	var archived *time.Time
	err = a.db.QueryRow(ctx, `SELECT i.archived_at FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE w.slug = $1 AND i.project_id = $2 AND i.id = $3 AND i.deleted_at IS NULL AND i.archived_at IS NOT NULL`,
		c.Param("slug"), projectID, pk).Scan(&archived)
	if err != nil {
		return err
	}
	a.archiveActivity(ctx, c, pk, projectID, `{"archived_at": null}`, archivedAtJSON(archived))
	if err := a.saveIssueFields(ctx, pk, c.User.ID, "archived_at = NULL"); err != nil {
		return err
	}
	return c.NoContent()
}

// archivedIssueDetail is IssueDetailSerializer over the archive view's
// plain queryset: the annotated fields are missing from the instance, so
// DRF skips them.
type archivedIssueDetail struct {
	ID              uuid.UUID   `json:"id"`
	Name            string      `json:"name"`
	StateID         *uuid.UUID  `json:"state_id"`
	SortOrder       float64     `json:"sort_order"`
	CompletedAt     *time.Time  `json:"completed_at"`
	EstimatePoint   *uuid.UUID  `json:"estimate_point"`
	Priority        string      `json:"priority"`
	StartDate       *httpx.Date `json:"start_date"`
	TargetDate      *httpx.Date `json:"target_date"`
	SequenceID      int         `json:"sequence_id"`
	ProjectID       uuid.UUID   `json:"project_id"`
	ParentID        *uuid.UUID  `json:"parent_id"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	CreatedBy       *uuid.UUID  `json:"created_by"`
	UpdatedBy       *uuid.UUID  `json:"updated_by"`
	IsDraft         bool        `json:"is_draft"`
	ArchivedAt      *httpx.Date `json:"archived_at"`
	DescriptionHTML string      `json:"description_html"`
	IsSubscribed    bool        `json:"is_subscribed"`
}

// getArchivedIssue ports IssueArchiveViewSet.retrieve.
func (a *API) getArchivedIssue(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	var (
		d                       archivedIssueDetail
		start, target, archived *time.Time
	)
	err = a.db.QueryRow(c.Context(), `SELECT i.id, i.name, i.state_id, i.sort_order, i.completed_at, i.estimate_point_id,
			i.priority, i.start_date, i.target_date, i.sequence_id, i.project_id, i.parent_id, i.created_at, i.updated_at,
			i.created_by_id, i.updated_by_id, i.is_draft, i.archived_at, i.description_html,
			EXISTS (SELECT 1 FROM issue_subscribers x WHERE x.issue_id = i.id AND x.project_id = $2
				AND x.subscriber_id = $4 AND x.deleted_at IS NULL AND x.workspace_id = w.id)
		FROM issues i JOIN workspaces w ON w.id = i.workspace_id LEFT JOIN issue_types t ON t.id = i.type_id
		WHERE w.slug = $1 AND i.project_id = $2 AND i.id = $3 AND i.deleted_at IS NULL AND i.archived_at IS NOT NULL
			AND (i.type_id IS NULL OR NOT t.is_epic)`, c.Param("slug"), projectID, pk, c.User.ID).
		Scan(&d.ID, &d.Name, &d.StateID, &d.SortOrder, &d.CompletedAt, &d.EstimatePoint, &d.Priority, &start, &target,
			&d.SequenceID, &d.ProjectID, &d.ParentID, &d.CreatedAt, &d.UpdatedAt, &d.CreatedBy, &d.UpdatedBy, &d.IsDraft,
			&archived, &d.DescriptionHTML, &d.IsSubscribed)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "The required object does not exist.")
	}
	if err != nil {
		return err
	}
	d.StartDate, d.TargetDate, d.ArchivedAt = dateOf(start), dateOf(target), dateOf(archived)
	return c.JSON(http.StatusOK, d)
}

// issueIDList reads request.data.get("issue_ids", []) as the bulk views
// use it: a falsy len() is the "required" error; a value len() rejects
// crashes.
func issueIDList(c *httpx.Ctx) ([]drf.Value, error) {
	data, err := drf.Parse(c.R)
	if err != nil {
		return nil, err
	}
	if !data.IsDict() {
		return nil, errViewCrash // request.data.get on a list
	}
	v, ok := data.Get("issue_ids")
	if !ok {
		return nil, errIssueIDsRequired
	}
	var items []drf.Value
	switch v.Kind() {
	case '[':
		items, _ = v.Elems()
	case '"':
		for _, r := range v.Str() {
			items = append(items, drf.JSONValue(jsonString(string(r))))
		}
	case '{':
		for _, k := range drf.DataFromJSON(v.Raw()).Keys() {
			items = append(items, drf.JSONValue(jsonString(k)))
		}
	default:
		return nil, errViewCrash // len() of a number, bool or None
	}
	if len(items) == 0 {
		return nil, errIssueIDsRequired
	}
	return items, nil
}

var errIssueIDsRequired = httpx.Err(http.StatusBadRequest, "Issue IDs are required")

// modelUUIDs is pk__in=values through models.UUIDField.to_python: None is
// dropped and anything that is not a UUID is a ValidationError.
func modelUUIDs(vals []drf.Value) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, v := range vals {
		switch v.Kind() {
		case 'n':
			continue
		case '"':
			id, ok := drf.ParseUUID(v.Str())
			if !ok {
				return nil, errFilterDetail
			}
			out = append(out, id)
		case '0', 't', 'f':
			n, ok := drf.PyInt(v)
			if !ok {
				return nil, errFilterDetail // a float: UUID(hex=1.5)
			}
			if n.Sign() < 0 || n.BitLen() > 128 {
				return nil, errFilterDetail
			}
			var id uuid.UUID
			n.FillBytes(id[:])
			out = append(out, id)
		default:
			return nil, errFilterDetail
		}
	}
	return out, nil
}

// bulkArchiveIssues ports BulkArchiveIssuesEndpoint.post.
func (a *API) bulkArchiveIssues(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	vals, err := issueIDList(c)
	if err != nil {
		return err
	}
	ids, err := modelUUIDs(vals)
	if err != nil {
		return err
	}
	ctx := c.Context()
	rows, err := a.db.Query(ctx, `SELECT i.id, s."group", i.archived_at FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		LEFT JOIN states s ON s.id = i.state_id
		WHERE w.slug = $1 AND i.project_id = $2 AND i.id = ANY($3) AND i.deleted_at IS NULL
		ORDER BY i.created_at DESC`, c.Param("slug"), projectID, ids)
	if err != nil {
		return err
	}
	type target struct {
		id       uuid.UUID
		group    *string
		archived *time.Time
	}
	issues, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (target, error) {
		var t target
		err := row.Scan(&t.id, &t.group, &t.archived)
		return t, err
	})
	if err != nil {
		return err
	}
	day := today()
	var archive []uuid.UUID
	for _, t := range issues {
		if t.group == nil {
			return errViewCrash
		}
		if *t.group != "completed" && *t.group != "cancelled" {
			return httpx.Body(http.StatusBadRequest, map[string]any{
				"error_code": 4091, "error_message": "INVALID_ARCHIVE_STATE_GROUP",
			})
		}
		a.archiveActivity(ctx, c, t.id, projectID, `{"archived_at": "`+day+`", "automation": false}`, archivedAtJSON(t.archived))
		archive = append(archive, t.id)
	}
	if len(archive) > 0 {
		if _, err := a.db.Exec(ctx, `UPDATE issues SET archived_at = $2::date WHERE id = ANY($1)`, archive, day); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"archived_at": day})
}

// bulkDeleteIssues ports BulkDeleteIssuesEndpoint.delete: queryset soft
// deletes, with no cascade and no activity.
func (a *API) bulkDeleteIssues(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	vals, err := issueIDList(c)
	if err != nil {
		return err
	}
	ids, err := modelUUIDs(vals)
	if err != nil {
		return err
	}
	ctx := c.Context()
	var n int
	err = pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT i.id FROM issues i JOIN workspaces w ON w.id = i.workspace_id
			JOIN projects p ON p.id = i.project_id LEFT JOIN states s ON s.id = i.state_id
			WHERE w.slug = $1 AND i.project_id = $2 AND i.id = ANY($3) AND `+issueBaseWhere, c.Param("slug"), projectID, ids)
		if err != nil {
			return err
		}
		matched, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		n = len(matched)
		for _, table := range []string{"cycle_issues", "module_issues", "issues"} {
			col := "issue_id"
			if table == "issues" {
				col = "id"
			}
			if _, err := tx.Exec(ctx, `UPDATE `+table+` SET deleted_at = now() WHERE `+col+` = ANY($1) AND deleted_at IS NULL`,
				matched); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"message": fmt.Sprintf("%d issues were deleted", n)})
}

// issueDateValue is an in-memory start_date/target_date during
// IssueBulkUpdateDateEndpoint.post: the model's date, or a raw request
// value assigned over it.
type issueDateValue struct {
	date *time.Time // the stored date (nil is None) when raw is unset
	raw  *drf.Value
}

func (d issueDateValue) truthy() bool {
	if d.raw != nil {
		return drf.PyTruthy(*d.raw)
	}
	return d.date != nil
}

// str is str() of the attribute, as current_instance records it.
func (d issueDateValue) str() string {
	if d.raw != nil {
		return drf.PyStr(*d.raw)
	}
	if d.date == nil {
		return "None"
	}
	return d.date.Format(time.DateOnly)
}

// asDate is validate_dates' conversion: strings through strptime
// "%Y-%m-%d" (a ValueError crashes the view); ok false means a value that
// is neither a string nor a date.
func (d issueDateValue) asDate() (time.Time, bool, error) {
	if d.raw == nil {
		return *d.date, true, nil
	}
	if !d.raw.IsString() {
		return time.Time{}, false, nil
	}
	t, ok := drf.StrptimeYMD(d.raw.Str())
	if !ok {
		return time.Time{}, false, errViewCrash
	}
	return t, true, nil
}

// dbDate is the value bulk_update writes: DateField.to_python of a raw
// value, which only strings survive.
func (d issueDateValue) dbDate() (*string, error) {
	if d.raw == nil {
		if d.date == nil {
			return nil, nil
		}
		s := d.date.Format(time.DateOnly)
		return &s, nil
	}
	switch d.raw.Kind() {
	case 'n':
		return nil, nil
	case '"':
		t, ok := drf.ParseDate(d.raw.Str())
		if !ok {
			return nil, errFilterDetail
		}
		s := t.Format(time.DateOnly)
		return &s, nil
	}
	return nil, errViewCrash // parse_date() of a non-string raises TypeError
}

// issueDates ports IssueBulkUpdateDateEndpoint.post.
func (a *API) issueDates(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	var updates []drf.Value
	if v, ok := data.Get("updates"); ok {
		switch v.Kind() {
		case '[':
			updates, _ = v.Elems()
		case '"', '{':
			// Iterating a string or dict yields strings, which update["id"]
			// rejects.
			if drf.PyTruthy(v) {
				return errViewCrash
			}
		default:
			return errViewCrash // not iterable
		}
	}
	var idVals []drf.Value
	for _, u := range updates {
		if u.Kind() != '{' {
			return errViewCrash
		}
		id, ok := u.Member("id")
		if !ok {
			return errKeyMissing
		}
		idVals = append(idVals, id)
	}
	ids, err := modelUUIDs(idVals)
	if err != nil {
		return err
	}
	ctx := c.Context()
	type issueDates struct {
		start, target issueDateValue
	}
	rows, err := a.db.Query(ctx, `SELECT i.id, i.start_date, i.target_date FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE i.id = ANY($1) AND w.slug = $2 AND i.project_id = $3 AND i.deleted_at IS NULL`, ids, c.Param("slug"), projectID)
	if err != nil {
		return err
	}
	byID := map[string]*issueDates{}
	for rows.Next() {
		var (
			id            uuid.UUID
			start, target *time.Time
		)
		if err := rows.Scan(&id, &start, &target); err != nil {
			rows.Close()
			return err
		}
		byID[id.String()] = &issueDates{start: issueDateValue{date: start}, target: issueDateValue{date: target}}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	epoch := time.Now().Unix()
	var order []string
	for _, u := range updates {
		idVal, _ := u.Member("id")
		if !idVal.IsString() {
			continue // issues_dict.get() of a non-string finds nothing
		}
		issueID := idVal.Str()
		issue, ok := byID[issueID]
		if !ok {
			continue
		}
		newStart := issueDateValue{}
		if v, ok := u.Member("start_date"); ok {
			newStart.raw = &v
		}
		newTarget := issueDateValue{}
		if v, ok := u.Member("target_date"); ok {
			newTarget.raw = &v
		}
		// validate_dates: the new value if truthy, else the current one.
		start, target := issue.start, issue.target
		if newStart.raw != nil && newStart.truthy() {
			start = newStart
		}
		if newTarget.raw != nil && newTarget.truthy() {
			target = newTarget
		}
		if start.truthy() && target.truthy() {
			s, sok, err := start.asDate()
			if err != nil {
				return err
			}
			t, tok, err := target.asDate()
			if err != nil {
				return err
			}
			if !sok || !tok {
				return errViewCrash // comparing a date with something else
			}
			if s.After(t) {
				return httpx.Body(http.StatusBadRequest, map[string]any{"message": "Start date cannot exceed target date"})
			}
		} else {
			// Strings are still parsed when only one side is set.
			for _, d := range []issueDateValue{start, target} {
				if d.truthy() {
					if _, _, err := d.asDate(); err != nil {
						return err
					}
				}
			}
		}
		for _, f := range []struct {
			name string
			val  issueDateValue
			cur  *issueDateValue
		}{{"start_date", newStart, &issue.start}, {"target_date", newTarget, &issue.target}} {
			if f.val.raw == nil || !f.val.truthy() {
				continue
			}
			requested := `{"` + f.name + `": ` + string(f.val.raw.Raw()) + `}`
			current, _ := httpx.Marshal(map[string]string{f.name: f.cur.str()}, nil)
			cur := string(current)
			a.enqueueIssueActivity(ctx, issueActivityJob{
				Type: "issue.activity.updated", RequestedData: &requested, CurrentInstance: &cur, IssueID: issueID,
				ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: epoch, Subscriber: true,
			})
			*f.cur = f.val
			order = append(order, issueID)
		}
	}
	// bulk_update(["start_date", "target_date"]) of every touched issue.
	type write struct {
		id            string
		start, target *string
	}
	var writes []write
	seen := map[string]bool{}
	for _, id := range order {
		if seen[id] {
			continue
		}
		seen[id] = true
		issue := byID[id]
		s, err := issue.start.dbDate()
		if err != nil {
			return err
		}
		t, err := issue.target.dbDate()
		if err != nil {
			return err
		}
		writes = append(writes, write{id, s, t})
	}
	if len(writes) > 0 {
		var b strings.Builder
		args := []any{}
		for i, w := range writes {
			if i > 0 {
				b.WriteString(", ")
			}
			args = append(args, w.id, w.start, w.target)
			fmt.Fprintf(&b, "($%d::uuid, $%d::date, $%d::date)", len(args)-2, len(args)-1, len(args))
		}
		if _, err := a.db.Exec(ctx, `UPDATE issues i SET start_date = v.s, target_date = v.t FROM (VALUES `+b.String()+
			`) AS v(id, s, t) WHERE i.id = v.id`, args...); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"message": "Issues updated successfully"})
}
