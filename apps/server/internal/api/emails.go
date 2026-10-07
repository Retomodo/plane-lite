package api

import (
	"context"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"plane-lite/server/internal/jobs"
)

// Email jobs port plane/bgtasks/*_task.py. A Celery task gave up silently on
// failure; these get a few retries instead.
var emailInsertOpts = river.InsertOpts{MaxAttempts: 3}

// magicLinkEmail is bgtasks.magic_link_code_task.magic_link.
type magicLinkEmail struct {
	Email string `json:"email"`
	Key   string `json:"key"`
	Token string `json:"token"`
}

func (magicLinkEmail) Kind() string                 { return "magic_link" }
func (magicLinkEmail) InsertOpts() river.InsertOpts { return emailInsertOpts }

// userActivationEmail is bgtasks.user_activation_email_task.user_activation_email.
type userActivationEmail struct {
	CurrentSite string    `json:"current_site"`
	UserID      uuid.UUID `json:"user_id"`
}

func (userActivationEmail) Kind() string                 { return "user_activation_email" }
func (userActivationEmail) InsertOpts() river.InsertOpts { return emailInsertOpts }

func (a *API) registerEmailJobs() {
	jobs.Register(a.jobs, func(ctx context.Context, j magicLinkEmail) error {
		return a.mailer.SendTemplate(ctx, j.Email, "Your unique Plane login code is "+j.Token,
			"auth/magic_signin.html", map[string]string{"code": j.Token, "email": j.Email})
	})
	jobs.Register(a.jobs, func(ctx context.Context, j userActivationEmail) error {
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
		return a.mailer.SendTemplate(ctx, email, name+" has been activated on Plane", "user/user_activation.html",
			map[string]string{"email": email, "profile_url": j.CurrentSite + "/profile"})
	})
}
