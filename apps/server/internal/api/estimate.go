package api

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// estimatePoint is EstimatePointSerializer ("__all__").
type estimatePoint struct {
	ID          uuid.UUID  `json:"id"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
	CreatedBy   *uuid.UUID `json:"created_by"`
	UpdatedBy   *uuid.UUID `json:"updated_by"`
	Key         int32      `json:"key"`
	Description string     `json:"description"`
	Value       string     `json:"value"`
	Estimate    uuid.UUID  `json:"estimate"`
	Workspace   uuid.UUID  `json:"workspace"`
	Project     uuid.UUID  `json:"project"`
}

const estimatePointCols = `ep.id, ep.created_at, ep.updated_at, ep.deleted_at, ep.created_by_id, ep.updated_by_id,
	ep.key, ep.description, ep.value, ep.estimate_id, ep.workspace_id, ep.project_id`

func scanEstimatePoint(row pgx.Row) (*estimatePoint, error) {
	var p estimatePoint
	err := row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &p.CreatedBy, &p.UpdatedBy,
		&p.Key, &p.Description, &p.Value, &p.Estimate, &p.Workspace, &p.Project)
	return &p, err
}

func (a *API) loadEstimatePoint(ctx context.Context, id uuid.UUID) (*estimatePoint, error) {
	return scanEstimatePoint(a.db.QueryRow(ctx, `SELECT `+estimatePointCols+` FROM estimate_points ep WHERE ep.id = $1`, id))
}

// estimateRead is EstimateReadSerializer and WorkspaceEstimateSerializer
// ("__all__", with the live points nested).
type estimateRead struct {
	ID          uuid.UUID        `json:"id"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	DeletedAt   *time.Time       `json:"deleted_at"`
	CreatedBy   *uuid.UUID       `json:"created_by"`
	UpdatedBy   *uuid.UUID       `json:"updated_by"`
	Project     uuid.UUID        `json:"project"`
	Workspace   uuid.UUID        `json:"workspace"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Type        string           `json:"type"`
	LastUsed    bool             `json:"last_used"`
	Points      []*estimatePoint `json:"points"`
}

const estimateCols = `e.id, e.created_at, e.updated_at, e.deleted_at, e.created_by_id, e.updated_by_id,
	e.project_id, e.workspace_id, e.name, e.description, e.type, e.last_used`

// queryEstimates runs a query over estimates e (selecting estimateCols) and
// attaches each one's live points, ordered by value as the model's Meta does.
func (a *API) queryEstimates(ctx context.Context, sql string, args ...any) ([]*estimateRead, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	estimates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*estimateRead, error) {
		e := &estimateRead{Points: []*estimatePoint{}}
		err := row.Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt, &e.DeletedAt, &e.CreatedBy, &e.UpdatedBy,
			&e.Project, &e.Workspace, &e.Name, &e.Description, &e.Type, &e.LastUsed)
		return e, err
	})
	if err != nil || len(estimates) == 0 {
		return estimates, err
	}
	ids := make([]uuid.UUID, len(estimates))
	byID := map[uuid.UUID]*estimateRead{}
	for i, e := range estimates {
		ids[i] = e.ID
		byID[e.ID] = e
	}
	prows, err := a.db.Query(ctx, `SELECT `+estimatePointCols+` FROM estimate_points ep
		WHERE ep.estimate_id = ANY($1) AND ep.deleted_at IS NULL ORDER BY ep.value`, ids)
	if err != nil {
		return nil, err
	}
	points, err := pgx.CollectRows(prows, func(row pgx.CollectableRow) (*estimatePoint, error) { return scanEstimatePoint(row) })
	if err != nil {
		return nil, err
	}
	for _, p := range points {
		byID[p.Estimate].Points = append(byID[p.Estimate].Points, p)
	}
	return estimates, nil
}

func (a *API) loadEstimate(ctx context.Context, id uuid.UUID) (*estimateRead, error) {
	estimates, err := a.queryEstimates(ctx, `SELECT `+estimateCols+` FROM estimates e WHERE e.id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(estimates) == 0 {
		return nil, pgx.ErrNoRows
	}
	return estimates[0], nil
}

// listEstimates ports BulkEstimatePointEndpoint.list.
func (a *API) listEstimates(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	estimates, err := a.queryEstimates(c.Context(), `SELECT `+estimateCols+` FROM estimates e
		JOIN workspaces w ON w.id = e.workspace_id
		WHERE w.slug = $1 AND e.project_id = $2 AND e.deleted_at IS NULL ORDER BY e.name`, c.Param("slug"), projectID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, estimates)
}

// workspaceEstimates ports WorkspaceEstimatesEndpoint.get: the estimates the
// workspace's projects use. Django caches it in Redis; Go doesn't
// (DEVIATIONS.md).
func (a *API) workspaceEstimates(c *httpx.Ctx) error {
	estimates, err := a.queryEstimates(c.Context(), `SELECT `+estimateCols+` FROM estimates e
		JOIN workspaces w ON w.id = e.workspace_id
		WHERE w.slug = $1 AND e.deleted_at IS NULL AND e.id IN (
			SELECT p.estimate_id FROM projects p JOIN workspaces pw ON pw.id = p.workspace_id
			WHERE pw.slug = $1 AND p.estimate_id IS NOT NULL AND p.deleted_at IS NULL)
		ORDER BY e.name`, c.Param("slug"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, estimates)
}

// randomName is generate_random_name: ten lowercase letters.
func randomName() string {
	b := make([]byte, 10)
	for i := range b {
		b[i] = byte('a' + rand.IntN(26))
	}
	return string(b)
}

// pyTypeOf is type(value).__name__ for a decoded JSON value.
func pyTypeOf(v drf.Value) string {
	switch v.Kind() {
	case '"':
		return "str"
	case '{':
		return "dict"
	case '[':
		return "list"
	case 't', 'f':
		return "bool"
	case 'n':
		return "NoneType"
	}
	if strings.ContainsAny(string(v.Raw()), ".eE") {
		return "float"
	}
	return "int"
}

// validateEstimatePoint runs EstimatePointSerializer over one object: the
// writable columns, plus validate()'s checks once every field is valid.
func (a *API) validateEstimatePoint(ctx context.Context, v *drf.Validator, partial bool) (*setList, error) {
	var set setList
	if !partial {
		v.Require("value")
	}
	if t, ok := v.DateTime("deleted_at", true); ok {
		set.add("deleted_at", t)
	}
	if err := a.auditFields(ctx, v, &set); err != nil {
		return nil, err
	}
	if n, ok := v.Int("key", 0, 2147483647); ok {
		set.add("key", n)
	}
	if s, ok := v.Char("description", drf.CharField{AllowBlank: true}); ok {
		set.add("description", *s)
	}
	if s, ok := v.Char("value", drf.CharField{MaxLength: 255}); ok {
		set.add("value", *s)
	}
	if v.Valid() {
		if len(set.cols) == 0 {
			v.Add("non_field_errors", "Estimate points are required")
		} else if i := indexOf(set.cols, "value"); i >= 0 && set.args[i].(string) != "" && utf8.RuneCountInString(set.args[i].(string)) > 20 {
			v.Add("non_field_errors", "Value can't be more than 20 characters")
		}
	}
	return &set, nil
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// createEstimate ports BulkEstimatePointEndpoint.create. The estimate is
// written before its points are validated, so a rejected body still leaves
// one behind; the points are inserted from the raw request values.
func (a *API) createEstimate(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	est, _ := data.Get("estimate")
	if est.Kind() != '{' {
		return errViewCrash // None, str, list ... have no .get
	}
	estData := drf.DataFromJSON(est.Raw())
	var name, typ any = randomName(), "categories"
	if v, ok := estData.Get("name"); ok {
		name = nullOrStr(v)
	}
	if v, ok := estData.Get("type"); ok {
		typ = nullOrStr(v)
	}
	lastUsed := new(bool)
	if v, ok := estData.Get("last_used"); ok {
		if lastUsed, err = djangoBool(v); err != nil {
			return err
		}
	}
	var lastUsedArg any
	if lastUsed != nil {
		lastUsedArg = *lastUsed
	}
	ctx := c.Context()
	var id, workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `
		INSERT INTO estimates (project_id, workspace_id, name, type, last_used, created_by_id)
		SELECT p.id, p.workspace_id, $2, $3, $4, $5 FROM projects p WHERE p.id = $1
		RETURNING id, workspace_id`, projectID, name, typ, lastUsedArg, c.User.ID).Scan(&id, &workspaceID); err != nil {
		return err
	}

	pts, ok := data.Get("estimate_points")
	if !ok || pts.IsNull() {
		return httpx.Body(http.StatusBadRequest, map[string]any{"non_field_errors": []string{"No data provided"}})
	}
	elems, isList := pts.Elems()
	if !isList || pts.Kind() != '[' {
		return httpx.Body(http.StatusBadRequest, map[string]any{
			"non_field_errors": []string{`Expected a list of items but got type "` + pyTypeOf(pts) + `".`}})
	}
	var (
		errs                 = make([]any, len(elems))
		failed               bool
		keys                 = make([]int32, len(elems))
		values, descriptions = make([]string, len(elems)), make([]string, len(elems))
	)
	for i, e := range elems {
		d := drf.DataFromJSON(e.Raw())
		v := drf.NewValidator(d, c.Loc())
		set, err := a.validateEstimatePoint(ctx, v, false)
		if err != nil {
			return err
		}
		if err := v.Err(); err != nil {
			var he *httpx.Error
			if errors.As(err, &he) {
				errs[i] = he.Body
				failed = true
			}
			continue
		}
		errs[i] = map[string]any{}
		if j := indexOf(set.cols, "key"); j >= 0 {
			keys[i] = int32(set.args[j].(int64))
		}
		// bulk_create writes the raw values, not the validated ones.
		rv, _ := d.Get("value")
		values[i] = drf.PyStr(rv)
		if dv, ok := d.Get("description"); ok {
			descriptions[i] = drf.PyStr(dv)
		}
	}
	if failed {
		return httpx.Body(http.StatusBadRequest, errs)
	}
	if len(elems) > 0 {
		if _, err := a.db.Exec(ctx, `
			INSERT INTO estimate_points (estimate_id, project_id, workspace_id, key, value, description, created_by_id, updated_by_id)
			SELECT $1, $2, $3, t.k, t.v, t.d, $4, $4 FROM unnest($5::int[], $6::text[], $7::text[]) AS t(k, v, d)`,
			id, projectID, workspaceID, c.User.ID, keys, values, descriptions); err != nil {
			return err
		}
	}
	e, err := a.loadEstimate(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, e)
}

// nullOrStr is the value a CharField column takes from a raw request value:
// str(value), or NULL for None.
func nullOrStr(v drf.Value) any {
	if v.IsNull() {
		return nil
	}
	return drf.PyStr(v)
}

// deleteEstimate ports BulkEstimatePointEndpoint.destroy.
func (a *API) deleteEstimate(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	estimateID, err := c.UUIDParam("estimate_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT e.id FROM estimates e JOIN workspaces w ON w.id = e.workspace_id
		WHERE e.id = $1 AND w.slug = $2 AND e.project_id = $3 AND e.deleted_at IS NULL`,
		estimateID, c.Param("slug"), projectID).Scan(&id); err != nil {
		return err
	}
	if err := softdelete.Row(ctx, a.db, "estimates", id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// createEstimatePoint ports EstimatePointEndpoint.create: key and value go
// to the column as raw request values, whatever their type.
func (a *API) createEstimatePoint(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	estimateID, err := c.UUIDParam("estimate_id")
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	key, hasKey := data.Get("key")
	value, hasValue := data.Get("value")
	if !hasKey || !hasValue || !drf.PyTruthy(key) || !drf.PyTruthy(value) {
		return httpx.Err(http.StatusBadRequest, "Key and value are required")
	}
	ctx := c.Context()
	var workspaceID uuid.UUID
	err = a.db.QueryRow(ctx, `SELECT e.workspace_id FROM estimates e JOIN workspaces w ON w.id = e.workspace_id
		WHERE e.id = $1 AND w.slug = $2 AND e.project_id = $3 AND e.deleted_at IS NULL`,
		estimateID, c.Param("slug"), projectID).Scan(&workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "Estimate not found")
	}
	if err != nil {
		return err
	}
	n, ok := drf.PyInt(key)
	if !ok || !n.IsInt64() || n.Int64() < -2147483648 || n.Int64() > 2147483647 {
		return errViewCrash // int() fails, or the column overflows
	}
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `
		INSERT INTO estimate_points (estimate_id, project_id, workspace_id, key, value, created_by_id)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		estimateID, projectID, workspaceID, int32(n.Int64()), drf.PyStr(value), c.User.ID).Scan(&id); err != nil {
		return err
	}
	p, err := a.loadEstimatePoint(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

// updateEstimatePoint ports EstimatePointEndpoint.partial_update.
func (a *API) updateEstimatePoint(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	estimateID, err := c.UUIDParam("estimate_id")
	if err != nil {
		return err
	}
	// The route takes a plain string, so a bad id fails in the lookup.
	pointID, ok := drf.ParseUUID(c.Param("estimate_point_id"))
	if !ok {
		return errFilterDetail
	}
	ctx := c.Context()
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT ep.id FROM estimate_points ep JOIN workspaces w ON w.id = ep.workspace_id
		WHERE ep.id = $1 AND ep.estimate_id = $2 AND ep.project_id = $3 AND w.slug = $4 AND ep.deleted_at IS NULL`,
		pointID, estimateID, projectID, c.Param("slug")).Scan(&id); err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	set, err := a.validateEstimatePoint(ctx, v, true)
	if err != nil {
		return err
	}
	if err := v.Err(); err != nil {
		return err
	}
	set.add("updated_at", time.Now())
	set.add("updated_by_id", c.User.ID) // save() overrides a given updated_by
	if _, err := a.db.Exec(ctx, `UPDATE estimate_points SET `+set.sql()+` WHERE id = $1`, append([]any{id}, set.args...)...); err != nil {
		return err
	}
	p, err := a.loadEstimatePoint(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}
