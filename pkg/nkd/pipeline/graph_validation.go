package pipeline

import (
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// validateGraphIdentity is the common execution/lowering gate. A recipeDigest is
// authority only for the exact semantically valid graph that produced it; accepting
// a caller-supplied label here would let an unsafe graph masquerade as another recipe.
func validateGraphIdentity(g *ir.Graph, recipeDigest string) error {
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
	if strings.TrimSpace(recipeDigest) == "" || recipeDigest != digest {
		return e.New(e.CodeVerifyFailed,
			"pipeline recipeDigest %q does not match validated graph digest %s", recipeDigest, digest)
	}
	return nil
}
