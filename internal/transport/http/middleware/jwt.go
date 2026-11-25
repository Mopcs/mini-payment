package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Mopcs/mini-payment-system/internal/observability"
)

type TokenValidator interface {
	Validate(string) (uuid.UUID, error)
}

// Unexported type, so no other package can overwrite the value in the context.
type contextKey struct{}

func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(contextKey{}).(uuid.UUID)
	return id, ok
}

func Authenticate(
	tokens TokenValidator,
	auditor *observability.Auditor,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			auditor.Event(r.Context(), "security.authentication_failed",
				slog.String("reason", "missing_token"),
				slog.String("path", r.URL.Path))
			writeUnauthorized(w)
			return
		}

		// The token itself is never logged, only the fact that it was rejected.
		userID, err := tokens.Validate(parts[1])
		if err != nil || userID == uuid.Nil {
			auditor.Event(r.Context(), "security.authentication_failed",
				slog.String("reason", "invalid_token"),
				slog.String("path", r.URL.Path))
			writeUnauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), contextKey{}, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeUnauthorized(w http.ResponseWriter) {
	writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
}
