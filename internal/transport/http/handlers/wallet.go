package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	walletservice "github.com/Mopcs/mini-payment-system/internal/service/wallet"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/middleware"
)

const maxWalletRequestBytes = 4 << 10

type WalletService interface {
	List(context.Context, uuid.UUID) ([]domain.Wallet, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (*domain.Wallet, error)
	Create(context.Context, uuid.UUID, string) (*domain.Wallet, error)
	Deposit(context.Context, uuid.UUID, uuid.UUID, int64, string, string) (domain.Deposit, bool, error)
}

type Wallet struct {
	service WalletService
	logger  *slog.Logger
}

type createWalletRequest struct {
	Currency string `json:"currency"`
}

type depositRequest struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type walletResponse struct {
	ID        uuid.UUID `json:"id"`
	Currency  string    `json:"currency"`
	Balance   int64     `json:"balance"`
	CreatedAt time.Time `json:"created_at"`
}

type walletsResponse struct {
	Wallets []walletResponse `json:"wallets"`
}

type depositResponse struct {
	TransactionID uuid.UUID `json:"transaction_id"`
	WalletID      uuid.UUID `json:"wallet_id"`
	Amount        int64     `json:"amount"`
	Currency      string    `json:"currency"`
	Balance       int64     `json:"balance"`
	CreatedAt     time.Time `json:"created_at"`
}

func NewWallet(service WalletService, logger *slog.Logger) *Wallet {
	return &Wallet{service: service, logger: logger}
}

func (h *Wallet) Register(mux *http.ServeMux, tokens middleware.TokenValidator) {
	mux.Handle("GET /api/v1/wallets", middleware.Authenticate(tokens, http.HandlerFunc(h.list)))
	mux.Handle("GET /api/v1/wallets/{id}", middleware.Authenticate(tokens, http.HandlerFunc(h.get)))
	mux.Handle("POST /api/v1/wallets", middleware.Authenticate(tokens, http.HandlerFunc(h.create)))
	mux.Handle("POST /api/v1/wallets/{id}/deposits", middleware.Authenticate(tokens, http.HandlerFunc(h.deposit)))
}

func (h *Wallet) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	wallets, err := h.service.List(r.Context(), userID)
	if err != nil {
		h.logger.Error("list wallets failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
		return
	}
	response := walletsResponse{Wallets: make([]walletResponse, 0, len(wallets))}
	for _, item := range wallets {
		response.Wallets = append(response.Wallets, toWalletResponse(item))
	}
	writeAuthJSON(w, http.StatusOK, response)
}

func (h *Wallet) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	walletID, err := uuid.Parse(r.PathValue("id"))
	if err != nil || walletID == uuid.Nil {
		writeError(w, http.StatusNotFound, "wallet_not_found", "wallet was not found")
		return
	}
	item, err := h.service.Get(r.Context(), userID, walletID)
	if errors.Is(err, walletservice.ErrNotFound) {
		writeError(w, http.StatusNotFound, "wallet_not_found", "wallet was not found")
		return
	}
	if err != nil {
		h.logger.Error("get wallet failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
		return
	}
	writeAuthJSON(w, http.StatusOK, toWalletResponse(*item))
}

func (h *Wallet) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var request createWalletRequest
	if err := decodeWalletRequest(w, r, &request); err != nil {
		writeError(w, err.status, err.code, err.text)
		return
	}
	item, err := h.service.Create(r.Context(), userID, strings.ToUpper(strings.TrimSpace(request.Currency)))
	if errors.Is(err, walletservice.ErrInvalidCurrency) {
		writeError(w, http.StatusBadRequest, "invalid_currency", "currency must be USD, EUR, or JPY")
		return
	}
	if errors.Is(err, walletservice.ErrAlreadyExists) {
		writeError(w, http.StatusConflict, "wallet_already_exists", "wallet already exists for this currency")
		return
	}
	if err != nil {
		h.logger.Error("create wallet failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
		return
	}
	writeAuthJSON(w, http.StatusCreated, toWalletResponse(*item))
}

func (h *Wallet) deposit(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	walletID, err := uuid.Parse(r.PathValue("id"))
	if err != nil || walletID == uuid.Nil {
		writeError(w, http.StatusNotFound, "wallet_not_found", "wallet was not found")
		return
	}
	var request depositRequest
	if err := decodeWalletRequest(w, r, &request); err != nil {
		writeError(w, err.status, err.code, err.text)
		return
	}
	result, replay, err := h.service.Deposit(r.Context(), userID, walletID, request.Amount, request.Currency, r.Header.Get("Idempotency-Key"))
	switch {
	case errors.Is(err, walletservice.ErrInvalidAmount):
		writeError(w, http.StatusBadRequest, "invalid_amount", "amount must be greater than zero")
	case errors.Is(err, walletservice.ErrInvalidCurrency):
		writeError(w, http.StatusBadRequest, "invalid_currency", "currency must be USD, EUR, or JPY")
	case errors.Is(err, walletservice.ErrInvalidIdempotencyKey):
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "a valid Idempotency-Key header is required")
	case errors.Is(err, walletservice.ErrCurrencyMismatch):
		writeError(w, http.StatusBadRequest, "currency_mismatch", "currency does not match wallet")
	case errors.Is(err, walletservice.ErrNotFound):
		writeError(w, http.StatusNotFound, "wallet_not_found", "wallet was not found")
	case errors.Is(err, walletservice.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key was used for a different request")
	case errors.Is(err, walletservice.ErrBalanceLimit):
		writeError(w, http.StatusConflict, "balance_limit", "deposit would exceed the wallet balance limit")
	case err != nil:
		h.logger.Error("deposit failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
	default:
		if replay {
			w.Header().Set("Idempotent-Replay", "true")
		}
		w.Header().Set("Cache-Control", "no-store")
		writeAuthJSON(w, http.StatusCreated, depositResponse{
			TransactionID: result.TransactionID,
			WalletID:      result.WalletID,
			Amount:        result.Amount,
			Currency:      result.Currency,
			Balance:       result.BalanceAfter,
			CreatedAt:     result.CreatedAt,
		})
	}
}

func decodeWalletRequest(w http.ResponseWriter, r *http.Request, target any) *requestDecodeError {
	return decodeJSONRequest(w, r, target, "wallet")
}

func decodeJSONRequest(w http.ResponseWriter, r *http.Request, target any, requestName string) *requestDecodeError {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return &requestDecodeError{status: http.StatusUnsupportedMediaType, code: "unsupported_media_type", text: "content type must be application/json"}
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWalletRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return &requestDecodeError{status: http.StatusRequestEntityTooLarge, code: "request_too_large", text: "request body is too large"}
		}
		return &requestDecodeError{status: http.StatusBadRequest, code: "invalid_request", text: "request body must contain valid " + requestName + " JSON"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &requestDecodeError{status: http.StatusBadRequest, code: "invalid_request", text: "request body must contain exactly one JSON value"}
	}
	return nil
}

func toWalletResponse(wallet domain.Wallet) walletResponse {
	return walletResponse{ID: wallet.ID, Currency: wallet.Currency, Balance: wallet.Balance, CreatedAt: wallet.CreatedAt}
}
