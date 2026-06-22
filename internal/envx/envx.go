// Package envx centralizes environment-variable lookups for Omnia with backward
// compatibility for the pre-rebrand ENGRAM_* names.
//
// Omnia was previously named "Engram". Every tunable used to be read from an
// ENGRAM_* environment variable (ENGRAM_DATA_DIR, ENGRAM_JWT_SECRET, ...). After
// the rebrand the canonical names are OMNIA_*, but existing user setups still
// export the ENGRAM_* names. To keep those setups working, callers ask for the
// new OMNIA_* name and envx transparently falls back to the legacy ENGRAM_*
// equivalent when the new one is unset.
//
// Usage: pass the canonical OMNIA_* name. envx derives the legacy name by
// swapping the OMNIA_ prefix for ENGRAM_.
//
//	envx.Get("OMNIA_DATA_DIR") // reads OMNIA_DATA_DIR, else ENGRAM_DATA_DIR
package envx

import (
	"os"
	"strings"
)

const (
	newPrefix    = "OMNIA_"
	legacyPrefix = "ENGRAM_"
)

// Get returns the value of an OMNIA_* environment variable, falling back to the
// legacy ENGRAM_* equivalent when the new name is unset. It mirrors os.Getenv:
// an unset (or empty-and-no-legacy) variable yields "".
func Get(name string) string {
	v, _ := Lookup(name)
	return v
}

// Lookup mirrors os.LookupEnv with the same OMNIA_* → ENGRAM_* fallback. The
// boolean reports whether either the new or the legacy variable was present in
// the environment (even if set to an empty string).
func Lookup(name string) (string, bool) {
	if v, ok := os.LookupEnv(name); ok {
		return v, true
	}
	if legacy, ok := legacyName(name); ok {
		if v, ok := os.LookupEnv(legacy); ok {
			return v, true
		}
	}
	return "", false
}

// legacyName maps an OMNIA_* name to its ENGRAM_* predecessor. Names that do not
// start with the OMNIA_ prefix have no legacy equivalent.
func legacyName(name string) (string, bool) {
	if strings.HasPrefix(name, newPrefix) {
		return legacyPrefix + strings.TrimPrefix(name, newPrefix), true
	}
	return "", false
}
