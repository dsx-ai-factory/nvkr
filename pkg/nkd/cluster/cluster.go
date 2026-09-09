// Package cluster is the cluster-composition layer above the ReleaseSet model
// (ADR-19): the engine-agnostic ClusterDefinition NKD OWNS, and the provisioning-
// adapter seam that instantiates it. NKD produces the definition + verified artifacts;
// it does NOT provision, reconcile, or operate the cluster — that is the adapter +
// engine's job. A digest string in a definition is only an immutable reference, not
// proof of promotion: an adapter must authenticate the exact ReleaseSet and its
// clearance before projection or provisioning. The recursion holds: a control-plane
// cluster is a ClusterDefinition with clusterType=operator, a tenant cluster with
// clusterType=tenant — NKD defines both, the engine operates both.
package cluster

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

const SchemaVersion = "nkd.dev/cluster-definition/v1"

var (
	kubernetesSemver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// Cluster types (ADR-12 §3 / ADR-19).
const (
	TypeTenant   = "tenant"
	TypeOperator = "operator"
)

// RoleBinding maps a node role to the node-level category subset composing its SKU.
type RoleBinding struct {
	Role                string   `json:"role"`                // control-plane | worker | gpu-worker
	NodeLevelCategories []string `json:"nodeLevelCategories"` // e.g. [os, kubernetes]
	Subset              []string `json:"subset,omitempty"`    // component names filtered into this SKU (by roles affinity)
}

// ClusterDefinition is engine-agnostic and digest-addressable (ADR-01). It references
// the ReleaseSet by digest; this field is descriptive until a downstream consumer
// authenticates the exact ReleaseSet and clearance. Its reproducibility guarantee is
// "the same definition resolves to the same pinned artifact identities + requirements",
// NOT a bit-identical running cluster (IPs/scheduling/runtime state are the engine's domain).
type ClusterDefinition struct {
	SchemaVersion          string        `json:"schemaVersion"`
	ClusterType            string        `json:"clusterType"`
	KubernetesVersion      string        `json:"kubernetesVersion"`
	CNI                    string        `json:"cni,omitempty"`
	ReleaseSetRef          string        `json:"releaseSetRef"` // immutable reference only; not promotion evidence
	RoleBindings           []RoleBinding `json:"roleBindings"`
	ClusterCategories      []string      `json:"clusterCategories,omitempty"` // cluster-level categories to helm-deploy
	Capability             string        `json:"capability,omitempty"`        // e.g. an AICR profile ref
	ValidationRequirements []string      `json:"validationRequirements,omitempty"`
}

// Digest is the definition's canonical identity.
func (d *ClusterDefinition) Digest() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindClusterDefinition, d)
}

// Marshal returns the exact canonical bytes over which Digest computes identity.
func (d *ClusterDefinition) Marshal() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(d)
}

// Normalize orders authored set-like fields before validation and publication. It
// does not remove duplicates: duplicates remain a semantic error rather than being
// silently collapsed into a different definition.
func (d *ClusterDefinition) Normalize() {
	if d == nil {
		return
	}
	sort.Slice(d.RoleBindings, func(i, j int) bool { return d.RoleBindings[i].Role < d.RoleBindings[j].Role })
	for i := range d.RoleBindings {
		sort.Strings(d.RoleBindings[i].NodeLevelCategories)
		sort.Strings(d.RoleBindings[i].Subset)
	}
	sort.Strings(d.ClusterCategories)
	sort.Strings(d.ValidationRequirements)
}

// Validate fails closed on a malformed definition: an unknown cluster type, a
// non-digest releaseSetRef (the reference must be immutable), or no role bindings.
func (d *ClusterDefinition) Validate() error {
	if d == nil {
		return e.New(e.CodeInvalidCatalog, "ClusterDefinition is required")
	}
	if d.SchemaVersion != SchemaVersion {
		return e.New(e.CodeInvalidCatalog,
			"ClusterDefinition schemaVersion %q is unsupported (want %q)", d.SchemaVersion, SchemaVersion)
	}
	switch d.ClusterType {
	case TypeTenant, TypeOperator:
	default:
		return e.New(e.CodeUnsupported, "unknown clusterType %q (tenant|operator)", d.ClusterType)
	}
	if !kubernetesSemver.MatchString(d.KubernetesVersion) {
		return e.New(e.CodeInvalidCatalog, "kubernetesVersion %q is not canonical major.minor.patch", d.KubernetesVersion)
	}
	if d.CNI != "" && !canonicalText(d.CNI) {
		return e.New(e.CodeInvalidCatalog, "CNI %q is not canonical", d.CNI)
	}
	if !canonical.IsDigest(d.ReleaseSetRef) {
		return e.New(e.CodeUnverifiedRef,
			"releaseSetRef %q must be an immutable digest; syntax validation does not authenticate promotion or clearance", d.ReleaseSetRef)
	}
	if len(d.RoleBindings) == 0 {
		return e.New(e.CodeInvalidCatalog, "ClusterDefinition has no role bindings")
	}
	previousRole := ""
	for i, binding := range d.RoleBindings {
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
		if err := validateSortedStrings("node-level categories for "+binding.Role, binding.NodeLevelCategories, criteria.IsNodeCategory); err != nil {
			return err
		}
		if err := validateSortedStrings("component subset for "+binding.Role, binding.Subset, nil); err != nil {
			return err
		}
	}
	if err := validateSortedStrings("cluster categories", d.ClusterCategories, criteria.IsClusterCategory); err != nil {
		return err
	}
	if err := validateSortedStrings("validation requirements", d.ValidationRequirements, nil); err != nil {
		return err
	}
	if d.Capability != "" && !canonicalText(d.Capability) {
		return e.New(e.CodeInvalidCatalog, "capability %q is not canonical", d.Capability)
	}
	return nil
}

// Projection is the engine-specific instantiation output — node-pool counts, provider
// instance types, site bindings, the concrete test selector, the provisioning workflow
// (ADR-19 §3). It is an ADAPTER output, opaque to NKD (never an NKD object).
type Projection struct {
	Engine string
	Body   any
}

// ProvisioningAdapter is the seam (ADR-19 §2): it projects a ClusterDefinition into an
// engine-specific form and (elsewhere) drives that engine. Before Project, a downstream
// authority boundary must authenticate the exact definition, referenced ReleaseSet, and
// clearance; this descriptive interface cannot perform or prove that step. NKD holds the
// definition; the adapter + engine own instantiation.
type ProvisioningAdapter interface {
	Name() string
	Project(ctx context.Context, def *ClusterDefinition) (*Projection, error)
}

func validateSortedStrings(field string, values []string, allowed func(string) bool) error {
	previous := ""
	for i, value := range values {
		if !canonicalText(value) {
			return e.New(e.CodeInvalidCatalog, "%s contains a non-canonical value at index %d", field, i)
		}
		if allowed != nil && !allowed(value) {
			return e.New(e.CodeUnsupported, "%s contains unsupported value %q", field, value)
		}
		if i > 0 && value <= previous {
			return e.New(e.CodeInvalidCatalog, "%s must be strictly sorted and duplicate-free", field)
		}
		previous = value
	}
	return nil
}

func canonicalText(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
