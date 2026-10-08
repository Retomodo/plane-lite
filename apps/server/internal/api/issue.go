package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// issueObjects is Issue.issue_objects (IssueManager) for the issues row
// aliased a: live, not a draft, not archived, not in a triage state and not
// in an archived project.
func issueObjects(a string) string {
	return a + `.deleted_at IS NULL AND ` + a + `.archived_at IS NULL AND NOT ` + a + `.is_draft
		AND NOT EXISTS (SELECT 1 FROM states xs WHERE xs.id = ` + a + `.state_id AND xs."group" = 'triage')
		AND NOT EXISTS (SELECT 1 FROM projects xp WHERE xp.id = ` + a + `.project_id AND xp.archived_at IS NOT NULL)`
}

// Annotation subqueries shared by the issue read paths (issue alias i).
const (
	// cycle_id: CycleIssue.objects.filter(issue=OuterRef("id")).values("cycle_id")[:1].
	issueCycleSQL = `(SELECT ci.cycle_id FROM cycle_issues ci WHERE ci.issue_id = i.id AND ci.deleted_at IS NULL
		ORDER BY ci.created_at DESC LIMIT 1)`
	issueLabelIDsSQL = `COALESCE((SELECT array_agg(DISTINCT il.label_id) FROM issue_labels il
		WHERE il.issue_id = i.id AND il.deleted_at IS NULL), '{}')`
	// assignee_ids filtered on assignee__member_project__is_active: any
	// active membership row of the user's, in any project.
	issueActiveAssigneeIDsSQL = `COALESCE((SELECT array_agg(DISTINCT ia.assignee_id) FROM issue_assignees ia
		WHERE ia.issue_id = i.id AND ia.deleted_at IS NULL
			AND EXISTS (SELECT 1 FROM project_members apm WHERE apm.member_id = ia.assignee_id AND apm.is_active)), '{}')`
	issueAssigneeIDsSQL = `COALESCE((SELECT array_agg(DISTINCT ia.assignee_id) FROM issue_assignees ia
		WHERE ia.issue_id = i.id AND ia.deleted_at IS NULL), '{}')`
	issueModuleIDsSQL = `COALESCE((SELECT array_agg(DISTINCT mi.module_id) FROM module_issues mi
		JOIN modules m ON m.id = mi.module_id
		WHERE mi.issue_id = i.id AND mi.deleted_at IS NULL AND m.archived_at IS NULL), '{}')`
	issueLinkCountSQL       = `(SELECT count(*) FROM issue_links l WHERE l.issue_id = i.id AND l.deleted_at IS NULL)`
	issueAttachmentCountSQL = `(SELECT count(*) FROM file_assets f WHERE f.issue_id = i.id
		AND f.entity_type = 'ISSUE_ATTACHMENT' AND f.deleted_at IS NULL)`
)

var issueSubIssuesCountSQL = `(SELECT count(*) FROM issues s WHERE s.parent_id = i.id AND ` + issueObjects("s") + `)`

// nullIfZero is a Subquery(... .values("x").annotate(count=Count("id"))),
// which yields no row, hence NULL, rather than 0.
func nullIfZero(sql string) string { return "NULLIF(" + sql + ", 0)" }

// issueValues is the dict IssueViewSet.create answers with: get_queryset()
// with apply_annotations and issue_queryset_grouper, through .values().
// created_at and updated_at go through user_timezone_converter; the other
// timestamps stay in UTC.
type issueValues struct {
	ID              uuid.UUID   `json:"id"`
	Name            string      `json:"name"`
	StateID         *uuid.UUID  `json:"state_id"`
	SortOrder       float64     `json:"sort_order"`
	CompletedAt     *utcTime    `json:"completed_at"`
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
	SubIssuesCount  *int        `json:"sub_issues_count"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	CreatedBy       *uuid.UUID  `json:"created_by"`
	UpdatedBy       *uuid.UUID  `json:"updated_by"`
	AttachmentCount *int        `json:"attachment_count"`
	LinkCount       *int        `json:"link_count"`
	IsDraft         bool        `json:"is_draft"`
	ArchivedAt      *httpx.Date `json:"archived_at"`
	DeletedAt       *utcTime    `json:"deleted_at"`
}

var issueValuesSQL = `SELECT i.id, i.name, i.state_id, i.sort_order, i.completed_at, i.estimate_point_id, i.priority,
		i.start_date, i.target_date, i.sequence_id, i.project_id, i.parent_id, ` + issueCycleSQL + `,
		` + issueModuleIDsSQL + `, ` + issueLabelIDsSQL + `, ` + issueAssigneeIDsSQL + `,
		` + nullIfZero(issueSubIssuesCountSQL) + `, i.created_at, i.updated_at, i.created_by_id, i.updated_by_id,
		` + nullIfZero(issueAttachmentCountSQL) + `, ` + nullIfZero(issueLinkCountSQL) + `,
		i.is_draft, i.archived_at, i.deleted_at
	FROM issues i JOIN workspaces w ON w.id = i.workspace_id`

func scanIssueValues(row pgx.Row) (*issueValues, error) {
	var (
		v                        issueValues
		completed, deleted       *time.Time
		start, target, archived  *time.Time
		subs, attachments, links *int64
	)
	err := row.Scan(&v.ID, &v.Name, &v.StateID, &v.SortOrder, &completed, &v.EstimatePoint, &v.Priority,
		&start, &target, &v.SequenceID, &v.ProjectID, &v.ParentID, &v.CycleID, &v.ModuleIDs, &v.LabelIDs,
		&v.AssigneeIDs, &subs, &v.CreatedAt, &v.UpdatedAt, &v.CreatedBy, &v.UpdatedBy, &attachments, &links,
		&v.IsDraft, &archived, &deleted)
	if err != nil {
		return nil, err
	}
	v.CompletedAt, v.DeletedAt = (*utcTime)(completed), (*utcTime)(deleted)
	v.StartDate, v.TargetDate, v.ArchivedAt = dateOf(start), dateOf(target), dateOf(archived)
	v.SubIssuesCount, v.AttachmentCount, v.LinkCount = intOf(subs), intOf(attachments), intOf(links)
	return &v, nil
}

func dateOf(t *time.Time) *httpx.Date {
	if t == nil {
		return nil
	}
	return &httpx.Date{Time: *t}
}

func intOf(n *int64) *int {
	if n == nil {
		return nil
	}
	i := int(*n)
	return &i
}

// issueDetail is IssueDetailSerializer (expanded as asked).
type issueDetail struct {
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
	CycleID         *uuid.UUID  `json:"cycle_id"`
	ModuleIDs       []uuid.UUID `json:"module_ids"`
	LabelIDs        []uuid.UUID `json:"label_ids"`
	AssigneeIDs     []uuid.UUID `json:"assignee_ids"`
	SubIssuesCount  *int        `json:"sub_issues_count"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	CreatedBy       *uuid.UUID  `json:"created_by"`
	UpdatedBy       *uuid.UUID  `json:"updated_by"`
	AttachmentCount *int        `json:"attachment_count"`
	LinkCount       *int        `json:"link_count"`
	IsDraft         bool        `json:"is_draft"`
	ArchivedAt      *httpx.Date `json:"archived_at"`
	DescriptionHTML string      `json:"description_html"`
	IsSubscribed    *bool       `json:"is_subscribed,omitzero"`
	IsIntake        *bool       `json:"is_intake,omitzero"`

	IssueReactions   *[]issueReactionLite   `json:"issue_reactions,omitzero"`
	IssueLink        *[]issueLinkLite       `json:"issue_link,omitzero"`
	Parent           jsontext.Value         `json:"parent,omitzero"`
	IssueAttachments *[]issueAttachmentLite `json:"issue_attachments,omitzero"`

	workspaceID uuid.UUID
}

// issueDetailCols lists the columns scanIssueDetail reads after the
// per-endpoint subqueries (cycle, labels, assignees, modules, the three
// counts and is_subscribed), which come first.
const issueDetailCols = `i.id, i.name, i.state_id, i.sort_order, i.completed_at, i.estimate_point_id, i.priority,
	i.start_date, i.target_date, i.sequence_id, i.project_id, i.parent_id, i.created_at, i.updated_at,
	i.created_by_id, i.updated_by_id, i.is_draft, i.archived_at, i.description_html, i.workspace_id`

func scanIssueDetail(row pgx.Row) (*issueDetail, error) {
	var (
		d                        issueDetail
		start, target, archived  *time.Time
		subs, attachments, links *int64
		subscribed               bool
	)
	err := row.Scan(&d.CycleID, &d.LabelIDs, &d.AssigneeIDs, &d.ModuleIDs, &subs, &attachments, &links, &subscribed,
		&d.ID, &d.Name, &d.StateID, &d.SortOrder, &d.CompletedAt, &d.EstimatePoint, &d.Priority, &start, &target,
		&d.SequenceID, &d.ProjectID, &d.ParentID, &d.CreatedAt, &d.UpdatedAt, &d.CreatedBy, &d.UpdatedBy, &d.IsDraft,
		&archived, &d.DescriptionHTML, &d.workspaceID)
	if err != nil {
		return nil, err
	}
	d.StartDate, d.TargetDate, d.ArchivedAt = dateOf(start), dateOf(target), dateOf(archived)
	d.SubIssuesCount, d.AttachmentCount, d.LinkCount = intOf(subs), intOf(attachments), intOf(links)
	d.IsSubscribed = &subscribed
	return &d, nil
}

type issueReactionLite struct {
	ID          uuid.UUID `json:"id"`
	Actor       uuid.UUID `json:"actor"`
	Issue       uuid.UUID `json:"issue"`
	Reaction    string    `json:"reaction"`
	DisplayName string    `json:"display_name"`
}

type issueLinkLite struct {
	ID          uuid.UUID      `json:"id"`
	IssueID     uuid.UUID      `json:"issue_id"`
	Title       *string        `json:"title"`
	URL         string         `json:"url"`
	Metadata    jsontext.Value `json:"metadata"`
	CreatedByID *uuid.UUID     `json:"created_by_id"`
	CreatedAt   time.Time      `json:"created_at"`
}

// issueAttachmentLite is IssueAttachmentLiteSerializer. asset is the
// FileField's storage URL, which S3Storage.url returns as the stored object
// key (asset_attachments golden).
type issueAttachmentLite struct {
	ID         uuid.UUID      `json:"id"`
	Asset      string         `json:"asset"`
	Attributes jsontext.Value `json:"attributes"`
	CreatedBy  *uuid.UUID     `json:"created_by"`
	UpdatedAt  time.Time      `json:"updated_at"`
	UpdatedBy  *uuid.UUID     `json:"updated_by"`
	AssetURL   string         `json:"asset_url"`
}

type issueLite struct {
	ID         uuid.UUID `json:"id"`
	SequenceID int       `json:"sequence_id"`
	ProjectID  uuid.UUID `json:"project_id"`
}

// expandList is BaseViewSet.expand: ?expand= split on commas.
func expandList(c *httpx.Ctx) []string {
	var out []string
	for _, e := range strings.Split(c.Query("expand"), ",") {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// expandIssue applies the DynamicBaseSerializer expansions the web asks
// for. Other names are ignored (see DEVIATIONS.md).
func (a *API) expandIssue(ctx context.Context, d *issueDetail, expand []string) error {
	for _, e := range expand {
		switch e {
		case "issue_reactions":
			rows, err := a.db.Query(ctx, `SELECT r.id, r.actor_id, r.issue_id, r.reaction, u.display_name
				FROM issue_reactions r JOIN users u ON u.id = r.actor_id
				WHERE r.issue_id = $1 AND r.deleted_at IS NULL ORDER BY r.created_at DESC`, d.ID)
			if err != nil {
				return err
			}
			list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (issueReactionLite, error) {
				var r issueReactionLite
				err := row.Scan(&r.ID, &r.Actor, &r.Issue, &r.Reaction, &r.DisplayName)
				return r, err
			})
			if err != nil {
				return err
			}
			d.IssueReactions = &list
		case "issue_link":
			rows, err := a.db.Query(ctx, `SELECT id, issue_id, title, url, metadata, created_by_id, created_at
				FROM issue_links WHERE issue_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC`, d.ID)
			if err != nil {
				return err
			}
			list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (issueLinkLite, error) {
				var l issueLinkLite
				err := row.Scan(&l.ID, &l.IssueID, &l.Title, &l.URL, &l.Metadata, &l.CreatedByID, &l.CreatedAt)
				return l, err
			})
			if err != nil {
				return err
			}
			d.IssueLink = &list
		case "parent":
			d.Parent = jsontext.Value("null")
			if d.ParentID != nil {
				// instance.parent goes through the base manager: a deleted
				// parent still expands.
				var p issueLite
				err := a.db.QueryRow(ctx, `SELECT id, sequence_id, project_id FROM issues WHERE id = $1`, *d.ParentID).
					Scan(&p.ID, &p.SequenceID, &p.ProjectID)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if err == nil {
					raw, err := httpx.Marshal(p, nil)
					if err != nil {
						return err
					}
					d.Parent = raw
				}
			}
		case "issue_attachments":
			if err := a.expandIssueAttachments(ctx, d); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *API) expandIssueAttachments(ctx context.Context, d *issueDetail) error {
	rows, err := a.db.Query(ctx, `SELECT f.id, f.asset, f.attributes, f.created_by_id, f.updated_at, f.updated_by_id,
			w.slug, f.project_id
		FROM file_assets f JOIN workspaces w ON w.id = f.workspace_id
		WHERE f.issue_id = $1 AND f.entity_type = 'ISSUE_ATTACHMENT' AND f.deleted_at IS NULL
		ORDER BY f.created_at DESC`, d.ID)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (issueAttachmentLite, error) {
		var (
			f       issueAttachmentLite
			slug    string
			project *uuid.UUID
		)
		err := row.Scan(&f.ID, &f.Asset, &f.Attributes, &f.CreatedBy, &f.UpdatedAt, &f.UpdatedBy, &slug, &project)
		proj := "None"
		if project != nil {
			proj = project.String()
		}
		f.AssetURL = "/api/assets/v2/workspaces/" + slug + "/projects/" + proj + "/issues/" + d.ID.String() +
			"/attachments/" + f.ID.String() + "/"
		return f, err
	})
	if err != nil {
		return err
	}
	d.IssueAttachments = &list
	return nil
}

// allowIssue is allow_permission(roles, creator=True, model=Issue): an
// active workspace member who created the issue (Issue.objects, so any live
// issue) passes whatever their project role.
func (a *API) allowIssue(roles []int, h httpx.HandlerFunc) httpx.HandlerFunc {
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
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issues
			WHERE id = $1 AND created_by_id = $2 AND deleted_at IS NULL)`, pk, c.User.ID).Scan(&creator); err != nil {
			return err
		}
		if creator {
			return h(c)
		}
		return a.allowProject(roles, h)(c)
	}
}

// guestBlocked is the retrieve views' check: a guest of a project without
// guest_view_all_features only sees issues they created.
func (a *API) guestBlocked(ctx context.Context, slug string, projectID, user uuid.UUID, createdBy *uuid.UUID, guestViewAll bool) (bool, error) {
	if guestViewAll || createdBy != nil && *createdBy == user {
		return false, nil
	}
	var guest bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE w.slug = $1 AND pm.project_id = $2 AND pm.member_id = $3 AND pm.role = 5 AND pm.is_active
			AND pm.deleted_at IS NULL)`, slug, projectID, user).Scan(&guest)
	return guest, err
}

var errIssueGuest = httpx.Err(http.StatusForbidden, "You are not allowed to view this issue")

// getIssue ports IssueViewSet.retrieve.
func (a *API) getIssue(c *httpx.Ctx) error {
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
	var guestViewAll bool
	if err := a.db.QueryRow(ctx, `SELECT p.guest_view_all_features FROM projects p JOIN workspaces w ON w.id = p.workspace_id
		WHERE p.id = $1 AND w.slug = $2 AND p.deleted_at IS NULL`, projectID, slug).Scan(&guestViewAll); err != nil {
		return err
	}
	d, err := scanIssueDetail(a.db.QueryRow(ctx, `SELECT `+issueCycleSQL+`, `+issueLabelIDsSQL+`, `+issueActiveAssigneeIDsSQL+`,
			`+issueModuleIDsSQL+`, `+nullIfZero(issueSubIssuesCountSQL)+`, `+nullIfZero(issueAttachmentCountSQL)+`,
			`+nullIfZero(issueLinkCountSQL)+`,
			EXISTS (SELECT 1 FROM issue_subscribers x WHERE x.issue_id = i.id AND x.project_id = $1
				AND x.subscriber_id = $4 AND x.deleted_at IS NULL AND x.workspace_id = w.id),
			`+issueDetailCols+`
		FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE i.project_id = $1 AND w.slug = $2 AND i.id = $3 AND i.deleted_at IS NULL`, projectID, slug, pk, c.User.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "The required object does not exist.")
	}
	if err != nil {
		return err
	}
	if blocked, err := a.guestBlocked(ctx, slug, projectID, c.User.ID, d.CreatedBy, guestViewAll); err != nil {
		return err
	} else if blocked {
		return errIssueGuest
	}
	a.recordVisit(ctx, slug, "issue", pk, c.User.ID, &projectID)
	if err := a.expandIssue(ctx, d, expandList(c)); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, d)
}

// getIssueByIdentifier ports IssueDetailIdentifierEndpoint.get
// (W/work-items/<project_identifier>-<issue_identifier>/).
func (a *API) getIssueByIdentifier(c *httpx.Ctx) error {
	ident := c.Param("ident")
	// Django's <str:a>-<str:b> is greedy: it splits at the last hyphen.
	cut := strings.LastIndexByte(ident, '-')
	if cut <= 0 || cut == len(ident)-1 {
		return httpx.ErrPageNotFound
	}
	projectIdent, issueIdent := ident[:cut], ident[cut+1:]
	seq, ok := strictInt(issueIdent)
	if !ok {
		return httpx.Err(http.StatusBadRequest, "Invalid issue identifier")
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var (
		projectID    uuid.UUID
		guestViewAll bool
	)
	err := a.db.QueryRow(ctx, `SELECT p.id, p.guest_view_all_features FROM projects p JOIN workspaces w ON w.id = p.workspace_id
		WHERE upper(p.identifier) = upper($1) AND w.slug = $2 AND p.deleted_at IS NULL`, projectIdent, slug).
		Scan(&projectID, &guestViewAll)
	if err != nil {
		return err
	}
	var member bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE w.slug = $1 AND pm.project_id = $2 AND pm.member_id = $3 AND pm.is_active AND pm.deleted_at IS NULL)`,
		slug, projectID, c.User.ID).Scan(&member); err != nil {
		return err
	}
	if !member {
		return errIssueGuest
	}
	if seq == nil {
		return httpx.Err(http.StatusNotFound, "The required object does not exist.")
	}
	d, err := scanIssueDetail(a.db.QueryRow(ctx, `SELECT `+issueCycleSQL+`, `+issueLabelIDsSQL+`, `+issueActiveAssigneeIDsSQL+`,
			`+issueModuleIDsSQL+`, `+issueSubIssuesCountSQL+`, `+issueAttachmentCountSQL+`, `+issueLinkCountSQL+`,
			EXISTS (SELECT 1 FROM issue_subscribers x JOIN issues xi ON xi.id = x.issue_id
				WHERE x.project_id = $1 AND xi.sequence_id = $3 AND x.subscriber_id = $4 AND x.deleted_at IS NULL
					AND x.workspace_id = w.id),
			`+issueDetailCols+`
		FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE i.project_id = $1 AND w.slug = $2 AND i.sequence_id = $3 AND i.deleted_at IS NULL
		ORDER BY i.created_at DESC LIMIT 1`, projectID, slug, *seq, c.User.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "The required object does not exist.")
	}
	if err != nil {
		return err
	}
	var intake bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM intake_issues ii WHERE ii.issue_id = $1
		AND ii.status IN (-2, 0) AND ii.project_id = $2 AND ii.deleted_at IS NULL)`, d.ID, projectID).Scan(&intake); err != nil {
		return err
	}
	d.IsIntake = &intake
	if blocked, err := a.guestBlocked(ctx, slug, projectID, c.User.ID, d.CreatedBy, guestViewAll); err != nil {
		return err
	} else if blocked {
		return errIssueGuest
	}
	a.recordVisit(ctx, slug, "issue", d.ID, c.User.ID, &projectID)
	if err := a.expandIssue(ctx, d, expandList(c)); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, d)
}

// strictInt is IssueDetailIdentifierEndpoint.strict_str_to_int: str.isdigit()
// (optionally after "-"), then int(). ok is false for a 400; a nil result is
// a well-formed number no issue can carry (out of the column's range).
func strictInt(s string) (*int32, bool) {
	digits := strings.TrimPrefix(s, "-")
	if digits == "" || !isPyDigits(digits) {
		return nil, false
	}
	n, ok := drf.PyIntString(s)
	if !ok {
		return nil, false // isdigit() accepts superscripts; int() does not
	}
	if !n.IsInt64() || n.Int64() < -1<<31 || n.Int64() > 1<<31-1 {
		return nil, true
	}
	v := int32(n.Int64())
	return &v, true
}

// isPyDigits is str.isdigit() as far as int() goes: other digit
// characters (superscripts) pass isdigit() but fail int(), a 400 either
// way, so only decimal digits matter.
func isPyDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// issueMeta ports IssueMetaEndpoint.get.
func (a *API) issueMeta(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	var (
		seq   int
		ident string
	)
	err = a.db.QueryRow(c.Context(), `SELECT i.sequence_id, p.identifier FROM issues i
		JOIN projects p ON p.id = i.project_id JOIN workspaces w ON w.id = i.workspace_id
		WHERE i.id = $1 AND i.project_id = $2 AND w.slug = $3 AND `+issueObjects("i"), issueID, projectID, c.Param("slug")).
		Scan(&seq, &ident)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"sequence_id": seq, "project_identifier": ident})
}
