package api

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/http"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/sanitize"
	"plane-lite/server/internal/softdelete"
)

var issuePriorities = []string{"urgent", "high", "medium", "low", "none"}

// issueInput is IssueCreateSerializer's validated_data.
type issueInput struct {
	set setList
	// state and parent: the last of state_id/state (parent_id/parent) given.
	state, parent   **uuid.UUID
	startDate       **time.Time
	targetDate      **time.Time
	descriptionHTML *string
	labelIDs        *[]uuid.UUID
	assigneeIDs     *[]uuid.UUID
}

func (a *API) liveExists(ctx context.Context, sql string) func(uuid.UUID) (bool, error) {
	return func(id uuid.UUID) (bool, error) {
		var ok bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS (`+sql+`)`, id).Scan(&ok)
		return ok, err
	}
}

// validateIssue runs IssueCreateSerializer (fields, then validate()) for
// project projectID. A nil input with a nil error means the validator holds
// the errors.
func (a *API) validateIssue(ctx context.Context, data *drf.Data, loc *time.Location, projectID uuid.UUID, partial bool) (*issueInput, *drf.Validator, error) {
	v := drf.NewValidator(data, loc)
	in := &issueInput{}
	set := &in.set
	if !partial {
		v.Require("name")
	}
	// Field order matters only where two fields share a source.
	pk := func(name, sql string) (*uuid.UUID, bool, error) {
		return v.PK(name, true, a.liveExists(ctx, sql))
	}
	const anyState = `SELECT 1 FROM states WHERE id = $1`
	const liveIssue = `SELECT 1 FROM issues WHERE id = $1 AND deleted_at IS NULL`
	for _, f := range []struct {
		name string
		dst  ***uuid.UUID
		sql  string
	}{{"state_id", &in.state, anyState}, {"parent_id", &in.parent, liveIssue}} {
		id, ok, err := pk(f.name, f.sql)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			*f.dst = &id
		}
	}
	for _, f := range []struct {
		name string
		dst  **[]uuid.UUID
		sql  string
	}{
		{"label_ids", &in.labelIDs, `SELECT 1 FROM labels WHERE id = $1 AND deleted_at IS NULL`},
		{"assignee_ids", &in.assigneeIDs, `SELECT 1 FROM users WHERE id = $1`},
	} {
		ids, ok, err := v.PKList(f.name, a.liveExists(ctx, f.sql))
		if err != nil {
			return nil, nil, err
		}
		if ok {
			*f.dst = &ids
		}
	}
	if t, ok := v.DateTime("deleted_at", true); ok {
		set.add("deleted_at", t)
	}
	if n, ok := v.IntNull("point", 0, 12); ok {
		set.add("point", n)
	}
	if s, ok := v.Char("name", drf.CharField{MaxLength: 255}); ok {
		set.add("name", *s)
	}
	if raw, ok := v.JSON("description_json", false); ok {
		set.addCast("description_json", string(raw), "::jsonb")
	}
	if s, ok := v.Char("description_html", drf.CharField{AllowBlank: true}); ok {
		in.descriptionHTML = s
	}
	v.Char("description_stripped", drf.CharField{AllowBlank: true, AllowNull: true}) // overwritten by save()
	if s, ok := v.Choice("priority", issuePriorities, drf.ChoiceField{}); ok {
		set.add("priority", *s)
	}
	if t, ok := v.Date("start_date", true); ok {
		in.startDate = &t
		set.add("start_date", t)
	}
	if t, ok := v.Date("target_date", true); ok {
		in.targetDate = &t
		set.add("target_date", t)
	}
	if n, ok := v.Int("sequence_id", -1<<31, 1<<31-1); ok {
		set.add("sequence_id", n)
	}
	if f, ok := v.Float("sort_order"); ok {
		set.add("sort_order", f)
	}
	if t, ok := v.Date("archived_at", true); ok {
		set.add("archived_at", t)
	}
	if b, ok := v.Bool("is_draft"); ok {
		set.add("is_draft", b)
	}
	for _, name := range []string{"external_source", "external_id"} {
		if s, ok := v.Char(name, drf.CharField{MaxLength: 255, AllowBlank: true, AllowNull: true}); ok {
			set.add(name, s)
		}
	}
	for _, f := range []struct {
		name string
		dst  ***uuid.UUID
		sql  string
	}{{"parent", &in.parent, liveIssue}, {"state", &in.state, anyState}} {
		id, ok, err := pk(f.name, f.sql)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			*f.dst = &id
		}
	}
	estimate, estimateOK, err := pk("estimate_point", `SELECT 1 FROM estimate_points WHERE id = $1 AND deleted_at IS NULL`)
	if err != nil {
		return nil, nil, err
	}
	if estimateOK {
		set.add("estimate_point_id", estimate)
	}
	if t, ok, err := pk("type", `SELECT 1 FROM issue_types WHERE id = $1 AND deleted_at IS NULL`); err != nil {
		return nil, nil, err
	} else if ok {
		set.add("type_id", t)
	}
	if !v.Valid() {
		return nil, v, nil
	}

	// validate()
	if in.startDate != nil && *in.startDate != nil && in.targetDate != nil && *in.targetDate != nil &&
		(*in.startDate).After(**in.targetDate) {
		v.Add("non_field_errors", "Start date cannot exceed target date")
		return nil, v, nil
	}
	if in.descriptionHTML != nil && *in.descriptionHTML != "" {
		clean, _, err := sanitize.HTML(*in.descriptionHTML)
		if err != nil {
			v.Add("error", "html content is not valid")
			return nil, v, nil
		}
		in.descriptionHTML = &clean
	}
	if in.assigneeIDs != nil && len(*in.assigneeIDs) > 0 {
		rows, err := a.db.Query(ctx, `SELECT member_id FROM project_members WHERE project_id = $1 AND role >= 15
			AND is_active AND deleted_at IS NULL AND member_id = ANY($2)`, projectID, *in.assigneeIDs)
		if err != nil {
			return nil, nil, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return nil, nil, err
		}
		in.assigneeIDs = &ids
	}
	if in.labelIDs != nil && len(*in.labelIDs) > 0 {
		rows, err := a.db.Query(ctx, `SELECT id FROM labels WHERE project_id = $1 AND id = ANY($2) AND deleted_at IS NULL`,
			projectID, *in.labelIDs)
		if err != nil {
			return nil, nil, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return nil, nil, err
		}
		in.labelIDs = &ids
	}
	checks := []struct {
		id  **uuid.UUID
		sql string
		msg string
	}{
		{in.state, `SELECT 1 FROM states WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL AND "group" <> 'triage'`,
			"State is not valid please pass a valid state_id"},
		{in.parent, `SELECT 1 FROM issues WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL`,
			"Parent is not valid issue_id please pass a valid issue_id"},
	}
	if estimateOK {
		checks = append(checks, struct {
			id  **uuid.UUID
			sql string
			msg string
		}{&estimate, `SELECT 1 FROM estimate_points WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL`,
			"Estimate point is not valid please pass a valid estimate_point_id"})
	}
	for _, ch := range checks {
		if ch.id == nil || *ch.id == nil {
			continue
		}
		var ok bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (`+ch.sql+`)`, **ch.id, projectID).Scan(&ok); err != nil {
			return nil, nil, err
		}
		if !ok {
			v.Add("non_field_errors", ch.msg)
			return nil, v, nil
		}
	}
	if in.state != nil {
		set.add("state_id", *in.state)
	}
	if in.parent != nil {
		set.add("parent_id", *in.parent)
	}
	return in, v, nil
}

// stripTags is Issue.save()'s description_stripped.
func stripTags(html string) *string {
	if html == "" {
		return nil
	}
	_, text, err := sanitize.HTML(html)
	if err != nil {
		return nil
	}
	return &text
}

// projectLockKey is plane.utils.uuid.convert_uuid_to_integer, the advisory
// lock Issue.save() takes per project while numbering an issue.
func projectLockKey(id uuid.UUID) int64 {
	h := sha256.Sum256([]byte(id.String()))
	return int64(binary.BigEndian.Uint64(h[:8]))
}

// defaultState is Issue._ensure_default_state for a project.
func defaultState(ctx context.Context, q db.Querier, projectID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM states WHERE project_id = $1 AND deleted_at IS NULL AND "group" <> 'triage'
		AND NOT is_triage ORDER BY "default" DESC, sequence LIMIT 1`, projectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &id, err
}

// insertIssue is Issue.objects.create(...) for IssueCreateSerializer.create.
func (a *API) insertIssue(ctx context.Context, in *issueInput, projectID, workspaceID, actor uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		set := in.set
		var state *uuid.UUID
		if in.state != nil {
			state = *in.state
		}
		if state == nil {
			var err error
			if state, err = defaultState(ctx, tx, projectID); err != nil {
				return err
			}
			set.drop("state_id")
			set.add("state_id", state)
		}
		if state != nil {
			var completed bool
			if err := tx.QueryRow(ctx, `SELECT "group" = 'completed' FROM states WHERE id = $1`, *state).Scan(&completed); err != nil {
				return err
			}
			if completed {
				set.add("completed_at", time.Now())
			}
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, projectLockKey(projectID)); err != nil {
			return err
		}
		var seq int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(sequence), 0) + 1 FROM issue_sequences
			WHERE project_id = $1 AND deleted_at IS NULL`, projectID).Scan(&seq); err != nil {
			return err
		}
		set.drop("sequence_id")
		set.add("sequence_id", seq)
		html := "<p></p>"
		if in.descriptionHTML != nil {
			html = *in.descriptionHTML
			set.add("description_html", html)
		}
		set.add("description_stripped", stripTags(html))
		var largest *float64
		if err := tx.QueryRow(ctx, `SELECT max(sort_order) FROM issues WHERE project_id = $1
			AND state_id IS NOT DISTINCT FROM $2 AND deleted_at IS NULL`, projectID, state).Scan(&largest); err != nil {
			return err
		}
		if largest != nil {
			set.drop("sort_order")
			set.add("sort_order", *largest+10000)
		}
		set.add("project_id", projectID)
		set.add("workspace_id", workspaceID)
		set.add("created_by_id", actor)
		set.addCast("created_at", nil, "::timestamptz")
		set.drop("created_at")
		if err := tx.QueryRow(ctx, set.insertSQL("issues"), set.args...).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO issue_sequences (issue_id, sequence, project_id, workspace_id, created_by_id)
			VALUES ($1, $2, $3, $4, $5)`, id, seq, projectID, workspaceID, actor)
		return err
	})
	return id, err
}

// bulkIssueRelations is IssueAssignee/IssueLabel bulk_create: one INSERT,
// all or nothing. ignoreConflicts adds ON CONFLICT DO NOTHING.
func (a *API) bulkIssueRelations(ctx context.Context, table, col string, issue, project, workspace uuid.UUID,
	ids []uuid.UUID, createdBy, updatedBy *uuid.UUID, ignoreConflicts bool) error {
	if len(ids) == 0 {
		return nil
	}
	sql := `INSERT INTO ` + table + ` (` + col + `, issue_id, project_id, workspace_id, created_by_id, updated_by_id, created_at, updated_at)
		SELECT x, $2, $3, $4, $5, $6, clock_timestamp(), clock_timestamp() FROM unnest($1::uuid[]) AS x`
	if ignoreConflicts {
		sql += ` ON CONFLICT DO NOTHING`
	}
	_, err := a.db.Exec(ctx, sql, ids, issue, project, workspace, createdBy, updatedBy)
	if db.IsIntegrityError(err) {
		return nil // except IntegrityError: pass
	}
	return err
}

// createIssue ports IssueViewSet.create.
func (a *API) createIssue(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	var (
		workspaceID     uuid.UUID
		defaultAssignee *uuid.UUID
	)
	if err := a.db.QueryRow(ctx, `SELECT workspace_id, default_assignee_id FROM projects WHERE id = $1 AND deleted_at IS NULL`,
		projectID).Scan(&workspaceID, &defaultAssignee); err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	in, v, err := a.validateIssue(ctx, data, c.Loc(), projectID, false)
	if err != nil {
		return err
	}
	if in == nil {
		return v.Err()
	}
	id, err := a.insertIssue(ctx, in, projectID, workspaceID, c.User.ID)
	if err != nil {
		return err
	}
	if in.assigneeIDs != nil && len(*in.assigneeIDs) > 0 {
		if err := a.bulkIssueRelations(ctx, "issue_assignees", "assignee_id", id, projectID, workspaceID,
			*in.assigneeIDs, &c.User.ID, nil, false); err != nil {
			return err
		}
	} else if defaultAssignee != nil {
		var member bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE member_id = $1
			AND project_id = $2 AND role >= 15 AND is_active AND deleted_at IS NULL)`, *defaultAssignee, projectID).Scan(&member); err != nil {
			return err
		}
		if member {
			if _, err := a.db.Exec(ctx, `INSERT INTO issue_assignees (assignee_id, issue_id, project_id, workspace_id, created_by_id)
				VALUES ($1, $2, $3, $4, $5)`, *defaultAssignee, id, projectID, workspaceID, c.User.ID); err != nil && !db.IsIntegrityError(err) {
				return err
			}
		}
	}
	if in.labelIDs != nil && len(*in.labelIDs) > 0 {
		if err := a.bulkIssueRelations(ctx, "issue_labels", "label_id", id, projectID, workspaceID,
			*in.labelIDs, &c.User.ID, nil, false); err != nil {
			return err
		}
	}

	requested := string(data.Object())
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "issue.activity.created", RequestedData: &requested, IssueID: id.String(), ActorID: c.User.ID.String(),
		ProjectID: projectID.String(), Epoch: time.Now().Unix(), Subscriber: true, Notification: true,
	})
	res, err := scanIssueValues(a.db.QueryRow(ctx, issueValuesSQL+`
		WHERE i.project_id = $1 AND w.slug = $2 AND i.id = $3 AND `+issueObjects("i"), projectID, c.Param("slug"), id))
	if errors.Is(err, pgx.ErrNoRows) {
		// .first() is None: user_timezone_converter(None, ...) raises
		// before the description version task is queued.
		return errViewCrash
	}
	if err != nil {
		return err
	}
	a.enqueueDescriptionVersion(ctx, descriptionVersionJob{
		UpdatedIssue: requested, IssueID: id.String(), UserID: c.User.ID.String(), IsCreating: true,
	})
	return c.JSON(http.StatusCreated, res)
}

// currentIssue is IssueDetailSerializer(issue).data for partial_update's
// annotated queryset: what the activity task diffs the request against.
func (a *API) currentIssue(ctx context.Context, slug string, projectID, pk uuid.UUID) (map[string]jsontext.Value, *issueDetail, error) {
	d, err := scanIssueDetail(a.db.QueryRow(ctx, `SELECT `+issueCycleSQL+`, `+issueLabelIDsSQL+`, `+issueActiveAssigneeIDsSQL+`,
			`+issueModuleIDsSQL+`, `+nullIfZero(issueSubIssuesCountSQL)+`, `+nullIfZero(issueAttachmentCountSQL)+`,
			`+nullIfZero(issueLinkCountSQL)+`, false, `+issueDetailCols+`
		FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE i.project_id = $1 AND w.slug = $2 AND i.id = $3 AND `+issueObjects("i"), projectID, slug, pk))
	if err != nil {
		return nil, nil, err
	}
	d.IsSubscribed = nil
	raw, err := httpx.Marshal(d, time.UTC)
	if err != nil {
		return nil, nil, err
	}
	var m map[string]jsontext.Value
	if err := drf.Decode(raw, &m); err != nil {
		return nil, nil, err
	}
	return m, d, nil
}

// updateIssue ports IssueViewSet.partial_update.
func (a *API) updateIssue(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.pop() on a list
	}
	skip, _ := data.Get("skip_activity")
	skipActivity := data.Has("skip_activity") && drf.PyTruthy(skip)
	data.Delete("skip_activity")
	desc, hasDesc := data.Get("description_html")
	isDescriptionUpdate := hasDesc && !desc.IsNull()

	current, cur, err := a.currentIssue(ctx, c.Param("slug"), projectID, pk)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "Issue not found")
	}
	if err != nil {
		return err
	}
	in, v, err := a.validateIssue(ctx, data, c.Loc(), projectID, true)
	if err != nil {
		return err
	}
	if in == nil {
		return v.Err()
	}
	if err := a.saveIssue(ctx, in, cur, c.User.ID); err != nil {
		return err
	}
	if !(skipActivity && isDescriptionUpdate) {
		requested := string(data.Object())
		currentJSON, err := httpx.Marshal(current, time.UTC)
		if err != nil {
			return err
		}
		cj := string(currentJSON)
		a.enqueueIssueActivity(ctx, issueActivityJob{
			Type: "issue.activity.updated", RequestedData: &requested, CurrentInstance: &cj, IssueID: pk.String(),
			ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
			Subscriber: true, Notification: true,
		})
		a.enqueueDescriptionVersion(ctx, descriptionVersionJob{UpdatedIssue: cj, IssueID: pk.String(), UserID: c.User.ID.String()})
	}
	return c.NoContent()
}

// saveIssue is IssueCreateSerializer.update: replace assignees and labels
// when given, then save() the instance.
func (a *API) saveIssue(ctx context.Context, in *issueInput, cur *issueDetail, actor uuid.UUID) error {
	for _, rel := range []struct {
		table, col string
		ids        *[]uuid.UUID
	}{{"issue_assignees", "assignee_id", in.assigneeIDs}, {"issue_labels", "label_id", in.labelIDs}} {
		if rel.ids == nil {
			continue
		}
		if _, err := a.db.Exec(ctx, `UPDATE `+rel.table+` SET deleted_at = now() WHERE issue_id = $1 AND deleted_at IS NULL`,
			cur.ID); err != nil {
			return err
		}
		if err := a.bulkIssueRelations(ctx, rel.table, rel.col, cur.ID, cur.ProjectID, cur.workspaceID, *rel.ids,
			cur.CreatedBy, cur.UpdatedBy, true); err != nil {
			return err
		}
	}
	set := in.set
	state := cur.StateID
	if in.state != nil {
		state = *in.state
		if state == nil {
			var err error
			if state, err = defaultState(ctx, a.db, cur.ProjectID); err != nil {
				return err
			}
			set.drop("state_id")
			set.add("state_id", state)
		}
	}
	// _sync_completed_at: only when the state changes.
	if !uuidPtrEqual(state, cur.StateID) {
		var completedAt *time.Time
		if state != nil {
			var completed bool
			if err := a.db.QueryRow(ctx, `SELECT "group" = 'completed' FROM states WHERE id = $1`, *state).Scan(&completed); err != nil {
				return err
			}
			if completed {
				now := time.Now()
				completedAt = &now
			}
		}
		set.add("completed_at", completedAt)
	}
	html := cur.DescriptionHTML
	if in.descriptionHTML != nil {
		html = *in.descriptionHTML
		set.add("description_html", html)
	}
	set.add("description_stripped", stripTags(html))
	set.add("updated_at", time.Now())
	set.add("updated_by_id", actor)
	_, err := a.db.Exec(ctx, `UPDATE issues SET `+set.sql()+` WHERE id = $1`, append([]any{cur.ID}, set.args...)...)
	return err
}

func uuidPtrEqual(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// deleteIssue ports IssueViewSet.destroy.
func (a *API) deleteIssue(c *httpx.Ctx) error {
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
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT i.id FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE w.slug = $1 AND i.project_id = $2 AND i.id = $3 AND i.deleted_at IS NULL`, slug, projectID, pk).Scan(&id); err != nil {
		return err
	}
	if err := softdelete.Row(ctx, a.db, "issues", id, c.User.ID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `DELETE FROM user_recent_visits v USING workspaces w WHERE w.id = v.workspace_id
		AND w.slug = $1 AND v.project_id = $2 AND v.entity_identifier = $3 AND v.entity_name = 'issue'`, slug, projectID, pk); err != nil {
		return err
	}
	requested := `{"issue_id": "` + pk.String() + `"}`
	empty := "{}"
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "issue.activity.deleted", RequestedData: &requested, CurrentInstance: &empty, CurrentIsDict: true,
		IssueID: pk.String(), ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
		Subscriber: false, Notification: true,
	})
	return c.NoContent()
}
