package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/sanitize"
	"plane-lite/server/internal/softdelete"
)

// issueFlat is IssueFlatSerializer.
type issueFlat struct {
	ID              uuid.UUID      `json:"id"`
	Name            string         `json:"name"`
	DescriptionJSON jsontext.Value `json:"description_json"`
	DescriptionHTML string         `json:"description_html"`
	Priority        string         `json:"priority"`
	StartDate       *httpx.Date    `json:"start_date"`
	TargetDate      *httpx.Date    `json:"target_date"`
	SequenceID      int            `json:"sequence_id"`
	SortOrder       float64        `json:"sort_order"`
	IsDraft         bool           `json:"is_draft"`
}

// detailLoader reads the nested *_detail objects (actor, issue, project,
// workspace) that the comment, reaction and activity serializers render.
// Forward foreign keys go through the base manager, so soft-deleted rows
// still render. Each row is read once per request.
type detailLoader struct {
	ctx        context.Context
	q          db.Querier
	users      map[uuid.UUID]*userLite
	issues     map[uuid.UUID]*issueFlat
	projects   map[uuid.UUID]*projectLite
	workspaces map[uuid.UUID]*workspaceLite
}

func (a *API) details(ctx context.Context) *detailLoader {
	return &detailLoader{ctx: ctx, q: a.db, users: map[uuid.UUID]*userLite{}, issues: map[uuid.UUID]*issueFlat{},
		projects: map[uuid.UUID]*projectLite{}, workspaces: map[uuid.UUID]*workspaceLite{}}
}

// user is UserLiteSerializer(source="actor"): None for a null actor.
func (l *detailLoader) user(id *uuid.UUID) (*userLite, error) {
	if id == nil {
		return nil, nil
	}
	if u, ok := l.users[*id]; ok {
		return u, nil
	}
	var (
		u          userLite
		avatarID   *uuid.UUID
		avatarType *string
	)
	err := l.q.QueryRow(l.ctx, `SELECT u.id, u.first_name, u.last_name, u.avatar, u.is_bot, u.display_name, fa.id,
			fa.entity_type
		FROM users u LEFT JOIN file_assets fa ON fa.id = u.avatar_asset_id WHERE u.id = $1`, *id).
		Scan(&u.ID, &u.FirstName, &u.LastName, &u.Avatar, &u.IsBot, &u.DisplayName, &avatarID, &avatarType)
	if err != nil {
		return nil, err
	}
	u.AvatarURL = imageURL(avatarID, avatarType, &u.Avatar)
	l.users[*id] = &u
	return &u, nil
}

func (l *detailLoader) issue(id *uuid.UUID) (*issueFlat, error) {
	if id == nil {
		return nil, nil
	}
	if i, ok := l.issues[*id]; ok {
		return i, nil
	}
	var (
		i             issueFlat
		desc          string
		start, target *time.Time
	)
	err := l.q.QueryRow(l.ctx, `SELECT id, name, description_json::text, description_html, priority, start_date,
			target_date, sequence_id, sort_order, is_draft
		FROM issues WHERE id = $1`, *id).
		Scan(&i.ID, &i.Name, &desc, &i.DescriptionHTML, &i.Priority, &start, &target, &i.SequenceID, &i.SortOrder, &i.IsDraft)
	if err != nil {
		return nil, err
	}
	i.DescriptionJSON, i.StartDate, i.TargetDate = jsontext.Value(desc), dateOf(start), dateOf(target)
	l.issues[*id] = &i
	return &i, nil
}

func (l *detailLoader) project(id uuid.UUID) (*projectLite, error) {
	if p, ok := l.projects[id]; ok {
		return p, nil
	}
	var (
		p         projectLite
		coverID   *uuid.UUID
		coverType *string
		logo      string
	)
	err := l.q.QueryRow(l.ctx, `SELECT p.id, p.identifier, p.name, p.cover_image, p.cover_image_asset_id, ca.entity_type,
			p.logo_props::text, p.description
		FROM projects p LEFT JOIN file_assets ca ON ca.id = p.cover_image_asset_id WHERE p.id = $1`, id).
		Scan(&p.ID, &p.Identifier, &p.Name, &p.CoverImage, &coverID, &coverType, &logo, &p.Description)
	if err != nil {
		return nil, err
	}
	p.CoverImageURL, p.LogoProps = imageURL(coverID, coverType, p.CoverImage), jsontext.Value(logo)
	l.projects[id] = &p
	return &p, nil
}

func (l *detailLoader) workspace(id uuid.UUID) (*workspaceLite, error) {
	if w, ok := l.workspaces[id]; ok {
		return w, nil
	}
	var (
		w        workspaceLite
		logo     *string
		logoID   *uuid.UUID
		logoType *string
	)
	err := l.q.QueryRow(l.ctx, `SELECT w.id, w.name, w.slug, w.logo, w.logo_asset_id, wa.entity_type
		FROM workspaces w LEFT JOIN file_assets wa ON wa.id = w.logo_asset_id WHERE w.id = $1`, id).
		Scan(&w.ID, &w.Name, &w.Slug, &logo, &logoID, &logoType)
	if err != nil {
		return nil, err
	}
	w.LogoURL = imageURL(logoID, logoType, logo)
	l.workspaces[id] = &w
	return &w, nil
}

// commentReaction is CommentReactionSerializer.
type commentReaction struct {
	ID          uuid.UUID  `json:"id"`
	Actor       uuid.UUID  `json:"actor"`
	Comment     uuid.UUID  `json:"comment"`
	Reaction    string     `json:"reaction"`
	DisplayName string     `json:"display_name"`
	DeletedAt   *time.Time `json:"deleted_at"`
	Workspace   uuid.UUID  `json:"workspace"`
	Project     uuid.UUID  `json:"project"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CreatedBy   *uuid.UUID `json:"created_by"`
	UpdatedBy   *uuid.UUID `json:"updated_by"`
}

const commentReactionCols = `r.id, r.actor_id, r.comment_id, r.reaction, u.display_name, r.deleted_at, r.workspace_id,
	r.project_id, r.created_at, r.updated_at, r.created_by_id, r.updated_by_id`

func scanCommentReaction(row pgx.Row) (commentReaction, error) {
	var r commentReaction
	err := row.Scan(&r.ID, &r.Actor, &r.Comment, &r.Reaction, &r.DisplayName, &r.DeletedAt, &r.Workspace, &r.Project,
		&r.CreatedAt, &r.UpdatedAt, &r.CreatedBy, &r.UpdatedBy)
	return r, err
}

// issueComment is IssueCommentSerializer. Its is_member field is never
// annotated by the ported views, so DRF leaves the key out.
type issueComment struct {
	ID               uuid.UUID         `json:"id"`
	ActorDetail      *userLite         `json:"actor_detail"`
	IssueDetail      *issueFlat        `json:"issue_detail"`
	ProjectDetail    *projectLite      `json:"project_detail"`
	WorkspaceDetail  *workspaceLite    `json:"workspace_detail"`
	CommentReactions []commentReaction `json:"comment_reactions"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	DeletedAt        *time.Time        `json:"deleted_at"`
	CommentStripped  string            `json:"comment_stripped"`
	CommentJSON      jsontext.Value    `json:"comment_json"`
	CommentHTML      string            `json:"comment_html"`
	Attachments      []string          `json:"attachments"`
	Access           string            `json:"access"`
	ExternalSource   *string           `json:"external_source"`
	ExternalID       *string           `json:"external_id"`
	EditedAt         *time.Time        `json:"edited_at"`
	CreatedBy        *uuid.UUID        `json:"created_by"`
	UpdatedBy        *uuid.UUID        `json:"updated_by"`
	Project          uuid.UUID         `json:"project"`
	Workspace        uuid.UUID         `json:"workspace"`
	Issue            uuid.UUID         `json:"issue"`
	Actor            *uuid.UUID        `json:"actor"`
	Description      *uuid.UUID        `json:"description"`
	Parent           *uuid.UUID        `json:"parent"`
}

const commentCols = `c.id, c.created_at, c.updated_at, c.deleted_at, c.comment_stripped, c.comment_json::text,
	c.comment_html, c.attachments, c.access, c.external_source, c.external_id, c.edited_at, c.created_by_id,
	c.updated_by_id, c.project_id, c.workspace_id, c.issue_id, c.actor_id, c.description_id, c.parent_id`

func scanComment(row pgx.Row) (*issueComment, error) {
	var (
		m    issueComment
		json string
	)
	err := row.Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt, &m.CommentStripped, &json, &m.CommentHTML,
		&m.Attachments, &m.Access, &m.ExternalSource, &m.ExternalID, &m.EditedAt, &m.CreatedBy, &m.UpdatedBy,
		&m.Project, &m.Workspace, &m.Issue, &m.Actor, &m.Description, &m.Parent)
	if err != nil {
		return nil, err
	}
	m.CommentJSON = jsontext.Value(json)
	if m.Attachments == nil {
		m.Attachments = []string{}
	}
	return &m, nil
}

// fillComments adds the nested details and the live comment_reactions
// (newest first, the model's ordering) to serialized comments.
func (l *detailLoader) fillComments(comments []*issueComment) error {
	byID := map[uuid.UUID]*issueComment{}
	ids := make([]uuid.UUID, 0, len(comments))
	for _, m := range comments {
		var err error
		if m.ActorDetail, err = l.user(m.Actor); err != nil {
			return err
		}
		if m.IssueDetail, err = l.issue(&m.Issue); err != nil {
			return err
		}
		if m.ProjectDetail, err = l.project(m.Project); err != nil {
			return err
		}
		if m.WorkspaceDetail, err = l.workspace(m.Workspace); err != nil {
			return err
		}
		m.CommentReactions = []commentReaction{}
		byID[m.ID] = m
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := l.q.Query(l.ctx, `SELECT `+commentReactionCols+`
		FROM comment_reactions r JOIN users u ON u.id = r.actor_id
		WHERE r.comment_id = ANY($1) AND r.deleted_at IS NULL ORDER BY r.created_at DESC`, ids)
	if err != nil {
		return err
	}
	reactions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (commentReaction, error) {
		return scanCommentReaction(row)
	})
	if err != nil {
		return err
	}
	for _, r := range reactions {
		m := byID[r.Comment]
		m.CommentReactions = append(m.CommentReactions, r)
	}
	return nil
}

// loadComment serializes one comment row (whatever its deleted_at).
func (a *API) loadComment(ctx context.Context, id uuid.UUID) (*issueComment, error) {
	m, err := scanComment(a.db.QueryRow(ctx, `SELECT `+commentCols+` FROM issue_comments c WHERE c.id = $1`, id))
	if err != nil {
		return nil, err
	}
	if err := a.details(ctx).fillComments([]*issueComment{m}); err != nil {
		return nil, err
	}
	return m, nil
}

// commentObject is IssueComment.objects.get(workspace__slug=slug,
// project_id=project_id, issue_id=issue_id, pk=pk), serialized.
func (a *API) commentObject(c *httpx.Ctx) (*issueComment, error) {
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
	ctx := c.Context()
	m, err := scanComment(a.db.QueryRow(ctx, `SELECT `+commentCols+`
		FROM issue_comments c JOIN workspaces w ON w.id = c.workspace_id
		WHERE w.slug = $1 AND c.project_id = $2 AND c.issue_id = $3 AND c.id = $4 AND c.deleted_at IS NULL`,
		c.Param("slug"), projectID, issueID, pk))
	if err != nil {
		return nil, err
	}
	if err := a.details(ctx).fillComments([]*issueComment{m}); err != nil {
		return nil, err
	}
	return m, nil
}

// allowComment is allow_permission(roles, creator=True, model=IssueComment):
// an active workspace member who created the comment (any live comment)
// passes whatever their project role.
func (a *API) allowComment(roles []int, h httpx.HandlerFunc) httpx.HandlerFunc {
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
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue_comments
			WHERE id = $1 AND created_by_id = $2 AND deleted_at IS NULL)`, pk, c.User.ID).Scan(&creator); err != nil {
			return err
		}
		if creator {
			return h(c)
		}
		return a.allowProject(roles, h)(c)
	}
}

var commentAccess = []string{"INTERNAL", "EXTERNAL"}

// commentInput is IssueCommentSerializer's validated_data.
type commentInput struct {
	set  setList
	html *string        // comment_html, sanitized
	json jsontext.Value // comment_json
}

// validateComment runs IssueCommentSerializer (partial or not; no field is
// required) and its validate(). current is the instance being updated,
// which description's UniqueValidator leaves out. A nil input with a nil
// error means the validator holds the errors.
func (a *API) validateComment(ctx context.Context, data *drf.Data, loc *time.Location, current *uuid.UUID) (*commentInput, *drf.Validator, error) {
	v := drf.NewValidator(data, loc)
	in := &commentInput{}
	set := &in.set
	if t, ok := v.DateTime("deleted_at", true); ok {
		set.add("deleted_at", t)
	}
	v.Char("comment_stripped", drf.CharField{AllowBlank: true}) // overwritten by save()
	if raw, ok := v.JSON("comment_json", false); ok {
		in.json = raw
		set.addCast("comment_json", string(raw), "::jsonb")
	}
	if s, ok := v.Char("comment_html", drf.CharField{AllowBlank: true}); ok {
		in.html = s
	}
	// ArrayField(URLField(), size=10).
	if list, ok := v.CharList("attachments", drf.CharField{URL: true, MaxLength: 200}, 10); ok {
		set.add("attachments", list)
	}
	if s, ok := v.Choice("access", commentAccess, drf.ChoiceField{}); ok {
		set.add("access", *s)
	}
	for _, name := range []string{"external_source", "external_id"} {
		if s, ok := v.Char(name, drf.CharField{MaxLength: 255, AllowBlank: true, AllowNull: true}); ok {
			set.add(name, s)
		}
	}
	if t, ok := v.DateTime("edited_at", true); ok {
		set.add("edited_at", t)
	}
	if id, ok, err := v.PK("actor", true, a.liveExists(ctx, `SELECT 1 FROM users WHERE id = $1`)); err != nil {
		return nil, nil, err
	} else if ok {
		set.add("actor_id", id)
	}
	desc, ok, err := v.PK("description", true, a.liveExists(ctx, `SELECT 1 FROM descriptions WHERE id = $1 AND deleted_at IS NULL`))
	if err != nil {
		return nil, nil, err
	}
	if ok && desc != nil {
		// The OneToOneField's UniqueValidator(IssueComment.objects).
		var taken bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue_comments WHERE description_id = $1
			AND deleted_at IS NULL AND id IS DISTINCT FROM $2)`, *desc, current).Scan(&taken); err != nil {
			return nil, nil, err
		}
		if taken {
			v.Add("description", "Issue Comment with this description already exists.")
			ok = false
		}
	}
	if ok {
		set.add("description_id", desc)
	}
	if id, ok, err := v.PK("parent", true, a.liveExists(ctx, `SELECT 1 FROM issue_comments WHERE id = $1 AND deleted_at IS NULL`)); err != nil {
		return nil, nil, err
	} else if ok {
		set.add("parent_id", id)
	}
	if !v.Valid() {
		return nil, v, nil
	}
	// validate()
	if in.html != nil && *in.html != "" {
		clean, _, err := sanitize.HTML(*in.html)
		if err != nil {
			v.Add("comment_html", "HTML content is not valid")
			return nil, v, nil
		}
		in.html = &clean
	}
	return in, v, nil
}

// commentStripped is IssueComment.save()'s comment_stripped.
func commentStripped(html string) string {
	if html == "" {
		return ""
	}
	if s := stripTags(html); s != nil {
		return *s
	}
	return ""
}

// createDescription is the Description row IssueComment.save() creates for
// a comment without one, linked with save(update_fields=["description_id"]).
func createDescription(ctx context.Context, q db.Querier, comment, actor uuid.UUID) error {
	_, err := q.Exec(ctx, `WITH d AS (
			INSERT INTO descriptions (workspace_id, project_id, created_by_id, description_stripped, description_json,
				description_html, created_at, updated_at)
			SELECT c.workspace_id, c.project_id, $2, CASE WHEN c.comment_html = '' THEN NULL ELSE c.comment_stripped END,
				c.comment_json, c.comment_html, clock_timestamp(), clock_timestamp()
			FROM issue_comments c WHERE c.id = $1
			RETURNING id)
		UPDATE issue_comments SET description_id = d.id FROM d WHERE issue_comments.id = $1`, comment, actor)
	return err
}

// createComment ports IssueCommentViewSet.create.
func (a *API) createComment(c *httpx.Ctx) error {
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
	var (
		guestViewAll bool
		workspaceID  uuid.UUID
		createdBy    *uuid.UUID
	)
	if err := a.db.QueryRow(ctx, `SELECT guest_view_all_features, workspace_id FROM projects
		WHERE id = $1 AND deleted_at IS NULL`, projectID).Scan(&guestViewAll, &workspaceID); err != nil {
		return err
	}
	// Issue.objects.get(pk=issue_id): any live issue, in any project.
	if err := a.db.QueryRow(ctx, `SELECT created_by_id FROM issues WHERE id = $1 AND deleted_at IS NULL`, issueID).
		Scan(&createdBy); err != nil {
		return err
	}
	if blocked, err := a.guestBlocked(ctx, slug, projectID, c.User.ID, createdBy, guestViewAll); err != nil {
		return err
	} else if blocked {
		return httpx.Err(http.StatusBadRequest, "You are not allowed to comment on the issue")
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	in, v, err := a.validateComment(ctx, data, c.Loc(), nil)
	if err != nil {
		return err
	}
	if in == nil {
		return v.Err()
	}
	// serializer.save(project_id=..., issue_id=..., actor=request.user); a
	// new comment always gets a new Description.
	set := in.set
	set.drop("actor_id")
	set.drop("description_id")
	html := "<p></p>"
	if in.html != nil {
		html = *in.html
	}
	if in.json == nil {
		set.addCast("comment_json", "{}", "::jsonb")
	}
	if !slices.Contains(set.cols, "attachments") {
		set.add("attachments", []string{})
	}
	if !slices.Contains(set.cols, "access") {
		set.add("access", "INTERNAL")
	}
	set.add("comment_html", html)
	set.add("comment_stripped", commentStripped(html))
	set.add("issue_id", issueID)
	set.add("project_id", projectID)
	set.add("workspace_id", workspaceID)
	set.add("actor_id", c.User.ID)
	set.add("created_by_id", c.User.ID)
	var id uuid.UUID
	if err := pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, insertStamped("issue_comments", &set), set.args...).Scan(&id); err != nil {
			return err
		}
		return createDescription(ctx, tx, id, c.User.ID)
	}); err != nil {
		return err
	}
	m, err := a.loadComment(ctx, id)
	if err != nil {
		return err
	}
	// The instance still carries the updated_by the description_id save()
	// set in memory; only description_id was written.
	m.UpdatedBy = &c.User.ID
	requested, err := httpx.Marshal(m, c.Loc())
	if err != nil {
		return err
	}
	rd := string(requested)
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "comment.activity.created", RequestedData: &rd, IssueID: issueID.String(), ActorID: c.User.ID.String(),
		ProjectID: projectID.String(), Epoch: time.Now().Unix(), Subscriber: true, Notification: true,
	})
	return c.JSON(http.StatusCreated, m)
}

// insertStamped is an INSERT of the columns (parameters from $1) with
// created_at and updated_at from the database clock, returning id.
func insertStamped(table string, s *setList) string {
	vals := make([]string, len(s.cols))
	for i := range s.cols {
		vals[i] = fmt.Sprintf("$%d%s", i+1, s.casts[i])
	}
	return fmt.Sprintf("INSERT INTO %s (%s, created_at, updated_at) VALUES (%s, clock_timestamp(), clock_timestamp()) RETURNING id",
		table, strings.Join(s.cols, ", "), strings.Join(vals, ", "))
}

// jsonEqual is Python == between two JSON documents.
func jsonEqual(a, b jsontext.Value) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return pyEqualAny(av, bv)
}

// updateComment ports IssueCommentViewSet.partial_update.
func (a *API) updateComment(c *httpx.Ctx) error {
	cur, err := a.commentObject(c)
	if err != nil {
		return err
	}
	ctx := c.Context()
	current, err := httpx.Marshal(cur, c.Loc())
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	requested := string(data.Object())
	in, v, err := a.validateComment(ctx, data, c.Loc(), &cur.ID)
	if err != nil {
		return err
	}
	if in == nil {
		return v.Err()
	}
	// "comment_html" in request.data and request.data["comment_html"] !=
	// issue_comment.comment_html: the raw value, before trimming and nh3.
	raw, given := data.Get("comment_html")
	edited := given && !(raw.IsString() && raw.Str() == cur.CommentHTML)
	set := in.set
	html := cur.CommentHTML
	if in.html != nil {
		html = *in.html
		set.add("comment_html", html)
	}
	stripped := commentStripped(html)
	set.add("comment_stripped", stripped)
	set.add("updated_by_id", c.User.ID)
	stamp := "updated_at = clock_timestamp()"
	if edited {
		set.drop("edited_at")
		stamp += ", edited_at = now()"
	}
	err = pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		var (
			updatedAt time.Time
			descID    *uuid.UUID
		)
		if err := tx.QueryRow(ctx, `UPDATE issue_comments SET `+set.sql()+`, `+stamp+` WHERE id = $1
			RETURNING updated_at, description_id`, append([]any{cur.ID}, set.args...)...).Scan(&updatedAt, &descID); err != nil {
			return err
		}
		if descID == nil {
			return createDescription(ctx, tx, cur.ID, c.User.ID)
		}
		// The tracked fields (ChangeTrackerMixin) that changed, copied to
		// the description with a queryset update().
		var changed setList
		if html != cur.CommentHTML {
			changed.add("description_html", html)
		}
		if stripped != cur.CommentStripped {
			changed.add("description_stripped", stripped)
		}
		if in.json != nil && !jsonEqual(in.json, cur.CommentJSON) {
			changed.addCast("description_json", string(in.json), "::jsonb")
		}
		if len(changed.cols) == 0 {
			return nil
		}
		changed.add("updated_by_id", c.User.ID)
		changed.add("updated_at", updatedAt)
		_, err := tx.Exec(ctx, `UPDATE descriptions SET `+changed.sql()+` WHERE id = $1`, append([]any{*descID}, changed.args...)...)
		return err
	})
	if err != nil {
		return err
	}
	cs := string(current)
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "comment.activity.updated", RequestedData: &requested, CurrentInstance: &cs, IssueID: cur.Issue.String(),
		ActorID: c.User.ID.String(), ProjectID: cur.Project.String(), Epoch: time.Now().Unix(), Subscriber: true,
		Notification: true,
	})
	m, err := a.loadComment(ctx, cur.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m)
}

// deleteComment ports IssueCommentViewSet.destroy.
func (a *API) deleteComment(c *httpx.Ctx) error {
	cur, err := a.commentObject(c)
	if err != nil {
		return err
	}
	ctx := c.Context()
	current, err := httpx.Marshal(cur, c.Loc())
	if err != nil {
		return err
	}
	// issue_comment.delete(): save() (which gives a comment without a
	// description one), then the cascade.
	if err := pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		if err := softdelete.Row(ctx, tx, "issue_comments", cur.ID, c.User.ID); err != nil {
			return err
		}
		if cur.Description == nil {
			return createDescription(ctx, tx, cur.ID, c.User.ID)
		}
		return nil
	}); err != nil {
		return err
	}
	requested := `{"comment_id": "` + cur.ID.String() + `"}`
	cs := string(current)
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "comment.activity.deleted", RequestedData: &requested, CurrentInstance: &cs, IssueID: cur.Issue.String(),
		ActorID: c.User.ID.String(), ProjectID: cur.Project.String(), Epoch: time.Now().Unix(), Subscriber: true,
		Notification: true,
	})
	return c.NoContent()
}

// errIntegrity reports whether err is one Django raises as IntegrityError.
func errIntegrity(err error) bool {
	var he *httpx.Error
	return err != nil && !errors.As(err, &he) && db.IsIntegrityError(err)
}
