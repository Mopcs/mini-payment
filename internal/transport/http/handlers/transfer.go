package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/middleware"
)

type TransferService interface {
	Transfer(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64, string, string) (domain.Transfer, bool, error)
	List(context.Context, uuid.UUID, domain.TransferHistoryFilter) ([]domain.TransferHistoryItem, int64, error)
}

type Transfers struct {
	service TransferService
	logger  *slog.Logger
}

type createTransferRequest struct {
	SourceWalletID      uuid.UUID `json:"source_wallet_id"`
	DestinationWalletID uuid.UUID `json:"destination_wallet_id"`
	Amount              int64     `json:"amount"`
	Currency            string    `json:"currency"`
}

type transferResponse struct {
	TransferID          uuid.UUID `json:"transfer_id"`
	TransactionID       uuid.UUID `json:"transaction_id"`
	SourceWalletID      uuid.UUID `json:"source_wallet_id"`
	DestinationWalletID uuid.UUID `json:"destination_wallet_id"`
	Amount              int64     `json:"amount"`
	Currency            string    `json:"currency"`
	SourceBalance       int64     `json:"source_balance"`
	DestinationBalance  int64     `json:"destination_balance"`
	CreatedAt           time.Time `json:"created_at"`
}

type transferHistoryResponse struct {
	Items  []domain.TransferHistoryItem `json:"items"`
	Limit  int                          `json:"limit"`
	Offset int                          `json:"offset"`
	Total  int64                        `json:"total"`
}

const maxTransferHistoryOffset = 1000000

func NewTransfers(service TransferService, logger *slog.Logger) *Transfers {
	return &Transfers{service: service, logger: logger}
}

func (h *Transfers) Register(mux *http.ServeMux, tokens middleware.TokenValidator) {
	mux.Handle("POST /api/v1/transfers", middleware.Authenticate(tokens, http.HandlerFunc(h.create)))
	mux.Handle("GET /api/v1/transfers", middleware.Authenticate(tokens, http.HandlerFunc(h.list)))
}

func parseTransferHistoryFilter(r *http.Request) (domain.TransferHistoryFilter, error) {
	filter := domain.TransferHistoryFilter{Limit: 20}
	query := r.URL.Query()
	// Unknown parameters are rejected instead of ignored, otherwise a typo in a
	// filter silently returns unfiltered results.
	for key := range query {
		switch key {
		case "limit", "offset", "direction", "currency":
		default:
			return filter, errors.New("unsupported query parameter")
		}
	}
	for _, key := range []string{"limit", "offset", "direction", "currency"} {
		if values, ok := query[key]; ok && len(values) != 1 {
			return filter, errors.New("query parameter must be provided once")
		}
	}
	if values, ok := query["limit"]; ok {
		value := values[0]
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			return filter, errors.New("limit must be between 1 and 100")
		}
		filter.Limit = limit
	}
	if values, ok := query["offset"]; ok {
		value := values[0]
		offset, err := strconv.Atoi(value)
		if err != nil || offset < 0 || offset > maxTransferHistoryOffset {
			return filter, errors.New("offset is out of range")
		}
		filter.Offset = offset
	}
	if values, ok := query["direction"]; ok {
		filter.Direction = strings.ToLower(strings.TrimSpace(values[0]))
	}
	if _, provided := query["direction"]; provided && filter.Direction != "incoming" && filter.Direction != "outgoing" {
		return filter, errors.New("direction must be incoming or outgoing")
	}
	if values, ok := query["currency"]; ok {
		filter.Currency = strings.ToUpper(strings.TrimSpace(values[0]))
	}
	if _, provided := query["currency"]; provided && filter.Currency != "USD" && filter.Currency != "EUR" && filter.Currency != "JPY" {
		return filter, errors.New("currency must be USD, EUR, or JPY")
	}
	return filter, nil
}

func (h *Transfers) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	filter, err := parseTransferHistoryFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	items, total, err := h.service.List(r.Context(), userID, filter)
	if err != nil {
		h.logger.Error("transfer history failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAuthJSON(w, http.StatusOK, transferHistoryResponse{Items: items, Limit: filter.Limit, Offset: filter.Offset, Total: total})
}

func (h *Transfers) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var request createTransferRequest
	if err := decodeJSONRequest(w, r, &request, "transfer"); err != nil {
		writeError(w, err.status, err.code, err.text)
		return
	}
	result, replay, err := h.service.Transfer(r.Context(), userID, request.SourceWalletID,
		request.DestinationWalletID, request.Amount, request.Currency, r.Header.Get("Idempotency-Key"))
	switch {
	case errors.Is(err, transferservice.ErrInvalidAmount):
		writeError(w, http.StatusBadRequest, "invalid_amount", "amount must be greater than zero")
	case errors.Is(err, transferservice.ErrInvalidCurrency):
		writeError(w, http.StatusBadRequest, "invalid_currency", "currency must be USD, EUR, or JPY")
	case errors.Is(err, transferservice.ErrSameWallet):
		writeError(w, http.StatusBadRequest, "invalid_transfer", "source and destination wallets must differ")
	case errors.Is(err, transferservice.ErrInvalidWalletID):
		writeError(w, http.StatusBadRequest, "invalid_wallet_id", "source and destination wallet IDs are required")
	case errors.Is(err, transferservice.ErrInvalidIdempotencyKey):
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "a valid Idempotency-Key header is required")
	case errors.Is(err, transferservice.ErrNotFound):
		writeError(w, http.StatusNotFound, "wallet_not_found", "wallet was not found")
	case errors.Is(err, transferservice.ErrCurrencyMismatch):
		writeError(w, http.StatusBadRequest, "currency_mismatch", "both wallets must use the requested currency")
	case errors.Is(err, transferservice.ErrInsufficientFunds):
		writeError(w, http.StatusUnprocessableEntity, "insufficient_funds", "source wallet has insufficient funds")
	case errors.Is(err, transferservice.ErrBalanceLimit):
		writeError(w, http.StatusUnprocessableEntity, "balance_limit", "destination wallet balance limit would be exceeded")
	case errors.Is(err, transferservice.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key was used for a different request")
	case err != nil:
		h.logger.Error("transfer failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
	default:
		if replay { // same key and body as an earlier request
			w.Header().Set("Idempotent-Replay", "true")
		}
		w.Header().Set("Cache-Control", "no-store")
		writeAuthJSON(w, http.StatusCreated, transferResponse{
			TransferID:          result.TransactionID,
			TransactionID:       result.TransactionID,
			SourceWalletID:      result.SourceWalletID,
			DestinationWalletID: result.DestinationWalletID,
			Amount:              result.Amount,
			Currency:            result.Currency,
			SourceBalance:       result.SourceBalanceAfter,
			DestinationBalance:  result.DestinationBalanceAfter,
			CreatedAt:           result.CreatedAt,
		})
	}
}
