// Package trust owns signer policy and authenticated document receipts (ADR-04).
// It remains independent of the build engine and artifact attestation packages so
// verification policy cannot invoke a builder.
package trust

import (
	"sort"
	"strings"
)

// Store is the trust anchor set with ADR-04 §2 precedence: CLI flags > operator trust
// store > catalog-advisory. A catalog-declared identity is ADVISORY ONLY — it can
// seed a listing but is never auto-trusted (else a tampered catalog self-authenticates).
type Store struct {
	CLIIdentities      []string // highest precedence (operator passed them explicitly)
	OperatorIdentities []string // the operator's own trust store
	CatalogAdvisory    []string // advisory only — NOT trusted unless promoted
	PromoteAdvisory    bool     // an explicit operator action promotes advisory → trusted
}

// Trusted resolves the effective trusted identity set and the source it came from,
// honoring precedence. Catalog-advisory contributes only when PromoteAdvisory is set.
func (s Store) Trusted() (ids []string, source string) {
	switch {
	case len(s.CLIIdentities) > 0:
		return dedupeSort(s.CLIIdentities), "cli"
	case len(s.OperatorIdentities) > 0:
		return dedupeSort(s.OperatorIdentities), "operator"
	case s.PromoteAdvisory && len(s.CatalogAdvisory) > 0:
		return dedupeSort(s.CatalogAdvisory), "catalog-advisory(promoted)"
	default:
		return nil, "none"
	}
}

func dedupeSort(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
