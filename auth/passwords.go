package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP recommendation: 19 MiB, 2 iterations, 1 lane).
const (
	argonMemory     = 19 * 1024
	argonIterations = 2
	argonThreads    = 1
	argonKeyLength  = 32
	argonSaltLength = 16
)

const unusablePassword = "!"

// HashPassword returns an encoded argon2id hash: argon2id$v=19$m=..,t=..,p=..$salt$hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonThreads, argonKeyLength)
	encoding := base64.RawStdEncoding
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonIterations, argonThreads,
		encoding.EncodeToString(salt), encoding.EncodeToString(key)), nil
}

// CheckPassword verifies a password against an encoded hash in constant time.
func CheckPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	var version int
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[1], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// SetPassword hashes and stores a password (the user must still be saved).
func (u *User) SetPassword(password string) error {
	hashed, err := HashPassword(password)
	if err != nil {
		return err
	}
	u.Password = hashed
	return nil
}

// SetUnusablePassword prevents password logins.
func (u *User) SetUnusablePassword() { u.Password = unusablePassword }

// CheckPassword reports whether password matches the stored hash.
func (u *User) CheckPassword(password string) bool { return CheckPassword(password, u.Password) }

// HasUsablePassword reports whether a password is set.
func (u *User) HasUsablePassword() bool { return strings.HasPrefix(u.Password, "argon2id$") }
