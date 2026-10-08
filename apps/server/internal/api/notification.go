package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

var errNoNotification = httpx.Detail(http.StatusNotFound, "No Notification matches the given query.")

// notification is NotificationSerializer ("__all__" plus the nested
// triggered_by_details). The three annotations exist only on the list
// queryset; elsewhere the read-only fields are skipped.
type notification struct {
	ID                      uuid.UUID      `json:"id"`
	TriggeredByDetails      *userLite      `json:"triggered_by_details"`
	IsInboxIssue            *bool          `json:"is_inbox_issue,omitzero"`
	IsIntakeIssue           *bool          `json:"is_intake_issue,omitzero"`
	IsMentionedNotification *bool          `json:"is_mentioned_notification,omitzero"`
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
	DeletedAt               *time.Time     `json:"deleted_at"`
	CreatedBy               *uuid.UUID     `json:"created_by"`
	UpdatedBy               *uuid.UUID     `json:"updated_by"`
	Workspace               uuid.UUID      `json:"workspace"`
	Project                 *uuid.UUID     `json:"project"`
	Data                    jsontext.Value `json:"data"`
	EntityIdentifier        *uuid.UUID     `json:"entity_identifier"`
	EntityName              string         `json:"entity_name"`
	Title                   string         `json:"title"`
	Message                 jsontext.Value `json:"message"`
	MessageHTML             string         `json:"message_html"`
	MessageStripped         *string        `json:"message_stripped"`
	Sender                  string         `json:"sender"`
	TriggeredBy             *uuid.UUID     `json:"triggered_by"`
	Receiver                uuid.UUID      `json:"receiver"`
	ReadAt                  *time.Time     `json:"read_at"`
	SnoozedTill             *time.Time     `json:"snoozed_till"`
	ArchivedAt              *time.Time     `json:"archived_at"`
}

// mentionedLike is icontains "mentioned" on the sender.
const mentionedLike = `n.sender ILIKE '%mentioned%'`

const notificationCols = `n.id, n.created_at, n.updated_at, n.deleted_at, n.created_by_id, n.updated_by_id,
	n.workspace_id, n.project_id, n.data::text, n.entity_identifier, n.entity_name, n.title, n.message::text,
	n.message_html, n.message_stripped, n.sender, n.triggered_by_id, n.receiver_id, n.read_at, n.snoozed_till,
	n.archived_at, t.id, t.first_name, t.last_name, t.avatar, t.is_bot, t.display_name, fa.id, fa.entity_type`

// intakeExists is the Exists(intake_issue) annotation behind both
// is_inbox_issue and is_intake_issue; the intake_issues join keeps rows
// that were soft-deleted, as Django's does.
const intakeExists = `EXISTS (SELECT 1 FROM issues i JOIN intake_issues ii ON i.id = ii.issue_id
		JOIN workspaces iw ON iw.id = i.workspace_id
		WHERE i.deleted_at IS NULL AND ii.status IN (0, 2, -2) AND i.id = n.entity_identifier AND iw.slug = $2)`

const notificationFrom = `
	FROM notifications n
	JOIN workspaces w ON w.id = n.workspace_id
	LEFT JOIN users t ON t.id = n.triggered_by_id
	LEFT JOIN file_assets fa ON fa.id = t.avatar_asset_id`

// scanNotification reads notificationCols, then the annotations when
// annotated is set.
func scanNotification(row pgx.Row, annotated bool) (*notification, error) {
	var (
		n                      notification
		data, message          *string
		tid                    *uuid.UUID
		first, last, avatar    *string
		isBot                  *bool
		display                *string
		avatarID               *uuid.UUID
		avatarType             *string
		inbox, intake, mention bool
	)
	dest := []any{&n.ID, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &n.CreatedBy, &n.UpdatedBy, &n.Workspace,
		&n.Project, &data, &n.EntityIdentifier, &n.EntityName, &n.Title, &message, &n.MessageHTML,
		&n.MessageStripped, &n.Sender, &n.TriggeredBy, &n.Receiver, &n.ReadAt, &n.SnoozedTill, &n.ArchivedAt,
		&tid, &first, &last, &avatar, &isBot, &display, &avatarID, &avatarType}
	if annotated {
		dest = append(dest, &inbox, &intake, &mention)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	n.Data, n.Message = jsontext.Value("null"), jsontext.Value("null")
	if data != nil {
		n.Data = jsontext.Value(*data)
	}
	if message != nil {
		n.Message = jsontext.Value(*message)
	}
	if tid != nil {
		u := &userLite{ID: *tid, FirstName: *first, LastName: *last, Avatar: *avatar, IsBot: *isBot, DisplayName: *display}
		u.AvatarURL = imageURL(avatarID, avatarType, &u.Avatar)
		n.TriggeredByDetails = u
	}
	if annotated {
		n.IsInboxIssue, n.IsIntakeIssue, n.IsMentionedNotification = &inbox, &intake, &mention
	}
	return &n, nil
}

// notificationFilters collects the WHERE clauses of a notification queryset
// for the receiver ($1) in the workspace with the slug ($2).
type notificationFilters struct {
	where []string
	args  []any
	none  bool // queryset.none()
}

func newNotificationFilters(c *httpx.Ctx) *notificationFilters {
	return &notificationFilters{
		where: []string{"n.deleted_at IS NULL", "n.receiver_id = $1", "w.slug = $2"},
		args:  []any{c.User.ID, c.Param("slug")},
	}
}

func (f *notificationFilters) add(clause string) { f.where = append(f.where, clause) }

// snoozed filters on the snoozed state; now is $3.
func (f *notificationFilters) snoozed(on bool) {
	f.args = append(f.args, time.Now())
	if on {
		f.add("(n.snoozed_till < $3 OR n.snoozed_till IS NOT NULL)")
	} else {
		f.add("(n.snoozed_till >= $3 OR n.snoozed_till IS NULL)")
	}
}

func (f *notificationFilters) archived(on bool) {
	if on {
		f.add("n.archived_at IS NOT NULL")
	} else {
		f.add("n.archived_at IS NULL")
	}
}

func (f *notificationFilters) sql() string { return " WHERE " + joinAnd(f.where) }

func joinAnd(parts []string) string { return strings.Join(parts, " AND ") }

// assignedIssues, createdIssues and subscribedIssues are the issue id
// subqueries of the type filters.
const (
	assignedIssues = `SELECT a.issue_id FROM issue_assignees a JOIN workspaces aw ON aw.id = a.workspace_id
		WHERE a.deleted_at IS NULL AND a.assignee_id = $1 AND aw.slug = $2`
	createdIssues = `SELECT i.id FROM issues i JOIN workspaces iw ON iw.id = i.workspace_id
		WHERE i.deleted_at IS NULL AND i.created_by_id = $1 AND iw.slug = $2`
	// The "assigned" annotation compares the assignee row's own id with the
	// issue id (IssueAssignee.objects.filter(pk=OuterRef("issue_id"))), so
	// it never matches and only "created" excludes.
	subscribedIssues = `SELECT s.issue_id FROM issue_subscribers s JOIN workspaces sw ON sw.id = s.workspace_id
		WHERE s.deleted_at IS NULL AND s.subscriber_id = $1 AND sw.slug = $2
		AND NOT EXISTS (SELECT 1 FROM issue_assignees a WHERE a.deleted_at IS NULL AND a.assignee_id = $1 AND a.id = s.issue_id)
		AND NOT EXISTS (SELECT 1 FROM issues i WHERE i.deleted_at IS NULL AND i.created_by_id = $1 AND i.id = s.issue_id)`
)

// isLowRole is the "created" branch's WorkspaceMember(role__lt=15) check.
func (a *API) isLowRole(ctx context.Context, slug string, user uuid.UUID) (bool, error) {
	var low bool
	err := a.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
		WHERE w.slug = $1 AND wm.member_id = $2 AND wm.role < 15 AND wm.is_active AND wm.deleted_at IS NULL)`,
		slug, user).Scan(&low)
	return low, err
}

// listNotifications ports NotificationViewSet.list. Results are paginated
// only when both ?per_page= and ?cursor= are given.
func (a *API) listNotifications(c *httpx.Ctx) error {
	ctx := c.Context()
	slug := c.Param("slug")
	snoozed, archived := "false", "false"
	if c.HasQuery("snoozed") {
		snoozed = c.Query("snoozed")
	}
	if c.HasQuery("archived") {
		archived = c.Query("archived")
	}
	read := c.Query("read")
	types := "all"
	if c.HasQuery("type") {
		types = c.Query("type")
	}

	f := newNotificationFilters(c)
	f.add("n.entity_name = 'issue'")
	// snoozed_filters[snoozed] and archived_filters[archived] raise KeyError
	// for anything but "true" and "false".
	if snoozed != "true" && snoozed != "false" {
		return errKeyMissing
	}
	f.snoozed(snoozed == "true")
	if archived != "true" && archived != "false" {
		return errKeyMissing
	}
	f.archived(archived == "true")
	switch read {
	case "false":
		f.add("n.read_at IS NULL")
	case "true":
		f.add("n.read_at IS NOT NULL")
	}
	if c.Query("mentioned") != "" {
		f.add(mentionedLike)
	} else {
		f.add("NOT (" + mentionedLike + ")")
	}

	var or []string
	for _, t := range strings.Split(types, ",") {
		switch t {
		case "subscribed":
			or = append(or, "n.entity_identifier IN ("+subscribedIssues+")")
		case "assigned":
			or = append(or, "n.entity_identifier IN ("+assignedIssues+")")
		case "created":
			low, err := a.isLowRole(ctx, slug, c.User.ID)
			if err != nil {
				return err
			}
			if low {
				f.none = true
			} else {
				or = append(or, "n.entity_identifier IN ("+createdIssues+")")
			}
		}
	}
	if len(or) > 0 {
		f.add("(" + strings.Join(or, " OR ") + ")")
	}

	paginated := c.Query("per_page") != "" && c.Query("cursor") != ""
	field, desc := "", false
	var page *offsetPage
	if paginated {
		field, desc = sanitizeOrderBy(c.Query("order_by"), []string{"created_at", "updated_at"}, "-created_at")
		var err error
		if page, err = parseOffsetPage(c); err != nil {
			return err
		}
	}
	if f.none {
		if !paginated {
			return c.JSON(http.StatusOK, []*notification{})
		}
		return c.JSON(http.StatusOK, page.response([]*notification{}, 0, 0))
	}

	cols := notificationCols + ", " + intakeExists + ", " + intakeExists + ", n.sender ILIKE '%mentioned%'"
	query := "SELECT " + cols + notificationFrom + f.sql()
	if !paginated {
		out, err := a.queryNotifications(ctx, query+" ORDER BY n.snoozed_till ASC, n.created_at DESC", true, f.args...)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, out)
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	args := append(slices.Clone(f.args), page.limit+1, page.offset)
	out, err := a.queryNotifications(ctx, fmt.Sprintf("%s ORDER BY n.%s %s NULLS LAST, n.created_at DESC LIMIT $%d OFFSET $%d",
		query, field, dir, len(args)-1, len(args)), true, args...)
	if err != nil {
		return err
	}
	var total int64
	if err := a.db.QueryRow(ctx, "SELECT count(*)"+notificationFromCount+f.sql(), f.args...).Scan(&total); err != nil {
		return err
	}
	n := len(out)
	return c.JSON(http.StatusOK, page.response(out[:min(int64(n), page.limit)], n, total))
}

const notificationFromCount = ` FROM notifications n JOIN workspaces w ON w.id = n.workspace_id`

func (a *API) queryNotifications(ctx context.Context, sql string, annotated bool, args ...any) ([]*notification, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*notification{}
	for rows.Next() {
		n, err := scanNotification(rows, annotated)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// loadNotification is Notification.objects.get for the receiver in the
// workspace; pgx.ErrNoRows when it isn't theirs.
func (a *API) loadNotification(c *httpx.Ctx, id uuid.UUID) (*notification, error) {
	return scanNotification(a.db.QueryRow(c.Context(), "SELECT "+notificationCols+notificationFrom+`
		WHERE n.deleted_at IS NULL AND n.id = $3 AND n.receiver_id = $1 AND w.slug = $2`,
		c.User.ID, c.Param("slug"), id), false)
}

// retrieveNotification ports NotificationViewSet.retrieve (DRF default).
func (a *API) retrieveNotification(c *httpx.Ctx) error {
	id, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	n, err := a.loadNotification(c, id)
	if err == pgx.ErrNoRows {
		return errNoNotification
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, n)
}

// notificationUpdate ports mark_read, mark_unread, archive and unarchive:
// notification.<field> = ...; notification.save().
func (a *API) notificationUpdate(set string) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		id, err := c.UUIDParam("pk")
		if err != nil {
			return err
		}
		if _, err := a.loadNotification(c, id); err != nil {
			return err
		}
		if _, err := a.db.Exec(c.Context(), `UPDATE notifications SET `+set+`, updated_at = now(), updated_by_id = $2
			WHERE id = $1`, id, c.User.ID); err != nil {
			return err
		}
		n, err := a.loadNotification(c, id)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, n)
	}
}

// patchNotification ports NotificationViewSet.partial_update: only
// snoozed_till is taken from the body (null when missing).
func (a *API) patchNotification(c *httpx.Ctx) error {
	id, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	if _, err := a.loadNotification(c, id); err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get() on a list or str
	}
	raw := jsontext.Value("null")
	if val, ok := data.Get("snoozed_till"); ok {
		raw = val.Raw()
	}
	v := drf.NewValidator(drf.DataFromJSON(jsontext.Value(`{"snoozed_till":`+string(raw)+`}`)), c.Loc())
	snoozed, _ := v.DateTime("snoozed_till", true)
	if err := v.Err(); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE notifications SET snoozed_till = $3, updated_at = now(), updated_by_id = $2
		WHERE id = $1`, id, c.User.ID, snoozed); err != nil {
		return err
	}
	n, err := a.loadNotification(c, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, n)
}

// unreadNotifications ports UnreadNotificationEndpoint.get.
func (a *API) unreadNotifications(c *httpx.Ctx) error {
	count := func(mentioned bool) (int64, error) {
		clause := "NOT (" + mentionedLike + ")"
		if mentioned {
			clause = mentionedLike
		}
		var n int64
		err := a.db.QueryRow(c.Context(), `SELECT count(*) FROM notifications n JOIN workspaces w ON w.id = n.workspace_id
			WHERE n.deleted_at IS NULL AND w.slug = $1 AND n.receiver_id = $2 AND n.read_at IS NULL
			AND n.archived_at IS NULL AND n.snoozed_till IS NULL AND `+clause, c.Param("slug"), c.User.ID).Scan(&n)
		return n, err
	}
	total, err := count(false)
	if err != nil {
		return err
	}
	mention, err := count(true)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]int64{
		"total_unread_notifications_count":   total,
		"mention_unread_notifications_count": mention,
	})
}

// markAllRead ports MarkAllReadNotificationViewSet.create. bulk_update only
// writes read_at: updated_at and updated_by stay.
func (a *API) markAllRead(c *httpx.Ctx) error {
	ctx := c.Context()
	slug := c.Param("slug")
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	truthy := func(name string) bool {
		v, ok := data.Get(name)
		return ok && drf.PyTruthy(v)
	}
	kind := ""
	if v, ok := data.Get("type"); ok && v.IsString() {
		kind = v.Str()
	}

	f := newNotificationFilters(c)
	f.add("n.read_at IS NULL")
	f.snoozed(truthy("snoozed"))
	f.archived(truthy("archived"))
	switch kind {
	case "watching":
		f.add(`n.entity_identifier IN (SELECT s.issue_id FROM issue_subscribers s JOIN workspaces sw ON sw.id = s.workspace_id
			WHERE s.deleted_at IS NULL AND s.subscriber_id = $1 AND sw.slug = $2)`)
	case "assigned":
		f.add("n.entity_identifier IN (" + assignedIssues + ")")
	case "created":
		low, err := a.isLowRole(ctx, slug, c.User.ID)
		if err != nil {
			return err
		}
		if low {
			f.none = true
		} else {
			f.add("n.entity_identifier IN (" + createdIssues + ")")
		}
	}
	if !f.none {
		if _, err := a.db.Exec(ctx, `UPDATE notifications n SET read_at = clock_timestamp()
			FROM workspaces w WHERE w.id = n.workspace_id AND `+joinAnd(f.where), f.args...); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Successful"})
}

// notificationPreference is UserNotificationPreferenceSerializer.
type notificationPreference struct {
	ID             uuid.UUID  `json:"id"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"deleted_at"`
	CreatedBy      *uuid.UUID `json:"created_by"`
	UpdatedBy      *uuid.UUID `json:"updated_by"`
	User           uuid.UUID  `json:"user"`
	Workspace      *uuid.UUID `json:"workspace"`
	Project        *uuid.UUID `json:"project"`
	PropertyChange bool       `json:"property_change"`
	StateChange    bool       `json:"state_change"`
	Comment        bool       `json:"comment"`
	Mention        bool       `json:"mention"`
	IssueCompleted bool       `json:"issue_completed"`
}

// userPreference is UserNotificationPreference.objects.get(user=user):
// no row is a 404, several rows a 500.
func (a *API) userPreference(ctx context.Context, user uuid.UUID) (*notificationPreference, error) {
	rows, err := a.db.Query(ctx, `
		SELECT id, created_at, updated_at, deleted_at, created_by_id, updated_by_id, user_id, workspace_id, project_id,
			property_change, state_change, comment, mention, issue_completed
		FROM user_notification_preferences WHERE deleted_at IS NULL AND user_id = $1 LIMIT 2`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []*notificationPreference
	for rows.Next() {
		var p notificationPreference
		if err := rows.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &p.CreatedBy, &p.UpdatedBy, &p.User,
			&p.Workspace, &p.Project, &p.PropertyChange, &p.StateChange, &p.Comment, &p.Mention, &p.IssueCompleted); err != nil {
			return nil, err
		}
		found = append(found, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, pgx.ErrNoRows
	case 1:
		return found[0], nil
	}
	return nil, errViewCrash // MultipleObjectsReturned
}

// getNotificationPreferences ports UserNotificationPreferenceEndpoint.get.
func (a *API) getNotificationPreferences(c *httpx.Ctx) error {
	p, err := a.userPreference(c.Context(), c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

// patchNotificationPreferences ports UserNotificationPreferenceEndpoint.patch.
func (a *API) patchNotificationPreferences(c *httpx.Ctx) error {
	ctx := c.Context()
	p, err := a.userPreference(ctx, c.User.ID)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	var set setList
	// User.objects is a plain manager; workspaces and projects hide deleted rows.
	exists := func(table, live string) func(uuid.UUID) (bool, error) {
		return func(id uuid.UUID) (bool, error) {
			var ok bool
			err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1`+live+`)`, id).Scan(&ok)
			return ok, err
		}
	}
	if id, ok, err := v.PK("user", false, exists("users", "")); err != nil {
		return err
	} else if ok {
		set.add("user_id", id)
	}
	if id, ok, err := v.PK("workspace", true, exists("workspaces", " AND deleted_at IS NULL")); err != nil {
		return err
	} else if ok {
		set.add("workspace_id", id)
	}
	if id, ok, err := v.PK("project", true, exists("projects", " AND deleted_at IS NULL")); err != nil {
		return err
	} else if ok {
		set.add("project_id", id)
	}
	for _, col := range []string{"property_change", "state_change", "comment", "mention", "issue_completed"} {
		if b, ok := v.Bool(col); ok {
			set.add(col, b)
		}
	}
	if err := a.auditFields(ctx, v, &set); err != nil {
		return err
	}
	if t, ok := v.DateTime("deleted_at", true); ok {
		set.add("deleted_at", t)
	}
	if err := v.Err(); err != nil {
		return err
	}
	set.add("updated_at", time.Now())
	set.add("updated_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, `UPDATE user_notification_preferences SET `+set.sql()+` WHERE id = $1`,
		append([]any{p.ID}, set.args...)...); err != nil {
		return err
	}
	return a.respondPreference(c, p.ID)
}

// respondPreference renders the preference row with this id.
func (a *API) respondPreference(c *httpx.Ctx, id uuid.UUID) error {
	var p notificationPreference
	if err := a.db.QueryRow(c.Context(), `
		SELECT id, created_at, updated_at, deleted_at, created_by_id, updated_by_id, user_id, workspace_id, project_id,
			property_change, state_change, comment, mention, issue_completed
		FROM user_notification_preferences WHERE id = $1`, id).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt,
		&p.CreatedBy, &p.UpdatedBy, &p.User, &p.Workspace, &p.Project, &p.PropertyChange, &p.StateChange,
		&p.Comment, &p.Mention, &p.IssueCompleted); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, &p)
}
