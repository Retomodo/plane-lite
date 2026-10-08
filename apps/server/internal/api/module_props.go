package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// ModuleUserPropertiesEndpoint (P/modules/<module_id>/user-properties/).

// moduleUserProperties is ModuleUserPropertiesSerializer.
type moduleUserProperties struct {
	ID                uuid.UUID      `json:"id"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         *time.Time     `json:"deleted_at"`
	Filters           jsontext.Value `json:"filters"`
	DisplayFilters    jsontext.Value `json:"display_filters"`
	DisplayProperties jsontext.Value `json:"display_properties"`
	RichFilters       jsontext.Value `json:"rich_filters"`
	CreatedBy         *uuid.UUID     `json:"created_by"`
	UpdatedBy         *uuid.UUID     `json:"updated_by"`
	Project           uuid.UUID      `json:"project"`
	Workspace         uuid.UUID      `json:"workspace"`
	Module            uuid.UUID      `json:"module"`
	User              uuid.UUID      `json:"user"`
}

const moduleUserPropertiesSelect = `SELECT x.id, x.created_at, x.updated_at, x.deleted_at, x.filters::text,
		x.display_filters::text, x.display_properties::text, x.rich_filters::text, x.created_by_id, x.updated_by_id,
		x.project_id, x.workspace_id, x.module_id, x.user_id
	FROM module_user_properties x JOIN workspaces w ON w.id = x.workspace_id`

func scanModuleUserProperties(row pgx.Row) (*moduleUserProperties, error) {
	var (
		p                     moduleUserProperties
		filters, df, dp, rich string
	)
	if err := row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &filters, &df, &dp, &rich, &p.CreatedBy,
		&p.UpdatedBy, &p.Project, &p.Workspace, &p.Module, &p.User); err != nil {
		return nil, err
	}
	p.Filters, p.DisplayFilters = jsontext.Value(filters), jsontext.Value(df)
	p.DisplayProperties, p.RichFilters = jsontext.Value(dp), jsontext.Value(rich)
	return &p, nil
}

// moduleUserPropertiesGet is ModuleUserProperties.objects.get(user,
// module_id, project_id, workspace__slug).
func (a *API) moduleUserPropertiesGet(ctx context.Context, c *httpx.Ctx) (*moduleUserProperties, uuid.UUID, uuid.UUID, error) {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	moduleID, err := c.UUIDParam("module_id")
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	p, err := scanModuleUserProperties(a.db.QueryRow(ctx, moduleUserPropertiesSelect+`
		WHERE x.deleted_at IS NULL AND x.user_id = $1 AND x.module_id = $2 AND x.project_id = $3 AND w.slug = $4`,
		c.User.ID, moduleID, projectID, c.Param("slug")))
	return p, projectID, moduleID, err
}

// getModuleUserProperties ports ModuleUserPropertiesEndpoint.get
// (get_or_create; the create sets no workspace__slug, save() takes the
// project's workspace).
func (a *API) getModuleUserProperties(c *httpx.Ctx) error {
	ctx := c.Context()
	p, projectID, moduleID, err := a.moduleUserPropertiesGet(ctx, c)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := a.db.Exec(ctx, `INSERT INTO module_user_properties (id, created_at, updated_at, module_id, user_id,
				project_id, workspace_id, created_by_id)
			SELECT gen_random_uuid(), now(), now(), $1, $2, p.id, p.workspace_id, $2 FROM projects p WHERE p.id = $3`,
			moduleID, c.User.ID, projectID); err != nil {
			// get_or_create retries the get, then re-raises.
			if p, _, _, gerr := a.moduleUserPropertiesGet(ctx, c); gerr == nil {
				return c.JSON(http.StatusOK, p)
			}
			return err
		}
		p, _, _, err = a.moduleUserPropertiesGet(ctx, c)
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

// patchModuleUserProperties ports ModuleUserPropertiesEndpoint.patch: each
// key replaces its column as given (no validation), and answers 201.
func (a *API) patchModuleUserProperties(c *httpx.Ctx) error {
	ctx := c.Context()
	p, _, _, err := a.moduleUserPropertiesGet(ctx, c)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get on a list
	}
	cols := map[string]*jsontext.Value{"filters": &p.Filters, "rich_filters": &p.RichFilters,
		"display_filters": &p.DisplayFilters, "display_properties": &p.DisplayProperties}
	for name, dst := range cols {
		if v, ok := data.Get(name); ok {
			*dst = v.Raw()
		}
	}
	// save(): every column written back. A JSON null fails NOT NULL.
	nullable := func(v jsontext.Value) any {
		if v.Kind() == 'n' {
			return nil
		}
		return string(v)
	}
	if _, err := a.db.Exec(ctx, `UPDATE module_user_properties SET filters = $2::jsonb, rich_filters = $3::jsonb,
			display_filters = $4::jsonb, display_properties = $5::jsonb, updated_at = now(), updated_by_id = $6
		WHERE id = $1`, p.ID, nullable(p.Filters), nullable(p.RichFilters), nullable(p.DisplayFilters),
		nullable(p.DisplayProperties), c.User.ID); err != nil {
		return err
	}
	updated, err := scanModuleUserProperties(a.db.QueryRow(ctx, moduleUserPropertiesSelect+` WHERE x.id = $1`, p.ID))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, updated)
}
