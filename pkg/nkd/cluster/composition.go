package cluster

import (
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

const CompositionSchemaVersion = "nkd.dev/cluster-composition/v1"

// RecipeBinding is one real resolved category recipe selected into a cluster
// composition. It binds the exact recipe identity and complete pre-SKU component set.
// AICR is present only for an accelerated-runtime recipe and carries the exact logical
// activation already included in RecipeDigest.
type RecipeBinding struct {
	Category            string             `json:"category"`
	RecipeID            string             `json:"recipeId"`
	RecipeDigest        string             `json:"recipeDigest"`
	Criteria            criteria.Criteria  `json:"criteria"`
	Components          []string           `json:"components,omitempty"`
	AICRProfileIdentity string             `json:"aicrProfileIdentity,omitempty"`
	AICR                *ir.AICRActivation `json:"aicr,omitempty"`
	AICRBundle          *AICRBundleInputs  `json:"aicrBundle,omitempty"`
}

// AICRBundleInputs are the concrete, output-affecting deployment choices passed to
// `aicr bundle` after recipe generation. They belong to cluster composition rather
// than recipe identity: the same accelerated-runtime recipe can be deployed into
// clusters with different storage and scheduling policy, but those clusters must not
// claim the same composition identity.
type AICRBundleInputs struct {
	Deployer                   string   `json:"deployer"`
	StorageClass               string   `json:"storageClass,omitempty"`
	AcceleratedNodeSelectors   []string `json:"acceleratedNodeSelectors,omitempty"`
	AcceleratedNodeTolerations []string `json:"acceleratedNodeTolerations,omitempty"`
	WorkloadSelectors          []string `json:"workloadSelectors,omitempty"`
}

// ClusterComposition is the honest pre-release composition form. It says which exact
// resolved recipes make up a cluster and how node roles select their delivery subsets.
// It does not claim those recipes have produced artifacts, passed validation, or become
// release content. Product authors declare this view once inside a Definition; the Release binds its exact realization.
type ClusterComposition struct {
	SchemaVersion          string          `json:"schemaVersion"`
	ClusterType            string          `json:"clusterType"`
	KubernetesVersion      string          `json:"kubernetesVersion"`
	OS                     string          `json:"os"`
	Arch                   string          `json:"arch"`
	CNI                    string          `json:"cni,omitempty"`
	RoleBindings           []RoleBinding   `json:"roleBindings"`
	ClusterCategories      []string        `json:"clusterCategories,omitempty"`
	Recipes                []RecipeBinding `json:"recipes"`
	ValidationRequirements []string        `json:"validationRequirements,omitempty"`
}

func (c *ClusterComposition) Normalize() {
	if c == nil {
		return
	}
	sort.Slice(c.RoleBindings, func(i, j int) bool { return c.RoleBindings[i].Role < c.RoleBindings[j].Role })
	for i := range c.RoleBindings {
		sort.Strings(c.RoleBindings[i].NodeLevelCategories)
		sort.Strings(c.RoleBindings[i].Subset)
	}
	sort.Strings(c.ClusterCategories)
	sort.Strings(c.ValidationRequirements)
	for i := range c.Recipes {
		sort.Strings(c.Recipes[i].Components)
		if bundle := c.Recipes[i].AICRBundle; bundle != nil {
			sort.Strings(bundle.AcceleratedNodeSelectors)
			sort.Strings(bundle.AcceleratedNodeTolerations)
			sort.Strings(bundle.WorkloadSelectors)
		}
	}
	sort.Slice(c.Recipes, func(i, j int) bool { return c.Recipes[i].Category < c.Recipes[j].Category })
}

func (c *ClusterComposition) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindClusterComposition, c)
}

func (c *ClusterComposition) Marshal() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(c)
}

// Validate proves exact recipe/category coverage and rejects authority-shaped gaps.
// In particular, arbitrary capability strings and placeholder ReleaseSet digests have
// no place in this object.
func (c *ClusterComposition) Validate() error {
	if c == nil {
		return e.New(e.CodeInvalidCatalog, "ClusterComposition is required")
	}
	if c.SchemaVersion != CompositionSchemaVersion {
		return e.New(e.CodeInvalidCatalog,
			"ClusterComposition schemaVersion %q is unsupported (want %q)", c.SchemaVersion, CompositionSchemaVersion)
	}
	switch c.ClusterType {
	case TypeTenant, TypeOperator:
	default:
		return e.New(e.CodeUnsupported, "unknown clusterType %q (tenant|operator)", c.ClusterType)
	}
	frame := criteria.Criteria{K8sVersion: c.KubernetesVersion, OS: c.OS, Arch: c.Arch}
	if err := frame.ValidateActive(); err != nil {
		return err
	}
	if !kubernetesSemver.MatchString(c.KubernetesVersion) {
		return e.New(e.CodeInvalidCatalog, "kubernetesVersion %q is not canonical major.minor.patch", c.KubernetesVersion)
	}
	if c.CNI != "" && !canonicalText(c.CNI) {
		return e.New(e.CodeInvalidCatalog, "CNI %q is not canonical", c.CNI)
	}
	if len(c.RoleBindings) == 0 {
		return e.New(e.CodeInvalidCatalog, "ClusterComposition has no role bindings")
	}

	required := map[string]bool{}
	previousRole := ""
	for i, binding := range c.RoleBindings {
		if !criteria.ValidRole(binding.Role) {
			return e.New(e.CodeUnsupported, "roleBindings[%d] has unknown role %q", i, binding.Role)
		}
		if i > 0 && binding.Role <= previousRole {
			return e.New(e.CodeInvalidCatalog, "roleBindings must be strictly sorted and duplicate-free")
		}
		previousRole = binding.Role
		if len(binding.NodeLevelCategories) == 0 {
			return e.New(e.CodeInvalidCatalog, "role binding %q has no node-level categories", binding.Role)
		}
		if err := validateSortedStrings("node-level categories for "+binding.Role,
			binding.NodeLevelCategories, criteria.IsNodeCategory); err != nil {
			return err
		}
		if err := validateSortedStrings("component subset for "+binding.Role, binding.Subset, nil); err != nil {
			return err
		}
		for _, category := range binding.NodeLevelCategories {
			required[category] = true
		}
	}
	if err := validateSortedStrings("cluster categories", c.ClusterCategories, criteria.IsClusterCategory); err != nil {
		return err
	}
	for _, category := range c.ClusterCategories {
		required[category] = true
	}
	if err := validateSortedStrings("validation requirements", c.ValidationRequirements, nil); err != nil {
		return err
	}

	bindings := make(map[string]RecipeBinding, len(c.Recipes))
	previousCategory := ""
	for i, binding := range c.Recipes {
		if i > 0 && binding.Category <= previousCategory {
			return e.New(e.CodeInvalidCatalog, "recipe bindings must be strictly category-sorted and duplicate-free")
		}
		previousCategory = binding.Category
		if !required[binding.Category] {
			return e.New(e.CodeInvalidCatalog, "recipe binding %q is not selected by any role or cluster category", binding.Category)
		}
		if binding.Criteria.Category != binding.Category || binding.Criteria.K8sVersion != c.KubernetesVersion ||
			binding.Criteria.OS != c.OS || binding.Criteria.Arch != c.Arch ||
			!criteria.IsWildcard(binding.Criteria.Role) || !criteria.IsWildcard(binding.Criteria.ClusterType) {
			return e.New(e.CodeInvalidCatalog, "recipe binding %q does not match the composition resolution frame", binding.Category)
		}
		if !canonicalText(binding.RecipeID) || !canonical.IsDigest(binding.RecipeDigest) {
			return e.New(e.CodeUnverifiedRef, "recipe binding %q has a noncanonical id or recipe digest", binding.Category)
		}
		if err := validateSortedStrings("components for "+binding.Category, binding.Components, nil); err != nil {
			return err
		}
		if binding.AICR != nil {
			if binding.Category != "accelerated-runtime" || !canonicalText(binding.AICRProfileIdentity) {
				return e.New(e.CodeInvalidCatalog, "AICR activation belongs only to a named accelerated-runtime recipe")
			}
			if err := binding.AICR.Validate(); err != nil {
				return err
			}
			if binding.AICR.OS != c.OS {
				return e.New(e.CodeInvalidCatalog, "AICR activation OS does not match the composition frame")
			}
			if err := validateAICRBundle(binding.AICRBundle); err != nil {
				return err
			}
		} else if binding.Category == "accelerated-runtime" {
			return e.New(e.CodeInvalidCatalog, "accelerated-runtime recipe has no AICR activation")
		} else if binding.AICRBundle != nil {
			return e.New(e.CodeInvalidCatalog, "AICR bundle inputs belong only to an accelerated-runtime recipe")
		}
		if len(binding.Components) == 0 && binding.AICR == nil {
			return e.New(e.CodeInvalidCatalog, "recipe binding %q has neither components nor an AICR activation", binding.Category)
		}
		bindings[binding.Category] = binding
	}
	for category := range required {
		if _, ok := bindings[category]; !ok {
			return e.New(e.CodeNoMatch, "ClusterComposition has no resolved recipe for required category %q", category)
		}
	}
	for _, role := range c.RoleBindings {
		available := map[string]bool{}
		for _, category := range role.NodeLevelCategories {
			for _, component := range bindings[category].Components {
				available[component] = true
			}
		}
		for _, component := range role.Subset {
			if !available[component] {
				return e.New(e.CodeInvalidCatalog,
					"role %q subset contains %q outside its node-level recipes", role.Role, component)
			}
		}
	}
	return nil
}

// VerifyRecipeIdentities authenticates each recipe binding's declared identity by
// recomputing it from the exact compiled graph the caller resolved for that category.
// Validate alone can only regex-check that RecipeDigest is a well-formed digest (a
// description); this recompute-and-compare turns it into proof, so a caller — CLI, SDK,
// or service — cannot bind a well-formed but fabricated recipeDigest that the deep module
// would otherwise accept as identity. graphs is keyed by binding RecipeID and must cover
// every recipe binding. Because a graph's digest already folds its AICR activation, a
// matching digest also authenticates the bound AICR selection.
func (c *ClusterComposition) VerifyRecipeIdentities(graphs map[string]*ir.Graph) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if len(graphs) < len(c.Recipes) {
		return e.New(e.CodeUnverifiedRef,
			"ClusterComposition identity check needs one compiled graph per recipe binding")
	}
	for _, binding := range c.Recipes {
		g := graphs[binding.RecipeID]
		if g == nil {
			return e.New(e.CodeUnverifiedRef,
				"recipe binding %q has no compiled graph to authenticate its identity", binding.Category)
		}
		if g.RecipeID != binding.RecipeID || g.Criteria.Category != binding.Category {
			return e.New(e.CodeUnverifiedRef,
				"recipe binding %q compiled graph identity does not match its binding", binding.Category)
		}
		view, err := g.DigestView()
		if err != nil {
			return err
		}
		if err := (canonical.TypedDigest{Kind: canonical.KindRecipe, Value: binding.RecipeDigest}).Verify(view); err != nil {
			return e.Wrap(e.CodeUnverifiedRef, err, "recipe binding %q", binding.Category)
		}
	}
	return nil
}

func validateAICRBundle(bundle *AICRBundleInputs) error {
	if bundle == nil {
		return e.New(e.CodeInvalidCatalog,
			"accelerated-runtime recipe has no concrete AICR bundle inputs")
	}
	switch bundle.Deployer {
	case "helm", "argocd", "argocd-helm", "flux", "helmfile":
	default:
		return e.New(e.CodeUnsupported, "AICR deployer %q is unsupported", bundle.Deployer)
	}
	if bundle.StorageClass != "" && !canonicalText(bundle.StorageClass) {
		return e.New(e.CodeInvalidCatalog, "AICR storageClass %q is not canonical", bundle.StorageClass)
	}
	for field, values := range map[string][]string{
		"accelerated node selectors":   bundle.AcceleratedNodeSelectors,
		"accelerated node tolerations": bundle.AcceleratedNodeTolerations,
		"workload selectors":           bundle.WorkloadSelectors,
	} {
		if err := validateSortedStrings("AICR "+field, values, nil); err != nil {
			return err
		}
		for _, value := range values {
			if !strings.Contains(value, "=") {
				return e.New(e.CodeInvalidCatalog, "AICR %s value %q must contain '='", field, value)
			}
		}
	}
	return nil
}
