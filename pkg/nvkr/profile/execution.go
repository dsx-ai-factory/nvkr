// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// HostPreflightReceipt proves that the plan/host stage was performed for one
// exact profile. Its critical state is private: a zero value or caller-composed
// value cannot satisfy VerifyExecution.
type HostPreflightReceipt struct {
	profileDigest      string
	observationBinding string
	valid              bool
}

// ProfileDigest identifies the exact signed profile checked at the host stage.
func (r HostPreflightReceipt) ProfileDigest() string { return r.profileDigest }

// RequireFor validates the opaque stage-one receipt against an exact profile. It is
// intentionally usable before network access so artifact runners can snapshot the
// authenticated profile and reject a zero, forged, or changed host receipt before
// materializing any execution inputs.
func (r HostPreflightReceipt) RequireFor(ctx context.Context, p BuilderProfile) error {
	if err := contextFailure(ctx, "host-preflight receipt validation"); err != nil {
		return err
	}
	if !r.valid || r.observationBinding == "" {
		return e.New(e.CodePreflightFailed, "host-preflight receipt is absent or invalid")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	digest, err := p.Digest()
	if err != nil {
		return err
	}
	if digest != r.profileDigest {
		return e.New(e.CodePreflightFailed,
			"host-preflight receipt profile mismatch: receipt %s, selected %s", r.profileDigest, digest)
	}
	return contextFailure(ctx, "host-preflight receipt validation")
}

// NodeExecutionObservation is the execution adapter's measurement for one
// selected build node. Values describe what actually executed; expected pins are
// independently derived from the profile and IR by VerifyExecution.
type NodeExecutionObservation struct {
	NodeID           string `json:"nodeId"`
	EngineClass      string `json:"engineClass"`
	ToolchainDigest  string `json:"toolchainDigest"`
	SandboxMode      string `json:"sandboxMode"`
	NixClosureDigest string `json:"nixClosureDigest,omitempty"`
}

// RunnerOrigin records whether this process was already installed when the
// artifact run began or was reproduced by a bootstrap step in the same execution.
// Bootstrap identity is meaningful only in the latter case: requiring a bootstrap
// image for an already-running, byte-hashed local runner would bind execution to a
// tool that did not participate in it.
type RunnerOrigin string

const (
	RunnerPreexisting  RunnerOrigin = "preexisting"
	RunnerBootstrapped RunnerOrigin = "bootstrapped"
)

// ExecutionObservation is the complete stage-two measurement. The adapter at
// the materialized-input seam must hash/inspect these values; copying expected
// strings is not a measurement and is outside this module's trust model.
type ExecutionObservation struct {
	RunnerDigest             string                     `json:"runnerDigest"`
	RunnerOrigin             RunnerOrigin               `json:"runnerOrigin"`
	BootstrapToolchainDigest string                     `json:"bootstrapToolchainDigest,omitempty"`
	RuntimeIdentity          string                     `json:"runtimeIdentity"`
	NixVersion               string                     `json:"nixVersion,omitempty"`
	NixExecutableDigest      string                     `json:"nixExecutableDigest,omitempty"`
	Nodes                    []NodeExecutionObservation `json:"nodes"`
}

// VerifiedNodeIdentity is the immutable evidence view exposed by an
// ExecutionReceipt. Mutating a returned copy cannot alter the receipt.
type VerifiedNodeIdentity struct {
	NodeID           string `json:"nodeId"`
	Component        string `json:"component"`
	EngineClass      string `json:"engineClass"`
	ToolchainDigest  string `json:"toolchainDigest"`
	SandboxMode      string `json:"sandboxMode"`
	NixClosureDigest string `json:"nixClosureDigest,omitempty"`
}

// ExecutionReceipt proves that both preflight stages succeeded for one exact
// profile and selected build-node set. Downstream artifact-producing code should
// accept this type and call RequireFor before starting work.
type ExecutionReceipt struct {
	profileDigest          string
	selectionBinding       string
	hostObservationBinding string
	executionBinding       string
	nodes                  []VerifiedNodeIdentity
	valid                  bool
}

func (r ExecutionReceipt) ProfileDigest() string    { return r.profileDigest }
func (r ExecutionReceipt) ExecutionBinding() string { return r.executionBinding }

// NodeIdentities returns a defensive copy of the exact verified per-node identity.
func (r ExecutionReceipt) NodeIdentities() []VerifiedNodeIdentity {
	return append([]VerifiedNodeIdentity(nil), r.nodes...)
}

// RequireFor fails closed unless this receipt was created for the exact profile
// and selected build nodes supplied by the artifact runner.
func (r ExecutionReceipt) RequireFor(ctx context.Context, p BuilderProfile, buildNodes []ir.Node) error {
	if err := contextFailure(ctx, "execution receipt validation"); err != nil {
		return err
	}
	if !r.valid {
		return e.New(e.CodePreflightFailed, "execution preflight receipt is absent or invalid")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	profileDigest, err := p.Digest()
	if err != nil {
		return err
	}
	if profileDigest != r.profileDigest {
		return e.New(e.CodePreflightFailed, "execution receipt profile mismatch: receipt %s, selected %s", r.profileDigest, profileDigest)
	}
	selectionBinding, err := selectionBinding(buildNodes)
	if err != nil {
		return err
	}
	if selectionBinding != r.selectionBinding {
		return e.New(e.CodePreflightFailed, "execution receipt is not bound to the selected build nodes")
	}
	return contextFailure(ctx, "execution receipt validation")
}

type expectedExecution struct {
	runnerDigest             string
	bootstrapToolchainDigest string
	runtimeIdentity          string
	nixVersion               string
	nixExecutableDigest      string
	nodes                    []expectedNodeIdentity
}

type expectedNodeIdentity struct {
	nodeID           string
	component        string
	engineClass      string
	toolchainDigest  string
	sandboxMode      string
	nixClosureDigest string
}

// ValidateExecutionPlan checks that an exact build selection has every
// declaration-owned identity required for stage two. It performs no observation and
// issues no authority receipt; runners use it before network access so a structurally
// incomplete profile or node cannot trigger fetches only to fail later. Bootstrap is
// intentionally omitted because it is conditional on the observer's runner origin.
func ValidateExecutionPlan(ctx context.Context, p BuilderProfile, buildNodes []ir.Node) error {
	if err := contextFailure(ctx, "execution-identity plan validation"); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if _, err := deriveExpectedExecution(p, buildNodes, RunnerPreexisting); err != nil {
		return err
	}
	return contextFailure(ctx, "execution-identity plan validation")
}

// VerifyExecution performs ADR-07's execution-identity preflight after inputs
// are materialized and before artifact-producing work. It consumes a valid host
// receipt, derives every expected identity from its owning authority (profile or
// selected IR node), rejects missing/extra observations, and returns an opaque
// receipt only after every comparison succeeds.
func VerifyExecution(
	ctx context.Context,
	host HostPreflightReceipt,
	p BuilderProfile,
	buildNodes []ir.Node,
	observed ExecutionObservation,
) (ExecutionReceipt, error) {
	if err := contextFailure(ctx, "builder-profile execution preflight"); err != nil {
		return ExecutionReceipt{}, err
	}
	if err := p.Validate(); err != nil {
		return ExecutionReceipt{}, err
	}
	profileDigest, err := p.Digest()
	if err != nil {
		return ExecutionReceipt{}, err
	}
	if err := host.RequireFor(ctx, p); err != nil {
		return ExecutionReceipt{}, err
	}

	expected, err := deriveExpectedExecution(p, buildNodes, observed.RunnerOrigin)
	if err != nil {
		return ExecutionReceipt{}, err
	}
	selection, err := selectionBinding(buildNodes)
	if err != nil {
		return ExecutionReceipt{}, err
	}
	if err := compareDigest("runner", expected.runnerDigest, observed.RunnerDigest); err != nil {
		return ExecutionReceipt{}, err
	}
	switch observed.RunnerOrigin {
	case RunnerPreexisting:
		if observed.BootstrapToolchainDigest != "" {
			return ExecutionReceipt{}, e.New(e.CodePreflightFailed,
				"preexisting runner observation cannot claim a bootstrap toolchain")
		}
	case RunnerBootstrapped:
		if err := compareDigest("bootstrap toolchain", expected.bootstrapToolchainDigest, observed.BootstrapToolchainDigest); err != nil {
			return ExecutionReceipt{}, err
		}
	default:
		return ExecutionReceipt{}, e.New(e.CodePreflightFailed,
			"execution observation has unknown runner origin %q", observed.RunnerOrigin)
	}
	if err := compareDigest("runtime/engine implementation", expected.runtimeIdentity, observed.RuntimeIdentity); err != nil {
		return ExecutionReceipt{}, err
	}
	if expected.nixVersion != observed.NixVersion {
		return ExecutionReceipt{}, identityMismatch("Nix version", expected.nixVersion, observed.NixVersion)
	}
	if expected.nixExecutableDigest != "" {
		if err := compareDigest("Nix executable", expected.nixExecutableDigest, observed.NixExecutableDigest); err != nil {
			return ExecutionReceipt{}, err
		}
	} else if observed.NixExecutableDigest != "" {
		return ExecutionReceipt{}, e.New(e.CodePreflightFailed, "unexpected Nix executable observation for a selection with no Nix build nodes")
	}

	byID := make(map[string]NodeExecutionObservation, len(observed.Nodes))
	for _, got := range observed.Nodes {
		if strings.TrimSpace(got.NodeID) == "" {
			return ExecutionReceipt{}, e.New(e.CodePreflightFailed, "execution observation contains a node with no ID")
		}
		if _, duplicate := byID[got.NodeID]; duplicate {
			return ExecutionReceipt{}, e.New(e.CodePreflightFailed, "execution observation contains duplicate node %q", got.NodeID)
		}
		byID[got.NodeID] = got
	}

	verified := make([]VerifiedNodeIdentity, 0, len(expected.nodes))
	for _, want := range expected.nodes {
		if err := contextFailure(ctx, "builder-profile execution preflight"); err != nil {
			return ExecutionReceipt{}, err
		}
		got, ok := byID[want.nodeID]
		if !ok {
			return ExecutionReceipt{}, e.New(e.CodePreflightFailed, "execution identity observation missing selected build node %q", want.nodeID)
		}
		delete(byID, want.nodeID)
		if got.EngineClass != want.engineClass {
			return ExecutionReceipt{}, identityMismatch("node "+want.nodeID+" engineClass", want.engineClass, got.EngineClass)
		}
		if err := compareDigest("node "+want.nodeID+" component toolchain", want.toolchainDigest, got.ToolchainDigest); err != nil {
			return ExecutionReceipt{}, err
		}
		if got.SandboxMode != want.sandboxMode {
			return ExecutionReceipt{}, identityMismatch("node "+want.nodeID+" sandbox mode", want.sandboxMode, got.SandboxMode)
		}
		if want.nixClosureDigest != "" {
			if err := compareDigest("node "+want.nodeID+" Nix closure", want.nixClosureDigest, got.NixClosureDigest); err != nil {
				return ExecutionReceipt{}, err
			}
		} else if got.NixClosureDigest != "" {
			return ExecutionReceipt{}, e.New(e.CodePreflightFailed, "node %q reported an unexpected Nix closure identity", want.nodeID)
		}
		verified = append(verified, VerifiedNodeIdentity{
			NodeID: want.nodeID, Component: want.component, EngineClass: got.EngineClass,
			ToolchainDigest: got.ToolchainDigest, SandboxMode: got.SandboxMode,
			NixClosureDigest: got.NixClosureDigest,
		})
	}
	if len(byID) != 0 {
		extra := make([]string, 0, len(byID))
		for id := range byID {
			extra = append(extra, id)
		}
		sort.Strings(extra)
		return ExecutionReceipt{}, e.New(e.CodePreflightFailed, "execution observation contains unselected build node(s) %s", strings.Join(extra, ", "))
	}

	canonicalObservation := observed
	canonicalObservation.Nodes = append([]NodeExecutionObservation(nil), observed.Nodes...)
	sort.Slice(canonicalObservation.Nodes, func(i, j int) bool {
		return canonicalObservation.Nodes[i].NodeID < canonicalObservation.Nodes[j].NodeID
	})
	executionBinding, err := bind(canonical.KindExecutionObservation, canonicalObservation)
	if err != nil {
		return ExecutionReceipt{}, err
	}
	if err := contextFailure(ctx, "builder-profile execution preflight"); err != nil {
		return ExecutionReceipt{}, err
	}
	return ExecutionReceipt{
		profileDigest: profileDigest, selectionBinding: selection,
		hostObservationBinding: host.observationBinding, executionBinding: executionBinding,
		nodes: verified, valid: true,
	}, nil
}

func deriveExpectedExecution(p BuilderProfile, buildNodes []ir.Node, origin RunnerOrigin) (expectedExecution, error) {
	if len(buildNodes) == 0 {
		return expectedExecution{}, e.New(e.CodePreflightFailed, "execution preflight requires at least one selected build node; all-prebuilt graphs are profile-free")
	}
	runner, err := requiredProfileDigest(p, FieldRunnerDigest)
	if err != nil {
		return expectedExecution{}, err
	}
	runtimeIdentity, err := requiredProfileDigest(p, FieldRuntimeIdentity)
	if err != nil {
		return expectedExecution{}, err
	}

	expected := expectedExecution{runnerDigest: runner, runtimeIdentity: runtimeIdentity}
	if origin == RunnerBootstrapped {
		expected.bootstrapToolchainDigest, err = requiredBootstrapDigest(p)
		if err != nil {
			return expectedExecution{}, err
		}
	}
	requiredBuilders := map[string]bool{}
	seenNodes := map[string]bool{}
	for _, node := range buildNodes {
		if node.Kind != ir.KindBuild {
			return expectedExecution{}, e.New(e.CodeInvalidCatalog, "execution preflight selection contains non-build node %q of kind %q", node.ID, node.Kind)
		}
		if node.Platform == "darwin/arm64" {
			return expectedExecution{}, e.New(e.CodeEngineCapability, "qualified execution does not admit diagnostic payload %q for node %q", node.Platform, node.ID)
		}
		if strings.TrimSpace(node.ID) == "" || strings.TrimSpace(node.Component) == "" {
			return expectedExecution{}, e.New(e.CodeInvalidCatalog, "selected build node has no ID or component")
		}
		if seenNodes[node.ID] {
			return expectedExecution{}, e.New(e.CodeInvalidCatalog, "execution preflight selection contains duplicate node %q", node.ID)
		}
		seenNodes[node.ID] = true
		if node.Network != ir.NetworkNone {
			return expectedExecution{}, e.New(e.CodePreflightFailed, "selected build node %q is not network-isolated", node.ID)
		}
		if !node.SourceReadOnly {
			return expectedExecution{}, e.New(e.CodePreflightFailed, "selected build node %q does not mount source read-only", node.ID)
		}
		if strings.TrimSpace(node.WriteDir) == "" {
			return expectedExecution{}, e.New(e.CodePreflightFailed,
				"selected build node %q has no private writable output directory", node.ID)
		}

		builder := node.BuilderType
		if builder == "" {
			builder = ir.BuilderOCIHermetic
		}
		if builder != ir.BuilderOCIHermetic && builder != ir.BuilderNix {
			return expectedExecution{}, e.New(e.CodeBuilderNotProvided, "selected build node %q declares unsupported builder %q", node.ID, builder)
		}
		requiredBuilders[builder] = true
		toolchainDigest, err := imageDigest(node.Toolchain)
		if err != nil {
			return expectedExecution{}, e.New(e.CodeInvalidCatalog, "selected build node %q toolchain is not an exact manifest digest: %v", node.ID, err)
		}
		sandbox, err := expectedSandbox(p, builder)
		if err != nil {
			return expectedExecution{}, err
		}
		want := expectedNodeIdentity{
			nodeID: node.ID, component: node.Component, engineClass: builder,
			toolchainDigest: toolchainDigest, sandboxMode: sandbox,
		}
		if builder == ir.BuilderNix {
			if err := requireDigest(node.FlakeLockDigest); err != nil {
				return expectedExecution{}, e.New(e.CodeInvalidCatalog, "selected Nix build node %q has no exact closure pin: %v", node.ID, err)
			}
			want.nixClosureDigest = node.FlakeLockDigest
		}
		expected.nodes = append(expected.nodes, want)
	}
	builders := make([]string, 0, len(requiredBuilders))
	for builder := range requiredBuilders {
		builders = append(builders, builder)
	}
	sort.Strings(builders)
	if err := p.RequireBuilders(builders); err != nil {
		return expectedExecution{}, err
	}
	if requiredBuilders[ir.BuilderNix] {
		expected.nixVersion, err = requiredProfileValue(p, FieldNixVersion)
		if err != nil {
			return expectedExecution{}, err
		}
		expected.nixExecutableDigest, err = requiredProfileDigest(p, FieldNixExecutableDigest)
		if err != nil {
			return expectedExecution{}, err
		}
	}
	sort.Slice(expected.nodes, func(i, j int) bool { return expected.nodes[i].nodeID < expected.nodes[j].nodeID })
	return expected, nil
}

func expectedSandbox(p BuilderProfile, builder string) (string, error) {
	specific := FieldOCISandboxMode
	if builder == ir.BuilderNix {
		specific = FieldNixSandboxMode
	}
	specificValue, hasSpecific := profileField(p, specific)
	commonValue, hasCommon := profileField(p, FieldSandboxMode)
	if hasSpecific && hasCommon {
		return "", e.New(e.CodeInvalidCatalog, "builder profile %q declares both %q and %q; sandbox authority is ambiguous", p.Identity, specific, FieldSandboxMode)
	}
	if hasSpecific {
		return specificValue, nil
	}
	if hasCommon {
		return commonValue, nil
	}
	return "", e.New(e.CodePreflightFailed, "builder profile %q has no expected sandbox mode for builder %q", p.Identity, builder)
}

func requiredBootstrapDigest(p BuilderProfile) (string, error) {
	value, ok := profileField(p, FieldBootstrapToolchainDigest)
	if ok {
		if err := requireDigest(value); err != nil {
			return "", e.New(e.CodePreflightFailed, "builder profile %q field %q is not an exact digest: %v", p.Identity, FieldBootstrapToolchainDigest, err)
		}
		return value, nil
	}
	return "", e.New(e.CodePreflightFailed, "builder profile %q has no expected bootstrap toolchain digest", p.Identity)
}

func requiredProfileDigest(p BuilderProfile, name string) (string, error) {
	value, err := requiredProfileValue(p, name)
	if err != nil {
		return "", err
	}
	if err := requireDigest(value); err != nil {
		return "", e.New(e.CodePreflightFailed, "builder profile %q field %q is not an exact digest: %v", p.Identity, name, err)
	}
	return value, nil
}

func requiredProfileValue(p BuilderProfile, name string) (string, error) {
	value, ok := profileField(p, name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", e.New(e.CodePreflightFailed, "builder profile %q has no expected execution identity field %q", p.Identity, name)
	}
	return value, nil
}

func profileField(p BuilderProfile, name string) (string, bool) {
	for _, field := range p.Fields {
		if field.Name == name {
			return field.Value, true
		}
	}
	return "", false
}

func compareDigest(label, want, got string) error {
	if err := requireDigest(got); err != nil {
		if got == "" {
			got = "<absent>"
		}
		return e.New(e.CodePreflightFailed, "%s execution identity is not an exact digest (got %q): %v", label, got, err)
	}
	if want != got {
		return identityMismatch(label, want, got)
	}
	return nil
}

func identityMismatch(label, want, got string) error {
	if got == "" {
		got = "<absent>"
	}
	return e.New(e.CodePreflightFailed, "%s execution identity mismatch: expected %q, observed %q", label, want, got)
}

func requireDigest(value string) error {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return fmt.Errorf("want sha256:<64 lowercase hex>")
	}
	raw := strings.TrimPrefix(value, "sha256:")
	if strings.ToLower(raw) != raw {
		return fmt.Errorf("digest hex must be lowercase")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return fmt.Errorf("invalid digest hex: %w", err)
	}
	return nil
}

func imageDigest(ref string) (string, error) {
	if strings.HasPrefix(ref, "sha256:") {
		if err := requireDigest(ref); err != nil {
			return "", err
		}
		return ref, nil
	}
	if strings.Count(ref, "@") != 1 {
		return "", fmt.Errorf("image ref %q must contain exactly one manifest separator", ref)
	}
	i := strings.LastIndex(ref, "@")
	if i < 1 || i == len(ref)-1 {
		return "", fmt.Errorf("image ref %q is not pinned by manifest digest", ref)
	}
	digest := ref[i+1:]
	if err := requireDigest(digest); err != nil {
		return "", err
	}
	return digest, nil
}

func selectionBinding(nodes []ir.Node) (string, error) {
	copyNodes := make([]ir.Node, len(nodes))
	for i := range nodes {
		copyNodes[i] = cloneNode(nodes[i])
	}
	sort.Slice(copyNodes, func(i, j int) bool { return copyNodes[i].ID < copyNodes[j].ID })
	return bind(canonical.KindExecutionSelection, copyNodes)
}

func cloneNode(node ir.Node) ir.Node {
	clone := node
	clone.Needs = append([]string(nil), node.Needs...)
	clone.VendorCmd = append([]string(nil), node.VendorCmd...)
	clone.Env = append([]ir.EnvVar(nil), node.Env...)
	clone.Patches = append([]ir.PatchRef(nil), node.Patches...)
	clone.Command = append([]string(nil), node.Command...)
	sort.Strings(clone.Needs)
	sort.Slice(clone.Env, func(i, j int) bool { return clone.Env[i].Name < clone.Env[j].Name })
	return clone
}

type boundHostObservation struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	Present bool   `json:"present"`
}

func hostObservationBinding(observed []boundHostObservation) (string, error) {
	copyObserved := append([]boundHostObservation(nil), observed...)
	sort.Slice(copyObserved, func(i, j int) bool { return copyObserved[i].Field < copyObserved[j].Field })
	return bind(canonical.KindHostObservation, copyObserved)
}

func bind(kind string, value any) (string, error) {
	return canonical.Digest(kind, value)
}

func contextFailure(ctx context.Context, stage string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", stage)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", stage)
	}
	return nil
}
