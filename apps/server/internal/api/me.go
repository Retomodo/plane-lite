package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
)

// errViewCrash stands in for an uncaught exception inside a Plane view,
// which BaseAPIView turns into a 500 "Something went wrong" response.
var errViewCrash = errors.New("api: view raised")

// setList builds the SET clause of an UPDATE whose $1 is the row id.
type setList struct {
	cols []string
	args []any
}

func (s *setList) add(col string, v any) { s.addCast(col, v, "") }

func (s *setList) addCast(col string, v any, cast string) {
	s.args = append(s.args, v)
	s.cols = append(s.cols, fmt.Sprintf("%s = $%d%s", col, len(s.args)+1, cast))
}

func (s *setList) sql() string { return strings.Join(s.cols, ", ") }

// userSaveSQL is what User.save() adds to every write: auto_now and the API
// token rotation it performs once a token has been issued.
const userSaveSQL = `updated_at = now(),
	token = CASE WHEN token_updated_at IS NOT NULL THEN replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', '') ELSE token END,
	token_updated_at = CASE WHEN token_updated_at IS NOT NULL THEN now() END`

// userSession ports app.views.user.base.UserSessionEndpoint.
func (a *API) userSession(c *httpx.Ctx) error {
	if c.User == nil {
		return c.JSON(http.StatusOK, map[string]any{"is_authenticated": false})
	}
	u, err := a.loadUserMe(c.Context(), c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"is_authenticated": true, "user": u})
}

// userSettings ports UserEndpoint.retrieve_user_settings (UserMeSettingsSerializer).
func (a *API) userSettings(c *httpx.Ctx) error {
	ctx := c.Context()
	var (
		email         *string
		lastWorkspace *uuid.UUID
		invites       int
	)
	if err := a.db.QueryRow(ctx, `
		SELECT u.email, p.last_workspace_id,
			(SELECT count(*) FROM workspace_member_invites i WHERE i.email = u.email AND i.deleted_at IS NULL)
		FROM users u JOIN profiles p ON p.user_id = u.id WHERE u.id = $1`, c.User.ID,
	).Scan(&email, &lastWorkspace, &invites); err != nil {
		return err
	}
	workspace := map[string]any{"invites": invites}
	found := false
	if lastWorkspace != nil {
		var (
			slug, name string
			logoID     *uuid.UUID
			logoType   *string
		)
		err := a.db.QueryRow(ctx, `
			SELECT w.slug, w.name, fa.id, fa.entity_type
			FROM workspaces w
			LEFT JOIN file_assets fa ON fa.id = w.logo_asset_id
			WHERE w.id = $1 AND w.deleted_at IS NULL AND EXISTS (
				SELECT 1 FROM workspace_members wm
				WHERE wm.workspace_id = w.id AND wm.member_id = $2 AND wm.is_active)`,
			*lastWorkspace, c.User.ID,
		).Scan(&slug, &name, &logoID, &logoType)
		switch {
		case err == nil:
			found = true
			var logo any = ""
			if logoID != nil {
				logo = staticAssetURL(*logoID, logoType)
			}
			workspace["last_workspace_id"] = *lastWorkspace
			workspace["last_workspace_slug"] = slug
			workspace["last_workspace_name"] = name
			workspace["last_workspace_logo"] = logo
			workspace["fallback_workspace_id"] = *lastWorkspace
			workspace["fallback_workspace_slug"] = slug
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
	}
	if !found {
		var (
			id   *uuid.UUID
			slug *string
		)
		err := a.db.QueryRow(ctx, `
			SELECT w.id, w.slug FROM workspaces w
			WHERE w.deleted_at IS NULL AND EXISTS (
				SELECT 1 FROM workspace_members wm
				WHERE wm.workspace_id = w.id AND wm.member_id = $1 AND wm.is_active)
			ORDER BY w.created_at LIMIT 1`, c.User.ID,
		).Scan(&id, &slug)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		workspace["last_workspace_id"] = nil
		workspace["last_workspace_slug"] = nil
		workspace["fallback_workspace_id"] = id
		workspace["fallback_workspace_slug"] = slug
	}
	cachePrivate(c)
	return c.JSON(http.StatusOK, map[string]any{"id": c.User.ID, "email": email, "workspace": workspace})
}

// instanceAdmin ports UserEndpoint.retrieve_instance_admin.
func (a *API) instanceAdmin(c *httpx.Ctx) error {
	var isAdmin bool
	if err := a.db.QueryRow(c.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM instance_admins ia
			WHERE ia.user_id = $1 AND ia.deleted_at IS NULL
				AND ia.instance_id = (SELECT id FROM instances ORDER BY created_at LIMIT 1))`, c.User.ID,
	).Scan(&isAdmin); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"is_instance_admin": isAdmin})
}

// Social-login accounts (AccountEndpoint). OAuth is cut, so a user never
// has any: the list is empty and every lookup is DoesNotExist.
func (a *API) listAccounts(c *httpx.Ctx) error { return c.JSON(http.StatusOK, []any{}) }

func (a *API) accountsNoPK(c *httpx.Ctx) error {
	return errViewCrash // delete() without pk raises TypeError
}

func (a *API) account(c *httpx.Ctx) error {
	if _, err := c.UUIDParam("pk"); err != nil {
		return err
	}
	return httpx.Err(http.StatusNotFound, "The required object does not exist.")
}

// patchMe ports UserEndpoint.partial_update (UserSerializer, partial).
func (a *API) patchMe(c *httpx.Ctx) error {
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	ctx := c.Context()
	v := drf.NewValidator(data, c.Loc())
	var set setList
	assetExists := func(id uuid.UUID) (bool, error) {
		var ok bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM file_assets WHERE id = $1 AND deleted_at IS NULL)`, id).Scan(&ok)
		return ok, err
	}
	if t, ok := v.DateTime("last_login", true); ok {
		set.add("last_login", t)
	}
	if s, ok := v.Char("display_name", drf.CharField{MaxLength: 255}); ok {
		set.add("display_name", *s)
	}
	for _, f := range []struct{ col, label string }{{"first_name", "First name"}, {"last_name", "Last name"}} {
		if s, ok := v.Char(f.col, drf.CharField{MaxLength: 255, AllowBlank: true}); ok {
			if containsURL(*s) {
				v.Add(f.col, f.label+" cannot contain a URL.")
			} else {
				set.add(f.col, *s)
			}
		}
	}
	if s, ok := v.Char("avatar", drf.CharField{AllowBlank: true}); ok {
		set.add("avatar", *s)
	}
	if id, ok, err := v.PK("avatar_asset", true, assetExists); err != nil {
		return err
	} else if ok {
		set.add("avatar_asset_id", id)
	}
	if s, ok := v.Char("cover_image", drf.CharField{MaxLength: 800, AllowBlank: true, AllowNull: true, URL: true}); ok {
		set.add("cover_image", s)
	}
	if id, ok, err := v.PK("cover_image_asset", true, assetExists); err != nil {
		return err
	} else if ok {
		set.add("cover_image_asset_id", id)
	}
	for _, col := range []string{"is_password_expired", "is_password_reset_required"} {
		if b, ok := v.Bool(col); ok {
			set.add(col, b)
		}
	}
	if s, ok := v.Char("bot_type", drf.CharField{MaxLength: 30, AllowBlank: true, AllowNull: true}); ok {
		set.add("bot_type", s)
	}
	if s, ok := v.Choice("user_timezone", userTimezones, drf.ChoiceField{}); ok {
		set.add("user_timezone", *s)
	}
	if b, ok := v.Bool("is_email_valid"); ok {
		set.add("is_email_valid", b)
	}
	if t, ok := v.DateTime("masked_at", true); ok {
		set.add("masked_at", t)
	}
	if err := v.Err(); err != nil {
		return err
	}
	sep := ""
	if len(set.cols) > 0 {
		sep = ", "
	}
	if _, err := a.db.Exec(ctx, `UPDATE users SET `+set.sql()+sep+userSaveSQL+` WHERE id = $1`,
		append([]any{c.User.ID}, set.args...)...); err != nil {
		return err
	}
	u, err := a.loadUserFull(ctx, c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, u)
}

// urlPattern is plane.utils.url.URL_PATTERN, with Python's Unicode \s.
var urlPattern = regexp.MustCompile(`(?i)(?:https?://[^\t\n\v\f\r \x1c-\x1f\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]+` +
	`|www\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*` +
	`|(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,6}` +
	`|(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?))`)

// containsURL is plane.utils.url.contains_url.
func containsURL(value string) bool {
	if len([]rune(value)) > 1000 {
		return false
	}
	for _, line := range strings.Split(value, "\n") {
		if r := []rune(line); len(r) > 500 {
			line = string(r[:500])
		}
		if urlPattern.MatchString(line) {
			return true
		}
	}
	return false
}

// userDeactivationEmail is bgtasks.user_deactivation_email_task.user_deactivation_email.
type userDeactivationEmail struct {
	CurrentSite string    `json:"current_site"`
	UserID      uuid.UUID `json:"user_id"`
}

func (userDeactivationEmail) Kind() string                 { return "user_deactivation_email" }
func (userDeactivationEmail) InsertOpts() river.InsertOpts { return emailInsertOpts }

func (a *API) registerUserJobs() {
	jobs.Register(a.jobs, func(ctx context.Context, j userDeactivationEmail) error {
		var email, firstName, displayName string
		if err := a.db.QueryRow(ctx, `SELECT coalesce(email, ''), first_name, display_name FROM users WHERE id = $1`, j.UserID).
			Scan(&email, &firstName, &displayName); err != nil {
			return err
		}
		name := firstName
		if name == "" {
			name = displayName
		}
		if name == "" {
			name = email
		}
		return a.mailer.SendTemplate(ctx, email, name+" has been deactivated on Plane", "user/user_deactivation.html",
			map[string]string{"email": email, "login_url": j.CurrentSite + "/login"})
	})
	jobs.Register(a.jobs, func(ctx context.Context, j emailUpdateCode) error {
		return a.mailer.SendTemplate(ctx, j.Email, "Verify your new email address",
			"auth/magic_signin.html", map[string]string{"code": j.Token, "email": j.Email})
	})
	jobs.Register(a.jobs, func(ctx context.Context, j emailUpdated) error {
		return a.mailer.SendTemplate(ctx, j.Email, "Plane email address successfully updated",
			"user/email_updated.html", map[string]string{"email": j.Email})
	})
}

// deactivateMe ports UserEndpoint.deactivate.
func (a *API) deactivateMe(c *httpx.Ctx) error {
	ctx := c.Context()
	var isAdmin bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM instance_admins WHERE user_id = $1 AND deleted_at IS NULL)`,
		c.User.ID).Scan(&isAdmin); err != nil {
		return err
	}
	if isAdmin {
		return httpx.Err(http.StatusBadRequest, "You cannot deactivate your account since you are an instance admin")
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Django's "only admin" guard can never fire: it counts per membership
	// row, so total_members is always 1. Every active membership goes.
	// (bulk_update: no updated_at.)
	for _, q := range []string{
		`UPDATE project_members SET is_active = false WHERE member_id = $1 AND is_active AND deleted_at IS NULL`,
		`UPDATE workspace_members SET is_active = false WHERE member_id = $1 AND is_active AND deleted_at IS NULL`,
		`UPDATE workspace_member_invites SET deleted_at = now()
			WHERE email = (SELECT email FROM users WHERE id = $1) AND deleted_at IS NULL`,
		`DELETE FROM sessions WHERE user_id = $1::text`,
		`UPDATE profiles SET last_workspace_id = NULL, is_tour_completed = false, is_onboarded = false,
			onboarding_step = '{"workspace_join": false, "profile_complete": false, "workspace_create": false, "workspace_invite": false}',
			updated_at = now()
			WHERE user_id = $1`,
	} {
		if _, err := tx.Exec(ctx, q, c.User.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users SET is_password_autoset = true, password = $2, is_active = false,
			last_logout_ip = $3, last_logout_time = now(), `+userSaveSQL+`
		WHERE id = $1`,
		c.User.ID, auth.HashPassword(strings.ReplaceAll(uuid.NewString(), "-", "")), auth.ClientIP(c.R)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if err := jobs.Enqueue(ctx, a.jobs, userDeactivationEmail{CurrentSite: a.baseHost(true), UserID: c.User.ID}); err != nil {
		return err
	}
	if err := a.sessions.Logout(ctx, c.W, c.R); err != nil {
		return err
	}
	return c.NoContent()
}

// djangoBool is models.BooleanField.to_python as a save() applies it to a
// raw request value; nil means NULL (an IntegrityError on save).
func djangoBool(v drf.Value) (*bool, error) {
	t, f := true, false
	switch v.Kind() {
	case 'n':
		return nil, nil
	case 't':
		return &t, nil
	case 'f':
		return &f, nil
	case '0':
		if b, ok := drf.PyBool(v); ok { // 1 == True, 0.0 == False
			return &b, nil
		}
	case '"':
		switch v.Str() {
		case "t", "True", "1":
			return &t, nil
		case "f", "False", "0":
			return &f, nil
		}
	}
	return nil, httpx.Err(http.StatusBadRequest, "Please provide valid detail")
}

// setProfileFlag ports UpdateUserOnBoardedEndpoint / UpdateUserTourCompletedEndpoint:
// profile.<field> = request.data.get(field, False); profile.save().
func (a *API) setProfileFlag(field string) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		data, err := drf.Parse(c.R)
		if err != nil {
			return err
		}
		if !data.IsDict() {
			return errViewCrash
		}
		value := new(bool)
		if raw, ok := data.Get(field); ok {
			if value, err = djangoBool(raw); err != nil {
				return err
			}
			if value == nil {
				return httpx.Err(http.StatusBadRequest, "The payload is not valid")
			}
		}
		if _, err := a.db.Exec(c.Context(), `UPDATE profiles SET `+field+` = $2, updated_at = now() WHERE user_id = $1`,
			c.User.ID, *value); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]any{"message": "Updated successfully"})
	}
}

// strData is request.data.get(name, "") followed by a str method call: a
// non-string value raises AttributeError.
func strData(d *drf.Data, name string) (string, error) {
	if !d.IsDict() {
		return "", errViewCrash
	}
	v, ok := d.Get(name)
	if !ok {
		return "", nil
	}
	if !v.IsString() {
		return "", errViewCrash
	}
	return v.Str(), nil
}
