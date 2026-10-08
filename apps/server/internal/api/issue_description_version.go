package api

import (
	"encoding/base64"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// WorkItemDescriptionVersionEndpoint
// (P/work-items/<work_item_id>/description-versions/[<pk>/]). The rows come
// from issue_description_version_task (issue_activity.go).

// descriptionVersionValues is one dict of the list's .values().
type descriptionVersionValues struct {
	ID          uuid.UUID  `json:"id"`
	Workspace   uuid.UUID  `json:"workspace"`
	Project     uuid.UUID  `json:"project"`
	Issue       uuid.UUID  `json:"issue"`
	LastSavedAt utcTime    `json:"last_saved_at"`
	OwnedBy     uuid.UUID  `json:"owned_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CreatedBy   *uuid.UUID `json:"created_by"`
	UpdatedBy   *uuid.UUID `json:"updated_by"`
}

// descriptionVersionDetail is IssueDescriptionVersionDetailSerializer.
// description_binary goes through ModelField.value_to_string: base64.
type descriptionVersionDetail struct {
	ID                  uuid.UUID      `json:"id"`
	Workspace           uuid.UUID      `json:"workspace"`
	Project             uuid.UUID      `json:"project"`
	Issue               uuid.UUID      `json:"issue"`
	DescriptionBinary   *string        `json:"description_binary"`
	DescriptionHTML     string         `json:"description_html"`
	DescriptionStripped *string        `json:"description_stripped"`
	DescriptionJSON     jsontext.Value `json:"description_json"`
	LastSavedAt         time.Time      `json:"last_saved_at"`
	OwnedBy             uuid.UUID      `json:"owned_by"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	CreatedBy           *uuid.UUID     `json:"created_by"`
	UpdatedBy           *uuid.UUID     `json:"updated_by"`
}

// descriptionVersions ports WorkItemDescriptionVersionEndpoint.get.
func (a *API) descriptionVersions(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("work_item_id")
	if err != nil {
		return err
	}
	var pk *uuid.UUID
	if c.Param("pk") != "" {
		id, err := c.UUIDParam("pk")
		if err != nil {
			return err
		}
		pk = &id
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var (
		createdBy    *uuid.UUID
		guestViewAll bool
	)
	// Issue.objects.get(...): any live issue, archived and drafts included.
	if err := a.db.QueryRow(ctx, `SELECT i.created_by_id, p.guest_view_all_features
		FROM issues i JOIN workspaces w ON w.id = i.workspace_id JOIN projects p ON p.id = $2
		WHERE w.slug = $1 AND i.project_id = $2 AND i.id = $3 AND i.deleted_at IS NULL`, slug, projectID, issueID).
		Scan(&createdBy, &guestViewAll); err != nil {
		return err
	}
	if blocked, err := a.guestBlocked(ctx, slug, projectID, c.User.ID, createdBy, guestViewAll); err != nil {
		return err
	} else if blocked {
		return errIssueGuest
	}
	const scope = `FROM issue_description_versions v JOIN workspaces w ON w.id = v.workspace_id
		WHERE w.slug = $1 AND v.project_id = $2 AND v.issue_id = $3 AND v.deleted_at IS NULL`
	if pk != nil {
		var (
			d      descriptionVersionDetail
			binary []byte
			doc    string
		)
		if err := a.db.QueryRow(ctx, `SELECT v.id, v.workspace_id, v.project_id, v.issue_id, v.description_binary,
				v.description_html, v.description_stripped, v.description_json::text, v.last_saved_at, v.owned_by_id,
				v.created_at, v.updated_at, v.created_by_id, v.updated_by_id `+scope+` AND v.id = $4`,
			slug, projectID, issueID, *pk).Scan(&d.ID, &d.Workspace, &d.Project, &d.Issue, &binary, &d.DescriptionHTML,
			&d.DescriptionStripped, &doc, &d.LastSavedAt, &d.OwnedBy, &d.CreatedAt, &d.UpdatedAt, &d.CreatedBy,
			&d.UpdatedBy); err != nil {
			return err
		}
		if binary != nil {
			s := base64.StdEncoding.EncodeToString(binary)
			d.DescriptionBinary = &s
		}
		d.DescriptionJSON = jsontext.Value(doc)
		return c.JSON(http.StatusOK, d)
	}

	cur, err := parseGlobalCursor(c.Query("cursor"), c.HasQuery("cursor"))
	if err != nil {
		return err
	}
	var total int
	if err := a.db.QueryRow(ctx, `SELECT count(*) `+scope, slug, projectID, issueID).Scan(&total); err != nil {
		return err
	}
	page, err := cur.slice(total)
	if err != nil {
		return err
	}
	results := []descriptionVersionValues{}
	if page.end > page.start {
		rows, err := a.db.Query(ctx, `SELECT v.id, v.workspace_id, v.project_id, v.issue_id, v.last_saved_at,
				v.owned_by_id, v.created_at, v.updated_at, v.created_by_id, v.updated_by_id `+scope+`
			ORDER BY v.created_at DESC OFFSET $4 LIMIT $5`, slug, projectID, issueID, page.start, page.end-page.start)
		if err != nil {
			return err
		}
		results, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (descriptionVersionValues, error) {
			var (
				v     descriptionVersionValues
				saved time.Time
			)
			err := row.Scan(&v.ID, &v.Workspace, &v.Project, &v.Issue, &saved, &v.OwnedBy, &v.CreatedAt, &v.UpdatedAt,
				&v.CreatedBy, &v.UpdatedBy)
			v.LastSavedAt = utcTime(saved)
			return v, err
		})
		if err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, page.body(results, len(results)))
}

// globalCursor is utils/global_paginator.PaginateCursor:
// "<page size>:<page>:<offset>", the offset unused.
type globalCursor struct{ size, page int64 }

// parseGlobalCursor reads ?cursor= (default 1000:0:0); a malformed one is a
// ValueError the view does not catch.
func parseGlobalCursor(s string, given bool) (globalCursor, error) {
	if !given {
		return globalCursor{size: 1000}, nil
	}
	bits := strings.Split(s, ":")
	if len(bits) != 3 {
		return globalCursor{}, errViewCrash
	}
	var vals [3]int64
	for i, b := range bits {
		n, ok := drf.PyInt(drf.JSONValue(jsonString(b)))
		if !ok || !n.IsInt64() {
			return globalCursor{}, errViewCrash
		}
		vals[i] = n.Int64()
	}
	return globalCursor{size: vals[0], page: vals[1]}, nil
}

type globalPage struct {
	cur        globalCursor
	size       int64
	start, end int64
	total      int
}

// slice is paginate()'s arithmetic. A zero page size divides by zero and a
// negative one makes a negative slice index: both crash the view.
func (c globalCursor) slice(total int) (*globalPage, error) {
	size := min(c.size, 1000)
	if size <= 0 {
		return nil, errViewCrash
	}
	var start int64
	if c.page > 0 {
		start = c.page * size
	}
	end := min(start+size, int64(total))
	return &globalPage{cur: c, size: size, start: start, end: end, total: total}, nil
}

func (p *globalPage) body(results any, count int) map[string]any {
	size := strconv.FormatInt(p.size, 10)
	cursor := func(page int64) string { return size + ":" + strconv.FormatInt(page, 10) + ":0" }
	var next any
	if p.end < int64(p.total) {
		next = cursor(p.cur.page + 1)
	}
	return map[string]any{
		"prev_cursor":       cursor(p.cur.page - 1),
		"cursor":            cursor(p.cur.page),
		"next_cursor":       next,
		"prev_page_results": p.cur.page > 0,
		"next_page_results": next != nil,
		"page_count":        count,
		"total_results":     p.total,
		"total_pages":       int(math.Ceil(float64(p.total) / float64(p.size))),
		"results":           results,
	}
}
