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
	listResult      []domain.Wallet
	getResult       *domain.Wallet
	createResult    *domain.Wallet
	getError        error
	createError     error
	gotUserID       uuid.UUID
	gotWalletID     uuid.UUID
	gotCurrency     string
	depositResult   domain.Deposit
	depositReplay   bool
	depositError    error
	depositUserID   uuid.UUID
	depositWalletID uuid.UUID
	depositAmount   int64
	depositCurrency string
	depositKey      string
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

func (s *walletServiceStub) Deposit(_ context.Context, userID, walletID uuid.UUID, amount int64, currency, key string) (domain.Deposit, bool, error) {
	s.depositUserID, s.depositWalletID, s.depositAmount = userID, walletID, amount
	s.depositCurrency, s.depositKey = currency, key
	if strings.TrimSpace(key) == "" || len(key) > 128 {
		return domain.Deposit{}, false, walletservice.ErrInvalidIdempotencyKey
	}
	return s.depositResult, s.depositReplay, s.depositError
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

func TestDepositHandlerUsesAuthenticatedOwnerAndIdempotencyKey(t *testing.T) {
	userID, walletID, transactionID := uuid.New(), uuid.New(), uuid.New()
	service := &walletServiceStub{depositResult: domain.Deposit{
		TransactionID: transactionID, WalletID: walletID, Amount: 5000, Currency: "USD", BalanceAfter: 5000,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}
	response := sendDepositRequest(service, tokenValidatorStub{userID: userID}, walletID, `{"amount":5000,"currency":" usd "}`, "valid", "valid")
	if response.Code != http.StatusCreated || service.depositUserID != userID || service.depositWalletID != walletID || service.depositCurrency != " usd " || service.depositAmount != 5000 {
		t.Fatalf("request result: status=%d user=%s wallet=%s amount=%d currency=%q body=%s", response.Code, service.depositUserID, service.depositWalletID, service.depositAmount, service.depositCurrency, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "user_id") || !strings.Contains(response.Body.String(), transactionID.String()) {
		t.Fatalf("unexpected deposit response: %s", response.Body.String())
	}
	if service.depositKey != "valid" {
		t.Fatalf("idempotency key = %q", service.depositKey)
	}

	service.depositReplay = true
	response = sendDepositRequest(service, tokenValidatorStub{userID: userID}, walletID, `{"amount":5000,"currency":"USD"}`, "valid", "valid")
	if response.Code != http.StatusCreated || response.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay response: status=%d headers=%v", response.Code, response.Header())
	}
}

func TestDepositHandlerMapsErrorsAndRejectsMalformedRequests(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		body string
		key  string
		want int
	}{
		{name: "invalid amount", err: walletservice.ErrInvalidAmount, body: `{"amount":0,"currency":"USD"}`, key: "k", want: http.StatusBadRequest},
		{name: "currency mismatch", err: walletservice.ErrCurrencyMismatch, body: `{"amount":1,"currency":"EUR"}`, key: "k", want: http.StatusBadRequest},
		{name: "wallet not found", err: walletservice.ErrNotFound, body: `{"amount":1,"currency":"USD"}`, key: "k", want: http.StatusNotFound},
		{name: "idempotency conflict", err: walletservice.ErrIdempotencyConflict, body: `{"amount":1,"currency":"USD"}`, key: "k", want: http.StatusConflict},
		{name: "missing key", body: `{"amount":1,"currency":"USD"}`, want: http.StatusBadRequest},
		{name: "extra field", body: `{"amount":1,"currency":"USD","balance":10}`, key: "k", want: http.StatusBadRequest},
		{name: "trailing value", body: `{"amount":1,"currency":"USD"} {}`, key: "k", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &walletServiceStub{depositError: test.err}
			response := sendDepositRequest(service, tokenValidatorStub{userID: uuid.New()}, uuid.New(), test.body, "valid", test.key)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func sendWalletRequest(service WalletService, tokens middleware.TokenValidator, method, path, body, contentType, authorization string) *httptest.ResponseRecorder {
	return sendWalletRequestWithKey(service, tokens, method, path, body, contentType, authorization, "")
}

func sendDepositRequest(service WalletService, tokens middleware.TokenValidator, walletID uuid.UUID, body, authorization, idempotencyKey string) *httptest.ResponseRecorder {
	return sendWalletRequestWithKey(service, tokens, http.MethodPost, "/api/v1/wallets/"+walletID.String()+"/deposits", body, "application/json", authorization, idempotencyKey)
}

func sendWalletRequestWithKey(service WalletService, tokens middleware.TokenValidator, method, path, body, contentType, authorization, idempotencyKey string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	NewWallet(service, slog.New(slog.NewTextHandler(io.Discard, nil)), nil).Register(mux, tokens)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+authorization)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
