package api

import (
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
	var (
		u               userMe
		avatarAssetID   *uuid.UUID
		avatarAssetType *string
		coverAssetID    *uuid.UUID
		coverAssetType  *string
	)
	err := a.db.QueryRow(c.Context(), `
		SELECT u.id, u.avatar, u.cover_image, u.date_joined, u.display_name, u.email, u.first_name,
			u.last_name, u.is_active, u.is_bot, u.is_email_verified, u.user_timezone, u.username,
			u.is_password_autoset, u.last_login_medium, u.last_login_time,
			av.id, av.entity_type, cv.id, cv.entity_type
		FROM users u
		LEFT JOIN file_assets av ON av.id = u.avatar_asset_id
		LEFT JOIN file_assets cv ON cv.id = u.cover_image_asset_id
		WHERE u.id = $1`, c.User.ID,
	).Scan(&u.ID, &u.Avatar, &u.CoverImage, &u.DateJoined, &u.DisplayName, &u.Email, &u.FirstName,
		&u.LastName, &u.IsActive, &u.IsBot, &u.IsEmailVerified, &u.UserTimezone, &u.Username,
		&u.IsPasswordAutoset, &u.LastLoginMedium, &u.LastLoginTime,
		&avatarAssetID, &avatarAssetType, &coverAssetID, &coverAssetType)
	if err != nil {
		return err
	}
	u.AvatarURL = imageURL(avatarAssetID, avatarAssetType, &u.Avatar)
	u.CoverImageURL = imageURL(coverAssetID, coverAssetType, u.CoverImage)
	c.W.Header().Set("Cache-Control", "max-age=12, private")
	return c.JSON(http.StatusOK, u)
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
