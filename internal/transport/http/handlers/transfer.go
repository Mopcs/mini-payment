package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/observability"
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/middleware"
)

const (
	defaultTransferHistoryLimit = 20
	maxTransferHistoryLimit     = 100
	maxTransferHistoryOffset    = 1000000
)

type TransferService interface {
	Transfer(
		context.Context,
		uuid.UUID, uuid.UUID, uuid.UUID,
		int64,
		string, string,
	) (domain.Transfer, bool, error)
	List(
		context.Context,
		uuid.UUID,
		domain.TransferHistoryFilter,
	) ([]domain.TransferHistoryItem, int64, error)
}

type Transfers struct {
	service TransferService
	logger  *slog.Logger
	auditor *observability.Auditor
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

func NewTransfers(
	service TransferService,
	logger *slog.Logger,
	auditor *observability.Auditor,
) *Transfers {
	return &Transfers{service: service, logger: logger, auditor: auditor}
}

func (h *Transfers) Register(mux *http.ServeMux, tokens middleware.TokenValidator) {
	mux.Handle(
		"POST /api/v1/transfers",
		middleware.Authenticate(tokens, h.auditor, http.HandlerFunc(h.create)),
	)
	mux.Handle(
		"GET /api/v1/transfers",
		middleware.Authenticate(tokens, h.auditor, http.HandlerFunc(h.list)),
	)
}

func (h *Transfers) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}

	filter, err := parseTransferHistoryFilter(r)
	if err != nil {
		badRequest(w, "invalid_query", err.Error())
		return
	}

	items, total, err := h.service.List(r.Context(), userID, filter)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "transfer history failed", slog.Any("error", err))
		internalError(w)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeAuthJSON(w, http.StatusOK, transferHistoryResponse{
		Items:  items,
		Limit:  filter.Limit,
		Offset: filter.Offset,
		Total:  total,
	})
}

func (h *Transfers) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}

	var request createTransferRequest
	if err := decodeJSONRequest(w, r, &request, "transfer", maxJSONBodyBytes); err != nil {
		writeError(w, err.status, err.code, err.text)
		return
	}

	result, replay, err := h.service.Transfer(
		r.Context(), userID,
		request.SourceWalletID, request.DestinationWalletID,
		request.Amount, request.Currency,
		r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		h.writeTransferError(r.Context(), w, err)
		return
	}

	if replay {
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

// writeTransferError maps service errors to the API error contract.
func (h *Transfers) writeTransferError(
	ctx context.Context,
	w http.ResponseWriter,
	err error,
) {
	failure := transferFailure(err)
	if failure.status == http.StatusInternalServerError {
		h.logger.ErrorContext(ctx, "transfer failed", slog.Any("error", err))
	}
	writeError(w, failure.status, failure.code, failure.message)

	h.auditor.Event(ctx, "security.financial_request_rejected",
		slog.String("operation", "transfer"),
		slog.String("reason", failure.code))
}

// transferFailure is the single mapping from service error to response, so the
// client answer and the audit reason can never disagree.
func transferFailure(err error) apiFailure {
	switch {
	case errors.Is(err, transferservice.ErrInvalidAmount):
		return newFailure(http.StatusBadRequest, "invalid_amount", "amount must be greater than zero")
	case errors.Is(err, transferservice.ErrInvalidCurrency):
		return newFailure(http.StatusBadRequest, "invalid_currency", "currency must be USD, EUR, or JPY")
	case errors.Is(err, transferservice.ErrSameWallet):
		return newFailure(http.StatusBadRequest, "invalid_transfer",
			"source and destination wallets must differ")
	case errors.Is(err, transferservice.ErrInvalidWalletID):
		return newFailure(http.StatusBadRequest, "invalid_wallet_id",
			"source and destination wallet IDs are required")
	case errors.Is(err, transferservice.ErrInvalidIdempotencyKey):
		return newFailure(http.StatusBadRequest, "invalid_idempotency_key",
			"a valid Idempotency-Key header is required")
	case errors.Is(err, transferservice.ErrNotFound):
		return newFailure(http.StatusNotFound, "wallet_not_found", "wallet was not found")
	case errors.Is(err, transferservice.ErrCurrencyMismatch):
		return newFailure(http.StatusBadRequest, "currency_mismatch",
			"both wallets must use the requested currency")
	case errors.Is(err, transferservice.ErrInsufficientFunds):
		return newFailure(http.StatusUnprocessableEntity, "insufficient_funds",
			"source wallet has insufficient funds")
	case errors.Is(err, transferservice.ErrBalanceLimit):
		return newFailure(http.StatusUnprocessableEntity, "balance_limit",
			"destination wallet balance limit would be exceeded")
	case errors.Is(err, transferservice.ErrIdempotencyConflict):
		return newFailure(http.StatusConflict, "idempotency_conflict",
			"idempotency key was used for a different request")
	default:
		return newFailure(http.StatusInternalServerError, "internal_error",
			"request could not be completed")
	}
}

func parseTransferHistoryFilter(r *http.Request) (domain.TransferHistoryFilter, error) {
	query := r.URL.Query()
	if err := validateQueryKeys(query, "limit", "offset", "direction", "currency"); err != nil {
		return domain.TransferHistoryFilter{}, err
	}

	filter := domain.TransferHistoryFilter{Limit: defaultTransferHistoryLimit}

	if value, ok := query["limit"]; ok {
		limit, err := strconv.Atoi(value[0])
		if err != nil || limit < 1 || limit > maxTransferHistoryLimit {
			return domain.TransferHistoryFilter{}, errors.New("limit must be between 1 and 100")
		}
		filter.Limit = limit
	}

	if value, ok := query["offset"]; ok {
		offset, err := strconv.Atoi(value[0])
		if err != nil || offset < 0 || offset > maxTransferHistoryOffset {
			return domain.TransferHistoryFilter{}, errors.New("offset is out of range")
		}
		filter.Offset = offset
	}

	if value, ok := query["direction"]; ok {
		filter.Direction = strings.ToLower(strings.TrimSpace(value[0]))
		if filter.Direction != "incoming" && filter.Direction != "outgoing" {
			return domain.TransferHistoryFilter{}, errors.New("direction must be incoming or outgoing")
		}
	}

	if value, ok := query["currency"]; ok {
		filter.Currency = domain.NormalizeCurrency(value[0])
		if !domain.IsSupportedCurrency(filter.Currency) {
			return domain.TransferHistoryFilter{}, errors.New("currency must be USD, EUR, or JPY")
		}
	}

	return filter, nil
}

// validateQueryKeys rejects unknown and repeated parameters. Ignoring them would
// silently return unfiltered results.
func validateQueryKeys(query url.Values, allowed ...string) error {
	for key, values := range query {
		if !slices.Contains(allowed, key) {
			return errors.New("unsupported query parameter")
		}
		if len(values) != 1 {
			return errors.New("query parameter must be provided once")
		}
	}
	return nil
}
