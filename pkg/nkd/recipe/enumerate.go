package recipe

import (
	"errors"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// ResolvableRecipe is one concrete category recipe in an explicit resolution frame.
// It is an inspection projection, not an authority object: RecipeDigest remains the
// canonical identity and Components names the complete category set before role/SKU
// filtering.
type ResolvableRecipe struct {
	RecipeID            string             `json:"recipeId"`
	RecipeDigest        string             `json:"recipeDigest"`
	Criteria            criteria.Criteria  `json:"criteria"`
	Components          []string           `json:"components"`
	AICRProfileIdentity string             `json:"aicrProfileIdentity,omitempty"`
	AICR                *ir.AICRActivation `json:"aicr,omitempty"`
}

// Enumerate resolves every category admitted by the closed v1 taxonomy within one
// concrete k8sVersion×os×arch frame. Category may optionally narrow the scan. Role and
// clusterType are intentionally rejected: they compose a resolved recipe into a SKU or
// cluster and must not manufacture additional recipe identities.
//
// A context-free "all recipes" operation would be dishonest because k8sVersion and OS
// are open domains. Requiring them here makes the result finite and reproducible.
func Enumerate(cat *catalog.Catalog, frame criteria.Criteria, reserved map[string]string) ([]ResolvableRecipe, error) {
	if cat == nil {
		return nil, e.New(e.CodeInvalidCatalog, "cannot enumerate a nil catalog")
	}
	if criteria.IsWildcard(frame.K8sVersion) || strings.TrimSpace(frame.K8sVersion) != frame.K8sVersion {
		return nil, e.New(e.CodeUsage, "recipe enumeration requires a concrete canonical k8sVersion resolution frame")
	}
	if criteria.IsWildcard(frame.OS) || strings.TrimSpace(frame.OS) != frame.OS {
		return nil, e.New(e.CodeUsage, "recipe enumeration requires a concrete canonical os resolution frame")
	}
	if criteria.IsWildcard(frame.Arch) {
		return nil, e.New(e.CodeUsage, "recipe enumeration requires a concrete arch resolution frame")
	}
	if !criteria.IsWildcard(frame.Role) || !criteria.IsWildcard(frame.ClusterType) {
		return nil, e.New(e.CodeUsage,
			"recipe enumeration cannot use role or clusterType; enumerate recipes first, then compose node SKUs and clusters")
	}
	if err := frame.ValidateActive(); err != nil {
		return nil, err
	}
	if err := criteria.CheckHardReserved(reserved); err != nil {
		return nil, err
	}

	categories := criteria.Categories()
	if !criteria.IsWildcard(frame.Category) {
		categories = []string{frame.Category}
	}
	results := make([]ResolvableRecipe, 0, len(categories))
	for _, category := range categories {
		query := frame
		query.Category = category
		categoryReserved := map[string]string(nil)
		if category == "accelerated-runtime" {
			categoryReserved = reserved
		}
		resolved, err := Resolve(cat, query, categoryReserved)
		if err != nil {
			var coded *e.Error
			if errors.As(err, &coded) && coded.Code == e.CodeNoMatch {
				continue
			}
			return nil, err
		}
		graph, err := Compile(resolved)
		if err != nil {
			return nil, err
		}
		digest, err := graph.Digest()
		if err != nil {
			return nil, err
		}
		components := make([]string, 0, len(resolved.Components))
		for _, component := range resolved.Components {
			components = append(components, component.Name)
		}
		sort.Strings(components)
		item := ResolvableRecipe{
			RecipeID: resolved.RecipeID, RecipeDigest: digest,
			Criteria: resolved.Criteria, Components: components,
		}
		if resolved.AICRProfile != nil {
			item.AICRProfileIdentity = resolved.AICRProfile.Identity
		}
		if graph.AICR != nil {
			activation := *graph.AICR
			item.AICR = &activation
		}
		results = append(results, item)
	}
	if len(results) == 0 {
		return nil, e.New(e.CodeNoMatch,
			"catalog has no resolvable recipes in frame {k8sVersion:%q os:%q arch:%q}",
			frame.K8sVersion, frame.OS, frame.Arch)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].RecipeID != results[j].RecipeID {
			return results[i].RecipeID < results[j].RecipeID
		}
		return results[i].RecipeDigest < results[j].RecipeDigest
	})
	return results, nil
}
