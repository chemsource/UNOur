// Package credential owns the shared, versioned password hash format.
package credential

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const Iterations = 600000

func Hash(password string) (string, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, Iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", Iterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}
func parse(encoded string) (int, []byte, []byte, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return 0, nil, nil, false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 600000 || iterations > 2000000 {
		return 0, nil, nil, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) != 32 {
		return 0, nil, nil, false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(expected) != 32 {
		return 0, nil, nil, false
	}
	return iterations, salt, expected, true
}

func ValidHash(encoded string) bool {
	_, _, _, ok := parse(encoded)
	return ok
}

func Verify(encoded, password string) bool {
	iterations, salt, expected, ok := parse(encoded)
	if !ok {
		return false
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	return err == nil && subtle.ConstantTimeCompare(actual, expected) == 1
}
