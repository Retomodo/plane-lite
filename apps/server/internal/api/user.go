package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"plane-lite/server/internal/httpx"
)

// userMe is UserMeSerializer.
type userMe struct {
	ID                uuid.UUID  `json:"id"`
	Avatar            string     `json:"avatar"`
	CoverImage        *string    `json:"cover_image"`
	AvatarURL         *string    `json:"avatar_url"`
	CoverImageURL     *string    `json:"cover_image_url"`
	DateJoined        time.Time  `json:"date_joined"`
	DisplayName       string     `json:"display_name"`
	Email             *string    `json:"email"`
	FirstName         string     `json:"first_name"`
	LastName          string     `json:"last_name"`
	IsActive          bool       `json:"is_active"`
	IsBot             bool       `json:"is_bot"`
	IsEmailVerified   bool       `json:"is_email_verified"`
	UserTimezone      string     `json:"user_timezone"`
	Username          string     `json:"username"`
	IsPasswordAutoset bool       `json:"is_password_autoset"`
	LastLoginMedium   string     `json:"last_login_medium"`
	LastLoginTime     *time.Time `json:"last_login_time"`
}

// getMe ports app.views.user.base.UserEndpoint.retrieve.
func (a *API) getMe(c *httpx.Ctx) error {
	u, err := a.loadUserMe(c.Context(), c.User.ID)
	if err != nil {
		return err
	}
	cachePrivate(c)
	return c.JSON(http.StatusOK, u)
}

// cachePrivate is @cache_control(private=True, max_age=12) + @vary_on_cookie.
func cachePrivate(c *httpx.Ctx) {
	c.W.Header().Set("Cache-Control", "max-age=12, private")
	c.W.Header().Add("Vary", "Cookie")
}

func (a *API) loadUserMe(ctx context.Context, id uuid.UUID) (*userMe, error) {
	var (
		u               userMe
		avatarAssetID   *uuid.UUID
		avatarAssetType *string
		coverAssetID    *uuid.UUID
		coverAssetType  *string
	)
	err := a.db.QueryRow(ctx, `
		SELECT u.id, u.avatar, u.cover_image, u.date_joined, u.display_name, u.email, u.first_name,
			u.last_name, u.is_active, u.is_bot, u.is_email_verified, u.user_timezone, u.username,
			u.is_password_autoset, u.last_login_medium, u.last_login_time,
			av.id, av.entity_type, cv.id, cv.entity_type
		FROM users u
		LEFT JOIN file_assets av ON av.id = u.avatar_asset_id
		LEFT JOIN file_assets cv ON cv.id = u.cover_image_asset_id
		WHERE u.id = $1`, id,
	).Scan(&u.ID, &u.Avatar, &u.CoverImage, &u.DateJoined, &u.DisplayName, &u.Email, &u.FirstName,
		&u.LastName, &u.IsActive, &u.IsBot, &u.IsEmailVerified, &u.UserTimezone, &u.Username,
		&u.IsPasswordAutoset, &u.LastLoginMedium, &u.LastLoginTime,
		&avatarAssetID, &avatarAssetType, &coverAssetID, &coverAssetType)
	if err != nil {
		return nil, err
	}
	u.AvatarURL = imageURL(avatarAssetID, avatarAssetType, &u.Avatar)
	u.CoverImageURL = imageURL(coverAssetID, coverAssetType, u.CoverImage)
	return &u, nil
}

// imageURL is the avatar_url / cover_image_url / logo_url property pattern:
// the uploaded asset's URL if there is one, else the legacy URL field, else nil.
func imageURL(assetID *uuid.UUID, entityType *string, legacy *string) *string {
	if assetID != nil {
		if u := staticAssetURL(*assetID, entityType); u != nil {
			return u
		}
	}
	if legacy != nil && *legacy != "" {
		return legacy
	}
	return nil
}

// staticAssetURL is FileAsset.asset_url for the entity types served from
// /api/assets/v2/static/ (logos, avatars, covers).
func staticAssetURL(id uuid.UUID, entityType *string) *string {
	if entityType == nil {
		return nil
	}
	switch *entityType {
	case "WORKSPACE_LOGO", "USER_AVATAR", "USER_COVER", "PROJECT_COVER":
		s := "/api/assets/v2/static/" + id.String() + "/"
		return &s
	}
	return nil
}

// userFull is UserSerializer: every User column except password.
type userFull struct {
	ID                      uuid.UUID  `json:"id"`
	LastLogin               *time.Time `json:"last_login"`
	IsSuperuser             bool       `json:"is_superuser"`
	Username                string     `json:"username"`
	MobileNumber            *string    `json:"mobile_number"`
	Email                   *string    `json:"email"`
	DisplayName             string     `json:"display_name"`
	FirstName               string     `json:"first_name"`
	LastName                string     `json:"last_name"`
	Avatar                  string     `json:"avatar"`
	AvatarAsset             *uuid.UUID `json:"avatar_asset"`
	CoverImage              *string    `json:"cover_image"`
	CoverImageAsset         *uuid.UUID `json:"cover_image_asset"`
	DateJoined              time.Time  `json:"date_joined"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	LastLocation            string     `json:"last_location"`
	CreatedLocation         string     `json:"created_location"`
	IsManaged               bool       `json:"is_managed"`
	IsPasswordExpired       bool       `json:"is_password_expired"`
	IsActive                bool       `json:"is_active"`
	IsStaff                 bool       `json:"is_staff"`
	IsEmailVerified         bool       `json:"is_email_verified"`
	IsPasswordAutoset       bool       `json:"is_password_autoset"`
	IsPasswordResetRequired bool       `json:"is_password_reset_required"`
	Token                   string     `json:"token"`
	LastActive              *time.Time `json:"last_active"`
	LastLoginTime           *time.Time `json:"last_login_time"`
	LastLogoutTime          *time.Time `json:"last_logout_time"`
	LastLoginIP             string     `json:"last_login_ip"`
	LastLogoutIP            string     `json:"last_logout_ip"`
	LastLoginMedium         string     `json:"last_login_medium"`
	LastLoginUagent         string     `json:"last_login_uagent"`
	TokenUpdatedAt          *time.Time `json:"token_updated_at"`
	IsBot                   bool       `json:"is_bot"`
	BotType                 *string    `json:"bot_type"`
	UserTimezone            string     `json:"user_timezone"`
	IsEmailValid            bool       `json:"is_email_valid"`
	MaskedAt                *time.Time `json:"masked_at"`
}

func (a *API) loadUserFull(ctx context.Context, id uuid.UUID) (*userFull, error) {
	var u userFull
	err := a.db.QueryRow(ctx, `
		SELECT id, last_login, is_superuser, username, mobile_number, email, display_name, first_name,
			last_name, avatar, avatar_asset_id, cover_image, cover_image_asset_id, date_joined, created_at,
			updated_at, last_location, created_location, is_managed, is_password_expired, is_active,
			is_staff, is_email_verified, is_password_autoset, is_password_reset_required, token,
			last_active, last_login_time, last_logout_time, last_login_ip, last_logout_ip,
			last_login_medium, last_login_uagent, token_updated_at, is_bot, bot_type, user_timezone,
			is_email_valid, masked_at
		FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.LastLogin, &u.IsSuperuser, &u.Username, &u.MobileNumber, &u.Email, &u.DisplayName,
		&u.FirstName, &u.LastName, &u.Avatar, &u.AvatarAsset, &u.CoverImage, &u.CoverImageAsset,
		&u.DateJoined, &u.CreatedAt, &u.UpdatedAt, &u.LastLocation, &u.CreatedLocation, &u.IsManaged,
		&u.IsPasswordExpired, &u.IsActive, &u.IsStaff, &u.IsEmailVerified, &u.IsPasswordAutoset,
		&u.IsPasswordResetRequired, &u.Token, &u.LastActive, &u.LastLoginTime, &u.LastLogoutTime,
		&u.LastLoginIP, &u.LastLogoutIP, &u.LastLoginMedium, &u.LastLoginUagent, &u.TokenUpdatedAt,
		&u.IsBot, &u.BotType, &u.UserTimezone, &u.IsEmailValid, &u.MaskedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
