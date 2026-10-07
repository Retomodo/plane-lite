package contract

import (
	"net/url"
	"testing"
)

// strongPassword scores >= 3 with zxcvbn; weakPassword scores below.
const (
	strongPassword = "correct-Horse-battery-staple-91"
	weakPassword   = "password1"
)

// csrf fetches a CSRF token the way the web app's auth forms do.
func csrf(c *Client) string {
	return c.Get("/auth/get-csrf-token/", Mask("csrf_token")).String("csrf_token")
}

// signUp creates an account through the password sign-up form.
func signUp(c *Client, email, password string) *Response {
	return c.PostForm("/auth/sign-up/", url.Values{
		"csrfmiddlewaretoken": {csrf(c)},
		"email":               {email},
		"password":            {password},
	})
}

func TestAuthEmailCheck(t *testing.T) {
	Run(t, "auth_email_check", func(s *Scenario) {
		anon := s.Client("anon")
		anon.Get("/api/instances/")
		anon.Get("/api/users/me/")
		anon.Post("/auth/email-check/", map[string]any{})
		anon.Post("/auth/email-check/", map[string]any{"email": "not-an-email"})
		anon.Post("/auth/email-check/", map[string]any{"email": "  New@Example.com "})

		alice := s.Client("alice")
		signUp(alice, "alice@example.com", strongPassword)
		anon.Post("/auth/email-check/", map[string]any{"email": "ALICE@example.com"})
	})
}

func TestAuthSignUp(t *testing.T) {
	Run(t, "auth_sign_up", func(s *Scenario) {
		alice := s.Client("alice")

		// No CSRF token: Django's CSRF failure page.
		alice.PostForm("/auth/sign-up/", url.Values{"email": {"alice@example.com"}, "password": {strongPassword}})

		token := csrf(alice)
		alice.PostForm("/auth/sign-up/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"alice@example.com"}})
		alice.PostForm("/auth/sign-up/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"nope"}, "password": {strongPassword}})
		alice.PostForm("/auth/sign-up/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"alice@example.com"}, "password": {weakPassword}})
		alice.PostForm("/auth/sign-up/", url.Values{
			"csrfmiddlewaretoken": {token},
			"email":               {" Alice@Example.com"},
			"password":            {strongPassword},
			"next_path":           {"/acme/projects/"},
		})
		alice.Get("/api/users/me/")

		bob := s.Client("bob")
		signUp(bob, "alice@example.com", strongPassword)
		bob.Get("/api/users/me/")
	})
}

func TestAuthSignInOut(t *testing.T) {
	Run(t, "auth_sign_in_out", func(s *Scenario) {
		alice := s.Client("alice")
		signUp(alice, "alice@example.com", strongPassword)

		alice.PostForm("/auth/sign-out/", url.Values{"csrfmiddlewaretoken": {csrf(alice)}})
		alice.Get("/api/users/me/")

		token := csrf(alice)
		alice.PostForm("/auth/sign-in/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"alice@example.com"}, "password": {"wrong-" + strongPassword}})
		alice.PostForm("/auth/sign-in/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"nobody@example.com"}, "password": {strongPassword}})
		alice.PostForm("/auth/sign-in/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"alice@example.com"}})
		alice.PostForm("/auth/sign-in/", url.Values{
			"csrfmiddlewaretoken": {token},
			"email":               {"ALICE@example.com"},
			"password":            {strongPassword},
			"next_path":           {"//evil.example.com/steal"},
		})
		alice.Get("/api/users/me/")
	})
}
