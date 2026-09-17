package ir

import (
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"sigs.k8s.io/yaml"
)

// LogicalCriteria is the recipe-identity projection of resolved criteria. Role and
// clusterType are composition/delivery axes and therefore cannot be represented by
// this type. Adding a field to criteria.Criteria requires an explicit classification
// in the identity coverage tests before it can affect, or be excluded from, identity.
type LogicalCriteria struct {
	Category   string `json:"category,omitempty"`
	K8sVersion string `json:"k8sVersion,omitempty"`
	OS         string `json:"os,omitempty"`
	Arch       string `json:"arch,omitempty"`
}

// LogicalAICRActivation contains only output-affecting AICR inputs and pins. Human
// labels and transport locations do not exist in this representation.
type LogicalAICRActivation struct {
	Service         string `json:"service"`
	Accelerator     string `json:"accelerator"`
	OS              string `json:"os"`
	Intent          string `json:"intent"`
	Platform        string `json:"platform,omitempty"`
	RecipeDigest    string `json:"recipeDigest"`
	GeneratorDigest string `json:"generatorDigest"`
}

// LogicalNode is the explicit T1 recipe-identity DTO. It intentionally cannot carry
// source/artifact/flake transport locators. OCI references are reduced to their
// immutable manifest digest while constructing this value; the registry/name prefix
// is not representable here.
type LogicalNode struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Needs []string `json:"needs,omitempty"`

	Commit           string     `json:"commit,omitempty"`
	TreeHash         string     `json:"treeHash,omitempty"`
	Dest             string     `json:"dest,omitempty"`
	VendorCmd        []string   `json:"vendorCmd,omitempty"`
	VendorImage      string     `json:"vendorImage,omitempty"`
	Digest           string     `json:"digest,omitempty"`
	Component        string     `json:"component,omitempty"`
	BuilderType      string     `json:"builderType,omitempty"`
	FlakeOutput      string     `json:"flakeOutput,omitempty"`
	FlakeLockDigest  string     `json:"flakeLockDigest,omitempty"`
	Toolchain        string     `json:"toolchain,omitempty"`
	Platform         string     `json:"platform,omitempty"`
	Network          string     `json:"network,omitempty"`
	Env              []EnvVar   `json:"env,omitempty"`
	Patches          []PatchRef `json:"patches,omitempty"`
	Command          []string   `json:"command,omitempty"`
	Workdir          string     `json:"workdir,omitempty"`
	SourceDest       string     `json:"sourceDest,omitempty"`
	SourceCommit     string     `json:"sourceCommit,omitempty"`
	SourceTreeHash   string     `json:"sourceTreeHash,omitempty"`
	VendorSpecDigest string     `json:"vendorSpecDigest,omitempty"`
	SourceReadOnly   bool       `json:"sourceReadOnly,omitempty"`
	WriteDir         string     `json:"writeDir,omitempty"`
	OutputType       string     `json:"outputType,omitempty"`
	ArtifactPath     string     `json:"artifactPath,omitempty"`
	Version          string     `json:"version,omitempty"`
}

// LogicalGraph is the only shape accepted by recipe identity computation. The
// transport-bearing Graph is fully validated first and then projected into this
// structurally transport-free DTO.
type LogicalGraph struct {
	SchemaVersion string                 `json:"schemaVersion"`
	RecipeID      string                 `json:"recipeId"`
	Criteria      LogicalCriteria        `json:"criteria"`
	AICR          *LogicalAICRActivation `json:"aicr,omitempty"`
	Nodes         []LogicalNode          `json:"nodes"`
}

type recipeIdentityFieldRule uint8

const (
	includeLogicalValueInRecipeIdentity recipeIdentityFieldRule = iota + 1
	includeContentReferenceInRecipeIdentity
	excludeTransportFromRecipeIdentity
	excludeCompositionFromRecipeIdentity
	excludeExecutionMetadataFromRecipeIdentity
)

// These exhaustive rules are checked reflectively in marshal_test.go.
// They make adding a field to a full graph type a deliberate Recipe-identity boundary
// decision instead of silently inheriting a serializer's behavior.
var nodeRecipeIdentityRules = map[string]recipeIdentityFieldRule{
	"ID":               includeLogicalValueInRecipeIdentity,
	"Kind":             includeLogicalValueInRecipeIdentity,
	"Needs":            includeLogicalValueInRecipeIdentity,
	"Requirements":     excludeExecutionMetadataFromRecipeIdentity,
	"ExecutionBinding": excludeExecutionMetadataFromRecipeIdentity,
	"Repo":             excludeTransportFromRecipeIdentity,
	"Commit":           includeLogicalValueInRecipeIdentity,
	"TreeHash":         includeLogicalValueInRecipeIdentity,
	"Dest":             includeLogicalValueInRecipeIdentity,
	"VendorCmd":        includeLogicalValueInRecipeIdentity,
	"VendorImage":      includeContentReferenceInRecipeIdentity,
	"Artifact":         excludeTransportFromRecipeIdentity,
	"Digest":           includeLogicalValueInRecipeIdentity,
	"Component":        includeLogicalValueInRecipeIdentity,
	"BuilderType":      includeLogicalValueInRecipeIdentity,
	"FlakeRef":         excludeTransportFromRecipeIdentity,
	"FlakeOutput":      includeLogicalValueInRecipeIdentity,
	"FlakeLockDigest":  includeLogicalValueInRecipeIdentity,
	"Toolchain":        includeContentReferenceInRecipeIdentity,
	"Platform":         includeLogicalValueInRecipeIdentity,
	"Network":          includeLogicalValueInRecipeIdentity,
	"Env":              includeLogicalValueInRecipeIdentity,
	"Patches":          includeLogicalValueInRecipeIdentity,
	"Command":          includeLogicalValueInRecipeIdentity,
	"Workdir":          includeLogicalValueInRecipeIdentity,
	"SourceDest":       includeLogicalValueInRecipeIdentity,
	"SourceCommit":     includeLogicalValueInRecipeIdentity,
	"SourceTreeHash":   includeLogicalValueInRecipeIdentity,
	"VendorSpecDigest": includeLogicalValueInRecipeIdentity,
	"SourceReadOnly":   includeLogicalValueInRecipeIdentity,
	"WriteDir":         includeLogicalValueInRecipeIdentity,
	"OutputType":       includeLogicalValueInRecipeIdentity,
	"ArtifactPath":     includeLogicalValueInRecipeIdentity,
	"Version":          includeLogicalValueInRecipeIdentity,
}

var criteriaRecipeIdentityRules = map[string]recipeIdentityFieldRule{
	"Category":    includeLogicalValueInRecipeIdentity,
	"Role":        excludeCompositionFromRecipeIdentity,
	"K8sVersion":  includeLogicalValueInRecipeIdentity,
	"OS":          includeLogicalValueInRecipeIdentity,
	"Arch":        includeLogicalValueInRecipeIdentity,
	"ClusterType": excludeCompositionFromRecipeIdentity,
}

var graphRecipeIdentityRules = map[string]recipeIdentityFieldRule{
	"SchemaVersion": includeLogicalValueInRecipeIdentity,
	"RecipeID":      includeLogicalValueInRecipeIdentity,
	"Criteria":      includeLogicalValueInRecipeIdentity,
	"AICR":          includeLogicalValueInRecipeIdentity,
	"Nodes":         includeLogicalValueInRecipeIdentity,
}

// Normalize enforces a canonical ordering on the graph: nodes by ID, and within
// each node the Needs and Env slices sorted. Serialization is only deterministic
// if the in-memory graph is canonical first (NKD-FR-RC-017).
func (g *Graph) Normalize() {
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	for i := range g.Nodes {
		n := &g.Nodes[i]
		sort.Strings(n.Needs)
		sort.Slice(n.Env, func(a, b int) bool { return n.Env[a].Name < n.Env[b].Name })
	}
}

// normalizedCopy returns a defensive copy after the public boundary has required
// canonical authoring order. It does not make unsorted caller input acceptable;
// Graph.Validate rejects that input before serialization or identity computation.
func (g Graph) normalizedCopy() Graph {
	c := g
	c.Nodes = append([]Node(nil), g.Nodes...)
	for i := range c.Nodes {
		c.Nodes[i].Needs = append([]string(nil), g.Nodes[i].Needs...)
		c.Nodes[i].Env = append([]EnvVar(nil), g.Nodes[i].Env...)
		c.Nodes[i].Requirements = execution.Clone(g.Nodes[i].Requirements)
		c.Nodes[i].ExecutionBinding = execution.CloneBinding(g.Nodes[i].ExecutionBinding)
	}
	c.Normalize()
	return c
}

// MarshalDeterministic renders an already-normalized graph to YAML with stable key
// ordering and no runtime-varying fields (NKD-FR-RC-017). Unsorted input is rejected;
// callers constructing a graph directly must call Normalize before this authority
// boundary. This is the human-readable DISPLAY form (ADR-01 §4, the "--pretty"
// view); Digest uses the canonical JCS codec.
func (g Graph) MarshalDeterministic() ([]byte, error) {
	if err := (&g).Validate(); err != nil {
		return nil, err
	}
	c := g.normalizedCopy()
	// sigs.k8s.io/yaml marshals via encoding/json (stable struct field order,
	// sorted map keys) then to YAML (sorted keys) — deterministic on both hops.
	b, err := yaml.Marshal(c)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "serializing IR for recipe %q", g.RecipeID)
	}
	return b, nil
}

// logicalGraph projects a previously validated execution graph into a distinct DTO
// that cannot represent transport. This structural split prevents a newly added URL,
// registry, credential, or mirror field from leaking into recipe identity merely
// because a sanitizer forgot to clear it.
func (g Graph) logicalGraph() LogicalGraph {
	c := g.normalizedCopy()
	logical := LogicalGraph{
		SchemaVersion: c.SchemaVersion,
		RecipeID:      c.RecipeID,
		Criteria: LogicalCriteria{
			Category: c.Criteria.Category, K8sVersion: c.Criteria.K8sVersion,
			OS: c.Criteria.OS, Arch: c.Criteria.Arch,
		},
		Nodes: make([]LogicalNode, 0, len(c.Nodes)),
	}
	if c.AICR != nil {
		logical.AICR = &LogicalAICRActivation{
			Service: c.AICR.Service, Accelerator: c.AICR.Accelerator, OS: c.AICR.OS,
			Intent: c.AICR.Intent, Platform: c.AICR.Platform,
			RecipeDigest: c.AICR.RecipeDigest, GeneratorDigest: c.AICR.GeneratorDigest,
		}
	}
	for _, node := range c.Nodes {
		logical.Nodes = append(logical.Nodes, LogicalNode{
			ID: node.ID, Kind: node.Kind, Needs: append([]string(nil), node.Needs...),
			Commit: node.Commit, TreeHash: node.TreeHash, Dest: node.Dest,
			VendorCmd:   append([]string(nil), node.VendorCmd...),
			VendorImage: imageDigestID(node.VendorImage), Digest: node.Digest,
			Component: node.Component, BuilderType: node.BuilderType,
			FlakeOutput: node.FlakeOutput, FlakeLockDigest: node.FlakeLockDigest,
			Toolchain: imageDigestID(node.Toolchain), Platform: node.Platform,
			Network: node.Network, Env: append([]EnvVar(nil), node.Env...),
			Patches: append([]PatchRef(nil), node.Patches...),
			Command: append([]string(nil), node.Command...), Workdir: node.Workdir,
			SourceDest: node.SourceDest, SourceCommit: node.SourceCommit,
			SourceTreeHash: node.SourceTreeHash, VendorSpecDigest: node.VendorSpecDigest,
			SourceReadOnly: node.SourceReadOnly, WriteDir: node.WriteDir,
			OutputType: node.OutputType, ArtifactPath: node.ArtifactPath, Version: node.Version,
		})
	}
	return logical
}

// imageDigestID reduces an image ref to its content-address: the "sha256:…" suffix of
// a repo@sha256: ref, or the ref unchanged if it is already a bare digest / empty.
func imageDigestID(ref string) string {
	if i := strings.LastIndex(ref, "@sha256:"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// DigestView exposes the structurally transport-free content identity only after the
// complete transport-bearing graph passes semantic validation. Callers can marshal
// this value to inspect the exact canonical object hashed by Digest (ADR-01 §4).
func (g Graph) DigestView() (LogicalGraph, error) {
	if err := (&g).Validate(); err != nil {
		return LogicalGraph{}, err
	}
	return g.logicalGraph(), nil
}

// CanonicalJSON is the FULL, already-normalized graph in canonical JSON — every execution field
// (repo, commit, toolchain, command, env, …), not the identity-only DigestView. It is
// the committed nodes.json the from-source runner reads to execute each node (ADR-18
// §2). Deterministic (sorted) so the pipeline provably corresponds to one IR.
func (g Graph) CanonicalJSON() ([]byte, error) {
	if err := (&g).Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(g.normalizedCopy())
}

// Digest computes recipeDigest via the one canonical codec (ADR-01): a
// domain-separated sha256 over the RFC-8785 canonical-JSON form of the graph's content
// identity (digestView), "sha256:"+hex (NKD-FR-RC-018). There is no grandfathered YAML
// exception — the recipe identity uses the same codec as every other digest-addressed
// object, so an auditor can recompute it in any language.
func (g Graph) Digest() (string, error) {
	if err := (&g).Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindRecipe, g.logicalGraph())
}
