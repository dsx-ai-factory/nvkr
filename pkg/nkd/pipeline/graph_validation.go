package pipeline

import (
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// validateGraphIdentity is the common execution gate. A buildDigest is
// authority only for the exact semantically valid graph that produced it; accepting
// a caller-supplied label here would let an unsafe graph masquerade as another build.
func validateGraphIdentity(g *ir.Graph, buildDigest string) error {
	if g == nil {
		return e.New(e.CodeInvalidCatalog, "pipeline requires an IR graph")
	}
	if err := g.Validate(); err != nil {
		return err
	}
	digest, err := g.Digest()
	if err != nil {
		return err
	}
	if strings.TrimSpace(buildDigest) == "" || buildDigest != digest {
		return e.New(e.CodeVerifyFailed,
			"pipeline buildDigest %q does not match validated graph digest %s", buildDigest, digest)
	}
	return nil
}
