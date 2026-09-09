package main

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/urfave/cli/v2"
)

// trustStoreFile is the operator's on-disk trust store: the identities the operator
// has explicitly promoted to trusted (ADR-04 §2). It is deliberately OUTSIDE any
// verified object — trust must originate from outside what it authenticates.
type trustStoreFile struct {
	Identities            []string `json:"identities"`
	ProfileIdentities     []string `json:"profileIdentities,omitempty"`
	ConformanceIdentities []string `json:"conformanceIdentities,omitempty"`
	StatusIdentities      []string `json:"statusIdentities,omitempty"`
	PromotionIdentities   []string `json:"promotionPolicyIdentities,omitempty"`
	ReleaseSetIdentities  []string `json:"releaseSetIdentities,omitempty"`
	// StatusSigner is the v1 single-identity spelling. It is accepted only as
	// local policy input and is folded into StatusIdentities; it is never treated
	// as evidence that a document was authenticated.
	StatusSigner    string `json:"statusSigner,omitempty"`
	PromoteAdvisory bool   `json:"promoteAdvisory,omitempty"`
}

func trustStorePath(c *cli.Context) string {
	if p := c.String("trust-store"); p != "" {
		return p
	}
	return filepath.Join(".nkd", "trust.json")
}

func loadTrustStoreFile(ctx context.Context, path string) (trustStoreFile, error) {
	var s trustStoreFile
	if err := statusContextError(ctx, "reading trust store"); err != nil {
		return s, err
	}
	err := readCanonicalJSONContext(ctx, path, &s)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil // an absent store is empty, not an error
	}
	if err != nil {
		var coded *e.Error
		if errors.As(err, &coded) && (coded.Code == e.CodeTimeout || coded.Code == e.CodeUsage) {
			return s, err
		}
		return s, e.Wrap(e.CodeTrustPolicyMissing, err, "reading canonical trust store %s", path)
	}
	return s, nil
}

// normalizedRoleIdentities turns operator configuration into a stable, non-empty
// trust-policy input. It deliberately performs no evidence verification; role
// authorization remains the responsibility of trust.DocumentAuthenticator.
func normalizedRoleIdentities(values []string, label string) ([]string, error) {
	identities := append([]string(nil), values...)
	for index := range identities {
		identities[index] = strings.TrimSpace(identities[index])
	}
	sort.Strings(identities)
	unique := identities[:0]
	for _, identity := range identities {
		if identity == "" {
			return nil, e.New(e.CodeTrustPolicyMissing, "%s trust policy contains an empty identity", label)
		}
		if len(unique) == 0 || unique[len(unique)-1] != identity {
			unique = append(unique, identity)
		}
	}
	if len(unique) == 0 {
		return nil, e.New(e.CodeTrustPolicyMissing, "%s requires at least one authorized signer identity", label)
	}
	return append([]string(nil), unique...), nil
}

// The trust store has no dedicated public lifecycle verb; Verification Policy supplies
// consumer trust constraints. These helpers back verify's ADR-04 §2 anchor resolution.
