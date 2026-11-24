package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/dbutil"
	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

const maxEmailBytes = 255

type UserInfo struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
}

type Result struct {
	User        UserInfo
	AccessToken string
	ExpiresIn   int64
}

type Service struct {
	pool   *pgxpool.Pool
	tokens *TokenManager
}

func NewService(pool *pgxpool.Pool, tokens *TokenManager) *Service {
	return &Service{pool: pool, tokens: tokens}
}

func (s *Service) Register(
	ctx context.Context,
	email, password string,
) (result Result, returnedErr error) {
	normalizedEmail, err := normalizeAndValidateEmail(email)
	if err != nil {
		return Result{}, err
	}
	if !validPasswordLength(password) {
		return Result{}, ErrInvalidPassword
	}

	passwordHash, err := HashPassword(password)
	if err != nil {
		return Result{}, fmt.Errorf("hash registration password: %w", err)
	}

	// User and first wallet go in together. A half-registered account would leave
	// someone who can log in but has nowhere to deposit.
	user := &domain.User{ID: uuid.New(), Email: normalizedEmail, PasswordHash: passwordHash}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin registration transaction: %w", err)
	}
	defer dbutil.Rollback(ctx, tx, &returnedErr)

	if err := createUserWithWallet(ctx, tx, user); err != nil {
		return Result{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit registration transaction: %w", err)
	}

	return s.issueResult(user)
}

// createUserWithWallet inserts the account and its default wallet inside the
// caller's transaction.
func createUserWithWallet(ctx context.Context, tx pgx.Tx, user *domain.User) error {
	if err := postgres.NewUserRepository(tx).Create(ctx, user); err != nil {
		if errors.Is(err, postgres.ErrEmailAlreadyExists) {
			return ErrEmailAlreadyRegistered
		}
		return fmt.Errorf("create registration user: %w", err)
	}

	wallet := &domain.Wallet{ID: uuid.New(), UserID: user.ID, Currency: domain.USD, Balance: 0}
	if err := postgres.NewWalletRepository(tx).Create(ctx, wallet); err != nil {
		return fmt.Errorf("create initial wallet: %w", err)
	}
	return nil
}

func (s *Service) Login(ctx context.Context, email, password string) (Result, error) {
	normalizedEmail, err := normalizeAndValidateEmail(email)
	if err != nil || !validPasswordLength(password) {
		return Result{}, ErrInvalidCredentials
	}
	user, err := postgres.NewUserRepository(s.pool).GetByEmail(ctx, normalizedEmail)
	if errors.Is(err, postgres.ErrNotFound) {
		return Result{}, ErrInvalidCredentials
	}
	if err != nil {
		return Result{}, fmt.Errorf("load login user: %w", err)
	}
	valid, err := VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return Result{}, fmt.Errorf("verify stored password hash: %w", err)
	}
	// Same error as an unknown email, so login never confirms which addresses
	// are registered.
	if !valid {
		return Result{}, ErrInvalidCredentials
	}
	return s.issueResult(user)
}

func (s *Service) issueResult(user *domain.User) (Result, error) {
	token, err := s.tokens.Generate(user.ID)
	if err != nil {
		return Result{}, fmt.Errorf("generate access token: %w", err)
	}
	return Result{
		User:        UserInfo{ID: user.ID, Email: user.Email},
		AccessToken: token,
		ExpiresIn:   s.tokens.ExpiresIn(),
	}, nil
}

func normalizeAndValidateEmail(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if len(normalized) == 0 || len(normalized) > maxEmailBytes {
		return "", ErrInvalidEmail
	}
	parsed, err := mail.ParseAddress(normalized)
	if err != nil || parsed.Address != normalized {
		return "", ErrInvalidEmail
	}
	return normalized, nil
}
