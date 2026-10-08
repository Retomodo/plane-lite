package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// projectLitePerm is the ProjectLitePermission class: any active member of
// the project.
func (a *API) projectLitePerm(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		project, err := c.UUIDParam("project_id")
		if err != nil {
			return err
		}
		role, err := a.projectRole(c.Context(), c.Param("slug"), project, c.User.ID)
		if err != nil {
			return err
		}
		if role == 0 {
			return httpx.ErrForbidden
		}
		return h(c)
	}
}

// issueSubscriber is IssueSubscriberSerializer.
type issueSubscriber struct {
	ID         uuid.UUID  `json:"id"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at"`
	CreatedBy  *uuid.UUID `json:"created_by"`
	UpdatedBy  *uuid.UUID `json:"updated_by"`
	Project    uuid.UUID  `json:"project"`
	Workspace  uuid.UUID  `json:"workspace"`
	Issue      uuid.UUID  `json:"issue"`
	Subscriber uuid.UUID  `json:"subscriber"`
}

// subscriptionParams reads the project and issue of a subscribe/ URL.
func subscriptionParams(c *httpx.Ctx) (project, issue uuid.UUID, err error) {
	if project, err = c.UUIDParam("project_id"); err != nil {
		return
	}
	issue, err = c.UUIDParam("issue_id")
	return
}

// subscribedSQL is IssueSubscriber.objects.filter(issue=issue_id,
// subscriber=request.user, workspace__slug=slug, project=project_id).
const subscribedSQL = `FROM issue_subscribers x JOIN workspaces w ON w.id = x.workspace_id
	WHERE x.issue_id = $1 AND x.subscriber_id = $2 AND w.slug = $3 AND x.project_id = $4 AND x.deleted_at IS NULL`

// subscriptionStatus ports IssueSubscriberViewSet.subscription_status.
func (a *API) subscriptionStatus(c *httpx.Ctx) error {
	projectID, issueID, err := subscriptionParams(c)
	if err != nil {
		return err
	}
	var subscribed bool
	if err := a.db.QueryRow(c.Context(), `SELECT EXISTS (SELECT 1 `+subscribedSQL+`)`,
		issueID, c.User.ID, c.Param("slug"), projectID).Scan(&subscribed); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]bool{"subscribed": subscribed})
}

// subscribe ports IssueSubscriberViewSet.subscribe. The issue is not
// looked up: a missing one fails the foreign key (IntegrityError, 400).
func (a *API) subscribe(c *httpx.Ctx) error {
	projectID, issueID, err := subscriptionParams(c)
	if err != nil {
		return err
	}
	ctx := c.Context()
	var subscribed bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 `+subscribedSQL+`)`,
		issueID, c.User.ID, c.Param("slug"), projectID).Scan(&subscribed); err != nil {
		return err
	}
	if subscribed {
		return httpx.Body(http.StatusBadRequest, map[string]string{"message": "User already subscribed to the issue."})
	}
	var s issueSubscriber
	if err := a.db.QueryRow(ctx, `INSERT INTO issue_subscribers (issue_id, subscriber_id, project_id, workspace_id,
			created_by_id, created_at, updated_at)
		SELECT $1, $2, p.id, p.workspace_id, $2, clock_timestamp(), clock_timestamp() FROM projects p WHERE p.id = $3
		RETURNING id, created_at, updated_at, deleted_at, created_by_id, updated_by_id, project_id, workspace_id,
			issue_id, subscriber_id`, issueID, c.User.ID, projectID).
		Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt, &s.DeletedAt, &s.CreatedBy, &s.UpdatedBy, &s.Project, &s.Workspace,
			&s.Issue, &s.Subscriber); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, s)
}

// unsubscribe ports IssueSubscriberViewSet.unsubscribe.
func (a *API) unsubscribe(c *httpx.Ctx) error {
	projectID, issueID, err := subscriptionParams(c)
	if err != nil {
		return err
	}
	ctx := c.Context()
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT x.id `+subscribedSQL, issueID, c.User.ID, c.Param("slug"), projectID).
		Scan(&id); err != nil {
		return err
	}
	if err := softdelete.Row(ctx, a.db, "issue_subscribers", id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}
