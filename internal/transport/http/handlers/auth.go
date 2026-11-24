package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	authservice "github.com/Mopcs/mini-payment-system/internal/service/auth"
)

const maxAuthBodyBytes = 16 << 10

type AuthService interface {
	Register(context.Context, string, string) (authservice.Result, error)
	Login(context.Context, string, string) (authservice.Result, error)
}

type Auth struct {
	service AuthService
	logger  *slog.Logger
}

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	User        authservice.UserInfo `json:"user"`
	AccessToken string               `json:"access_token"`
	TokenType   string               `json:"token_type"`
	ExpiresIn   int64                `json:"expires_in"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type requestDecodeError struct {
	status int
	code   string
	text   string
}

func (e *requestDecodeError) Error() string { return e.text }

func NewAuth(service AuthService, logger *slog.Logger) *Auth {
	return &Auth{service: service, logger: logger}
}

func (h *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
}

func (h *Auth) register(w http.ResponseWriter, r *http.Request) {
	var request credentialsRequest
	if err := decodeJSONRequest(w, r, &request, "credentials", maxAuthBodyBytes); err != nil {
		writeError(w, err.status, err.code, err.text)
		return
	}

	result, err := h.service.Register(r.Context(), request.Email, request.Password)
	if err != nil {
		h.writeRegisterError(w, err)
		return
	}

	writeAuthResponse(w, http.StatusCreated, result)
}

func (h *Auth) login(w http.ResponseWriter, r *http.Request) {
	var request credentialsRequest
	if err := decodeJSONRequest(w, r, &request, "credentials", maxAuthBodyBytes); err != nil {
		writeError(w, err.status, err.code, err.text)
		return
	}

	result, err := h.service.Login(r.Context(), request.Email, request.Password)
	if err != nil {
		if errors.Is(err, authservice.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
			return
		}
		h.logger.Error("login failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
		return
	}

	writeAuthResponse(w, http.StatusOK, result)
}

// writeRegisterError maps registration errors to the API error contract.
func (h *Auth) writeRegisterError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authservice.ErrInvalidEmail):
		writeError(w, http.StatusBadRequest, "invalid_email", "email address is invalid")
	case errors.Is(err, authservice.ErrInvalidPassword):
		writeError(w, http.StatusBadRequest, "invalid_password", "password does not meet the length requirements")
	case errors.Is(err, authservice.ErrEmailAlreadyRegistered):
		writeError(w, http.StatusConflict, "email_already_registered", "email is already registered")
	default:
		h.logger.Error("registration failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

func writeAuthResponse(w http.ResponseWriter, status int, result authservice.Result) {
	w.Header().Set("Cache-Control", "no-store")
	writeAuthJSON(w, status, authResponse{
		User:        result.User,
		AccessToken: result.AccessToken,
		TokenType:   "Bearer",
		ExpiresIn:   result.ExpiresIn,
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	writeAuthJSON(w, status, errorResponse{Error: apiError{Code: code, Message: message}})
}

func writeAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
