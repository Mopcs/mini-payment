package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashAndVerification(t *testing.T) {
	password := "correct horse battery staple"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash does not include the expected Argon2id parameters")
	}
	if strings.Contains(hash, password) {
		t.Fatal("encoded hash contains the plaintext password")
	}

	valid, err := VerifyPassword(password, hash)
	if err != nil || !valid {
		t.Fatalf("VerifyPassword(correct) = %t, %v", valid, err)
	}
	valid, err = VerifyPassword("a different sufficiently long password", hash)
	if err != nil || valid {
		t.Fatalf("VerifyPassword(incorrect) = %t, %v", valid, err)
	}
}

func TestHashPasswordRejectsOutOfRangeInput(t *testing.T) {
	if _, err := HashPassword("short"); err != ErrInvalidPassword {
		t.Fatalf("HashPassword(short) error = %v, want ErrInvalidPassword", err)
	}
	if _, err := HashPassword(strings.Repeat("a", maxPasswordBytes+1)); err != ErrInvalidPassword {
		t.Fatalf("HashPassword(too long) error = %v, want ErrInvalidPassword", err)
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	if _, err := VerifyPassword("some sufficiently long password", "not-a-hash"); err == nil {
		t.Fatal("VerifyPassword() accepted a malformed hash")
	}
}
