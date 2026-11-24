package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// errInvalidPasswordHash means the stored hash cannot be parsed, which points at
// a corrupted row rather than at the submitted password.
var errInvalidPasswordHash = errors.New("invalid stored password hash")

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
	// Constant time, so the response doesn't leak how much of the hash matched.
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

type argonParameters struct {
	time    uint32
	memory  uint32
	threads uint8
}

// parsePasswordHash reads the PHC string written by HashPassword. Every field is
// range-checked before it is used, because the value comes from the database.
func parsePasswordHash(encoded string) (argonParameters, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argonParameters{}, nil, nil, errInvalidPasswordHash
	}

	version, err := strconv.ParseUint(strings.TrimPrefix(parts[2], "v="), 10, 32)
	if err != nil || version != uint64(argon2.Version) {
		return argonParameters{}, nil, nil, errInvalidPasswordHash
	}

	params, err := parseArgonParameters(parts[3])
	if err != nil {
		return argonParameters{}, nil, nil, errInvalidPasswordHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return argonParameters{}, nil, nil, errInvalidPasswordHash
	}

	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return argonParameters{}, nil, nil, errInvalidPasswordHash
	}

	return params, salt, expected, nil
}

func parseArgonParameters(encoded string) (argonParameters, error) {
	var params argonParameters

	values := make(map[string]uint64, 3)
	for _, part := range strings.Split(encoded, ",") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return argonParameters{}, errInvalidPasswordHash
		}

		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return argonParameters{}, errInvalidPasswordHash
		}
		if _, exists := values[key]; exists {
			return argonParameters{}, errInvalidPasswordHash
		}

		values[key] = parsed
	}

	// Bounds stop a corrupted or hostile stored hash from asking for gigabytes of
	// memory the next time somebody logs in.
	if len(values) != 3 ||
		values["m"] < 8*1024 || values["m"] > 256*1024 ||
		values["t"] < 1 || values["t"] > 10 ||
		values["p"] < 1 || values["p"] > 8 {
		return argonParameters{}, errInvalidPasswordHash
	}

	params.memory = uint32(values["m"])
	params.time = uint32(values["t"])
	params.threads = uint8(values["p"])
	return params, nil
}

func validPasswordLength(password string) bool {
	return utf8.ValidString(password) && len(password) <= maxPasswordBytes && utf8.RuneCountInString(password) >= minPasswordRunes
}
