package auth

import (
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type TokenManager struct {
	secret []byte
	issuer string
	ttl    time.Duration
}

func NewTokenManager(secret, issuer string, ttl time.Duration) (*TokenManager, error) {
	if len([]byte(secret)) < 32 {
		return nil, errors.New("JWT secret must be at least 32 bytes")
	}
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return nil, errors.New("JWT issuer must not be empty")
	}
	if ttl < time.Second || ttl > 24*time.Hour {
		return nil, errors.New("JWT access token TTL must be between 1s and 24h")
	}
	return &TokenManager{secret: []byte(secret), issuer: issuer, ttl: ttl}, nil
}

func (m *TokenManager) Generate(subject uuid.UUID) (string, error) {
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer:    m.issuer,
		Subject:   subject.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

func (m *TokenManager) Validate(encoded string) (uuid.UUID, error) {
	claims := new(jwt.RegisteredClaims)
	token, err := jwt.ParseWithClaims(encoded, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, ErrInvalidToken
		}
		return m.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(m.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil ||
		token == nil ||
		!token.Valid ||
		claims.IssuedAt == nil ||
		claims.ExpiresAt == nil {
		return uuid.Nil, ErrInvalidToken
	}
	subject, err := uuid.Parse(claims.Subject)
	if err != nil || subject == uuid.Nil {
		return uuid.Nil, ErrInvalidToken
	}
	return subject, nil
}

func (m *TokenManager) ExpiresIn() int64 {
	return int64(m.ttl.Seconds())
}
