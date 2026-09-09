// Package criteria implements nkdctl's asymmetric-match resolution front-end:
// the four active v1 axes, wildcard semantics, specificity tie-breaking, and the
// fail-closed handling of reserved axes. It mirrors AICR's criteria layer
// (SRD §4.1–§4.3).
package criteria

import (
	"sort"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

// Wildcard is the explicit "matches anything" value; the empty string is treated
// identically (NKD-FR-RC-002).
const Wildcard = "any"

// DefaultOS is applied when the os axis is left unspecified by both the query and
// the matched recipe, so the resolved IR always carries a concrete os (NKD-FR-RC-009).
const DefaultOS = "ubuntu"

// DefaultArch is applied when the arch axis is left unspecified, so the resolved IR
// always carries a concrete, deterministic arch (never host-derived). amd64 keeps
// existing single-arch behavior when no --arch is given; operators select arm64
// explicitly. Multi-arch is a native-per-runner model: the same recipe resolves for
// any arch and each arch builds on a toolchain native to it (no cross-compilation).
const DefaultArch = "amd64"

// Criteria carries the v1-active axes (NKD-FR-RC-007). A recipe uses it with wildcards
// allowed; a resolved query carries concrete values. ClusterType distinguishes an
// operator/management cluster (runs CAPI/NKE to provision tenants) from a tenant
// (workload) cluster; it is omitempty and never defaulted, so a catalog/query that
// does not use it leaves the IR criteria — and thus recipeDigest — unchanged.
type Criteria struct {
	// Category is the primary recipe-selection axis (ADR-12 §1): a recipe produces one
	// LAYER of the stack (os | kubernetes | core-runtime | accelerated-runtime |
	// operations | platform-services). Role and clusterType are COMPOSITION concerns,
	// not recipe axes — they filter/compose already-verified category artifacts and are
	// excluded from the recipe IDENTITY (ir.LogicalCriteria cannot represent them), so the
	// `kubernetes` category produces every binary under ONE identity regardless of role.
	Category    string `json:"category,omitempty"`
	Role        string `json:"role,omitempty"` // composition/delivery filter (control-plane|worker|gpu-worker); not in recipeDigest
	K8sVersion  string `json:"k8sVersion,omitempty"`
	OS          string `json:"os,omitempty"`
	Arch        string `json:"arch,omitempty"`
	ClusterType string `json:"clusterType,omitempty"` // composition: operator | tenant; not in recipeDigest
}

// ReservedAxes are declared but inactive by default. A non-empty value for any of
// them fails closed rather than being silently wildcarded (NKD-FR-RC-011/012) —
// except the AICR-governed axes, which activate when a recipe declares an AICR
// profile (see AICRAxes).
var ReservedAxes = []string{"accelerator", "service", "intent", "platform", "tier", "compliance"}

// AICRAxes are the accelerated-runtime axes governed by AICR (github.com/NVIDIA/aicr:
// accelerator type, cloud service, workload intent, platform). Reconciled toward NKD
// v1 goal 10 / §8.1: AICR is the foundational declarative layer for the
// accelerated-runtime profile and its validated component pairings, so these axes are
// no longer permanently reserved — they activate for a recipe that declares a
// digest-pinned AICR profile identity. Without such a profile they still fail closed.
var AICRAxes = map[string]bool{"accelerator": true, "service": true, "intent": true, "platform": true}

// AICRAxesSet returns the sorted non-empty AICR-governed axis keys in a query's
// reserved map (the accelerated-runtime selection an AICR profile must govern).
func AICRAxesSet(reserved map[string]string) []string {
	var keys []string
	for k, v := range reserved {
		if v != "" && AICRAxes[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// CheckHardReserved fails closed on the non-AICR reserved axes (tier, compliance),
// which remain inactive regardless of any AICR profile. AICR axes are handled by the
// resolver against the matched recipe's declared AICR profile.
func CheckHardReserved(reserved map[string]string) error {
	var keys []string
	for k, v := range reserved {
		if v != "" && !AICRAxes[k] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return nkderr.New(nkderr.CodeReservedAxis,
		"reserved axis not supported in v1: %v (reserved axes never wildcard silently)", keys)
}

// v1 domains for the active enum axes. os is intentionally open (operator-choosable,
// NKD-FR-RC-009) and k8sVersion is an arbitrary semver string, so neither is listed.
var (
	roleDomain        = map[string]bool{"control-plane": true, "worker": true, "gpu-worker": true}
	archDomain        = map[string]bool{"amd64": true, "arm64": true}
	clusterTypeDomain = map[string]bool{"operator": true, "tenant": true}
	// The ADR-12 §2 category taxonomy is a partition into two families. These two maps
	// are the single source of truth: node-level categories produce per-node artifacts a
	// Node SKU is carved from; cluster-level categories are helm-deployed onto a running
	// cluster. categoryDomain is their union so no third site can disagree about which
	// categories exist or which family one belongs to (the pre/post-release category
	// validators, the Node SKU role filter, and catalog enumeration all resolve here).
	nodeCategoryDomain    = map[string]bool{"os": true, "kubernetes": true}
	clusterCategoryDomain = map[string]bool{
		"core-runtime": true, "accelerated-runtime": true, "operations": true, "platform-services": true,
	}
	categoryDomain    = unionDomains(nodeCategoryDomain, clusterCategoryDomain)
	aicrServiceDomain = map[string]bool{
		"eks": true, "gke": true, "aks": true, "oke": true,
		"kind": true, "lke": true, "bcm": true,
	}
	aicrAcceleratorDomain = map[string]bool{
		"h100": true, "h200": true, "gb200": true, "b200": true,
		"a100": true, "l40": true, "l40s": true, "rtx-pro-6000": true,
	}
	aicrOSDomain       = map[string]bool{"ubuntu": true, "rhel": true, "cos": true, "amazonlinux": true, "ol": true, "talos": true}
	aicrIntentDomain   = map[string]bool{"training": true, "inference": true}
	aicrPlatformDomain = map[string]bool{"kubeflow": true, "dynamo": true, "slurm": true}
)

// Categories returns the closed v1 recipe-category domain in canonical order. It is
// used by catalog enumeration to probe every possible category without duplicating
// the taxonomy outside this package. The returned slice is caller-owned.
func Categories() []string { return sortedDomain(categoryDomain) }

// NodeCategories and ClusterCategories return the two halves of the category partition
// in canonical order. Callers that must distinguish per-node artifacts from
// cluster-level deployments (the Node SKU filter, ClusterComposition/ClusterDefinition
// validation) route here rather than re-listing the taxonomy.
func NodeCategories() []string    { return sortedDomain(nodeCategoryDomain) }
func ClusterCategories() []string { return sortedDomain(clusterCategoryDomain) }

// ValidCategory / IsNodeCategory / IsClusterCategory are the single predicates for the
// category taxonomy. IsNodeCategory gates the Node SKU role filter (only node-level
// categories are carved into per-role SKUs); the two are mutually exclusive and
// exhaustive over ValidCategory.
func ValidCategory(v string) bool     { return categoryDomain[v] }
func IsNodeCategory(v string) bool    { return nodeCategoryDomain[v] }
func IsClusterCategory(v string) bool { return clusterCategoryDomain[v] }

// Roles / ValidRole own the node-role domain (control-plane | worker | gpu-worker) so
// the disk/assembly and cluster layers stop re-declaring it. Arches / ValidArch own the
// supported-architecture set the catalog, lock, and IR all key on.
func Roles() []string         { return sortedDomain(roleDomain) }
func ValidRole(v string) bool { return roleDomain[v] }
func Arches() []string        { return sortedDomain(archDomain) }
func ValidArch(v string) bool { return archDomain[v] }

func unionDomains(sets ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, set := range sets {
		for value := range set {
			out[value] = true
		}
	}
	return out
}

func sortedDomain(domain map[string]bool) []string {
	values := make([]string, 0, len(domain))
	for value := range domain {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// ValidateAICRValues validates the concrete AICR profile tuple which activates the
// otherwise-reserved accelerator/service/intent/platform axes. Platform alone is
// optional; the other fields are required so asymmetric selection is reproducible.
func ValidateAICRValues(service, accelerator, osName, intent, platform string) error {
	for _, field := range []struct {
		name   string
		value  string
		domain map[string]bool
	}{
		{"service", service, aicrServiceDomain},
		{"accelerator", accelerator, aicrAcceleratorDomain},
		{"os", osName, aicrOSDomain},
		{"intent", intent, aicrIntentDomain},
	} {
		if field.value == "" {
			return nkderr.New(nkderr.CodeInvalidCatalog, "AICR profile %s is required", field.name)
		}
		if !field.domain[field.value] {
			return nkderr.New(nkderr.CodeUnsupported, "AICR profile %s %q is outside the supported v1 domain", field.name, field.value)
		}
	}
	if platform != "" && !aicrPlatformDomain[platform] {
		return nkderr.New(nkderr.CodeUnsupported, "AICR profile platform %q is outside the supported v1 domain", platform)
	}
	return nil
}

// IsWildcard reports whether v is the wildcard or empty.
func IsWildcard(v string) bool { return v == "" || v == Wildcard }

// MatchField applies asymmetric matching: a wildcard recipe field matches any
// query value; otherwise the query must supply exactly the recipe's value. A
// generic (wildcard) query does NOT match a specific recipe field
// (NKD-FR-RC-002/003/004).
func MatchField(recipeVal, queryVal string) bool {
	if IsWildcard(recipeVal) {
		return true
	}
	return recipeVal == queryVal
}

// Matches reports whether recipe (the receiver, wildcards allowed) matches query.
// Only category × k8sVersion × os × arch select a recipe (ADR-12 §1).
// Role and clusterType are deliberately absent: they compose already-verified recipe
// members and therefore cannot select a different build definition.
func (recipe Criteria) Matches(query Criteria) bool {
	return MatchField(recipe.Category, query.Category) &&
		MatchField(recipe.K8sVersion, query.K8sVersion) &&
		MatchField(recipe.OS, query.OS) &&
		MatchField(recipe.Arch, query.Arch)
}

// Specificity counts the recipe's non-wildcard fields; it is the tie-break key
// when multiple recipes match (NKD-FR-RC-005).
func (recipe Criteria) Specificity() int {
	n := 0
	for _, v := range []string{recipe.Category, recipe.K8sVersion, recipe.OS, recipe.Arch} {
		if !IsWildcard(v) {
			n++
		}
	}
	return n
}

// ValidateRecipePattern rejects composition fields in an authored recipe or overlay.
// Ignoring such a field would be a dangerous compatibility mode: an author could
// believe role/clusterType selected a different build while the resolver produced a
// shared category identity. The only safe compatibility behavior is fail-closed.
func (c Criteria) ValidateRecipePattern() error {
	if err := c.ValidateActive(); err != nil {
		return err
	}
	if !IsWildcard(c.Role) {
		return nkderr.New(nkderr.CodeInvalidCatalog,
			"recipe criteria must not contain role %q; role is node-SKU composition metadata", c.Role)
	}
	if !IsWildcard(c.ClusterType) {
		return nkderr.New(nkderr.CodeInvalidCatalog,
			"recipe criteria must not contain clusterType %q; clusterType composes released categories", c.ClusterType)
	}
	return nil
}

// ValidateActive rejects active-axis values outside their v1 domain (role, arch),
// failing closed with a coded error rather than silently ignoring them
// (NKD-FR-RC-007, NKD-FR-BLD-071). Wildcards are always permitted.
func (c Criteria) ValidateActive() error {
	if !IsWildcard(c.Category) && !categoryDomain[c.Category] {
		return nkderr.New(nkderr.CodeUnsupported, "category %q outside v1 domain {os, kubernetes, core-runtime, accelerated-runtime, operations, platform-services}", c.Category)
	}
	if !IsWildcard(c.Role) && !roleDomain[c.Role] {
		return nkderr.New(nkderr.CodeUnsupported, "role %q outside v1 domain {control-plane, worker, gpu-worker}", c.Role)
	}
	if !IsWildcard(c.Arch) && !archDomain[c.Arch] {
		return nkderr.New(nkderr.CodeUnsupported, "arch %q outside domain {amd64, arm64}", c.Arch)
	}
	if !IsWildcard(c.ClusterType) && !clusterTypeDomain[c.ClusterType] {
		return nkderr.New(nkderr.CodeUnsupported, "clusterType %q outside domain {operator, tenant}", c.ClusterType)
	}
	return nil
}

// CheckReserved fails closed if any reserved axis carries a non-empty value. The
// keys are sorted so the error is deterministic (NKD-FR-RC-012).
func CheckReserved(reserved map[string]string) error {
	if len(reserved) == 0 {
		return nil
	}
	keys := make([]string, 0, len(reserved))
	for k, v := range reserved {
		if v != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return nkderr.New(nkderr.CodeReservedAxis,
		"reserved axis not supported in v1: %v (reserved axes never wildcard silently)", keys)
}

// Resolve produces a fully-concrete query by filling each active axis from the
// query when specified, else from the matched recipe. This is what lands in the IR
// so downstream stages never re-derive it (NKD-FR-RC-019/020).
func Resolve(query, recipe Criteria) Criteria {
	pick := func(q, r string) string {
		if !IsWildcard(q) {
			return q
		}
		if !IsWildcard(r) {
			return r
		}
		return ""
	}
	os := pick(query.OS, recipe.OS)
	if os == "" {
		os = DefaultOS // NKD-FR-RC-009: ubuntu is the default, never a bare empty axis.
	}
	arch := pick(query.Arch, recipe.Arch)
	if arch == "" {
		arch = DefaultArch // never a bare empty axis; deterministic, host-independent.
	}
	return Criteria{
		Category:   pick(query.Category, recipe.Category),
		Role:       pick(query.Role, ""),
		K8sVersion: pick(query.K8sVersion, recipe.K8sVersion),
		OS:         os,
		Arch:       arch,
		// clusterType is never defaulted: an unused axis stays empty (omitempty), so a
		// catalog/query that doesn't distinguish operator vs tenant leaves recipeDigest
		// byte-identical to before the axis existed.
		ClusterType: pick(query.ClusterType, ""),
	}
}
