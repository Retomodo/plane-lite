package httpx

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
)

// Error is an HTTP error with an exact response body. Plane endpoints use
// both DRF's {"detail": ...} and their own {"error": ...} shapes.
type Error struct {
	Status int
	Body   any
}

func (e *Error) Error() string { return fmt.Sprintf("http %d: %v", e.Status, e.Body) }

// Err builds Plane's custom {"error": msg} body.
func Err(status int, msg string) *Error {
	return &Error{Status: status, Body: map[string]any{"error": msg}}
}

// Detail builds DRF's {"detail": msg} body.
func Detail(status int, msg string) *Error {
	return &Error{Status: status, Body: map[string]any{"detail": msg}}
}

// Body returns an error with an arbitrary JSON body (e.g. serializer errors).
func Body(status int, body any) *Error {
	return &Error{Status: status, Body: body}
}

var (
	ErrNotAuthenticated = Detail(http.StatusUnauthorized, "Authentication credentials were not provided.")
	ErrForbidden        = Detail(http.StatusForbidden, "You do not have permission to perform this action.")
	ErrPageNotFound     = Err(http.StatusNotFound, "Page not found.")
)

// toResponse maps an error to a status and body the way Plane's
// BaseAPIView.handle_exception does.
func toResponse(err error) (int, any) {
	var he *Error
	switch {
	case errors.As(err, &he):
		return he.Status, he.Body
	case errors.Is(err, pgx.ErrNoRows):
		// Model.objects.get() -> ObjectDoesNotExist
		return http.StatusNotFound, map[string]any{"error": "The required object does not exist."}
	case db.IsIntegrityError(err):
		return http.StatusBadRequest, map[string]any{"error": "The payload is not valid"}
	default:
		return http.StatusInternalServerError, map[string]any{"error": "Something went wrong please try again later"}
	}
}
