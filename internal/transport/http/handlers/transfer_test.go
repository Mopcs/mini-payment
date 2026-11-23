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
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/middleware"
)

type transferServiceStub struct {
	result         domain.Transfer
	replay         bool
	err            error
	userID         uuid.UUID
	sourceID       uuid.UUID
	destinationID  uuid.UUID
	amount         int64
	currency       string
	idempotencyKey string
}

func (s *transferServiceStub) Transfer(_ context.Context, userID, sourceID, destinationID uuid.UUID, amount int64, currency, key string) (domain.Transfer, bool, error) {
	s.userID, s.sourceID, s.destinationID = userID, sourceID, destinationID
	s.amount, s.currency, s.idempotencyKey = amount, currency, key
	if strings.TrimSpace(key) == "" || len(key) > 128 {
		return domain.Transfer{}, false, transferservice.ErrInvalidIdempotencyKey
	}
	return s.result, s.replay, s.err
}

func TestTransferHandlerCreatesTransferAndUsesAuthenticatedUser(t *testing.T) {
	userID, sourceID, destinationID, transferID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	service := &transferServiceStub{result: domain.Transfer{
		TransactionID: transferID, SourceWalletID: sourceID, DestinationWalletID: destinationID,
		Amount: 2500, Currency: "USD", SourceBalanceAfter: 7500, DestinationBalanceAfter: 3500,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}
	response := sendTransferRequest(service, tokenValidatorStub{userID: userID}, `{"source_wallet_id":"`+sourceID.String()+`","destination_wallet_id":"`+destinationID.String()+`","amount":2500,"currency":" usd "}`, "request-key", "request-key")
	if response.Code != http.StatusCreated || service.userID != userID || service.sourceID != sourceID || service.destinationID != destinationID || service.amount != 2500 || service.currency != " usd " {
		t.Fatalf("status=%d user=%s source=%s destination=%s amount=%d currency=%q body=%s", response.Code, service.userID, service.sourceID, service.destinationID, service.amount, service.currency, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"transfer_id":"`+transferID.String()+`"`) || strings.Contains(response.Body.String(), "user_id") {
		t.Fatalf("invalid transfer response: %s", response.Body.String())
	}
	if service.idempotencyKey != "request-key" {
		t.Fatalf("idempotency key = %q", service.idempotencyKey)
	}

	service.replay = true
	response = sendTransferRequest(service, tokenValidatorStub{userID: userID}, `{"source_wallet_id":"`+sourceID.String()+`","destination_wallet_id":"`+destinationID.String()+`","amount":2500,"currency":"USD"}`, "request-key", "request-key")
	if response.Code != http.StatusCreated || response.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay response: status=%d headers=%v", response.Code, response.Header())
	}
}

func TestTransferHandlerMapsErrorsAndValidatesRequest(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		body string
		key  string
		want int
	}{
		{name: "invalid amount", err: transferservice.ErrInvalidAmount, body: `{"source_wallet_id":"` + uuid.NewString() + `","destination_wallet_id":"` + uuid.NewString() + `","amount":0,"currency":"USD"}`, key: "k", want: http.StatusBadRequest},
		{name: "same wallet", err: transferservice.ErrSameWallet, body: `{}`, key: "k", want: http.StatusBadRequest},
		{name: "missing wallet IDs", err: transferservice.ErrInvalidWalletID, body: `{}`, key: "k", want: http.StatusBadRequest},
		{name: "wallet missing or not owned", err: transferservice.ErrNotFound, body: `{}`, key: "k", want: http.StatusNotFound},
		{name: "currency mismatch", err: transferservice.ErrCurrencyMismatch, body: `{}`, key: "k", want: http.StatusBadRequest},
		{name: "insufficient funds", err: transferservice.ErrInsufficientFunds, body: `{}`, key: "k", want: http.StatusUnprocessableEntity},
		{name: "overflow", err: transferservice.ErrBalanceLimit, body: `{}`, key: "k", want: http.StatusUnprocessableEntity},
		{name: "idempotency conflict", err: transferservice.ErrIdempotencyConflict, body: `{}`, key: "k", want: http.StatusConflict},
		{name: "missing key", body: `{}`, want: http.StatusBadRequest},
		{name: "unknown balance field", body: `{"source_wallet_id":"` + uuid.NewString() + `","destination_wallet_id":"` + uuid.NewString() + `","amount":5,"currency":"USD","balance":100}`, key: "k", want: http.StatusBadRequest},
		{name: "bad wallet UUID", body: `{"source_wallet_id":"bad","destination_wallet_id":"` + uuid.NewString() + `","amount":5,"currency":"USD"}`, key: "k", want: http.StatusBadRequest},
		{name: "trailing JSON", body: `{ } { }`, key: "k", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &transferServiceStub{err: test.err}
			response := sendTransferRequest(service, tokenValidatorStub{userID: uuid.New()}, test.body, "valid", test.key)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestTransferHandlerRequiresAuthentication(t *testing.T) {
	service := &transferServiceStub{}
	response := sendTransferRequest(service, tokenValidatorStub{err: errors.New("invalid token")}, `{}`, "bad", "key")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", response.Code)
	}
}

func sendTransferRequest(service TransferService, tokens middleware.TokenValidator, body, authorization, key string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	NewTransfers(service, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux, tokens)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/transfers", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+authorization)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
