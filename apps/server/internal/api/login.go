package api

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/httpx"
)

// credentials describes how the user proved their identity.
type credentials struct {
	password string // the password, or "" for magic-code logins
	autoset  bool   // magic code: account gets an unusable random password
	medium   string // last_login_medium: "email" or "magic-code"
}

type loginUser struct {
	id           uuid.UUID
	passwordHash string
}

// completeLoginOrSignup ports authentication.adapter.base.Adapter
// .complete_login_or_signup: create the account on first login, then record
// login metadata and accept pending invitations. It returns *authError for
// failures that redirect back to the sign-in page.
func (a *API) completeLoginOrSignup(ctx context.Context, c *httpx.Ctx, email string, cred credentials) (*loginUser, error) {
	email = normalizeEmail(email)
	if !auth.ValidEmail(email) {
		return nil, authErr("INVALID_EMAIL", kv{"email", email})
	}
	var (
		u           loginUser
		active      bool
		deactivated bool
		isBot       bool
	)
	err := a.db.QueryRow(ctx, `
		SELECT id, password, is_active, last_logout_time IS NOT NULL, is_bot FROM users WHERE email = $1`, email,
	).Scan(&u.id, &u.passwordHash, &active, &deactivated, &isBot)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if exists && !active && deactivated {
		return nil, authErr("USER_ACCOUNT_DEACTIVATED", kv{"email", email})
	}
	if exists && isBot {
		return nil, authErr("BOT_USER_LOGIN_FORBIDDEN", kv{"email", email})
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if !exists {
		if err := a.checkSignup(ctx, email); err != nil {
			return nil, err
		}
		emailVerified := false
		if cred.autoset {
			u.passwordHash = auth.HashPassword(strings.ReplaceAll(uuid.NewString(), "-", ""))
			emailVerified = true
		} else {
			if !auth.PasswordStrong(cred.password) {
				return nil, authErr("PASSWORD_TOO_WEAK", kv{"email", email})
			}
			u.passwordHash = auth.HashPassword(cred.password)
		}
		// User.save() derives display_name from the email's local part.
		displayName := strings.SplitN(email, "@", 2)[0]
		err := tx.QueryRow(ctx, `
			INSERT INTO users (password, username, email, display_name, is_password_autoset, is_email_verified)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			u.passwordHash, strings.ReplaceAll(uuid.NewString(), "-", ""), email, displayName, cred.autoset, emailVerified,
		).Scan(&u.id)
		if err != nil {
			return nil, err
		}
		// post_save signal: create_user_notification.
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_notification_preferences (user_id, property_change, state_change, comment, mention, issue_completed)
			VALUES ($1, true, true, true, true, true)`, u.id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO profiles (user_id, background_color) VALUES ($1, $2)", u.id, randomColor()); err != nil {
			return nil, err
		}
	} else if cred.password != "" && auth.NeedsRehash(u.passwordHash) {
		// Django's check_password() upgrades hashes made with fewer iterations.
		u.passwordHash = auth.HashPassword(cred.password)
		if _, err := tx.Exec(ctx, "UPDATE users SET password = $2 WHERE id = $1", u.id, u.passwordHash); err != nil {
			return nil, err
		}
	}

	// save_user_data(); User.save() rotates the API token alongside.
	if _, err := tx.Exec(ctx, `
		UPDATE users SET last_login_medium = $2, last_active = now(), last_login_time = now(),
			last_login_ip = $3, last_login_uagent = $4, token = $5, token_updated_at = now(),
			is_active = true, updated_at = now()
		WHERE id = $1`,
		u.id, cred.medium, auth.ClientIP(c.R), c.R.UserAgent(), newUserToken()); err != nil {
		return nil, err
	}
	if err := acceptInvitations(ctx, tx, u.id, email); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	// TODO(email): send user_activation_email when an inactive account is
	// activated here; lands with the email/jobs batch.
	return &u, nil
}

// checkSignup is Adapter.__check_signup.
func (a *API) checkSignup(ctx context.Context, email string) error {
	if a.cfg.EnableSignup {
		return nil
	}
	var invited bool
	if err := a.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM workspace_member_invites WHERE email = $1 AND deleted_at IS NULL)`, email,
	).Scan(&invited); err != nil {
		return err
	}
	if !invited {
		return authErr("SIGNUP_DISABLED", kv{"email", email})
	}
	return nil
}

// acceptInvitations ports process_workspace_project_invitations: accepted
// invites become memberships, then the invites are soft-deleted.
func acceptInvitations(ctx context.Context, tx pgx.Tx, userID uuid.UUID, email string) error {
	inserts := []string{
		`INSERT INTO workspace_members (workspace_id, member_id, role)
		 SELECT workspace_id, $1, role FROM workspace_member_invites
		 WHERE email = $2 AND accepted AND deleted_at IS NULL
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO workspace_members (workspace_id, member_id, role, created_by_id)
		 SELECT workspace_id, $1, CASE WHEN role IN (5, 15) THEN role ELSE 15 END, created_by_id
		 FROM project_member_invites
		 WHERE email = $2 AND accepted AND deleted_at IS NULL
		 ON CONFLICT DO NOTHING`,
		// Django omits project_id here, which violates NOT NULL; see DEVIATIONS.md.
		`INSERT INTO project_members (workspace_id, project_id, member_id, role, created_by_id)
		 SELECT workspace_id, project_id, $1, CASE WHEN role IN (5, 15) THEN role ELSE 15 END, created_by_id
		 FROM project_member_invites
		 WHERE email = $2 AND accepted AND deleted_at IS NULL
		 ON CONFLICT DO NOTHING`,
	}
	for _, s := range inserts {
		if _, err := tx.Exec(ctx, s, userID, email); err != nil {
			return err
		}
	}
	for _, table := range []string{"workspace_member_invites", "project_member_invites"} {
		if _, err := tx.Exec(ctx, "UPDATE "+table+" SET deleted_at = now() WHERE email = $1 AND accepted AND deleted_at IS NULL", email); err != nil {
			return err
		}
	}
	return nil
}
