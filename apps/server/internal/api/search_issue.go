package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// IssueSearchEndpoint (search/issue.py): the work item pickers (parent,
// relations, sub-issues, cycle and module "add existing", bulk delete).
// Only IsAuthenticated: the queryset joins the user's active project
// memberships, and a guest of the URL's project only sees what they
// created.

// searchPickerRow is one dict of the view's .values().
type searchPickerRow struct {
	Name              string      `json:"name"`
	ID                uuid.UUID   `json:"id"`
	StartDate         *httpx.Date `json:"start_date"`
	SequenceID        int         `json:"sequence_id"`
	ProjectName       string      `json:"project__name"`
	ProjectIdentifier string      `json:"project__identifier"`
	ProjectID         uuid.UUID   `json:"project_id"`
	WorkspaceSlug     string      `json:"workspace__slug"`
	StateName         *string     `json:"state__name"`
	StateGroup        *string     `json:"state__group"`
	StateColor        *string     `json:"state__color"`
}

// searchPickerIssue is Issue.issue_objects.filter(pk=issue_id).first(): its
// id and parent, or nil. An issue_id that isn't a UUID is the
// ValidationError the filter raises.
func (a *API) searchPickerIssue(ctx context.Context, raw string) (*uuid.UUID, *uuid.UUID, error) {
	id, ok := drf.ParseUUID(raw)
	if !ok {
		return nil, nil, errFilterDetail
	}
	var parent *uuid.UUID
	err := a.db.QueryRow(ctx, `SELECT i.parent_id FROM issues i WHERE i.id = $1 AND `+issueObjects("i"), id).Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &id, parent, nil
}

// searchIssues ports IssueSearchEndpoint.get. Each flag applies only when
// it is exactly "true" (and, for the pickers, with an issue_id); module
// excludes that module's issues; target_date=none keeps undated issues.
func (a *API) searchIssues(c *httpx.Ctx) error {
	project, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	flag := func(name string) string {
		if !c.HasQuery(name) {
			return "false"
		}
		return c.Query(name)
	}
	query, issueID, module := c.Query("search"), c.Query("issue_id"), c.Query("module")

	s := &searchSQL{}
	where := []string{searchIssueLive, `p.archived_at IS NULL AND pm.is_active AND pm.member_id = ` + s.arg(c.User.ID) +
		` AND w.slug = ` + s.arg(c.Param("slug"))}
	if flag("workspace_search") == "false" {
		where = append(where, `i.project_id = `+s.arg(project))
	}
	if query != "" {
		where = append(where, s.issueQuery(query, false))
	}
	if flag("parent") == "true" && issueID != "" {
		// Not the issue, its parent or its children.
		id, parent, err := a.searchPickerIssue(ctx, issueID)
		if err != nil {
			return err
		}
		if id != nil {
			where = append(where, `i.id <> `+s.arg(*id))
			if parent != nil {
				where = append(where, `i.id <> `+s.arg(*parent))
			}
			where = append(where, `NOT (i.parent_id = `+s.arg(*id)+` AND i.parent_id IS NOT NULL)`)
		}
	}
	if flag("issue_relation") == "true" && issueID != "" {
		// Not the issue nor anything related to it either way.
		id, _, err := a.searchPickerIssue(ctx, issueID)
		if err != nil {
			return err
		}
		if id != nil {
			where = append(where, `NOT (i.id = ANY(ARRAY[`+s.arg(*id)+`::uuid] || ARRAY(
				SELECT unnest(ARRAY[r.issue_id, r.related_issue_id]) FROM issue_relations r
				WHERE r.deleted_at IS NULL AND (r.related_issue_id = `+s.arg(*id)+` OR r.issue_id = `+s.arg(*id)+`))))`)
		}
	}
	if flag("sub_issue") == "true" && issueID != "" {
		// Root issues only, not the issue or its parent. A missing issue
		// crashes on issue.parent.
		id, parent, err := a.searchPickerIssue(ctx, issueID)
		if err != nil {
			return err
		}
		if id == nil {
			return errViewCrash
		}
		where = append(where, `i.id <> `+s.arg(*id), `i.parent_id IS NULL`)
		if parent != nil {
			where = append(where, `i.id <> `+s.arg(*parent))
		}
	}
	if flag("cycle") == "true" {
		// exclude(Q(issue_cycle__isnull=False) & Q(issue_cycle__deleted_at__isnull=True)),
		// as Django splits it into two subqueries.
		where = append(where, `NOT (EXISTS (SELECT 1 FROM cycle_issues u1 WHERE u1.id IS NOT NULL AND u1.issue_id = i.id)
			AND EXISTS (SELECT 1 FROM issues u0 LEFT JOIN cycle_issues u1 ON u0.id = u1.issue_id
				WHERE u1.deleted_at IS NULL AND u0.id = i.id))`)
	}
	if module != "" {
		// Likewise split: an issue once in the module (even removed) and
		// live in any module is excluded.
		m, ok := drf.ParseUUID(module)
		if !ok {
			return errFilterDetail
		}
		where = append(where, `NOT (EXISTS (SELECT 1 FROM module_issues u1 WHERE u1.module_id = `+s.arg(m)+` AND u1.issue_id = i.id)
			AND EXISTS (SELECT 1 FROM issues u0 LEFT JOIN module_issues u1 ON u0.id = u1.issue_id
				WHERE u1.deleted_at IS NULL AND u0.id = i.id))`)
	}
	if c.Query("target_date") == "none" {
		where = append(where, `i.target_date IS NULL`)
	}
	var guest bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE deleted_at IS NULL AND is_active
		AND member_id = $1 AND project_id = $2 AND role = 5)`, c.User.ID, project).Scan(&guest); err != nil {
		return err
	}
	if guest {
		where = append(where, `i.created_by_id = `+s.arg(c.User.ID))
	}

	// search_issues adds .distinct(), which also selects created_at.
	distinct, extra := "", ""
	if query != "" {
		distinct, extra = "DISTINCT ", ", i.created_at"
	}
	rows, err := a.db.Query(ctx, `SELECT `+distinct+`i.name, i.id, i.start_date, i.sequence_id, p.name, p.identifier,
			i.project_id, w.slug, s.name, s."group", s.color`+extra+`
		`+searchIssueFrom+` WHERE `+strings.Join(where, " AND ")+` ORDER BY i.created_at DESC LIMIT 100`, s.args...)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (searchPickerRow, error) {
		var (
			r     searchPickerRow
			start *time.Time
			dest  = []any{&r.Name, &r.ID, &start, &r.SequenceID, &r.ProjectName, &r.ProjectIdentifier,
				&r.ProjectID, &r.WorkspaceSlug, &r.StateName, &r.StateGroup, &r.StateColor}
		)
		if extra != "" {
			dest = append(dest, &searchIgnored{})
		}
		err := row.Scan(dest...)
		r.StartDate = dateOrNil(start)
		return r, err
	})
	if err != nil {
		return err
	}
	if out == nil {
		out = []searchPickerRow{}
	}
	return c.JSON(http.StatusOK, out)
}
