package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// issueReaction is IssueReactionSerializer.
type issueReaction struct {
	ID          uuid.UUID  `json:"id"`
	ActorDetail *userLite  `json:"actor_detail"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
	Reaction    string     `json:"reaction"`
	CreatedBy   *uuid.UUID `json:"created_by"`
	UpdatedBy   *uuid.UUID `json:"updated_by"`
	Project     uuid.UUID  `json:"project"`
	Workspace   uuid.UUID  `json:"workspace"`
	Actor       uuid.UUID  `json:"actor"`
	Issue       uuid.UUID  `json:"issue"`
}

const issueReactionCols = `r.id, r.created_at, r.updated_at, r.deleted_at, r.reaction, r.created_by_id, r.updated_by_id,
	r.project_id, r.workspace_id, r.actor_id, r.issue_id`

func (a *API) queryIssueReactions(ctx context.Context, sql string, args ...any) ([]*issueReaction, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*issueReaction, error) {
		var r issueReaction
		err := row.Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt, &r.Reaction, &r.CreatedBy, &r.UpdatedBy,
			&r.Project, &r.Workspace, &r.Actor, &r.Issue)
		return &r, err
	})
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []*issueReaction{}
	}
	l := a.details(ctx)
	for _, r := range list {
		if r.ActorDetail, err = l.user(&r.Actor); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// reactionMember is the get_queryset filter of the reaction viewsets: the
// request user is an active member of the (unarchived) project.
const reactionMember = `EXISTS (SELECT 1 FROM project_members pm JOIN projects p ON p.id = pm.project_id
	WHERE pm.project_id = r.project_id AND pm.member_id = $4 AND pm.is_active AND p.archived_at IS NULL)`

// listIssueReactions ports IssueReactionViewSet.list (the DRF default, with
// no permission beyond IsAuthenticated: non-members get an empty list).
func (a *API) listIssueReactions(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	list, err := a.queryIssueReactions(c.Context(), `SELECT `+issueReactionCols+`
		FROM issue_reactions r JOIN workspaces w ON w.id = r.workspace_id
		WHERE w.slug = $1 AND r.project_id = $2 AND r.issue_id = $3 AND r.deleted_at IS NULL AND `+reactionMember+`
		ORDER BY r.created_at DESC`, c.Param("slug"), projectID, issueID, c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, list)
}

// validateReaction runs the reaction serializers: reaction is required;
// IssueReactionSerializer also takes created_by and updated_by, which save()
// then overrides.
func (a *API) validateReaction(ctx context.Context, data *drf.Data, loc *time.Location, auditFields bool) (string, *drf.Validator, error) {
	v := drf.NewValidator(data, loc)
	v.Require("reaction")
	reaction, _ := v.Char("reaction", drf.CharField{})
	if auditFields {
		for _, name := range []string{"created_by", "updated_by"} {
			if _, _, err := v.PK(name, true, a.liveExists(ctx, `SELECT 1 FROM users WHERE id = $1`)); err != nil {
				return "", nil, err
			}
		}
	}
	if !v.Valid() || reaction == nil {
		return "", v, nil
	}
	return *reaction, v, nil
}

// createIssueReaction ports IssueReactionViewSet.create. The issue is not
// looked up: a missing one fails the foreign key (IntegrityError, 400).
func (a *API) createIssueReaction(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	reaction, v, err := a.validateReaction(ctx, data, c.Loc(), true)
	if err != nil {
		return err
	}
	if !v.Valid() {
		return v.Err()
	}
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `INSERT INTO issue_reactions (reaction, issue_id, project_id, workspace_id, actor_id,
			created_by_id, created_at, updated_at)
		SELECT $1, $2, p.id, p.workspace_id, $4, $4, clock_timestamp(), clock_timestamp() FROM projects p WHERE p.id = $3
		RETURNING id`, reaction, issueID, projectID, c.User.ID).Scan(&id); err != nil {
		return err
	}
	requested := string(data.Object())
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "issue_reaction.activity.created", RequestedData: &requested, IssueID: issueID.String(),
		ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(), Subscriber: true,
		Notification: true,
	})
	list, err := a.queryIssueReactions(ctx, `SELECT `+issueReactionCols+` FROM issue_reactions r WHERE r.id = $1`, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, list[0])
}

// deleteIssueReaction ports IssueReactionViewSet.destroy.
func (a *API) deleteIssueReaction(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	code := c.Param("reaction_code")
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT r.id FROM issue_reactions r JOIN workspaces w ON w.id = r.workspace_id
		WHERE w.slug = $1 AND r.project_id = $2 AND r.issue_id = $3 AND r.reaction = $4 AND r.actor_id = $5
			AND r.deleted_at IS NULL`, c.Param("slug"), projectID, issueID, code, c.User.ID).Scan(&id); err != nil {
		return err
	}
	current, err := pyJSONDumps(map[string]string{"reaction": code, "identifier": id.String()}, "reaction", "identifier")
	if err != nil {
		return err
	}
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "issue_reaction.activity.deleted", CurrentInstance: &current, IssueID: issueID.String(),
		ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(), Subscriber: true,
		Notification: true,
	})
	if err := softdelete.Row(ctx, a.db, "issue_reactions", id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// pyJSONDumps is json.dumps of a dict of strings, keys in the given order.
func pyJSONDumps(m map[string]string, keys ...string) (string, error) {
	out := "{"
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return "", err
		}
		vb, err := json.Marshal(m[k])
		if err != nil {
			return "", err
		}
		out += string(kb) + ": " + string(vb)
	}
	return out + "}", nil
}

func (a *API) queryCommentReactions(ctx context.Context, sql string, args ...any) ([]commentReaction, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (commentReaction, error) {
		return scanCommentReaction(row)
	})
}

// listCommentReactions ports CommentReactionViewSet.list (DRF default).
func (a *API) listCommentReactions(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	commentID, err := c.UUIDParam("comment_id")
	if err != nil {
		return err
	}
	list, err := a.queryCommentReactions(c.Context(), `SELECT `+commentReactionCols+`
		FROM comment_reactions r JOIN users u ON u.id = r.actor_id JOIN workspaces w ON w.id = r.workspace_id
		WHERE w.slug = $1 AND r.project_id = $2 AND r.comment_id = $3 AND r.deleted_at IS NULL AND `+reactionMember+`
		ORDER BY r.created_at DESC`, c.Param("slug"), projectID, commentID, c.User.ID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []commentReaction{}
	}
	return c.JSON(http.StatusOK, list)
}

var errReactionExists = httpx.Err(http.StatusBadRequest, "Reaction already exists for the user")

// createCommentReaction ports CommentReactionViewSet.create. Any
// IntegrityError (a duplicate, or a comment that does not exist) is
// reported as an existing reaction.
func (a *API) createCommentReaction(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	commentID, err := c.UUIDParam("comment_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	reaction, v, err := a.validateReaction(ctx, data, c.Loc(), false)
	if err != nil {
		return err
	}
	if !v.Valid() {
		return v.Err()
	}
	var id uuid.UUID
	err = a.db.QueryRow(ctx, `INSERT INTO comment_reactions (reaction, comment_id, project_id, workspace_id, actor_id,
			created_by_id, created_at, updated_at)
		SELECT $1, $2, p.id, p.workspace_id, $4, $4, clock_timestamp(), clock_timestamp() FROM projects p WHERE p.id = $3
		RETURNING id`, reaction, commentID, projectID, c.User.ID).Scan(&id)
	if errIntegrity(err) {
		return errReactionExists
	}
	if err != nil {
		return err
	}
	requested := string(data.Object())
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "comment_reaction.activity.created", RequestedData: &requested, ActorID: c.User.ID.String(),
		ProjectID: projectID.String(), Epoch: time.Now().Unix(), Subscriber: true, Notification: true,
	})
	list, err := a.queryCommentReactions(ctx, `SELECT `+commentReactionCols+`
		FROM comment_reactions r JOIN users u ON u.id = r.actor_id WHERE r.id = $1`, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, list[0])
}

// deleteCommentReaction ports CommentReactionViewSet.destroy.
func (a *API) deleteCommentReaction(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	commentID, err := c.UUIDParam("comment_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	code := c.Param("reaction_code")
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT r.id FROM comment_reactions r JOIN workspaces w ON w.id = r.workspace_id
		WHERE w.slug = $1 AND r.project_id = $2 AND r.comment_id = $3 AND r.reaction = $4 AND r.actor_id = $5
			AND r.deleted_at IS NULL`, c.Param("slug"), projectID, commentID, code, c.User.ID).Scan(&id); err != nil {
		return err
	}
	current, err := pyJSONDumps(map[string]string{"reaction": code, "identifier": id.String(), "comment_id": commentID.String()},
		"reaction", "identifier", "comment_id")
	if err != nil {
		return err
	}
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "comment_reaction.activity.deleted", CurrentInstance: &current, ActorID: c.User.ID.String(),
		ProjectID: projectID.String(), Epoch: time.Now().Unix(), Subscriber: true, Notification: true,
	})
	if err := softdelete.Row(ctx, a.db, "comment_reactions", id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}
