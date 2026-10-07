// Package api implements Plane's HTTP endpoints. Each handler documents the
// Django view it ports; behaviour (status codes, bodies, side effects) must
// match the contract goldens recorded from that view.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/config"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/mail"
	"plane-lite/server/internal/throttle"
)

type API struct {
	cfg      *config.Config
	db       *pgxpool.Pool
	rdb      *redis.Client
	log      *slog.Logger
	sessions *auth.Sessions
	csrf     *auth.CSRF
	limiter  *throttle.Limiter
	jobs     *jobs.Runner
	mailer   *mail.Mailer

	anonRate throttle.Rate // DRF DEFAULT_THROTTLE_RATES["anon"]
	authRate throttle.Rate // AuthenticationThrottle (AUTHENTICATION_RATE_LIMIT)
}

type Deps struct {
	Config   *config.Config
	DB       *pgxpool.Pool
	Redis    *redis.Client
	Log      *slog.Logger
	Sessions *auth.Sessions
	Jobs     *jobs.Runner
	Mailer   *mail.Mailer
}

func New(d Deps) *API {
	authRate, err := throttle.ParseRate(d.Config.AuthenticationRateLimit)
	if err != nil {
		authRate = throttle.MustRate("10/minute")
	}
	a := &API{
		cfg:      d.Config,
		db:       d.DB,
		rdb:      d.Redis,
		log:      d.Log,
		sessions: d.Sessions,
		csrf:     auth.NewCSRF(d.Config),
		limiter:  throttle.New(d.Redis, d.Config.RedisKeyPrefix),
		anonRate: throttle.MustRate("30/minute"),
		authRate: authRate,
		jobs:     d.Jobs,
		mailer:   d.Mailer,
	}
	a.registerEmailJobs()
	a.registerPasswordJobs()
	a.registerUserJobs()
	return a
}

// Register mounts every ported endpoint.
func (a *API) Register(rt *httpx.Router) {
	// plane/license/urls.py
	rt.HandlePublic("/api/instances/", httpx.Methods{"GET": a.anon(a.getInstance)})

	// plane/authentication/urls.py
	rt.HandlePublic("/auth/get-csrf-token/", httpx.Methods{"GET": a.anon(a.getCSRFToken)})
	rt.HandlePublic("/auth/email-check/", httpx.Methods{"POST": a.emailCheck})
	rt.HandlePublic("/auth/sign-up/", httpx.Methods{"POST": a.signUp})
	rt.HandlePublic("/auth/sign-in/", httpx.Methods{"POST": a.signIn})
	rt.HandlePublic("/auth/sign-out/", httpx.Methods{"POST": a.signOut})
	rt.HandlePublic("/auth/magic-generate/", httpx.Methods{"POST": a.magicGenerate})
	rt.HandlePublic("/auth/magic-sign-in/", httpx.Methods{"POST": a.magicSignIn})
	rt.HandlePublic("/auth/magic-sign-up/", httpx.Methods{"POST": a.magicSignUp})
	rt.HandlePublic("/auth/forgot-password/", httpx.Methods{"POST": a.forgotPassword})
	rt.HandlePublic("/auth/reset-password/{uidb64}/{token}/", httpx.Methods{"POST": a.resetPassword})
	rt.Handle("/auth/change-password/", httpx.Methods{"POST": a.changePassword})
	rt.Handle("/auth/set-password/", httpx.Methods{"POST": a.setPassword})

	// plane/app/urls/user.py
	rt.Handle("/api/users/me/", httpx.Methods{"GET": a.getMe, "PATCH": a.patchMe, "DELETE": a.deactivateMe})
	rt.HandlePublic("/api/users/session/", httpx.Methods{"GET": a.anon(a.userSession)})
	rt.Handle("/api/users/me/settings/", httpx.Methods{"GET": a.userSettings})
	rt.Handle("/api/users/me/email/generate-code/", httpx.Methods{"POST": a.emailGenerateCode})
	rt.Handle("/api/users/me/email/", httpx.Methods{"PATCH": a.updateEmail})
	rt.Handle("/api/users/me/profile/", httpx.Methods{"GET": a.getProfile, "PATCH": a.patchProfile})
	rt.Handle("/api/users/me/accounts/", httpx.Methods{"GET": a.listAccounts, "DELETE": a.accountsNoPK})
	rt.Handle("/api/users/me/accounts/{pk}/", httpx.Methods{"GET": a.account, "DELETE": a.account})
	rt.Handle("/api/users/me/instance-admin/", httpx.Methods{"GET": a.instanceAdmin})
	rt.Handle("/api/users/me/onboard/", httpx.Methods{"PATCH": a.setProfileFlag("is_onboarded")})
	rt.Handle("/api/users/me/tour-completed/", httpx.Methods{"PATCH": a.setProfileFlag("is_tour_completed")})
}

// anon applies DRF's default AnonRateThrottle to an AllowAny view.
func (a *API) anon(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		if c.User == nil {
			if err := a.throttle(c, "anon", a.anonRate); err != nil {
				return err
			}
		}
		return h(c)
	}
}

// throttle returns Plane's 429 body when the client is over rate.
func (a *API) throttle(c *httpx.Ctx, scope string, rate throttle.Rate) error {
	ok, wait, err := a.limiter.Allow(c.Context(), scope, throttle.Ident(c.R), rate)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return a.rateLimited(c, wait)
}

// rateLimited is Plane's throttle failure response.
func (a *API) rateLimited(c *httpx.Ctx, wait time.Duration) error {
	if wait > 0 {
		c.W.Header().Set("Retry-After", throttle.RetryAfter(wait))
	}
	return httpx.Body(http.StatusTooManyRequests, authErr("RATE_LIMIT_EXCEEDED").body())
}
