package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	authservice "github.com/Mopcs/mini-payment-system/internal/service/auth"
)

type authServiceStub struct {
	registerResult authservice.Result
	registerError  error
	loginResult    authservice.Result
	loginError     error
}

func (s *authServiceStub) Register(context.Context, string, string) (authservice.Result, error) {
	return s.registerResult, s.registerError
}

func (s *authServiceStub) Login(context.Context, string, string) (authservice.Result, error) {
	return s.loginResult, s.loginError
}

func TestRegisterReturnsUserAndToken(t *testing.T) {
	service := &authServiceStub{registerResult: authservice.Result{
		User:        authservice.UserInfo{ID: uuid.New(), Email: "user@example.com"},
		AccessToken: "signed-token",
		ExpiresIn:   900,
	}}
	response := sendAuthRequest(service, "/api/v1/auth/register", `{"email":"user@example.com","password":"StrongPassword123!"}`, "application/json")

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "password_hash") || strings.Contains(response.Body.String(), "StrongPassword123!") {
		t.Fatalf("response contains sensitive credentials: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"token_type":"Bearer"`) || !strings.Contains(response.Body.String(), `"expires_in":900`) {
		t.Fatalf("response is missing token metadata: %s", response.Body.String())
	}
}

func TestRegisterMapsValidationAndDuplicateErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid email", err: authservice.ErrInvalidEmail, want: http.StatusBadRequest},
		{name: "invalid password", err: authservice.ErrInvalidPassword, want: http.StatusBadRequest},
		{name: "duplicate email", err: authservice.ErrEmailAlreadyRegistered, want: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &authServiceStub{registerError: test.err}
			response := sendAuthRequest(service, "/api/v1/auth/register", `{"email":"user@example.com","password":"StrongPassword123!"}`, "application/json")
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestLoginUsesGenericCredentialsError(t *testing.T) {
	service := &authServiceStub{loginError: authservice.ErrInvalidCredentials}
	missingUser := sendAuthRequest(service, "/api/v1/auth/login", `{"email":"missing@example.com","password":"StrongPassword123!"}`, "application/json")
	wrongPassword := sendAuthRequest(service, "/api/v1/auth/login", `{"email":"known@example.com","password":"WrongPassword123!"}`, "application/json")
	if missingUser.Code != http.StatusUnauthorized || wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("login statuses = %d and %d, want 401", missingUser.Code, wrongPassword.Code)
	}
	if missingUser.Body.String() != wrongPassword.Body.String() || strings.Contains(missingUser.Body.String(), "known@example.com") {
		t.Fatalf("login responses reveal account state: %s / %s", missingUser.Body.String(), wrongPassword.Body.String())
	}
}

func TestAuthRequestValidation(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		contentType string
		want        int
	}{
		{name: "missing content type", body: `{}`, want: http.StatusUnsupportedMediaType},
		{name: "malformed json", body: `{"email":`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "trailing json", body: `{} {}`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "unknown field", body: `{"email":"a@example.com","password":"StrongPassword123!","admin":true}`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "large body", body: strings.Repeat(" ", maxAuthBodyBytes+1), contentType: "application/json", want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := sendAuthRequest(&authServiceStub{}, "/api/v1/auth/register", test.body, test.contentType)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestAuthHidesInternalErrors(t *testing.T) {
	service := &authServiceStub{loginError: errors.New("database password and SQL details")}
	response := sendAuthRequest(service, "/api/v1/auth/login", `{"email":"user@example.com","password":"StrongPassword123!"}`, "application/json")
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "database password") || strings.Contains(response.Body.String(), "SQL details") {
		t.Fatalf("internal error leaked: status=%d body=%s", response.Code, response.Body.String())
	}
}

func sendAuthRequest(service AuthService, path, body, contentType string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	NewAuth(service, logger).Register(mux)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
