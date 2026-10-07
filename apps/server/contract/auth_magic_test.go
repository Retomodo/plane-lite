package contract

import (
	"net/url"
	"testing"
)

// magicCode requests a login code and reads it from the email.
func magicCode(c *Client, email string) string {
	c.Post("/auth/magic-generate/", map[string]any{"email": email})
	return c.s.LatestEmail(email).Code()
}

func TestAuthMagicSignUp(t *testing.T) {
	Run(t, "auth_magic_sign_up", func(s *Scenario) {
		anon := s.ClientFrom("anon", "10.0.0.1")
		anon.Post("/auth/magic-generate/", map[string]any{})
		anon.Post("/auth/magic-generate/", map[string]any{"email": "nope"})

		carol := s.ClientFrom("carol", "10.0.0.2")
		code := magicCode(carol, " Carol@Example.com ")
		token := csrf(carol)
		carol.PostForm("/auth/magic-sign-up/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"carol@example.com"}})
		carol.PostForm("/auth/magic-sign-up/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"carol@example.com"}, "code": {"000000"}})
		carol.PostForm("/auth/magic-sign-up/", url.Values{
			"csrfmiddlewaretoken": {token},
			"email":               {"carol@example.com"},
			"code":                {code},
			"next_path":           {"/acme/"},
		})
		carol.Get("/api/users/me/")

		// The code was consumed; the account now exists.
		other := s.ClientFrom("other", "10.0.0.3")
		other.PostForm("/auth/magic-sign-up/", url.Values{"csrfmiddlewaretoken": {csrf(other)}, "email": {"carol@example.com"}, "code": {code}})
		other.Post("/auth/email-check/", map[string]any{"email": "carol@example.com"})
	})
}

func TestAuthMagicSignIn(t *testing.T) {
	Run(t, "auth_magic_sign_in", func(s *Scenario) {
		alice := s.ClientFrom("alice", "10.0.1.1")
		signUp(alice, "alice@example.com", strongPassword)
		alice.PostForm("/auth/sign-out/", url.Values{"csrfmiddlewaretoken": {csrf(alice)}})

		token := csrf(alice)
		alice.PostForm("/auth/magic-sign-in/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"alice@example.com"}})
		alice.PostForm("/auth/magic-sign-in/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"nobody@example.com"}, "code": {"123456"}})
		alice.PostForm("/auth/magic-sign-in/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"alice@example.com"}, "code": {"123456"}})

		code := magicCode(alice, "alice@example.com")
		alice.PostForm("/auth/magic-sign-in/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"alice@example.com"}, "code": {"000000"}})
		alice.PostForm("/auth/magic-sign-in/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"ALICE@example.com "}, "code": {code}})
		alice.Get("/api/users/me/")
	})
}

func TestAuthMagicAttempts(t *testing.T) {
	Run(t, "auth_magic_attempts", func(s *Scenario) {
		// Code generation: the 5th request inside the TTL is refused.
		gen := s.ClientFrom("gen", "10.0.2.1")
		for range 5 {
			gen.Post("/auth/magic-generate/", map[string]any{"email": "dave@example.com"})
		}

		// Verification: the 5th wrong code burns the token.
		guess := s.ClientFrom("guess", "10.0.2.2")
		guess.Post("/auth/magic-generate/", map[string]any{"email": "erin@example.com"})
		s.LatestEmail("erin@example.com")
		token := csrf(guess)
		for range 5 {
			guess.PostForm("/auth/magic-sign-up/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"erin@example.com"}, "code": {"000000"}})
		}
		guess.PostForm("/auth/magic-sign-up/", url.Values{"csrfmiddlewaretoken": {token}, "email": {"erin@example.com"}, "code": {"000000"}})
	})
}

func TestAuthRateLimit(t *testing.T) {
	Run(t, "auth_rate_limit", func(s *Scenario) {
		c := s.ClientFrom("client", "10.0.3.1")
		for range 11 {
			c.Post("/auth/email-check/", map[string]any{"email": "x@example.com"})
		}
		// Same scope, so form posts are throttled too: redirect with 5900.
		c.PostForm("/auth/sign-in/", url.Values{"csrfmiddlewaretoken": {csrf(c)}, "email": {"x@example.com"}, "password": {"x"}})
		// Other clients are unaffected.
		s.ClientFrom("neighbour", "10.0.3.2").Post("/auth/email-check/", map[string]any{"email": "x@example.com"})
	})
}
