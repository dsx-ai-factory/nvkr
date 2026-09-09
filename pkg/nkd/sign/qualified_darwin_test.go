//go:build darwin

package sign

import (
	"strings"
	"testing"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestProductionVerifierFailsDarwinAtConstructionBeforeAnyPathIO(t *testing.T) {
	_, err := NewPinnedCosignVerifier("/does/not/exist/cosign", "sha256:"+strings.Repeat("a", 64))
	if signCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("Darwin stock authority must fail engine-capability before path I/O: %v", err)
	}
}

func TestProductionSignerFailsDarwinAtConstructionBeforeAnyPathIO(t *testing.T) {
	_, err := NewPinnedCosignSigner("/does/not/exist/cosign", "sha256:"+strings.Repeat("a", 64))
	if signCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("Darwin stock signer must fail engine-capability before path/output I/O: %v", err)
	}
}
