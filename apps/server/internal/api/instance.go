package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"plane-lite/server/internal/httpx"
)

// instanceRow is InstanceSerializer (fields "__all__").
type instanceRow struct {
	ID                         uuid.UUID  `json:"id"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
	DeletedAt                  *time.Time `json:"deleted_at"`
	InstanceName               string     `json:"instance_name"`
	WhitelistEmails            *string    `json:"whitelist_emails"`
	InstanceID                 string     `json:"instance_id"`
	CurrentVersion             string     `json:"current_version"`
	LatestVersion              *string    `json:"latest_version"`
	LastCheckedAt              time.Time  `json:"last_checked_at"`
	Namespace                  *string    `json:"namespace"`
	IsTelemetryEnabled         bool       `json:"is_telemetry_enabled"`
	IsSupportRequired          bool       `json:"is_support_required"`
	IsSetupDone                bool       `json:"is_setup_done"`
	IsSignupScreenVisited      bool       `json:"is_signup_screen_visited"`
	IsVerified                 bool       `json:"is_verified"`
	CreatedBy                  *uuid.UUID `json:"created_by"`
	UpdatedBy                  *uuid.UUID `json:"updated_by"`
	Domain                     string     `json:"domain"`
	Edition                    string     `json:"edition"`
	IsTest                     bool       `json:"is_test"`
	IsCurrentVersionDeprecated bool       `json:"is_current_version_deprecated"`
	WorkspacesExist            bool       `json:"workspaces_exist"`
}

// EnsureInstance is register_instance plus a completed god-mode setup: this
// server has no admin app, so the instance is always configured.
func EnsureInstance(ctx context.Context, a *API) error {
	_, err := a.db.Exec(ctx, `
		WITH updated AS (
			UPDATE instances SET current_version = $1, latest_version = $1, last_checked_at = now(),
				is_setup_done = true, edition = 'PLANE_COMMUNITY', updated_at = now()
			WHERE deleted_at IS NULL
			RETURNING id
		)
		INSERT INTO instances (instance_name, instance_id, current_version, latest_version,
			last_checked_at, is_setup_done, is_signup_screen_visited, domain)
		SELECT 'Plane Community Edition', substr(md5(random()::text), 1, 24), $1, $1, now(), true, true, ''
		WHERE NOT EXISTS (SELECT 1 FROM updated)`, a.cfg.AppVersion)
	return err
}

// getInstance ports license.api.views.instance.InstanceEndpoint.get.
func (a *API) getInstance(c *httpx.Ctx) error {
	ctx := c.Context()
	var in instanceRow
	err := a.db.QueryRow(ctx, `
		SELECT id, created_at, updated_at, deleted_at, instance_name, whitelist_emails, instance_id,
			current_version, latest_version, last_checked_at, namespace, is_telemetry_enabled,
			is_support_required, is_setup_done, is_signup_screen_visited, is_verified, created_by_id,
			updated_by_id, domain, edition, is_test, is_current_version_deprecated,
			EXISTS (SELECT 1 FROM workspaces WHERE deleted_at IS NULL)
		FROM instances WHERE deleted_at IS NULL ORDER BY created_at LIMIT 1`,
	).Scan(&in.ID, &in.CreatedAt, &in.UpdatedAt, &in.DeletedAt, &in.InstanceName, &in.WhitelistEmails,
		&in.InstanceID, &in.CurrentVersion, &in.LatestVersion, &in.LastCheckedAt, &in.Namespace,
		&in.IsTelemetryEnabled, &in.IsSupportRequired, &in.IsSetupDone, &in.IsSignupScreenVisited,
		&in.IsVerified, &in.CreatedBy, &in.UpdatedBy, &in.Domain, &in.Edition, &in.IsTest,
		&in.IsCurrentVersionDeprecated, &in.WorkspacesExist)
	if err != nil {
		return err
	}
	cfg := a.cfg
	config := map[string]any{
		"enable_signup":                  cfg.EnableSignup,
		"is_workspace_creation_disabled": cfg.DisableWorkspaceCreation,
		"is_google_enabled":              false,
		"is_github_enabled":              false,
		"is_gitlab_enabled":              false,
		"is_gitea_enabled":               false,
		"is_magic_login_enabled":         cfg.EnableMagicLinkLogin,
		"is_email_password_enabled":      cfg.EnableEmailPassword,
		"github_app_name":                "",
		"slack_client_id":                nil,
		"has_unsplash_configured":        false,
		"has_llm_configured":             false,
		"file_size_limit":                float64(cfg.FileSizeLimit),
		"is_smtp_configured":             cfg.Email.Configured(),
		"admin_base_url":                 nullable(cfg.AdminBaseURL),
		"space_base_url":                 nullable(cfg.SpaceBaseURL),
		"app_base_url":                   nullable(cfg.AppBaseURL),
		"instance_changelog_url":         cfg.InstanceChangelogURL,
		"is_self_managed":                true,
	}
	c.W.Header().Set("Cache-Control", "max-age=12, private")
	return c.JSON(http.StatusOK, map[string]any{"config": config, "instance": in})
}

// nullable renders "" as JSON null, like Django settings left as None.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
