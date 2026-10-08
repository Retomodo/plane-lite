package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/httpx"
)

// issueActivityOut is IssueActivitySerializer. source_data reads intake
// rows, which plane-lite never has, so it is always null.
type issueActivityOut struct {
	ID              uuid.UUID      `json:"id"`
	ActorDetail     *userLite      `json:"actor_detail"`
	IssueDetail     *issueFlat     `json:"issue_detail"`
	ProjectDetail   *projectLite   `json:"project_detail"`
	WorkspaceDetail *workspaceLite `json:"workspace_detail"`
	SourceData      *struct{}      `json:"source_data"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       *time.Time     `json:"deleted_at"`
	Verb            string         `json:"verb"`
	Field           *string        `json:"field"`
	OldValue        *string        `json:"old_value"`
	NewValue        *string        `json:"new_value"`
	Comment         string         `json:"comment"`
	Attachments     []string       `json:"attachments"`
	OldIdentifier   *uuid.UUID     `json:"old_identifier"`
	NewIdentifier   *uuid.UUID     `json:"new_identifier"`
	Epoch           *float64       `json:"epoch"`
	CreatedBy       *uuid.UUID     `json:"created_by"`
	UpdatedBy       *uuid.UUID     `json:"updated_by"`
	Project         uuid.UUID      `json:"project"`
	Workspace       uuid.UUID      `json:"workspace"`
	Issue           *uuid.UUID     `json:"issue"`
	IssueComment    *uuid.UUID     `json:"issue_comment"`
	Actor           *uuid.UUID     `json:"actor"`
}

// historyMember is the history querysets' project__project_projectmember
// join: a plain join (no manager), so each matching membership row yields
// the activity once more.
const historyMember = `JOIN projects p ON p.id = x.project_id JOIN project_members pm ON pm.project_id = p.id
	JOIN workspaces w ON w.id = x.workspace_id`

const historyWhere = `x.issue_id = $1 AND x.deleted_at IS NULL AND pm.member_id = $2 AND pm.is_active
	AND p.archived_at IS NULL AND w.slug = $3 AND ($4::timestamptz IS NULL OR x.created_at > $4)`

func (a *API) historyActivities(ctx context.Context, l *detailLoader, args ...any) ([]*issueActivityOut, error) {
	rows, err := a.db.Query(ctx, `SELECT x.id, x.created_at, x.updated_at, x.deleted_at, x.verb, x.field, x.old_value,
			x.new_value, x.comment, x.attachments, x.old_identifier, x.new_identifier, x.epoch, x.created_by_id,
			x.updated_by_id, x.project_id, x.workspace_id, x.issue_id, x.issue_comment_id, x.actor_id
		FROM issue_activities x `+historyMember+`
		WHERE `+historyWhere+` AND NOT (x.field IS NOT NULL AND x.field IN ('comment', 'vote', 'reaction', 'draft'))
		ORDER BY x.created_at`, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*issueActivityOut, error) {
		var o issueActivityOut
		err := row.Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt, &o.DeletedAt, &o.Verb, &o.Field, &o.OldValue, &o.NewValue,
			&o.Comment, &o.Attachments, &o.OldIdentifier, &o.NewIdentifier, &o.Epoch, &o.CreatedBy, &o.UpdatedBy,
			&o.Project, &o.Workspace, &o.Issue, &o.IssueComment, &o.Actor)
		return &o, err
	})
	if err != nil {
		return nil, err
	}
	for _, o := range list {
		if o.Attachments == nil {
			o.Attachments = []string{}
		}
		if o.ActorDetail, err = l.user(o.Actor); err != nil {
			return nil, err
		}
		if o.IssueDetail, err = l.issue(o.Issue); err != nil {
			return nil, err
		}
		if o.ProjectDetail, err = l.project(o.Project); err != nil {
			return nil, err
		}
		if o.WorkspaceDetail, err = l.workspace(o.Workspace); err != nil {
			return nil, err
		}
	}
	if list == nil {
		list = []*issueActivityOut{}
	}
	return list, nil
}

func (a *API) historyComments(ctx context.Context, l *detailLoader, args ...any) ([]*issueComment, error) {
	rows, err := a.db.Query(ctx, `SELECT `+strings.ReplaceAll(commentCols, "c.", "x.")+`
		FROM issue_comments x `+historyMember+`
		WHERE `+historyWhere+`
		ORDER BY x.created_at`, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*issueComment, error) { return scanComment(row) })
	if err != nil {
		return nil, err
	}
	if err := l.fillComments(list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []*issueComment{}
	}
	return list, nil
}

// issueHistory ports IssueActivityEndpoint.get: the issue's activity (minus
// comment, vote, reaction and draft rows) and its comments, as seen through
// the request user's memberships of their projects, merged by the rendered
// created_at.
func (a *API) issueHistory(c *httpx.Ctx) error {
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	var gt *time.Time
	if c.HasQuery("created_at__gt") {
		// get_prep_value makes a naive value aware in the default
		// timezone (UTC), not the active one.
		t, ok := modelDateTime(c.Query("created_at__gt"), time.UTC)
		if !ok {
			return errFilterDetail
		}
		gt = &t
	}
	args := []any{issueID, c.User.ID, c.Param("slug"), gt}
	l := a.details(ctx)
	switch c.Query("activity_type") {
	case "issue-property":
		acts, err := a.historyActivities(ctx, l, args...)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, acts)
	case "issue-comment":
		comments, err := a.historyComments(ctx, l, args...)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, comments)
	}
	acts, err := a.historyActivities(ctx, l, args...)
	if err != nil {
		return err
	}
	comments, err := a.historyComments(ctx, l, args...)
	if err != nil {
		return err
	}
	// sorted(chain(activities, comments), key=created_at) over the
	// serialized strings: a stable sort, activities first on ties.
	type entry struct {
		key string
		v   any
	}
	loc := c.Loc()
	out := make([]entry, 0, len(acts)+len(comments))
	for _, o := range acts {
		out = append(out, entry{httpx.FormatDateTime(o.CreatedAt, loc), o})
	}
	for _, m := range comments {
		out = append(out, entry{httpx.FormatDateTime(m.CreatedAt, loc), m})
	}
	slices.SortStableFunc(out, func(x, y entry) int { return strings.Compare(x.key, y.key) })
	res := make([]any, len(out))
	for i, e := range out {
		res[i] = e.v
	}
	return c.JSON(http.StatusOK, res)
}
