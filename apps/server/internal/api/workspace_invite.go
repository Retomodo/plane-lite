package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/mail"
	"plane-lite/server/internal/softdelete"
)

// workspaceLite is WorkspaceLiteSerializer.
type workspaceLite struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Slug    string    `json:"slug"`
	LogoURL *string   `json:"logo_url"`
}

// invite is WorkSpaceMemberInviteSerializer.
type invite struct {
	ID          uuid.UUID     `json:"id"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	DeletedAt   *time.Time    `json:"deleted_at"`
	CreatedBy   *uuid.UUID    `json:"created_by"`
	UpdatedBy   *uuid.UUID    `json:"updated_by"`
	Workspace   workspaceLite `json:"workspace"`
	Email       string        `json:"email"`
	Accepted    bool          `json:"accepted"`
	Token       string        `json:"token"`
	Message     *string       `json:"message"`
	RespondedAt *time.Time    `json:"responded_at"`
	Role        int           `json:"role"`
	InviteLink  string        `json:"invite_link"`
}

// publicInvite is WorkSpaceMemberInvitePublicSerializer: no token or link.
type publicInvite struct {
	ID          uuid.UUID     `json:"id"`
	Email       string        `json:"email"`
	Workspace   workspaceLite `json:"workspace"`
	Role        int           `json:"role"`
	Message     *string       `json:"message"`
	Accepted    bool          `json:"accepted"`
	RespondedAt *time.Time    `json:"responded_at"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	CreatedBy   *uuid.UUID    `json:"created_by"`
}

const inviteSelect = `
	SELECT i.id, i.created_at, i.updated_at, i.deleted_at, i.created_by_id, i.updated_by_id, i.email,
		i.accepted, i.token, i.message, i.responded_at, i.role, w.id, w.name, w.slug, w.logo,
		w.logo_asset_id, fa.entity_type
	FROM workspace_member_invites i
	JOIN workspaces w ON w.id = i.workspace_id
	LEFT JOIN file_assets fa ON fa.id = w.logo_asset_id`

func scanInvite(row pgx.Row) (*invite, error) {
	var (
		inv      invite
		logo     *string
		logoID   *uuid.UUID
		logoType *string
	)
	if err := row.Scan(&inv.ID, &inv.CreatedAt, &inv.UpdatedAt, &inv.DeletedAt, &inv.CreatedBy, &inv.UpdatedBy,
		&inv.Email, &inv.Accepted, &inv.Token, &inv.Message, &inv.RespondedAt, &inv.Role,
		&inv.Workspace.ID, &inv.Workspace.Name, &inv.Workspace.Slug, &logo, &logoID, &logoType); err != nil {
		return nil, err
	}
	inv.Workspace.LogoURL = imageURL(logoID, logoType, logo)
	inv.InviteLink = "/workspace-invitations/?invitation_id=" + inv.ID.String() + "&slug=" + inv.Workspace.Slug + "&token=" + inv.Token
	return &inv, nil
}

func (inv *invite) public() *publicInvite {
	return &publicInvite{ID: inv.ID, Email: inv.Email, Workspace: inv.Workspace, Role: inv.Role,
		Message: inv.Message, Accepted: inv.Accepted, RespondedAt: inv.RespondedAt,
		CreatedAt: inv.CreatedAt, UpdatedAt: inv.UpdatedAt, CreatedBy: inv.CreatedBy}
}

func (a *API) queryInvites(ctx context.Context, where string, args ...any) ([]*invite, error) {
	rows, err := a.db.Query(ctx, inviteSelect+" WHERE "+where+" ORDER BY i.created_at DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*invite{}
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// listInvites ports WorkspaceInvitationsViewset.list.
func (a *API) listInvites(c *httpx.Ctx) error {
	invs, err := a.queryInvites(c.Context(), "w.slug = $1 AND i.deleted_at IS NULL", c.Param("slug"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, invs)
}

var errNoInvite = httpx.Detail(http.StatusNotFound, "No WorkspaceMemberInvite matches the given query.")

// inviteObject is WorkspaceInvitationsViewset.get_object().
func (a *API) inviteObject(c *httpx.Ctx) (*invite, error) {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return nil, err
	}
	inv, err := scanInvite(a.db.QueryRow(c.Context(), inviteSelect+
		" WHERE w.slug = $1 AND i.id = $2 AND i.deleted_at IS NULL", c.Param("slug"), pk))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoInvite
	}
	return inv, err
}

// getInvite ports WorkspaceInvitationsViewset.retrieve.
func (a *API) getInvite(c *httpx.Ctx) error {
	inv, err := a.inviteObject(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, inv)
}

var roleChoices = []string{"20", "15", "5"}

// patchInvite ports WorkspaceInvitationsViewset.partial_update (the stock
// ModelViewSet one).
func (a *API) patchInvite(c *httpx.Ctx) error {
	inv, err := a.inviteObject(c)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	ctx := c.Context()
	v := drf.NewValidator(data, c.Loc())
	var set setList
	if b, ok := v.Bool("accepted"); ok {
		set.add("accepted", b)
	}
	if s, ok := v.Choice("role", roleChoices, drf.ChoiceField{}); ok {
		role, _ := strconv.Atoi(*s)
		set.add("role", role)
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
	if _, err := a.db.Exec(ctx, `UPDATE workspace_member_invites SET `+set.sql()+` WHERE id = $1`,
		append([]any{inv.ID}, set.args...)...); err != nil {
		return err
	}
	updated, err := scanInvite(a.db.QueryRow(ctx, inviteSelect+" WHERE i.id = $1", inv.ID))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, updated)
}

// auditFields validates the writable created_by / updated_by
// PrimaryKeyRelatedFields that "__all__" serializers expose. BaseModel.save()
// overwrites updated_by with the requesting user, so only created_by sticks.
func (a *API) auditFields(ctx context.Context, v *drf.Validator, set *setList) error {
	userExists := func(id uuid.UUID) (bool, error) {
		var ok bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&ok)
		return ok, err
	}
	id, ok, err := v.PK("created_by", true, userExists)
	if err != nil {
		return err
	}
	if ok {
		set.add("created_by_id", id)
	}
	_, _, err = v.PK("updated_by", true, userExists)
	return err
}

// deleteInvite ports WorkspaceInvitationsViewset.destroy.
func (a *API) deleteInvite(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `
		SELECT i.id FROM workspace_member_invites i JOIN workspaces w ON w.id = i.workspace_id
		WHERE i.id = $1 AND w.slug = $2 AND i.deleted_at IS NULL`, pk, c.Param("slug")).Scan(&id); err != nil {
		return err
	}
	if err := softdelete.Row(ctx, a.db, "workspace_member_invites", id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// createInvites ports WorkspaceInvitationsViewset.create.
func (a *API) createInvites(c *httpx.Ctx) error {
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	emailsVal, ok := data.Get("emails")
	if !ok || !drf.PyTruthy(emailsVal) {
		return httpx.Err(http.StatusBadRequest, "Emails are required")
	}
	emails, ok := emailsVal.Elems()
	if !ok {
		return errViewCrash // iterating a str/dict yields strings without .get()
	}
	ctx := c.Context()
	slug := c.Param("slug")
	requesterRole, err := a.workspaceRole(ctx, slug, c.User.ID)
	if err != nil {
		return err
	}

	// int(email.get("role", 5)) for every entry first, then the check.
	roles := make([]*big.Int, len(emails))
	higher := false
	for i, e := range emails {
		if e.Kind() != '{' {
			return errViewCrash
		}
		roles[i] = big.NewInt(5)
		if r, ok := e.Member("role"); ok {
			n, ok := drf.PyInt(r)
			if !ok {
				return errViewCrash
			}
			roles[i] = n
		}
		higher = higher || roles[i].Cmp(big.NewInt(int64(requesterRole))) > 0
	}
	if higher {
		return httpx.Err(http.StatusBadRequest, "You cannot invite a user with higher role")
	}

	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, slug).Scan(&workspaceID); err != nil {
		return err
	}

	addresses := make([]*string, len(emails))
	var lookup []string
	for i, e := range emails {
		v, ok := e.Member("email")
		switch {
		case !ok || v.IsNull():
		case v.IsString():
			s := v.Str()
			addresses[i] = &s
			lookup = append(lookup, s)
		}
	}
	existing, err := a.queryMembers(ctx, false,
		"wm.workspace_id = $1 AND u.email = ANY($2) AND wm.is_active AND wm.deleted_at IS NULL", workspaceID, lookup)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return httpx.Body(http.StatusBadRequest, map[string]any{
			"error":           "Some users are already member of workspace",
			"workspace_users": existing,
		})
	}

	type row struct {
		email, token string
		role         *big.Int
	}
	rowsToInsert := make([]row, 0, len(emails))
	for i, e := range emails {
		if addresses[i] == nil || !auth.ValidEmail(*addresses[i]) {
			if addresses[i] == nil && emailTruthy(e) {
				return errViewCrash // validate_email on a non-string
			}
			return httpx.Err(http.StatusBadRequest,
				"Invalid email - "+drf.PyRepr(e)+" provided a valid email address is required to send the invite")
		}
		rowsToInsert = append(rowsToInsert, row{
			email: strings.ToLower(drf.PyStrip(*addresses[i])),
			token: a.inviteToken(e),
			role:  roles[i],
		})
	}

	type inserted struct{ email, token string }
	var sent []inserted
	for _, r := range rowsToInsert {
		// bulk_create(ignore_conflicts=True): an address already invited
		// is skipped, and its task finds no matching row.
		var id uuid.UUID
		err := a.db.QueryRow(ctx, `
			INSERT INTO workspace_member_invites (email, workspace_id, token, role, created_by_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4::text::numeric, $5, clock_timestamp(), clock_timestamp())
			ON CONFLICT DO NOTHING RETURNING id`, r.email, workspaceID, r.token, r.role.String(), c.User.ID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		sent = append(sent, inserted{r.email, r.token})
	}
	for _, s := range sent {
		if err := jobs.Enqueue(ctx, a.jobs, workspaceInvitationEmail{
			Email: s.email, WorkspaceID: workspaceID, Token: s.token,
			CurrentSite: a.baseHost(true), Inviter: c.User.Email, InviterID: c.User.ID,
		}); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"message": "Emails sent successfully"})
}

// emailTruthy reports whether entry["email"] is a truthy non-string, on
// which validate_email crashes instead of failing validation.
func emailTruthy(entry drf.Value) bool {
	v, ok := entry.Member("email")
	return ok && !v.IsString() && drf.PyTruthy(v)
}

// inviteToken is jwt.encode({"email": <entry>, "timestamp": now}, SECRET_KEY,
// "HS256"): opaque, only compared for equality on join.
func (a *API) inviteToken(entry drf.Value) string {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	ts := strconv.FormatFloat(float64(time.Now().UnixMicro())/1e6, 'f', -1, 64)
	payload := enc.EncodeToString([]byte(`{"email":` + string(entry.Raw()) + `,"timestamp":` + ts + `}`))
	mac := hmac.New(sha256.New, []byte(a.cfg.SecretKey))
	mac.Write([]byte(header + "." + payload))
	return header + "." + payload + "." + enc.EncodeToString(mac.Sum(nil))
}

// workspaceInvitationEmail is bgtasks.workspace_invitation_task.workspace_invitation.
type workspaceInvitationEmail struct {
	Email       string    `json:"email"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	Token       string    `json:"token"`
	CurrentSite string    `json:"current_site"`
	Inviter     string    `json:"inviter"`
	// InviterID is who the task's invite.save() records as updated_by.
	InviterID uuid.UUID `json:"inviter_id"`
}

func (workspaceInvitationEmail) Kind() string                 { return "workspace_invitation" }
func (workspaceInvitationEmail) InsertOpts() river.InsertOpts { return emailInsertOpts }

func (a *API) registerWorkspaceJobs() {
	jobs.Register(a.jobs, func(ctx context.Context, j workspaceInvitationEmail) error {
		var (
			firstName, displayName, inviterEmail string
			workspaceName, slug                  string
			inviteID                             uuid.UUID
		)
		err := a.db.QueryRow(ctx, `SELECT first_name, display_name, coalesce(email, '') FROM users WHERE email = $1`, j.Inviter).
			Scan(&firstName, &displayName, &inviterEmail)
		if err != nil {
			return err
		}
		err = a.db.QueryRow(ctx, `SELECT name, slug FROM workspaces WHERE id = $1 AND deleted_at IS NULL`, j.WorkspaceID).
			Scan(&workspaceName, &slug)
		if err == nil {
			err = a.db.QueryRow(ctx, `SELECT id FROM workspace_member_invites WHERE token = $1 AND email = $2 AND deleted_at IS NULL`,
				j.Token, j.Email).Scan(&inviteID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the workspace or invite is gone
		}
		if err != nil {
			return err
		}
		name := firstName
		if name == "" {
			name = displayName
		}
		if name == "" {
			name = inviterEmail
		}
		absURL := j.CurrentSite + "/workspace-invitations/?invitation_id=" + inviteID.String() + "&slug=" + slug + "&token=" + j.Token
		html, err := mail.Render("invitations/workspace_invitation.html", map[string]string{
			"email": j.Email, "first_name": name, "workspace_name": workspaceName, "abs_url": absURL,
		})
		if err != nil {
			return err
		}
		text := mail.PlainText(html)
		if _, err := a.db.Exec(ctx, `
			UPDATE workspace_member_invites SET message = $2, updated_at = now(), updated_by_id = $3 WHERE id = $1`,
			inviteID, text, j.InviterID); err != nil {
			return err
		}
		return a.mailer.Send(ctx, []string{j.Email},
			name+" has invited you to join them in "+workspaceName+" on Plane", text, html)
	})
}

// myInvites ports UserWorkspaceInvitationsViewSet.list.
func (a *API) myInvites(c *httpx.Ctx) error {
	invs, err := a.queryInvites(c.Context(), "i.email = $1 AND i.deleted_at IS NULL", c.User.Email)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, invs)
}

// errInvalidDetail is a Django ValidationError reaching BaseAPIView.
var errInvalidDetail = httpx.Err(http.StatusBadRequest, "Please provide valid detail")

// acceptMyInvites ports UserWorkspaceInvitationsViewSet.create: join every
// listed invitation addressed to the user.
func (a *API) acceptMyInvites(c *httpx.Ctx) error {
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	var ids []uuid.UUID
	if v, ok := data.Get("invitations"); ok {
		var elems []drf.Value
		switch v.Kind() {
		case '[':
			elems, _ = v.Elems()
		case '"': // pk__in iterates the characters
			for _, r := range v.Str() {
				raw, _ := jsontext.AppendQuote(nil, string(r))
				elems = append(elems, drf.JSONValue(raw))
			}
		default:
			return errViewCrash
		}
		for _, e := range elems {
			switch e.Kind() {
			case 'n':
				continue // the In lookup drops None
			case '[', '{':
				return errViewCrash // unhashable
			}
			id, ok := drf.UUIDValue(e)
			if !ok {
				return errInvalidDetail
			}
			ids = append(ids, id)
		}
	}
	ctx := c.Context()
	type pending struct {
		id, workspace uuid.UUID
		role          int
	}
	rows, err := a.db.Query(ctx, `
		SELECT id, workspace_id, role FROM workspace_member_invites
		WHERE id = ANY($1) AND email = $2 AND deleted_at IS NULL ORDER BY created_at DESC`, ids, c.User.Email)
	if err != nil {
		return err
	}
	invs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pending, error) {
		var p pending
		return p, r.Scan(&p.id, &p.workspace, &p.role)
	})
	if err != nil {
		return err
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, inv := range invs {
		if _, err := tx.Exec(ctx, `
			UPDATE workspace_members SET is_active = true, role = $3
			WHERE workspace_id = $1 AND member_id = $2 AND deleted_at IS NULL`, inv.workspace, c.User.ID, inv.role); err != nil {
			return err
		}
	}
	for _, inv := range invs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO workspace_members (workspace_id, member_id, role, created_by_id, created_at, updated_at)
			VALUES ($1, $2, $3, $2, clock_timestamp(), clock_timestamp()) ON CONFLICT DO NOTHING`,
			inv.workspace, c.User.ID, inv.role); err != nil {
			return err
		}
	}
	pks := make([]uuid.UUID, len(invs))
	for i, inv := range invs {
		pks[i] = inv.id
	}
	if _, err := tx.Exec(ctx, `UPDATE workspace_member_invites SET deleted_at = now() WHERE id = ANY($1)`, pks); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return c.NoContent()
}

// joinInviteInfo ports WorkspaceJoinEndpoint.get.
func (a *API) joinInviteInfo(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	inv, err := scanInvite(a.db.QueryRow(c.Context(), inviteSelect+
		" WHERE w.slug = $1 AND i.id = $2 AND i.deleted_at IS NULL", c.Param("slug"), pk))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, inv.public())
}

// joinInvite ports WorkspaceJoinEndpoint.post: answer an invitation from
// its email link.
func (a *API) joinInvite(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	inv, err := scanInvite(a.db.QueryRow(ctx, inviteSelect+
		" WHERE w.slug = $1 AND i.id = $2 AND i.deleted_at IS NULL", c.Param("slug"), pk))
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
	token, _ := data.Get("token")
	if !token.IsString() || token.Str() == "" || token.Str() != inv.Token {
		return httpx.Err(http.StatusForbidden, "You do not have permission to join the workspace")
	}
	if c.User == nil {
		return httpx.Err(http.StatusUnauthorized, "Authentication required to accept workspace invitation")
	}
	if strings.ToLower(c.User.Email) != strings.ToLower(inv.Email) {
		return httpx.Err(http.StatusForbidden, "You do not have permission to accept this invitation")
	}
	if inv.RespondedAt != nil {
		return httpx.Err(http.StatusBadRequest, "You have already responded to the invitation request")
	}

	// invite.accepted = request.data.get("accepted", False) is stored via
	// BooleanField.to_python, but the branch below tests the raw value.
	acceptedRaw, ok := data.Get("accepted")
	if !ok {
		acceptedRaw = drf.JSONValue(jsontext.Value("false"))
	}
	accepted, err := djangoBool(acceptedRaw)
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `
		UPDATE workspace_member_invites SET accepted = $2, responded_at = now(), updated_at = now(), updated_by_id = $3
		WHERE id = $1`, inv.ID, accepted, c.User.ID); err != nil {
		return err
	}
	if !drf.PyTruthy(acceptedRaw) {
		return c.JSON(http.StatusOK, map[string]any{"message": "Workspace Invitation was not accepted"})
	}

	var userID uuid.UUID
	err = a.db.QueryRow(ctx, `SELECT id FROM users WHERE email = $1 LIMIT 1`, inv.Email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusOK, map[string]any{"message": "Workspace Invitation Accepted"})
	}
	if err != nil {
		return err
	}
	tag, err := a.db.Exec(ctx, `
		UPDATE workspace_members SET is_active = true, role = $3, updated_at = now(), updated_by_id = $4
		WHERE id = (SELECT id FROM workspace_members WHERE workspace_id = $1 AND member_id = $2 AND deleted_at IS NULL
			ORDER BY created_at DESC LIMIT 1)`, inv.Workspace.ID, userID, inv.Role, c.User.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := a.db.Exec(ctx, `
			INSERT INTO workspace_members (workspace_id, member_id, role, created_by_id) VALUES ($1, $2, $3, $4)`,
			inv.Workspace.ID, userID, inv.Role, c.User.ID); err != nil {
			return err
		}
	}
	// user.last_workspace_id is not a User field: save() only bumps the row.
	if _, err := a.db.Exec(ctx, `UPDATE users SET `+userSaveSQL+` WHERE id = $1`, userID); err != nil {
		return err
	}
	if err := softdelete.Row(ctx, a.db, "workspace_member_invites", inv.ID, c.User.ID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"message": "Workspace Invitation Accepted"})
}
