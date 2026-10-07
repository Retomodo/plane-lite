package httpx

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/google/uuid"
)

// Principal is the authenticated user attached to a request.
type Principal struct {
	ID       uuid.UUID
	Email    string
	Timezone *time.Location
}

type principalKey struct{}

// WithPrincipal returns a context carrying the authenticated user.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the authenticated user, or nil for anonymous requests.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// Ctx is the per-request handle passed to handlers.
type Ctx struct {
	W    http.ResponseWriter
	R    *http.Request
	User *Principal // nil when anonymous
}

func (c *Ctx) Context() context.Context { return c.R.Context() }

// Loc is the timezone responses are rendered in: the user's own, or UTC for
// anonymous requests (Plane's TimezoneMixin).
func (c *Ctx) Loc() *time.Location {
	if c.User != nil && c.User.Timezone != nil {
		return c.User.Timezone
	}
	return time.UTC
}

func (c *Ctx) Param(name string) string { return c.R.PathValue(name) }

// UUIDParam parses a <uuid:...> path segment. Django's URL resolver rejects
// non-UUIDs before any view runs, so a bad value is a plain 404.
func (c *Ctx) UUIDParam(name string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.R.PathValue(name))
	if err != nil || strings.Count(c.R.PathValue(name), "-") != 4 {
		return uuid.Nil, ErrPageNotFound
	}
	return id, nil
}

func (c *Ctx) Query(name string) string { return c.R.URL.Query().Get(name) }

// JSON writes v with the given status.
func (c *Ctx) JSON(status int, v any) error {
	b, err := Marshal(v, c.Loc())
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "application/json")
	c.W.WriteHeader(status)
	_, err = c.W.Write(b)
	return err
}

// NoContent writes 204.
func (c *Ctx) NoContent() error {
	c.W.WriteHeader(http.StatusNoContent)
	return nil
}

// Redirect writes a 302 like Django's HttpResponseRedirect.
func (c *Ctx) Redirect(location string) error {
	c.W.Header().Set("Location", location)
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.WriteHeader(http.StatusFound)
	return nil
}

const maxBodyBytes = 10 << 20

// Bind decodes a JSON request body into v. Unknown fields are ignored, as
// DRF serializers do.
func (c *Ctx) Bind(v any) error {
	body, err := io.ReadAll(io.LimitReader(c.R.Body, maxBodyBytes))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, v, json.RejectUnknownMembers(false)); err != nil {
		var se *json.SemanticError
		if errors.As(err, &se) {
			return Detail(http.StatusBadRequest, "Invalid data.")
		}
		return Detail(http.StatusBadRequest, "JSON parse error - "+err.Error())
	}
	return nil
}

// Form returns the request's form values, from a urlencoded or multipart
// body, or from a JSON object body (DRF accepts all three on most views).
// JSON values are stringified the way request.data.get() would read them.
func (c *Ctx) Form() (url.Values, error) {
	ct, _, _ := mime.ParseMediaType(c.R.Header.Get("Content-Type"))
	switch ct {
	case "application/json":
		var m map[string]any
		if err := c.Bind(&m); err != nil {
			return nil, err
		}
		out := url.Values{}
		for k, v := range m {
			switch x := v.(type) {
			case string:
				out.Set(k, x)
			case nil:
			default:
				b, _ := json.Marshal(x)
				out.Set(k, string(b))
			}
		}
		return out, nil
	case "multipart/form-data":
		if err := c.R.ParseMultipartForm(maxBodyBytes); err != nil {
			return nil, err
		}
		return c.R.PostForm, nil
	default:
		if err := c.R.ParseForm(); err != nil {
			return nil, err
		}
		return c.R.PostForm, nil
	}
}
