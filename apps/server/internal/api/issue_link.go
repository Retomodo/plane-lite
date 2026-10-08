package api

import (
	"context"
	"errors"
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

// IssueLinkViewSet (P/issues/<issue_id>/issue-links/).

// issueLink is IssueLinkSerializer: every model field plus the creator as
// UserLite.
type issueLink struct {
	ID              uuid.UUID      `json:"id"`
	CreatedByDetail *userLite      `json:"created_by_detail"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       *time.Time     `json:"deleted_at"`
	Title           *string        `json:"title"`
	URL             string         `json:"url"`
	Metadata        jsontext.Value `json:"metadata"`
	CreatedBy       *uuid.UUID     `json:"created_by"`
	UpdatedBy       *uuid.UUID     `json:"updated_by"`
	Project         uuid.UUID      `json:"project"`
	Workspace       uuid.UUID      `json:"workspace"`
	Issue           uuid.UUID      `json:"issue"`
}

const issueLinkSelect = `SELECT l.id, l.created_at, l.updated_at, l.deleted_at, l.title, l.url, l.metadata::text,
		l.created_by_id, l.updated_by_id, l.project_id, l.workspace_id, l.issue_id, u.id, u.first_name, u.last_name,
		u.avatar, u.is_bot, u.display_name, fa.id, fa.entity_type
	FROM issue_links l JOIN workspaces w ON w.id = l.workspace_id JOIN projects pr ON pr.id = l.project_id
	LEFT JOIN users u ON u.id = l.created_by_id LEFT JOIN file_assets fa ON fa.id = u.avatar_asset_id`

func scanIssueLink(row pgx.Row) (*issueLink, error) {
	var (
		l                                  issueLink
		meta                               string
		userID, avatarID                   *uuid.UUID
		first, last, avatar, display, kind *string
		bot                                *bool
	)
	if err := row.Scan(&l.ID, &l.CreatedAt, &l.UpdatedAt, &l.DeletedAt, &l.Title, &l.URL, &meta, &l.CreatedBy,
		&l.UpdatedBy, &l.Project, &l.Workspace, &l.Issue, &userID, &first, &last, &avatar, &bot, &display,
		&avatarID, &kind); err != nil {
		return nil, err
	}
	l.Metadata = jsontext.Value(meta)
	if userID != nil {
		u := userLite{ID: *userID, FirstName: *first, LastName: *last, Avatar: *avatar, IsBot: *bot, DisplayName: *display}
		u.AvatarURL = imageURL(avatarID, kind, &u.Avatar)
		l.CreatedByDetail = &u
	}
	return &l, nil
}

// issueLinks is get_queryset(): the issue's live links, for a user with an
// active membership row of the (unarchived) project, newest first.
func (a *API) issueLinks(ctx context.Context, slug string, projectID, issueID, user uuid.UUID, extra string, args ...any) ([]*issueLink, error) {
	rows, err := a.db.Query(ctx, issueLinkSelect+`
		WHERE l.deleted_at IS NULL AND w.slug = $1 AND l.project_id = $2 AND l.issue_id = $3 AND pr.archived_at IS NULL
			AND EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = l.project_id AND pm.member_id = $4
				AND pm.is_active)`+extra+`
		ORDER BY l.created_at DESC`, append([]any{slug, projectID, issueID, user}, args...)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*issueLink, error) { return scanIssueLink(row) })
}

// issueLinkFromQueryset is self.get_queryset().get(id=...).
func (a *API) issueLinkFromQueryset(c *httpx.Ctx, projectID, issueID, id uuid.UUID) (*issueLink, error) {
	links, err := a.issueLinks(c.Context(), c.Param("slug"), projectID, issueID, c.User.ID, ` AND l.id = $5`, id)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, pgx.ErrNoRows
	}
	return links[0], nil
}

// issueLinkObject is IssueLink.objects.get(workspace__slug, project_id,
// issue_id, pk).
func (a *API) issueLinkObject(c *httpx.Ctx) (*issueLink, error) {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return nil, err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return nil, err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return nil, err
	}
	return scanIssueLink(a.db.QueryRow(c.Context(), issueLinkSelect+`
		WHERE l.deleted_at IS NULL AND w.slug = $1 AND l.project_id = $2 AND l.issue_id = $3 AND l.id = $4`,
		c.Param("slug"), projectID, issueID, pk))
}

// listIssueLinks ports IssueLinkViewSet.list (DRF's default, unpaginated).
func (a *API) listIssueLinks(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	links, err := a.issueLinks(c.Context(), c.Param("slug"), projectID, issueID, c.User.ID, "")
	if err != nil {
		return err
	}
	if links == nil {
		links = []*issueLink{}
	}
	return c.JSON(http.StatusOK, links)
}

// issueLinkInput is IssueLinkSerializer's validated data; nil fields were
// not sent.
type issueLinkInput struct {
	title     **string
	url       *string
	metadata  jsontext.Value
	deletedAt **time.Time
}

var errInvalidLinkURL = map[string]string{"error": "Invalid URL format."}

// validateIssueLink runs IssueLinkSerializer over request.data, which its
// to_internal_value first edits in place: a truthy url without an http(s)
// scheme gets "http://" prepended (a non-string one crashes).
func validateIssueLink(data *drf.Data, loc *time.Location, partial bool) (*issueLinkInput, error) {
	if !data.IsDict() {
		return nil, errViewCrash // data.get on a list
	}
	if raw, ok := data.Get("url"); ok && drf.PyTruthy(raw) {
		if !raw.IsString() {
			return nil, errViewCrash // url.startswith on a non-string
		}
		if s := raw.Str(); !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			data.Set("url", drf.JSONValue(jsonString("http://"+s)))
		}
	}
	v := drf.NewValidator(data, loc)
	in := &issueLinkInput{}
	if !partial {
		v.Require("url")
	}
	if data.Has("deleted_at") {
		if t, ok := v.DateTime("deleted_at", true); ok {
			in.deletedAt = &t
		}
	}
	if data.Has("title") {
		if s, ok := v.Char("title", drf.CharField{MaxLength: 255, AllowBlank: true, AllowNull: true}); ok {
			in.title = &s
		}
	}
	badURL := false
	if data.Has("url") {
		if s, ok := v.Char("url", drf.CharField{}); ok {
			// validate_url: Django's URLValidator.
			if drf.ValidURL(*s) {
				in.url = s
			} else {
				badURL = true
			}
		}
	}
	if data.Has("metadata") {
		if m, ok := v.JSON("metadata", false); ok {
			in.metadata = m
		}
	}
	err := v.Err()
	if err == nil && !badURL {
		return in, nil
	}
	body := map[string]any{}
	var he *httpx.Error
	if errors.As(err, &he) {
		switch b := he.Body.(type) {
		case map[string][]string:
			for k, msgs := range b {
				body[k] = msgs
			}
		case map[string]any:
			for k, msgs := range b {
				body[k] = msgs
			}
		}
	}
	if badURL {
		body["url"] = errInvalidLinkURL
	}
	return nil, httpx.Body(http.StatusBadRequest, body)
}

var errIssueLinkExists = httpx.Err(http.StatusBadRequest, "URL already exists for this Issue")

// linkURLTaken is the serializers' duplicate check: a live link of the issue
// with this url (url=None matches nothing).
func (a *API) linkURLTaken(ctx context.Context, url *string, issueID uuid.UUID, except *uuid.UUID) (bool, error) {
	if url == nil {
		return false, nil
	}
	var taken bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue_links WHERE url = $1 AND issue_id = $2
		AND deleted_at IS NULL AND ($3::uuid IS NULL OR id <> $3))`, *url, issueID, except).Scan(&taken)
	return taken, err
}

func (a *API) linkActivity(ctx context.Context, c *httpx.Ctx, typ string, requested, current *string, issueID, projectID uuid.UUID) {
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: typ, RequestedData: requested, CurrentInstance: current, IssueID: issueID.String(),
		ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
		Subscriber: true, Notification: true,
	})
}

// createIssueLink ports IssueLinkViewSet.create.
func (a *API) createIssueLink(c *httpx.Ctx) error {
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
	in, err := validateIssueLink(data, c.Loc(), false)
	if err != nil {
		return err
	}
	// IssueLinkSerializer.create
	if taken, err := a.linkURLTaken(ctx, in.url, issueID, nil); err != nil {
		return err
	} else if taken {
		return errIssueLinkExists
	}
	var title *string
	if in.title != nil {
		title = *in.title
	}
	var deletedAt *time.Time
	if in.deletedAt != nil {
		deletedAt = *in.deletedAt
	}
	metadata := in.metadata
	if metadata == nil {
		metadata = jsontext.Value("{}")
	}
	id := uuid.New()
	// The issue is not checked: another project's issue is linked as is,
	// and a missing one fails the foreign key (IntegrityError, a 400).
	if _, err := a.db.Exec(ctx, `INSERT INTO issue_links (id, created_at, updated_at, deleted_at, title, url, metadata,
			created_by_id, project_id, workspace_id, issue_id)
		SELECT $1, now(), now(), $2, $3, $4, $5::jsonb, $6, p.id, p.workspace_id, $8 FROM projects p WHERE p.id = $7`,
		id, deletedAt, title, *in.url, string(metadata), c.User.ID, projectID, issueID); err != nil {
		return err
	}
	saved, err := scanIssueLink(a.db.QueryRow(ctx, issueLinkSelect+` WHERE l.id = $1`, id))
	if err != nil {
		return err
	}
	a.enqueueCrawlLink(ctx, id, saved.URL, c.User.ID)
	requested, err := httpx.Marshal(saved, c.Loc())
	if err != nil {
		return err
	}
	rs := string(requested)
	a.linkActivity(ctx, c, "link.activity.created", &rs, nil, issueID, projectID)
	link, err := a.issueLinkFromQueryset(c, projectID, issueID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, link)
}

// updateIssueLink ports IssueLinkViewSet.partial_update.
func (a *API) updateIssueLink(c *httpx.Ctx) error {
	ctx := c.Context()
	cur, err := a.issueLinkObject(c)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	// requested_data is the body as sent, before the serializer edits it.
	requested := string(data.Object())
	currentJSON, err := httpx.Marshal(cur, c.Loc())
	if err != nil {
		return err
	}
	in, err := validateIssueLink(data, c.Loc(), true)
	if err != nil {
		return err
	}
	if taken, err := a.linkURLTaken(ctx, in.url, cur.Issue, &cur.ID); err != nil {
		return err
	} else if taken {
		return errIssueLinkExists
	}
	// save(): every field written back, updated_by the request user.
	title, url, metadata, deletedAt := cur.Title, cur.URL, cur.Metadata, cur.DeletedAt
	if in.title != nil {
		title = *in.title
	}
	if in.url != nil {
		url = *in.url
	}
	if in.metadata != nil {
		metadata = in.metadata
	}
	if in.deletedAt != nil {
		deletedAt = *in.deletedAt
	}
	if _, err := a.db.Exec(ctx, `UPDATE issue_links SET title = $2, url = $3, metadata = $4::jsonb, deleted_at = $5,
			updated_at = now(), updated_by_id = $6 WHERE id = $1`,
		cur.ID, title, url, string(metadata), deletedAt, c.User.ID); err != nil {
		return err
	}
	if url != cur.URL {
		a.enqueueCrawlLink(ctx, cur.ID, url, c.User.ID)
	}
	cj := string(currentJSON)
	a.linkActivity(ctx, c, "link.activity.updated", &requested, &cj, cur.Issue, cur.Project)
	link, err := a.issueLinkFromQueryset(c, cur.Project, cur.Issue, cur.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, link)
}

// deleteIssueLink ports IssueLinkViewSet.destroy: the activity goes out
// before the soft delete.
func (a *API) deleteIssueLink(c *httpx.Ctx) error {
	ctx := c.Context()
	cur, err := a.issueLinkObject(c)
	if err != nil {
		return err
	}
	currentJSON, err := httpx.Marshal(cur, c.Loc())
	if err != nil {
		return err
	}
	requested := `{"link_id": "` + cur.ID.String() + `"}`
	cj := string(currentJSON)
	a.linkActivity(ctx, c, "link.activity.deleted", &requested, &cj, cur.Issue, cur.Project)
	if err := softdelete.Row(ctx, a.db, "issue_links", cur.ID, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}
