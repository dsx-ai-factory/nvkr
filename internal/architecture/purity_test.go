// Package architecture hosts the ADR-02 §3 STATIC purity guard: an automated check that
// the identity-producing code paths contain no nondeterministic source — wall-clock
// reads, randomness, UUID generation, hostname, or host-environment reads — because a
// leak there would make buildDigest host-dependent and collapse the trust model. It
// is the static half; the empirical invariance test lives in pkg/nkd/ir
// (TestDigestHostInvariance). A deliberate exception is allowlisted only with an
// inline `//nkd:allow-nondeterminism` note on the offending line, forcing a
// justification into the diff.
package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// identityPaths are the packages whose output flows into a digest-addressed identity
// (buildDigest / builder-profile / trust objects). They MUST be pure.
var identityPaths = []string{"ir", "buildgraph", "canonical", "target"}

// forbidden are substrings that indicate a nondeterministic source. Kept as source
// substrings (not full AST) so the guard is cheap and dependency-free.
var forbidden = []string{
	"time.Now(",
	"time.Since(",
	"os.Hostname(",
	"os.Getenv(",
	"os.Environ(",
	"math/rand",
	"crypto/rand",
	"rand.Read(",
	"uuid.New",
	"uuid.NewString",
}

const allowMarker = "//nkd:allow-nondeterminism"

func TestIdentityPathsArePure(t *testing.T) {
	for _, pkg := range identityPaths {
		dir := filepath.Join("..", "..", "pkg", "nkd", pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, ent := range entries {
			name := ent.Name()
			if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			assertPureSource(t, path, b)
		}
	}
}

func assertPureSource(t *testing.T, path string, b []byte) {
	t.Helper()
	for i, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, allowMarker) {
			continue // explicitly justified exception
		}
		// Ignore import lines that merely name a std pkg in a comment; the
		// substrings above are specific enough (e.g. "math/rand") that a real
		// import or call is what trips them.
		for _, bad := range forbidden {
			if strings.Contains(line, bad) {
				t.Errorf("%s:%d: nondeterministic call %q in an identity path (add %s with a justification if truly deterministic)\n\t%s",
					path, i+1, bad, allowMarker, strings.TrimSpace(line))
			}
		}
	}
}
