package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
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

func Authenticate(tokens TokenValidator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			writeUnauthorized(w)
			return
		}

		userID, err := tokens.Validate(parts[1])
		if err != nil || userID == uuid.Nil {
			writeUnauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), contextKey{}, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"authentication required"}}` + "\n"))
}
