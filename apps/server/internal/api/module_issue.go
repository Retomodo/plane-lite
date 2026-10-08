package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// ModuleIssueViewSet's write actions. The module issue list goes through
// IssueViewSet.list (?module=); ModuleIssueViewSet.list is unused.

var moduleIssuesSuccess = map[string]string{"message": "success"}

// moduleIDsFrom iterates a request value as Python's for loop would: the
// members of a list, the keys of a dict, the characters of a string.
// Anything else (a number, a bool, null) raises TypeError.
func moduleIDsFrom(v drf.Value) ([]drf.Value, error) {
	if elems, ok := v.Elems(); ok {
		return elems, nil
	}
	switch v.Kind() {
	case '"':
		var out []drf.Value
		for _, r := range v.Str() {
			out = append(out, drf.JSONValue(jsonString(string(r))))
		}
		return out, nil
	case '{':
		var keys []drf.Value
		for _, k := range drf.DataFromJSON(v.Raw()).Keys() {
			keys = append(keys, drf.JSONValue(jsonString(k)))
		}
		return keys, nil
	}
	return nil, errViewCrash
}

// addModuleIssues is ModuleIssue.objects.bulk_create(..., ignore_conflicts=
// True) for issue/module pairs, created and updated by the user.
func (a *API) addModuleIssues(ctx context.Context, issues, modules []uuid.UUID, projectID uuid.UUID, user uuid.UUID) error {
	_, err := a.db.Exec(ctx, `INSERT INTO module_issues (id, created_at, updated_at, issue_id, module_id, project_id,
			workspace_id, created_by_id, updated_by_id)
		SELECT gen_random_uuid(), now(), now(), x.issue, x.module, p.id, p.workspace_id, $4, $4
		FROM unnest($1::uuid[], $2::uuid[]) AS x(issue, module), projects p WHERE p.id = $3
		ON CONFLICT DO NOTHING`, issues, modules, projectID, user)
	return err
}

// createModuleIssues ports ModuleIssueViewSet.create_module_issues: the
// project's issues among request.data["issues"] join the module (no check
// on the module), and each gets an activity, already linked or not.
func (a *API) createModuleIssues(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	moduleID, err := c.UUIDParam("module_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	raw, ok := data.Get("issues")
	if !ok || !drf.PyTruthy(raw) {
		return httpx.Err(http.StatusBadRequest, "Issues are required")
	}
	vals, err := moduleIDsFrom(raw)
	if err != nil {
		return err
	}
	ids, err := modelUUIDs(vals)
	if err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `SELECT i.id FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE `+issueObjects("i")+` AND w.slug = $1 AND i.project_id = $2 AND i.id = ANY($3)
		ORDER BY i.created_at DESC`, c.Param("slug"), projectID, ids)
	if err != nil {
		return err
	}
	issues, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	modules := make([]uuid.UUID, len(issues))
	for i := range modules {
		modules[i] = moduleID
	}
	if err := a.addModuleIssues(ctx, issues, modules, projectID, c.User.ID); err != nil {
		return err
	}
	for _, issue := range issues {
		a.moduleActivity(ctx, c, "module.activity.created", string(jsonString(moduleID.String())), nil, issue.String(), projectID)
	}
	return c.JSON(http.StatusCreated, moduleIssuesSuccess)
}

// createIssueModules ports ModuleIssueViewSet.create_issue_modules: the
// issue joins request.data["modules"] and leaves "removed_modules", one
// activity per entry (a removal with no link left still gets one).
func (a *API) createIssueModules(c *httpx.Ctx) error {
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
	if !data.IsDict() {
		return errViewCrash
	}
	var added, removed []drf.Value
	if raw, ok := data.Get("modules"); ok && drf.PyTruthy(raw) {
		if added, err = moduleIDsFrom(raw); err != nil {
			return err
		}
	}
	if raw, ok := data.Get("removed_modules"); ok {
		// for module_id in removed_modules: [] and None differ here.
		if removed, err = moduleIDsFrom(raw); err != nil {
			return err
		}
	}
	if len(added) > 0 {
		// Each module id goes through UUIDField when the rows are built.
		var modules, issues []uuid.UUID
		for _, v := range added {
			if v.IsNull() {
				return errModuleNullFK
			}
			ids, err := modelUUIDs([]drf.Value{v})
			if err != nil {
				return err
			}
			modules = append(modules, ids[0])
			issues = append(issues, issueID)
		}
		if err := a.addModuleIssues(ctx, issues, modules, projectID, c.User.ID); err != nil {
			return err
		}
		// json.dumps({"module_id": module}): the value as sent.
		for _, v := range added {
			a.moduleActivity(ctx, c, "module.activity.created", string(v.Raw()), nil, issueID.String(), projectID)
		}
	}
	for _, v := range removed {
		// The filter runs when module_issue.first() is read.
		var moduleID *uuid.UUID
		if !v.IsNull() {
			ids, err := modelUUIDs([]drf.Value{v})
			if err != nil {
				return err
			}
			moduleID = &ids[0]
		}
		var name *string
		err := a.db.QueryRow(ctx, `SELECT m.name FROM module_issues mi JOIN workspaces w ON w.id = mi.workspace_id
			JOIN modules m ON m.id = mi.module_id
			WHERE mi.deleted_at IS NULL AND w.slug = $1 AND mi.project_id = $2 AND mi.module_id = $3 AND mi.issue_id = $4
			ORDER BY mi.created_at DESC LIMIT 1`, c.Param("slug"), projectID, moduleID, issueID).Scan(&name)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		a.moduleActivity(ctx, c, "module.activity.deleted", string(jsonString(drf.PyStr(v))), moduleNameJSON(name), issueID.String(), projectID)
		if _, err := a.db.Exec(ctx, `UPDATE module_issues mi SET deleted_at = now() FROM workspaces w
			WHERE w.id = mi.workspace_id AND mi.deleted_at IS NULL AND w.slug = $1 AND mi.project_id = $2
				AND mi.module_id = $3 AND mi.issue_id = $4`, c.Param("slug"), projectID, moduleID, issueID); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusCreated, moduleIssuesSuccess)
}

// errModuleNullFK is ModuleIssue(module_id=None) failing NOT NULL.
var errModuleNullFK = httpx.Err(http.StatusBadRequest, "The payload is not valid")

// deleteModuleIssue ports ModuleIssueViewSet.destroy: nothing to do when
// the link is already gone.
func (a *API) deleteModuleIssue(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	moduleID, err := c.UUIDParam("module_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var name string
	err = a.db.QueryRow(ctx, `SELECT m.name FROM module_issues mi JOIN workspaces w ON w.id = mi.workspace_id
		JOIN modules m ON m.id = mi.module_id
		WHERE mi.deleted_at IS NULL AND w.slug = $1 AND mi.project_id = $2 AND mi.module_id = $3 AND mi.issue_id = $4
		ORDER BY mi.created_at DESC LIMIT 1`, slug, projectID, moduleID, issueID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.NoContent()
	}
	if err != nil {
		return err
	}
	a.moduleActivity(ctx, c, "module.activity.deleted", string(jsonString(moduleID.String())), moduleNameJSON(&name), issueID.String(), projectID)
	if _, err := a.db.Exec(ctx, `UPDATE module_issues mi SET deleted_at = now() FROM workspaces w
		WHERE w.id = mi.workspace_id AND mi.deleted_at IS NULL AND w.slug = $1 AND mi.project_id = $2
			AND mi.module_id = $3 AND mi.issue_id = $4`, slug, projectID, moduleID, issueID); err != nil {
		return err
	}
	return c.NoContent()
}
