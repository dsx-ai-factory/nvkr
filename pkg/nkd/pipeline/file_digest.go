package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
)

// FileDigest returns exact byte integrity for transport and generated files.
// Structured logical identities use the canonical codec and their typed family.
func FileDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
