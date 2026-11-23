package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime        uint32 = 3
	argonMemory      uint32 = 64 * 1024
	argonThreads     uint8  = 2
	argonSaltBytes          = 16
	argonKeyBytes           = 32
	minPasswordRunes        = 12
	maxPasswordBytes        = 1024
)

func HashPassword(password string) (string, error) {
	if !validPasswordLength(password) {
		return "", ErrInvalidPassword
	}

	salt := make([]byte, argonSaltBytes)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(password, encodedHash string) (bool, error) {
	if len(password) > maxPasswordBytes || !utf8.ValidString(password) {
		return false, ErrInvalidPassword
	}
	params, salt, expected, err := parsePasswordHash(encodedHash)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

type argonParameters struct {
	time    uint32
	memory  uint32
	threads uint8
}

func parsePasswordHash(encoded string) (argonParameters, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argonParameters{}, nil, nil, errorsInvalidPasswordHash()
	}

	version, err := strconv.ParseUint(strings.TrimPrefix(parts[2], "v="), 10, 32)
	if err != nil || version != uint64(argon2.Version) {
		return argonParameters{}, nil, nil, errorsInvalidPasswordHash()
	}
	params, err := parseArgonParameters(parts[3])
	if err != nil {
		return argonParameters{}, nil, nil, errorsInvalidPasswordHash()
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return argonParameters{}, nil, nil, errorsInvalidPasswordHash()
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return argonParameters{}, nil, nil, errorsInvalidPasswordHash()
	}
	return params, salt, expected, nil
}

func parseArgonParameters(encoded string) (argonParameters, error) {
	var params argonParameters
	values := make(map[string]uint64, 3)
	for _, part := range strings.Split(encoded, ",") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return argonParameters{}, errorsInvalidPasswordHash()
		}
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return argonParameters{}, errorsInvalidPasswordHash()
		}
		if _, exists := values[key]; exists {
			return argonParameters{}, errorsInvalidPasswordHash()
		}
		values[key] = parsed
	}
	if len(values) != 3 || values["m"] < 8*1024 || values["m"] > 256*1024 || values["t"] < 1 || values["t"] > 10 || values["p"] < 1 || values["p"] > 8 {
		return argonParameters{}, errorsInvalidPasswordHash()
	}
	params.memory = uint32(values["m"])
	params.time = uint32(values["t"])
	params.threads = uint8(values["p"])
	return params, nil
}

func validPasswordLength(password string) bool {
	return utf8.ValidString(password) && len(password) <= maxPasswordBytes && utf8.RuneCountInString(password) >= minPasswordRunes
}

func errorsInvalidPasswordHash() error {
	return fmt.Errorf("invalid stored password hash")
}
