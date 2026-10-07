package contract

import "testing"

// Django-level routing behaviour shared by every endpoint.
func TestRouting(t *testing.T) {
	Run(t, "routing", func(s *Scenario) {
		anon := s.Client("anon")
		anon.Get("/api/instances")                    // APPEND_SLASH redirect
		anon.Get("/api/instances?foo=bar")            // ...keeping the query string
		anon.Get("/api/no-such-endpoint/")            // custom JSON 404
		anon.Delete("/auth/get-csrf-token/")          // DRF 405
		anon.Post("/api/users/me/", map[string]any{}) // auth before method check
	})
}
