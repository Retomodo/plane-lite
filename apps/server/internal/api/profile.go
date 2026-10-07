package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// profile is ProfileSerializer (fields = "__all__").
type profile struct {
	ID                        uuid.UUID      `json:"id"`
	CreatedAt                 time.Time      `json:"created_at"`
	UpdatedAt                 time.Time      `json:"updated_at"`
	Theme                     jsontext.Value `json:"theme"`
	IsAppRailDocked           bool           `json:"is_app_rail_docked"`
	IsTourCompleted           bool           `json:"is_tour_completed"`
	OnboardingStep            jsontext.Value `json:"onboarding_step"`
	UseCase                   *string        `json:"use_case"`
	Role                      *string        `json:"role"`
	IsOnboarded               bool           `json:"is_onboarded"`
	LastWorkspaceID           *uuid.UUID     `json:"last_workspace_id"`
	BillingAddressCountry     string         `json:"billing_address_country"`
	BillingAddress            jsontext.Value `json:"billing_address"`
	HasBillingAddress         bool           `json:"has_billing_address"`
	CompanyName               string         `json:"company_name"`
	NotificationViewMode      string         `json:"notification_view_mode"`
	IsSmoothCursorEnabled     bool           `json:"is_smooth_cursor_enabled"`
	IsMobileOnboarded         bool           `json:"is_mobile_onboarded"`
	MobileOnboardingStep      jsontext.Value `json:"mobile_onboarding_step"`
	MobileTimezoneAutoSet     bool           `json:"mobile_timezone_auto_set"`
	Language                  string         `json:"language"`
	StartOfTheWeek            int            `json:"start_of_the_week"`
	Goals                     jsontext.Value `json:"goals"`
	BackgroundColor           string         `json:"background_color"`
	IsNavigationTourCompleted bool           `json:"is_navigation_tour_completed"`
	HasMarketingEmailConsent  bool           `json:"has_marketing_email_consent"`
	IsSubscribedToChangelog   bool           `json:"is_subscribed_to_changelog"`
	ProductTour               jsontext.Value `json:"product_tour"`
	User                      uuid.UUID      `json:"user"`
}

func (a *API) loadProfile(ctx context.Context, userID uuid.UUID) (*profile, error) {
	var (
		p                                                         profile
		theme, onboarding, billing, mobileOnboarding, goals, tour *string
	)
	err := a.db.QueryRow(ctx, `
		SELECT id, created_at, updated_at, theme::text, is_app_rail_docked, is_tour_completed,
			onboarding_step::text, use_case, role, is_onboarded, last_workspace_id, billing_address_country,
			billing_address::text, has_billing_address, company_name, notification_view_mode,
			is_smooth_cursor_enabled, is_mobile_onboarded, mobile_onboarding_step::text,
			mobile_timezone_auto_set, language, start_of_the_week, goals::text, background_color,
			is_navigation_tour_completed, has_marketing_email_consent, is_subscribed_to_changelog,
			product_tour::text, user_id
		FROM profiles WHERE user_id = $1`, userID,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &theme, &p.IsAppRailDocked, &p.IsTourCompleted,
		&onboarding, &p.UseCase, &p.Role, &p.IsOnboarded, &p.LastWorkspaceID, &p.BillingAddressCountry,
		&billing, &p.HasBillingAddress, &p.CompanyName, &p.NotificationViewMode,
		&p.IsSmoothCursorEnabled, &p.IsMobileOnboarded, &mobileOnboarding,
		&p.MobileTimezoneAutoSet, &p.Language, &p.StartOfTheWeek, &goals, &p.BackgroundColor,
		&p.IsNavigationTourCompleted, &p.HasMarketingEmailConsent, &p.IsSubscribedToChangelog,
		&tour, &p.User)
	if err != nil {
		return nil, err
	}
	p.Theme, p.OnboardingStep, p.BillingAddress = jsonValue(theme), jsonValue(onboarding), jsonValue(billing)
	p.MobileOnboardingStep, p.Goals, p.ProductTour = jsonValue(mobileOnboarding), jsonValue(goals), jsonValue(tour)
	return &p, nil
}

// jsonValue renders a jsonb column read as text; SQL NULL is JSON null.
func jsonValue(s *string) jsontext.Value {
	if s == nil {
		return jsontext.Value("null")
	}
	return jsontext.Value(*s)
}

// getProfile ports app.views.user.base.ProfileEndpoint.get.
func (a *API) getProfile(c *httpx.Ctx) error {
	p, err := a.loadProfile(c.Context(), c.User.ID)
	if err != nil {
		return err
	}
	cachePrivate(c)
	return c.JSON(http.StatusOK, p)
}

var (
	notificationViewModes = []string{"full", "compact"}
	weekdays              = []string{"0", "1", "2", "3", "4", "5", "6"}
)

// patchProfile ports ProfileEndpoint.patch (ProfileSerializer, partial).
func (a *API) patchProfile(c *httpx.Ctx) error {
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	var set setList
	jsonField := func(col string, allowNull bool) {
		if raw, ok := v.JSON(col, allowNull); ok {
			if raw.Kind() == 'n' {
				set.add(col, nil) // JSONField(null=True) stores None as SQL NULL
			} else {
				set.addCast(col, string(raw), "::jsonb")
			}
		}
	}
	boolField := func(col string) {
		if b, ok := v.Bool(col); ok {
			set.add(col, b)
		}
	}
	charField := func(col string, f drf.CharField) {
		if s, ok := v.Char(col, f); ok {
			set.add(col, s)
		}
	}
	jsonField("theme", false)
	boolField("is_app_rail_docked")
	boolField("is_tour_completed")
	jsonField("onboarding_step", false)
	charField("use_case", drf.CharField{AllowBlank: true, AllowNull: true})
	charField("role", drf.CharField{MaxLength: 300, AllowBlank: true, AllowNull: true})
	boolField("is_onboarded")
	if id, ok := v.UUID("last_workspace_id", true); ok {
		set.add("last_workspace_id", id)
	}
	charField("billing_address_country", drf.CharField{MaxLength: 255})
	jsonField("billing_address", true)
	boolField("has_billing_address")
	charField("company_name", drf.CharField{MaxLength: 255, AllowBlank: true})
	if s, ok := v.Choice("notification_view_mode", notificationViewModes, drf.ChoiceField{}); ok {
		set.add("notification_view_mode", *s)
	}
	boolField("is_smooth_cursor_enabled")
	boolField("is_mobile_onboarded")
	jsonField("mobile_onboarding_step", false)
	boolField("mobile_timezone_auto_set")
	charField("language", drf.CharField{MaxLength: 255})
	if s, ok := v.Choice("start_of_the_week", weekdays, drf.ChoiceField{}); ok {
		day, _ := strconv.Atoi(*s)
		set.add("start_of_the_week", day)
	}
	jsonField("goals", false)
	charField("background_color", drf.CharField{MaxLength: 255})
	boolField("is_navigation_tour_completed")
	boolField("has_marketing_email_consent")
	boolField("is_subscribed_to_changelog")
	jsonField("product_tour", false)
	if err := v.Err(); err != nil {
		return err
	}
	set.add("updated_at", time.Now())
	ctx := c.Context()
	if _, err := a.db.Exec(ctx, `UPDATE profiles SET `+set.sql()+` WHERE user_id = $1`,
		append([]any{c.User.ID}, set.args...)...); err != nil {
		return err
	}
	p, err := a.loadProfile(ctx, c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}
