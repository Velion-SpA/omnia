package cloudserver

import "testing"

// TestGenerateStrongPasswordLengthAndCharset verifies the generated password
// has the expected length and is drawn entirely from the safe, unambiguous
// charset (excludes 0/O, 1/l/I so an operator transcribing it by hand can't
// misread it).
func TestGenerateStrongPasswordLengthAndCharset(t *testing.T) {
	pw, err := generateStrongPassword()
	if err != nil {
		t.Fatalf("generate strong password: %v", err)
	}
	if len(pw) != generatedPasswordLength {
		t.Fatalf("expected length %d, got %d (%q)", generatedPasswordLength, len(pw), pw)
	}
	for _, c := range pw {
		found := false
		for _, allowed := range generatedPasswordCharset {
			if c == allowed {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("password %q contains character %q outside the allowed charset", pw, c)
		}
	}
	for _, ambiguous := range []rune{'0', 'O', '1', 'l', 'I'} {
		for _, c := range pw {
			if c == ambiguous {
				t.Fatalf("password %q must not contain visually ambiguous character %q", pw, ambiguous)
			}
		}
	}
}

// TestGenerateStrongPasswordIsRandomAcrossCalls triangulates the happy path:
// two consecutive generations must not produce the same value (statistically
// certain given the charset size and length — this proves the function calls
// a real random source, not a hardcoded fake).
func TestGenerateStrongPasswordIsRandomAcrossCalls(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		pw, err := generateStrongPassword()
		if err != nil {
			t.Fatalf("generate strong password: %v", err)
		}
		if seen[pw] {
			t.Fatalf("generated the same password twice across 20 calls: %q", pw)
		}
		seen[pw] = true
	}
}
