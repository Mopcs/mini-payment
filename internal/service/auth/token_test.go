package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const testJWTSecret = "auth-test-signing-secret-at-least-32-bytes"

func TestTokenGenerateAndValidate(t *testing.T) {
	manager, err := NewTokenManager(testJWTSecret, "test-issuer", 15*time.Minute)
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	subject := uuid.New()
	encoded, err := manager.Generate(subject)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	got, err := manager.Validate(encoded)
	if err != nil || got != subject {
		t.Fatalf("Validate() = %s, %v; want %s", got, err, subject)
	}
}

func TestTokenValidationRejectsInvalidSignature(t *testing.T) {
	issuer, _ := NewTokenManager(testJWTSecret, "test-issuer", time.Minute)
	verifier, _ := NewTokenManager("different-test-secret-with-32-bytes", "test-issuer", time.Minute)
	encoded, err := issuer.Generate(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Validate(encoded); err != ErrInvalidToken {
		t.Fatalf("Validate() error = %v, want ErrInvalidToken", err)
	}
}

func TestTokenValidationRejectsExpiredToken(t *testing.T) {
	manager, _ := NewTokenManager(testJWTSecret, "test-issuer", time.Minute)
	now := time.Now().UTC()
	encoded := signTestToken(t, testJWTSecret, jwt.RegisteredClaims{
		Issuer:    "test-issuer",
		Subject:   uuid.NewString(),
		IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(now.Add(-time.Second)),
	})
	if _, err := manager.Validate(encoded); err != ErrInvalidToken {
		t.Fatalf("Validate() error = %v, want ErrInvalidToken", err)
	}
}

func TestTokenValidationRejectsWrongIssuer(t *testing.T) {
	manager, _ := NewTokenManager(testJWTSecret, "expected-issuer", time.Minute)
	now := time.Now().UTC()
	encoded := signTestToken(t, testJWTSecret, jwt.RegisteredClaims{
		Issuer:    "other-issuer",
		Subject:   uuid.NewString(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
	})
	if _, err := manager.Validate(encoded); err != ErrInvalidToken {
		t.Fatalf("Validate() error = %v, want ErrInvalidToken", err)
	}
}

func signTestToken(t *testing.T, secret string, claims jwt.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	encoded, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
