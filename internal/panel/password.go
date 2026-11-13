package panel

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Password hashes have the form pbkdf2-sha256:<iterations>:<salt>:<hash>, with
// the 16-byte salt and the 32-byte PBKDF2-SHA256 key in unpadded base64url.
const (
	DefaultIterations = 600000
	MinIterations     = 100000
)

const (
	hashScheme = "pbkdf2-sha256"
	saltLen    = 16
	keyLen     = 32
)

var b64 = base64.RawURLEncoding.Strict()

// errHashFormat never quotes the hash, which is a secret.
var errHashFormat = errors.New("password hash must have the form " + hashScheme + ":<iterations>:<salt>:<hash>")

// HashPassword hashes password with a fresh random salt.
func HashPassword(password string, iterations int) (string, error) {
	if iterations < MinIterations {
		return "", fmt.Errorf("iterations must be >= %d, got %d", MinIterations, iterations)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d:%s:%s", hashScheme, iterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the encoded hash, comparing
// in constant time. It fails only when encoded is malformed.
func VerifyPassword(encoded, password string) (bool, error) {
	iterations, salt, want, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ParseHash checks the format of an encoded hash and returns its iteration
// count. Errors never quote the hash.
func ParseHash(encoded string) (iterations int, err error) {
	iterations, _, _, err = parseHash(encoded)
	return iterations, err
}

func parseHash(encoded string) (iterations int, salt, key []byte, err error) {
	parts := strings.Split(encoded, ":")
	if len(parts) != 4 || parts[0] != hashScheme {
		return 0, nil, nil, errHashFormat
	}
	iterations, err = strconv.Atoi(parts[1])
	if err != nil || parts[1] != strconv.Itoa(iterations) {
		return 0, nil, nil, errors.New("password hash iterations must be a decimal number")
	}
	if iterations < MinIterations {
		return 0, nil, nil, fmt.Errorf("password hash iterations must be >= %d, got %d", MinIterations, iterations)
	}
	salt, err = b64.DecodeString(parts[2])
	if err != nil || len(salt) != saltLen {
		return 0, nil, nil, fmt.Errorf("password hash salt must be %d bytes of unpadded base64url", saltLen)
	}
	key, err = b64.DecodeString(parts[3])
	if err != nil || len(key) != keyLen {
		return 0, nil, nil, fmt.Errorf("password hash key must be %d bytes of unpadded base64url", keyLen)
	}
	return iterations, salt, key, nil
}

// CredFingerprint identifies an encoded hash without revealing it: the first
// 16 hex characters of its SHA-256.
func CredFingerprint(encoded string) string {
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])[:16]
}
