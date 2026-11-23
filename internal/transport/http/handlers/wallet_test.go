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
	"time"

	"github.com/google/uuid"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	walletservice "github.com/Mopcs/mini-payment-system/internal/service/wallet"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/middleware"
)

type walletServiceStub struct {
	listResult   []domain.Wallet
	getResult    *domain.Wallet
	createResult *domain.Wallet
	getError     error
	createError  error
	gotUserID    uuid.UUID
	gotWalletID  uuid.UUID
	gotCurrency  string
}

func (s *walletServiceStub) List(_ context.Context, userID uuid.UUID) ([]domain.Wallet, error) {
	s.gotUserID = userID
	return s.listResult, nil
}
func (s *walletServiceStub) Get(_ context.Context, userID, walletID uuid.UUID) (*domain.Wallet, error) {
	s.gotUserID, s.gotWalletID = userID, walletID
	return s.getResult, s.getError
}
func (s *walletServiceStub) Create(_ context.Context, userID uuid.UUID, currency string) (*domain.Wallet, error) {
	s.gotUserID, s.gotCurrency = userID, currency
	if currency != "USD" && currency != "EUR" && currency != "JPY" {
		return nil, walletservice.ErrInvalidCurrency
	}
	return s.createResult, s.createError
}

type tokenValidatorStub struct {
	userID uuid.UUID
	err    error
}

func (s tokenValidatorStub) Validate(string) (uuid.UUID, error) { return s.userID, s.err }

func TestWalletListReturnsEmptyArrayAndUsesTokenIdentity(t *testing.T) {
	userID := uuid.New()
	service := &walletServiceStub{}
	response := sendWalletRequest(service, tokenValidatorStub{userID: userID}, http.MethodGet, "/api/v1/wallets", "", "application/json", "valid")
	if response.Code != http.StatusOK || response.Body.String() != "{\"wallets\":[]}\n" {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if service.gotUserID != userID {
		t.Fatalf("service received user ID %s, want token subject %s", service.gotUserID, userID)
	}
}

func TestWalletGetScopesByTokenUser(t *testing.T) {
	userID, walletID := uuid.New(), uuid.New()
	service := &walletServiceStub{getResult: &domain.Wallet{ID: walletID, UserID: userID, Currency: "USD", CreatedAt: time.Now()}}
	response := sendWalletRequest(service, tokenValidatorStub{userID: userID}, http.MethodGet, "/api/v1/wallets/"+walletID.String(), "", "", "valid")
	if response.Code != http.StatusOK || service.gotUserID != userID || service.gotWalletID != walletID {
		t.Fatalf("response=%d user=%s wallet=%s", response.Code, service.gotUserID, service.gotWalletID)
	}
	service.getError = walletservice.ErrNotFound
	response = sendWalletRequest(service, tokenValidatorStub{userID: userID}, http.MethodGet, "/api/v1/wallets/"+walletID.String(), "", "", "valid")
	if response.Code != http.StatusNotFound {
		t.Fatalf("foreign wallet status = %d, want 404", response.Code)
	}
}

func TestWalletCreateValidatesRequestAndUsesTokenIdentity(t *testing.T) {
	userID := uuid.New()
	for _, currency := range []string{"USD", "EUR", "JPY"} {
		service := &walletServiceStub{createResult: &domain.Wallet{ID: uuid.New(), UserID: userID, Currency: currency}}
		response := sendWalletRequest(service, tokenValidatorStub{userID: userID}, http.MethodPost, "/api/v1/wallets", `{"currency":" `+strings.ToLower(currency)+` "}`, "application/json", "valid")
		if response.Code != http.StatusCreated || service.gotUserID != userID || service.gotCurrency != currency {
			t.Fatalf("%s: status=%d user=%s currency=%s body=%s", currency, response.Code, service.gotUserID, service.gotCurrency, response.Body.String())
		}
	}
	for _, body := range []string{`{"currency":"CAD"}`, `{"currency":"USD","balance":100}`, `{"currency":"USD","user_id":"` + uuid.NewString() + `"}`, `{"currency":`, `{"currency":"USD"} {}`} {
		response := sendWalletRequest(&walletServiceStub{}, tokenValidatorStub{userID: userID}, http.MethodPost, "/api/v1/wallets", body, "application/json", "valid")
		if response.Code != http.StatusBadRequest {
			t.Errorf("body %s: status=%d, want 400", body, response.Code)
		}
	}
	for _, err := range []error{walletservice.ErrAlreadyExists, errors.New("database failure")} {
		status := http.StatusInternalServerError
		if errors.Is(err, walletservice.ErrAlreadyExists) {
			status = http.StatusConflict
		}
		service := &walletServiceStub{createError: err}
		response := sendWalletRequest(service, tokenValidatorStub{userID: userID}, http.MethodPost, "/api/v1/wallets", `{"currency":"USD"}`, "application/json", "valid")
		if response.Code != status {
			t.Errorf("error %v: status=%d want=%d", err, response.Code, status)
		}
	}
}

func TestWalletEndpointsRequireAuthentication(t *testing.T) {
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/wallets"}, {http.MethodGet, "/api/v1/wallets/" + uuid.NewString()}, {http.MethodPost, "/api/v1/wallets"},
	} {
		response := sendWalletRequest(&walletServiceStub{}, tokenValidatorStub{err: errors.New("invalid token")}, test.method, test.path, `{}`, "application/json", "bad")
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status=%d", test.method, test.path, response.Code)
		}
	}
}

func sendWalletRequest(service WalletService, tokens middleware.TokenValidator, method, path, body, contentType, authorization string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	NewWallet(service, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux, tokens)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+authorization)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
