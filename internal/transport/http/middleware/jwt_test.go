package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	authservice "github.com/Mopcs/mini-payment-system/internal/service/auth"
)

func TestAuthenticate(t *testing.T) {
	manager, err := authservice.NewTokenManager("0123456789abcdef0123456789abcdef", "test-issuer", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	valid, err := manager.Generate(userID)
	if err != nil {
		t.Fatal(err)
	}
	wrongSecret, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer: "test-issuer", Subject: userID.String(), IssuedAt: jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}).SignedString([]byte("abcdef0123456789abcdef0123456789"))
	expired, _ := signTestToken(t, "test-issuer", userID.String(), time.Now().Add(-time.Hour), time.Now().Add(-time.Minute))
	wrongIssuer, _ := signTestToken(t, "other-issuer", userID.String(), time.Now(), time.Now().Add(time.Minute))
	badSubject, _ := signTestToken(t, "test-issuer", "not-a-uuid", time.Now(), time.Now().Add(time.Minute))

	tests := []struct {
		name   string
		header string
		status int
		valid  bool
	}{
		{name: "missing authorization", status: http.StatusUnauthorized},
		{name: "malformed bearer", header: "Bearer", status: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic " + valid, status: http.StatusUnauthorized},
		{name: "invalid signature", header: "Bearer " + wrongSecret, status: http.StatusUnauthorized},
		{name: "expired", header: "Bearer " + expired, status: http.StatusUnauthorized},
		{name: "invalid issuer", header: "Bearer " + wrongIssuer, status: http.StatusUnauthorized},
		{name: "invalid subject", header: "Bearer " + badSubject, status: http.StatusUnauthorized},
		{name: "valid", header: "Bearer " + valid, status: http.StatusNoContent, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, ok := UserID(r.Context())
				if !ok || got != userID {
					t.Fatalf("context user = %s, %v; want %s", got, ok, userID)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}
			Authenticate(manager, next).ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
			if !test.valid && recorder.Code == http.StatusNoContent {
				t.Fatal("invalid token reached handler")
			}
		})
	}
	if _, ok := UserID(context.Background()); ok {
		t.Fatal("unexpected user identity in empty context")
	}
}

func signTestToken(t *testing.T, issuer, subject string, issued, expires time.Time) (string, error) {
	t.Helper()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer: issuer, Subject: subject,
		IssuedAt: jwt.NewNumericDate(issued), ExpiresAt: jwt.NewNumericDate(expires),
	}).SignedString([]byte("0123456789abcdef0123456789abcdef"))
}
