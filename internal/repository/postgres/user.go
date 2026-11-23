package postgres

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

type UserRepository struct {
	pool *pgxpool.Pool
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

func (r *UserRepository) Create(ctx context.Context, user *domain.User) error {
	if user == nil {
		return ErrInvalidInput
	}
	if user.ID == uuid.Nil {
		user.ID = uuid.New()
	}
	user.Email = normalizeEmail(user.Email)

	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (id, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id, email, password_hash, created_at, updated_at`,
		user.ID, user.Email, user.PasswordHash,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.CreatedAt, &user.UpdatedAt)
	return translateError("create user", err)
}

func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	user := new(domain.User)
	err := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, created_at, updated_at
		FROM users
		WHERE id = $1`, id,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, translateError("get user by id", err)
	}
	return user, nil
}

func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	user := new(domain.User)
	err := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, created_at, updated_at
		FROM users
		WHERE email = $1`, normalizeEmail(email),
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, translateError("get user by email", err)
	}
	return user, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
