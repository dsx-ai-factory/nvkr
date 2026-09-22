package ir

import (
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"sigs.k8s.io/yaml"
)

// LogicalAICRActivation contains only output-affecting AICR inputs and pins. Human
// labels and transport locations do not exist in this representation.
type LogicalAICRActivation struct {
	Service         string            `json:"service"`
	Accelerator     string            `json:"accelerator"`
	OS              string            `json:"os"`
	Intent          string            `json:"intent"`
	Platform        string            `json:"platform,omitempty"`
	ProfileDigest   string            `json:"profileDigest"`
	GeneratorDigest string            `json:"generatorDigest"`
	Bundle          aicr.BundleInputs `json:"bundle"`
}

// LogicalNode is the explicit T1 build-identity DTO. It intentionally cannot carry
// source/artifact/flake transport locators. OCI references are reduced to their
// immutable manifest digest while constructing this value; the registry/name prefix
// is not representable here.
type LogicalNode struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Needs []string `json:"needs,omitempty"`

	Commit            string            `json:"commit,omitempty"`
	TreeHash          string            `json:"treeHash,omitempty"`
	Dest              string            `json:"dest,omitempty"`
	VendorCmd         []string          `json:"vendorCmd,omitempty"`
	VendorImage       string            `json:"vendorImage,omitempty"`
	Digest            string            `json:"digest,omitempty"`
	Component         string            `json:"component,omitempty"`
	BuilderType       string            `json:"builderType,omitempty"`
	FlakeOutput       string            `json:"flakeOutput,omitempty"`
	FlakeLockDigest   string            `json:"flakeLockDigest,omitempty"`
	Toolchain         string            `json:"toolchain,omitempty"`
	ToolchainPlatform string            `json:"toolchainPlatform,omitempty"`
	Platform          string            `json:"platform,omitempty"`
	Network           string            `json:"network,omitempty"`
	Env               []EnvVar          `json:"env,omitempty"`
	Patches           []PatchRef        `json:"patches,omitempty"`
	Command           []string          `json:"command,omitempty"`
	Workdir           string            `json:"workdir,omitempty"`
	SourceDest        string            `json:"sourceDest,omitempty"`
	SourceCommit      string            `json:"sourceCommit,omitempty"`
	SourceTreeHash    string            `json:"sourceTreeHash,omitempty"`
	VendorSpecDigest  string            `json:"vendorSpecDigest,omitempty"`
	SourceReadOnly    bool              `json:"sourceReadOnly,omitempty"`
	WriteDir          string            `json:"writeDir,omitempty"`
	Disk              *DiskSpec         `json:"disk,omitempty"`
	Packer            *PackerConfig     `json:"packer,omitempty"`
	Materials         []MaterialBinding `json:"materials,omitempty"`
	OutputType        string            `json:"outputType,omitempty"`
	ArtifactPath      string            `json:"artifactPath,omitempty"`
	Version           string            `json:"version,omitempty"`
}

// LogicalGraph is the only shape accepted by build identity computation. The
// transport-bearing Graph is fully validated first and then projected into this
// structurally transport-free DTO.
type LogicalGraph struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Platform      string                 `json:"platform"`
	AICR          *LogicalAICRActivation `json:"aicr,omitempty"`
	Nodes         []LogicalNode          `json:"nodes"`
}

type buildIdentityFieldRule uint8

const (
	includeLogicalValueInBuildIdentity buildIdentityFieldRule = iota + 1
	includeContentReferenceInBuildIdentity
	excludeTransportFromBuildIdentity
	excludeExecutionMetadataFromBuildIdentity
)

// These exhaustive rules are checked reflectively in marshal_test.go.
// They make adding a field to a full graph type a deliberate Build-identity boundary
// decision instead of silently inheriting a serializer's behavior.
var nodeBuildIdentityRules = map[string]buildIdentityFieldRule{
	"ID":                includeLogicalValueInBuildIdentity,
	"Kind":              includeLogicalValueInBuildIdentity,
	"Needs":             includeLogicalValueInBuildIdentity,
	"Requirements":      excludeExecutionMetadataFromBuildIdentity,
	"ExecutionBinding":  excludeExecutionMetadataFromBuildIdentity,
	"Repo":              excludeTransportFromBuildIdentity,
	"Commit":            includeLogicalValueInBuildIdentity,
	"TreeHash":          includeLogicalValueInBuildIdentity,
	"Dest":              includeLogicalValueInBuildIdentity,
	"VendorCmd":         includeLogicalValueInBuildIdentity,
	"VendorImage":       includeContentReferenceInBuildIdentity,
	"Artifact":          excludeTransportFromBuildIdentity,
	"Digest":            includeLogicalValueInBuildIdentity,
	"Component":         includeLogicalValueInBuildIdentity,
	"BuilderType":       includeLogicalValueInBuildIdentity,
	"FlakeRef":          excludeTransportFromBuildIdentity,
	"FlakeOutput":       includeLogicalValueInBuildIdentity,
	"FlakeLockDigest":   includeLogicalValueInBuildIdentity,
	"Toolchain":         includeContentReferenceInBuildIdentity,
	"ToolchainPlatform": includeLogicalValueInBuildIdentity,
	"Platform":          includeLogicalValueInBuildIdentity,
	"Network":           includeLogicalValueInBuildIdentity,
	"Env":               includeLogicalValueInBuildIdentity,
	"Patches":           includeLogicalValueInBuildIdentity,
	"Command":           includeLogicalValueInBuildIdentity,
	"Workdir":           includeLogicalValueInBuildIdentity,
	"SourceDest":        includeLogicalValueInBuildIdentity,
	"SourceCommit":      includeLogicalValueInBuildIdentity,
	"SourceTreeHash":    includeLogicalValueInBuildIdentity,
	"VendorSpecDigest":  includeLogicalValueInBuildIdentity,
	"SourceReadOnly":    includeLogicalValueInBuildIdentity,
	"WriteDir":          includeLogicalValueInBuildIdentity,
	"Disk":              includeLogicalValueInBuildIdentity,
	"Packer":            includeLogicalValueInBuildIdentity,
	"Materials":         includeLogicalValueInBuildIdentity,
	"OutputType":        includeLogicalValueInBuildIdentity,
	"ArtifactPath":      includeLogicalValueInBuildIdentity,
	"Version":           includeLogicalValueInBuildIdentity,
}

var graphBuildIdentityRules = map[string]buildIdentityFieldRule{
	"SchemaVersion": includeLogicalValueInBuildIdentity,
	"Platform":      includeLogicalValueInBuildIdentity,
	"AICR":          includeLogicalValueInBuildIdentity,
	"Nodes":         includeLogicalValueInBuildIdentity,
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
		sort.Slice(n.Materials, func(a, b int) bool { return n.Materials[a].Binding < n.Materials[b].Binding })
		if n.Packer != nil {
			sort.Slice(n.Packer.Variables, func(a, b int) bool {
				return n.Packer.Variables[a].Name < n.Packer.Variables[b].Name
			})
		}
	}
}

// normalizedCopy returns a defensive copy after the public boundary has required
// canonical authoring order. It does not make unsorted caller input acceptable;
// Graph.Validate rejects that input before serialization or identity computation.
func (g Graph) normalizedCopy() Graph {
	c := g
	if g.AICR != nil {
		activation := *g.AICR
		activation.Bundle = g.AICR.Bundle.Clone()
		c.AICR = &activation
	}
	c.Nodes = append([]Node(nil), g.Nodes...)
	for i := range c.Nodes {
		c.Nodes[i].Needs = append([]string(nil), g.Nodes[i].Needs...)
		c.Nodes[i].Env = append([]EnvVar(nil), g.Nodes[i].Env...)
		c.Nodes[i].Requirements = execution.Clone(g.Nodes[i].Requirements)
		c.Nodes[i].ExecutionBinding = execution.CloneBinding(g.Nodes[i].ExecutionBinding)
		if g.Nodes[i].Disk != nil {
			disk := *g.Nodes[i].Disk
			c.Nodes[i].Disk = &disk
		}
		c.Nodes[i].Packer = clonePackerConfig(g.Nodes[i].Packer)
		c.Nodes[i].Materials = append([]MaterialBinding(nil), g.Nodes[i].Materials...)
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
		return nil, e.Wrap(e.CodeInternal, err, "serializing build IR")
	}
	return b, nil
}

// logicalGraph projects a previously validated execution graph into a distinct DTO
// that cannot represent transport. This structural split prevents a newly added URL,
// registry, credential, or mirror field from leaking into build identity merely
// because a sanitizer forgot to clear it.
func (g Graph) logicalGraph() LogicalGraph {
	c := g.normalizedCopy()
	logical := LogicalGraph{
		SchemaVersion: c.SchemaVersion,
		Platform:      c.Platform,
		Nodes:         make([]LogicalNode, 0, len(c.Nodes)),
	}
	if c.AICR != nil {
		logical.AICR = &LogicalAICRActivation{
			Service: c.AICR.Service, Accelerator: c.AICR.Accelerator, OS: c.AICR.OS,
			Intent: c.AICR.Intent, Platform: c.AICR.Platform,
			ProfileDigest: c.AICR.ProfileDigest, GeneratorDigest: c.AICR.GeneratorDigest,
			Bundle: c.AICR.Bundle.Clone(),
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
			Toolchain: imageDigestID(node.Toolchain), ToolchainPlatform: node.ToolchainPlatform,
			Platform: node.Platform,
			Network:  node.Network, Env: append([]EnvVar(nil), node.Env...),
			Patches: append([]PatchRef(nil), node.Patches...),
			Command: append([]string(nil), node.Command...), Workdir: node.Workdir,
			SourceDest: node.SourceDest, SourceCommit: node.SourceCommit,
			SourceTreeHash: node.SourceTreeHash, VendorSpecDigest: node.VendorSpecDigest,
			SourceReadOnly: node.SourceReadOnly, WriteDir: node.WriteDir,
			Disk: cloneDiskSpec(node.Disk), Packer: clonePackerConfig(node.Packer),
			Materials:  append([]MaterialBinding(nil), node.Materials...),
			OutputType: node.OutputType, ArtifactPath: node.ArtifactPath, Version: node.Version,
		})
	}
	return logical
}

func cloneDiskSpec(spec *DiskSpec) *DiskSpec {
	if spec == nil {
		return nil
	}
	clone := *spec
	return &clone
}

func clonePackerConfig(config *PackerConfig) *PackerConfig {
	if config == nil {
		return nil
	}
	clone := *config
	clone.Variables = append([]PackerVariable(nil), config.Variables...)
	clone.MaterialManifest = material.CloneProjection(config.MaterialManifest)
	return &clone
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

// Digest computes buildDigest via the one canonical codec (ADR-01): a
// domain-separated sha256 over the RFC-8785 canonical-JSON form of the graph's content
// identity (digestView), "sha256:"+hex (NKD-FR-RC-018). There is no grandfathered YAML
// exception — the build identity uses the same codec as every other digest-addressed
// object, so an auditor can recompute it in any language.
func (g Graph) Digest() (string, error) {
	if err := (&g).Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindBuild, g.logicalGraph())
}
