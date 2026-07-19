package cloudserver

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// Command Center v2, Slice 1: the operator-facing user-CRUD "create" and
// "reset password" flows generate a strong random password server-side, show
// it to the operator EXACTLY ONCE in the HTTP response, and never persist,
// log, or audit the plaintext — only its bcrypt hash reaches the store.

// generatedPasswordLength is the length (in characters) of an admin-generated
// account password. At ~5.9 bits of entropy per character (64-character
// charset) this yields well over 100 bits total — far beyond the auth
// package's 8-character Signup minimum.
const generatedPasswordLength = 20

// generatedPasswordCharset excludes visually ambiguous characters (0/O,
// 1/l/I) so an operator reading the one-time password aloud or copying it by
// hand doesn't transcribe it wrong.
const generatedPasswordCharset = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%^&*"

// generateStrongPassword returns a cryptographically-secure random password
// of generatedPasswordLength characters drawn from generatedPasswordCharset.
// This is the ONLY place the plaintext one-time admin-generated password is
// ever produced — callers must bcrypt-hash it immediately and must never log,
// audit, or persist the plaintext itself.
func generateStrongPassword() (string, error) {
	return randomStringFromCharset(generatedPasswordLength, generatedPasswordCharset)
}

// randomStringFromCharset draws n characters from charset using crypto/rand,
// rejecting a non-positive length or empty charset outright.
func randomStringFromCharset(n int, charset string) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("cloudserver: password length must be positive")
	}
	if charset == "" {
		return "", fmt.Errorf("cloudserver: charset must not be empty")
	}
	max := big.NewInt(int64(len(charset)))
	out := make([]byte, n)
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("cloudserver: generate random password: %w", err)
		}
		out[i] = charset[idx.Int64()]
	}
	return string(out), nil
}
