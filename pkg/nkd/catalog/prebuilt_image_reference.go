package catalog

import (
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// Acquired-image locks hold native subjects, not an authenticated relationship
// between an authored index and its children. Refuse that unsupported protocol
// before resolution rather than discarding an authored digest.
func validateAcquiredImageReference(component, ref string) error {
	if strings.Contains(ref, "{{") {
		// Existing renderRef/checkArchTokens own templates. The maintenance
		// boundary checks the rendered reference again before upstream work.
		if strings.Contains(ref, "@") {
			return e.New(e.CodeUnverifiedRef, "prebuilt image %q: digest-qualified templates require unsupported authenticated index/native membership", component)
		}
		return nil
	}
	parsed, err := name.ParseReference(strings.TrimPrefix(ref, "oci://"))
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "prebuilt image %q reference", component)
	}
	if _, pinned := parsed.(name.Digest); pinned {
		return e.New(e.CodeUnverifiedRef, "prebuilt image %q: digest-qualified authored references require authenticated index/native membership, which is unsupported; native subjects belong in artifact pins", component)
	}
	return nil
}
