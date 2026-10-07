package contract

import (
	"net/url"
	"regexp"
	"testing"
)

const newPassword = "another-Strong-passphrase-2026"

var resetLinkRe = regexp.MustCompile(`uidb64=([^&\s]+)&token=([^&\s]+)`)

func TestAuthForgotResetPassword(t *testing.T) {
	Run(t, "auth_forgot_reset_password", func(s *Scenario) {
		alice := s.ClientFrom("alice", "10.0.4.1")
		signUp(alice, "alice@example.com", strongPassword)
		alice.PostForm("/auth/sign-out/", url.Values{"csrfmiddlewaretoken": {csrf(alice)}})

		anon := s.ClientFrom("anon", "10.0.4.2")
		anon.Post("/auth/forgot-password/", map[string]any{})
		anon.Post("/auth/forgot-password/", map[string]any{"email": "nobody@example.com"})
		anon.Post("/auth/forgot-password/", map[string]any{"email": "ALICE@example.com"}) // exact-case lookup
		anon.Post("/auth/forgot-password/", map[string]any{"email": "alice@example.com"})
		m := resetLinkRe.FindStringSubmatch(s.LatestEmail("alice@example.com").Text)
		if m == nil {
			t.Fatal("no reset link in email")
		}
		uid, token := m[1], m[2]
		s.Alias(uid, "<uidb64>")
		s.Alias(token, "<token>")

		reset := func(uid, token string, form url.Values) {
			form.Set("csrfmiddlewaretoken", csrf(alice))
			alice.PostForm("/auth/reset-password/"+uid+"/"+token+"/", form)
		}
		reset(uid, "1-badtoken", url.Values{"password": {newPassword}})
		reset("!!!", token, url.Values{"password": {newPassword}})
		reset("bm9ib2R5", token, url.Values{"password": {newPassword}}) // "nobody": not a UUID
		reset("Zm9vY", token, url.Values{"password": {newPassword}})    // bad base64 padding
		reset("_w", token, url.Values{"password": {newPassword}})       // 0xff: not UTF-8
		reset("MDAwMDAwMDAtMDAwMC00MDAwLTgwMDAtMDAwMDAwMDAwMDk5", token, url.Values{"password": {newPassword}})
		reset(uid, token, url.Values{})
		reset(uid, token, url.Values{"password": {weakPassword}})
		reset(uid, token, url.Values{"password": {newPassword}})
		// The token is bound to the old password hash, so it's now spent.
		reset(uid, token, url.Values{"password": {newPassword + "x"}})

		signIn := func(password string) {
			alice.PostForm("/auth/sign-in/", url.Values{"csrfmiddlewaretoken": {csrf(alice)}, "email": {"alice@example.com"}, "password": {password}})
		}
		signIn(strongPassword)
		signIn(newPassword)
		alice.Get("/api/users/me/")
	})
}

func TestAuthChangePassword(t *testing.T) {
	Run(t, "auth_change_password", func(s *Scenario) {
		anon := s.ClientFrom("anon", "10.0.5.1")
		anon.Post("/auth/change-password/", map[string]any{"new_password": newPassword})

		alice := s.ClientFrom("alice", "10.0.5.2")
		signUp(alice, "alice@example.com", strongPassword)
		laptop := s.ClientFrom("alice-laptop", "10.0.5.3")
		laptop.PostForm("/auth/sign-in/", url.Values{"csrfmiddlewaretoken": {csrf(laptop)}, "email": {"alice@example.com"}, "password": {strongPassword}})

		// DRF's SessionAuthentication enforces CSRF for logged-in users.
		alice.Post("/auth/change-password/", map[string]any{"old_password": strongPassword, "new_password": newPassword}, NoCSRF())
		alice.Post("/auth/email-check/", map[string]any{"email": "alice@example.com"}, NoCSRF())

		alice.Post("/auth/change-password/", map[string]any{})
		alice.Post("/auth/change-password/", map[string]any{"old_password": strongPassword})
		alice.Post("/auth/change-password/", map[string]any{"old_password": "wrong", "new_password": newPassword})
		alice.Post("/auth/change-password/", map[string]any{"old_password": strongPassword, "new_password": weakPassword})
		alice.Post("/auth/change-password/", map[string]any{"old_password": strongPassword, "new_password": newPassword})
		alice.Get("/api/users/me/")
		// Other sessions are invalidated by the password change.
		laptop.Get("/api/users/me/")

		alice.Post("/auth/set-password/", map[string]any{"password": strongPassword})
	})
}

func TestAuthSetPassword(t *testing.T) {
	Run(t, "auth_set_password", func(s *Scenario) {
		carol := s.ClientFrom("carol", "10.0.6.1")
		code := magicCode(carol, "carol@example.com")
		carol.PostForm("/auth/magic-sign-up/", url.Values{"csrfmiddlewaretoken": {csrf(carol)}, "email": {"carol@example.com"}, "code": {code}})

		carol.Post("/auth/set-password/", map[string]any{}, NoCSRF())
		carol.Post("/auth/set-password/", map[string]any{})
		carol.Post("/auth/set-password/", map[string]any{"password": weakPassword})
		carol.Post("/auth/set-password/", map[string]any{"password": strongPassword}, Mask("token"))
		carol.Get("/api/users/me/")
		// Autoset users change passwords without the old one; now it's required.
		carol.Post("/auth/change-password/", map[string]any{"new_password": newPassword})
	})
}
