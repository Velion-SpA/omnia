package repodoc

import (
	"crypto/sha1" //nolint:gosec // content-addressing fingerprint, not a security use — see doc comment.
	"encoding/hex"
	"fmt"
)

// gitBlobHash computes the git blob object ID for content:
// sha1("blob " + len(content) + "\x00" + content) — git's own
// content-addressing algorithm.
//
// Computing it in pure Go, instead of shelling out to `git hash-object`,
// means the per-file change-detection cursor (see Source.Fetch) works even
// when `git` is not on PATH, honoring this package's local-first stance for
// its critical no-op-detection path. It still produces a value that is
// byte-for-byte identical to what `git hash-object <file>` prints for the
// same bytes (verified in hash_test.go against real `git hash-object`
// output), so the cursor genuinely is "the git blob SHA" the plan asks for,
// not a look-alike hash that happens to serve the same purpose.
//
// SHA-1's cryptographic weaknesses are irrelevant here: this is a
// change-detection fingerprint, not a security boundary, and it
// deliberately uses the exact algorithm git itself uses for the same job —
// diverging from it (e.g. using sha256) would stop values from lining up
// with a real `git hash-object` invocation for no benefit.
func gitBlobHash(content []byte) string {
	header := fmt.Sprintf("blob %d\x00", len(content))
	h := sha1.New() //nolint:gosec // see doc comment above
	h.Write([]byte(header))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
