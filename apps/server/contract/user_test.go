package contract

import (
	"net/url"
	"strings"
	"testing"
)

func TestUserProfile(t *testing.T) {
	Run(t, "user_profile", func(s *Scenario) {
		anon := s.ClientFrom("anon", "10.0.7.1")
		anon.Get("/api/users/session/")
		anon.Get("/api/users/me/profile/")
		anon.Patch("/api/users/me/onboard/", map[string]any{"is_onboarded": true})

		alice := s.ClientFrom("alice", "10.0.7.2")
		signUp(alice, "alice@example.com", strongPassword)
		alice.Get("/api/users/session/")
		alice.Get("/api/users/me/settings/")
		alice.Get("/api/users/me/instance-admin/")
		alice.Get("/api/users/me/accounts/")
		alice.Get("/api/users/me/accounts/00000000-0000-4000-8000-000000000001/")
		alice.Get("/api/users/me/profile/", Mask("background_color"))

		alice.Patch("/api/users/me/profile/", map[string]any{
			"theme":                  map[string]any{"theme": "dark", "sidebar": "#123"},
			"is_app_rail_docked":     "false",
			"start_of_the_week":      "1",
			"language":               "fr",
			"role":                   "  Engineer  ",
			"use_case":               nil,
			"notification_view_mode": "compact",
			"billing_address":        nil,
			"last_workspace_id":      "0B6D8F0E1C514C559A4E6F3F1D6C3D01",
			"company_name":           5,
			"goals":                  []any{1, "two"},
			"has_billing_address":    1,
			"is_mobile_onboarded":    "on",
			"user":                   "00000000-0000-4000-8000-000000000001", // read-only
			"id":                     "00000000-0000-4000-8000-000000000001",
			"unknown":                "ignored",
		}, Mask("background_color"))
		alice.Patch("/api/users/me/profile/", map[string]any{
			"theme":                    nil,
			"is_tour_completed":        "maybe",
			"is_smooth_cursor_enabled": 2,
			"start_of_the_week":        7,
			"notification_view_mode":   "",
			"language":                 "",
			"background_color":         "   ",
			"role":                     strings.Repeat("x", 301),
			"last_workspace_id":        "nope",
			"billing_address_country":  true,
			"company_name":             map[string]any{},
			"use_case":                 "a\x00b",
		})
		alice.Patch("/api/users/me/profile/", map[string]any{"start_of_the_week": 3.0, "is_onboarded": nil, "last_workspace_id": 5})
		alice.Patch("/api/users/me/profile/", []any{})
		alice.Patch("/api/users/me/profile/", map[string]any{"last_workspace_id": nil, "role": "", "use_case": "   "}, Mask("background_color"))

		alice.Patch("/api/users/me/onboard/", map[string]any{"is_onboarded": true})
		alice.Patch("/api/users/me/onboard/", map[string]any{"is_onboarded": "yes"})
		alice.Patch("/api/users/me/onboard/", map[string]any{"is_onboarded": nil})
		alice.Patch("/api/users/me/onboard/", map[string]any{"is_onboarded": "f"})
		alice.Patch("/api/users/me/tour-completed/", map[string]any{"is_tour_completed": 1})
		alice.Patch("/api/users/me/tour-completed/", map[string]any{})
		alice.Patch("/api/users/me/onboard/", map[string]any{"is_onboarded": "True"})
		alice.Get("/api/users/me/profile/", Mask("background_color"))
		alice.Post("/api/users/me/profile/", map[string]any{})
	})
}

func TestUserUpdate(t *testing.T) {
	Run(t, "user_update", func(s *Scenario) {
		alice := s.ClientFrom("alice", "10.0.8.1")
		signUp(alice, "alice@example.com", strongPassword)

		// Accepted fields, plus read-only ones that must be ignored.
		alice.Patch("/api/users/me/", map[string]any{
			"first_name":          " Alice ",
			"last_name":           "Liddell",
			"display_name":        "ali",
			"user_timezone":       "Asia/Kolkata",
			"cover_image":         "https://images.unsplash.com/photo-1?w=1200&q=80",
			"is_password_expired": "true",
			"bot_type":            nil,
			"email":               "evil@example.com",
			"is_active":           false,
			"is_superuser":        true,
			"token":               "x",
		}, Mask("token"))
		// The response above was rendered in the old timezone; this one isn't.
		alice.Get("/api/users/me/")

		alice.Patch("/api/users/me/", map[string]any{
			"first_name":        "see www.evil.com",
			"last_name":         strings.Repeat("x", 256),
			"display_name":      "  ",
			"user_timezone":     "Mars/Olympus",
			"cover_image":       "not a url",
			"avatar_asset":      "abc",
			"cover_image_asset": "00000000-0000-4000-8000-000000000099",
			"last_login":        "yesterday",
			"is_email_valid":    2,
			"bot_type":          strings.Repeat("b", 31),
		})
		alice.Patch("/api/users/me/", map[string]any{"first_name": "John.Doe", "last_name": "http://x"})
		alice.Patch("/api/users/me/", map[string]any{"first_name": true, "display_name": nil, "user_timezone": ""})
		alice.Patch("/api/users/me/", map[string]any{"cover_image": "ftp://files.example.com/a.png", "first_name": 12}, Mask("token"))
		alice.Patch("/api/users/me/", map[string]any{"cover_image": "javascript://example.com/%0a"})
		alice.Patch("/api/users/me/", map[string]any{"cover_image": "http://[::1]:8080/x"}, Mask("token"))
		alice.Patch("/api/users/me/", map[string]any{"cover_image": "http://[::zz]/x"})
		alice.Patch("/api/users/me/", map[string]any{"cover_image": "http://localhost:3000/a b"})
		alice.Patch("/api/users/me/", map[string]any{"cover_image": "https://-bad-.com/"})
		alice.Patch("/api/users/me/", map[string]any{"cover_image": "  "}, Mask("token"))
		alice.Patch("/api/users/me/", map[string]any{"cover_image": nil, "avatar_asset": nil, "avatar": "  pic  "}, Mask("token"))

		// Naive datetimes are read in the user's (now Kolkata) timezone.
		alice.Patch("/api/users/me/", map[string]any{"masked_at": "2026-10-07T10:00:00"}, Mask("token"), Exact("masked_at"))
		alice.Patch("/api/users/me/", map[string]any{"masked_at": "2026-10-07 10:00:00.5+01:00"}, Mask("token"), Exact("masked_at"))
		alice.Patch("/api/users/me/", map[string]any{"masked_at": "2026-10-07T10:00Z"}, Mask("token"), Exact("masked_at"))
		alice.Patch("/api/users/me/", map[string]any{"masked_at": "2026-10-07"}, Mask("token"), Exact("masked_at"))
		for _, v := range []string{
			"20261007T100000", "2026-10-07T10", "2026-10-07T10:00:00+0530", "2026-10-07T10:00:00,5",
			"2026-10-07T10:00:00.1234567", "2026-W41-3", "2026-10-07T24:00:00", "2026-10-07T10:00:00 +01:00",
			"2026-1-7 3:04", "2026-10-07T10:00:00-00:30:15", " 2026-10-07T10:00:00",
		} {
			alice.Patch("/api/users/me/", map[string]any{"masked_at": v}, Mask("token"), Exact("masked_at"))
		}
		alice.Patch("/api/users/me/", map[string]any{"masked_at": "2026-13-07T10:00:00"})
		alice.Patch("/api/users/me/", map[string]any{"masked_at": 5})
		alice.Patch("/api/users/me/", map[string]any{"masked_at": nil, "last_login": nil}, Mask("token"))
		alice.Get("/api/users/me/")
		alice.Put("/api/users/me/", map[string]any{})
	})
}

func TestUserEmailChange(t *testing.T) {
	Run(t, "user_email_change", func(s *Scenario) {
		bob := s.ClientFrom("bob", "10.0.9.1")
		signUp(bob, "bob@example.com", strongPassword)
		alice := s.ClientFrom("alice", "10.0.9.2")
		signUp(alice, "alice@example.com", strongPassword)

		alice.Patch("/api/users/me/email/", map[string]any{})
		alice.Patch("/api/users/me/email/", map[string]any{"email": "bad"})
		alice.Patch("/api/users/me/email/", map[string]any{"email": " ALICE@example.com"})
		alice.Patch("/api/users/me/email/", map[string]any{"email": "bob@example.com"})
		alice.Patch("/api/users/me/email/", map[string]any{"email": "alice2@example.com"})
		alice.Patch("/api/users/me/email/", map[string]any{"email": "alice2@example.com", "code": "123456"})
		alice.Patch("/api/users/me/email/", map[string]any{"email": 5})

		// Three code requests per hour, counted whether or not they succeed.
		gen := "/api/users/me/email/generate-code/"
		alice.Post(gen, map[string]any{})
		alice.Post(gen, map[string]any{"email": "bob@example.com"})
		alice.Post(gen, map[string]any{"email": " Alice2@Example.com "})
		alice.Post(gen, map[string]any{"email": "alice3@example.com"}, Mask("detail"))
		code := s.LatestEmail("alice2@example.com").Code()

		alice.Patch("/api/users/me/email/", map[string]any{"email": "alice2@example.com", "code": "000000"})
		alice.Patch("/api/users/me/email/", map[string]any{"email": "alice2@example.com", "code": " " + code + " "})
		alice.Get("/api/users/me/")
		s.LatestEmail("alice2@example.com")
		s.LatestEmail("alice@example.com")

		alice.PostForm("/auth/sign-in/", url.Values{"csrfmiddlewaretoken": {csrf(alice)}, "email": {"alice2@example.com"}, "password": {strongPassword}})
		alice.Get("/api/users/me/")
	})
}

func TestUserDeactivate(t *testing.T) {
	Run(t, "user_deactivate", func(s *Scenario) {
		anon := s.ClientFrom("anon", "10.0.10.1")
		anon.Delete("/api/users/me/")

		carol := s.ClientFrom("carol", "10.0.10.2")
		signUp(carol, "carol@example.com", strongPassword)
		carol.Patch("/api/users/me/onboard/", map[string]any{"is_onboarded": true})
		laptop := s.ClientFrom("carol-laptop", "10.0.10.3")
		laptop.PostForm("/auth/sign-in/", url.Values{"csrfmiddlewaretoken": {csrf(laptop)}, "email": {"carol@example.com"}, "password": {strongPassword}})

		carol.Delete("/api/users/me/")
		s.LatestEmail("carol@example.com")
		carol.Get("/api/users/me/")
		laptop.Get("/api/users/me/")

		carol.PostForm("/auth/sign-in/", url.Values{"csrfmiddlewaretoken": {csrf(carol)}, "email": {"carol@example.com"}, "password": {strongPassword}})
		code := magicCode(carol, "carol@example.com")
		carol.PostForm("/auth/magic-sign-in/", url.Values{"csrfmiddlewaretoken": {csrf(carol)}, "email": {"carol@example.com"}, "code": {code}})
		carol.Get("/api/users/me/")
		carol.Get("/api/users/me/profile/", Mask("background_color"))
	})
}
