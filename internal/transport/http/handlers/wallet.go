package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/observability"
	walletservice "github.com/Mopcs/mini-payment-system/internal/service/wallet"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/middleware"
)

const maxJSONBodyBytes = 4 << 10

type WalletService interface {
	List(context.Context, uuid.UUID) ([]domain.Wallet, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (*domain.Wallet, error)
	Create(context.Context, uuid.UUID, string) (*domain.Wallet, error)
	Deposit(context.Context, uuid.UUID, uuid.UUID, int64, string, string) (domain.Deposit, bool, error)
}

type Wallet struct {
	service WalletService
	logger  *slog.Logger
	auditor *observability.Auditor
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

func NewWallet(service WalletService, logger *slog.Logger, auditor *observability.Auditor) *Wallet {
	return &Wallet{service: service, logger: logger, auditor: auditor}
}

func (h *Wallet) Register(mux *http.ServeMux, tokens middleware.TokenValidator) {
	mux.Handle(
		"GET /api/v1/wallets",
		middleware.Authenticate(tokens, h.auditor, http.HandlerFunc(h.list)),
	)
	mux.Handle(
		"GET /api/v1/wallets/{id}",
		middleware.Authenticate(tokens, h.auditor, http.HandlerFunc(h.get)),
	)
	mux.Handle(
		"POST /api/v1/wallets",
		middleware.Authenticate(tokens, h.auditor, http.HandlerFunc(h.create)),
	)
	mux.Handle(
		"POST /api/v1/wallets/{id}/deposits",
		middleware.Authenticate(tokens, h.auditor, http.HandlerFunc(h.deposit)),
	)
}

func (h *Wallet) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}

	wallets, err := h.service.List(r.Context(), userID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "list wallets failed", slog.Any("error", err))
		internalError(w)
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
		unauthorized(w)
		return
	}

	walletID, ok := parseWalletID(w, r)
	if !ok {
		return
	}

	item, err := h.service.Get(r.Context(), userID, walletID)
	if errors.Is(err, walletservice.ErrNotFound) {
		h.auditor.Event(r.Context(), "security.wallet_access_denied",
			slog.String("wallet_id", walletID.String()),
			slog.String("operation", "read"))
		walletNotFound(w)
		return
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "get wallet failed", slog.Any("error", err))
		internalError(w)
		return
	}

	writeAuthJSON(w, http.StatusOK, toWalletResponse(*item))
}

func (h *Wallet) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}

	var request createWalletRequest
	if err := decodeJSONRequest(w, r, &request, "wallet", maxJSONBodyBytes); err != nil {
		writeError(w, err.status, err.code, err.text)
		return
	}

	item, err := h.service.Create(r.Context(), userID, domain.NormalizeCurrency(request.Currency))
	if err != nil {
		h.writeCreateWalletError(r.Context(), w, err)
		return
	}

	writeAuthJSON(w, http.StatusCreated, toWalletResponse(*item))
}

func (h *Wallet) deposit(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}

	walletID, ok := parseWalletID(w, r)
	if !ok {
		return
	}

	var request depositRequest
	if err := decodeJSONRequest(w, r, &request, "wallet", maxJSONBodyBytes); err != nil {
		writeError(w, err.status, err.code, err.text)
		return
	}

	result, replay, err := h.service.Deposit(
		r.Context(), userID, walletID,
		request.Amount, request.Currency,
		r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		h.writeDepositError(r.Context(), w, err)
		return
	}

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

// writeCreateWalletError maps service errors to the API error contract.
func (h *Wallet) writeCreateWalletError(
	ctx context.Context,
	w http.ResponseWriter,
	err error,
) {
	switch {
	case errors.Is(err, walletservice.ErrInvalidCurrency):
		badRequest(w, "invalid_currency", "currency must be USD, EUR, or JPY")
	case errors.Is(err, walletservice.ErrAlreadyExists):
		conflict(w, "wallet_already_exists", "wallet already exists for this currency")
	default:
		h.logger.ErrorContext(ctx, "create wallet failed", slog.Any("error", err))
		internalError(w)
	}
}

// writeDepositError maps service errors to the API error contract.
func (h *Wallet) writeDepositError(
	ctx context.Context,
	w http.ResponseWriter,
	err error,
) {
	failure := depositFailure(err)
	if failure.status == http.StatusInternalServerError {
		h.logger.ErrorContext(ctx, "deposit failed", slog.Any("error", err))
	}
	writeError(w, failure.status, failure.code, failure.message)

	// Amounts and idempotency keys stay out of the audit trail.
	h.auditor.Event(ctx, "security.financial_request_rejected",
		slog.String("operation", "deposit"),
		slog.String("reason", failure.code))
}

// depositFailure is the single mapping from service error to response.
func depositFailure(err error) apiFailure {
	switch {
	case errors.Is(err, walletservice.ErrInvalidAmount):
		return newFailure(http.StatusBadRequest, "invalid_amount", "amount must be greater than zero")
	case errors.Is(err, walletservice.ErrInvalidCurrency):
		return newFailure(http.StatusBadRequest, "invalid_currency", "currency must be USD, EUR, or JPY")
	case errors.Is(err, walletservice.ErrInvalidIdempotencyKey):
		return newFailure(http.StatusBadRequest, "invalid_idempotency_key",
			"a valid Idempotency-Key header is required")
	case errors.Is(err, walletservice.ErrCurrencyMismatch):
		return newFailure(http.StatusBadRequest, "currency_mismatch", "currency does not match wallet")
	case errors.Is(err, walletservice.ErrNotFound):
		return newFailure(http.StatusNotFound, "wallet_not_found", "wallet was not found")
	case errors.Is(err, walletservice.ErrIdempotencyConflict):
		return newFailure(http.StatusConflict, "idempotency_conflict",
			"idempotency key was used for a different request")
	case errors.Is(err, walletservice.ErrBalanceLimit):
		return newFailure(http.StatusConflict, "balance_limit",
			"deposit would exceed the wallet balance limit")
	default:
		return newFailure(http.StatusInternalServerError, "internal_error",
			"request could not be completed")
	}
}

// parseWalletID reads the {id} path segment and writes a 404 when it is not a
// usable UUID. The handler returns early when ok is false.
func parseWalletID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	walletID, err := uuid.Parse(r.PathValue("id"))
	if err != nil || walletID == uuid.Nil {
		walletNotFound(w)
		return uuid.Nil, false
	}
	return walletID, true
}

// decodeJSONRequest reads exactly one JSON value of the given shape, rejecting
// unknown fields and trailing data.
func decodeJSONRequest(
	w http.ResponseWriter,
	r *http.Request,
	target any,
	requestName string,
	maxBytes int64,
) *requestDecodeError {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return &requestDecodeError{
			status: http.StatusUnsupportedMediaType,
			code:   "unsupported_media_type",
			text:   "content type must be application/json",
		}
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return &requestDecodeError{
				status: http.StatusRequestEntityTooLarge,
				code:   "request_too_large",
				text:   "request body is too large",
			}
		}
		return &requestDecodeError{
			status: http.StatusBadRequest,
			code:   "invalid_request",
			text:   "request body must contain valid " + requestName + " JSON",
		}
	}

	// The second decode has to hit EOF; anything else is trailing garbage.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &requestDecodeError{
			status: http.StatusBadRequest,
			code:   "invalid_request",
			text:   "request body must contain exactly one JSON value",
		}
	}
	return nil
}

func toWalletResponse(wallet domain.Wallet) walletResponse {
	return walletResponse{
		ID:        wallet.ID,
		Currency:  wallet.Currency,
		Balance:   wallet.Balance,
		CreatedAt: wallet.CreatedAt,
	}
}
