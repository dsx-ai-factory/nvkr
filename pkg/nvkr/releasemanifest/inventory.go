// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"context"
	"encoding/json"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/versionpolicy"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// InventoryOptions selects publisher and qualifier trust independently. Platform
// filters native images after complete authentication; indexes retain their identity.
type InventoryOptions struct {
	Consumer  lifecycle.ConsumerOptions
	Publisher lifecycle.VerificationPolicy
	Qualifier Policy
	Platform  string
}

// InventoryExporter is a read-only adapter. It has no publication/signing method
// and grants no full-release, qualification, revocation or payload acceptance.
type InventoryExporter struct {
	reference      string
	options        InventoryOptions
	authenticate   func(context.Context, string, lifecycle.VerificationPolicy) (*lifecycle.AuthenticatedRelease, error)
	verifyManifest func(context.Context, sign.Subject, []byte) error
}

// NewInventoryExporter admits trust, platform and executable capabilities before
// the caller opens the manifest file or starts registry work.
func NewInventoryExporter(reference string, options InventoryOptions) (*InventoryExporter, error) {
	if err := delivery.ValidatePublicationReference(reference); err != nil {
		return nil, err
	}
	if err := options.Publisher.Validate(); err != nil {
		return nil, err
	}
	if options.Publisher.Lane != lifecycle.LanePipeline || options.Publisher.Scope != lifecycle.ScopeAuthenticity {
		return nil, e.New(e.CodeEngineCapability, "inventory export requires pipeline-attested authenticity policy")
	}
	if err := inventoryPlatform(options.Platform); err != nil {
		return nil, err
	}
	identity := options.Qualifier.QualificationIdentity
	if identity == "" || strings.ContainsAny(identity, " \t\r\n") || !canonical.IsDigest(options.Qualifier.QualificationPolicyDigest) {
		return nil, e.New(e.CodeTrustPolicyMissing, "inventory export requires independent exact qualifier identity and policy digest")
	}
	if options.Consumer.Timeout < 0 {
		return nil, e.New(e.CodeUsage, "inventory timeout must be positive")
	}
	if options.Consumer.Timeout == 0 {
		options.Consumer.Timeout = time.Hour
	}
	if err := options.Consumer.Registry.Validate(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "authenticated inventory export requires Linux pinned cosign execution")
	}
	if options.Consumer.CosignBinary == "" || !canonical.IsDigest(options.Consumer.CosignDigest) {
		return nil, e.New(e.CodeUnpinnedInput, "inventory export requires an exact cosign executable pin")
	}
	consumer, err := lifecycle.NewConsumer(options.Consumer)
	if err != nil {
		return nil, err
	}
	signerOptions := sign.CosignOCIOptions{Binary: options.Consumer.CosignBinary, BinaryDigest: options.Consumer.CosignDigest,
		Identity: "^" + regexp.QuoteMeta(identity) + "$", Issuer: qualificationIssuer,
		Credential: options.Consumer.Credential, PlainHTTP: options.Consumer.Registry.PlainHTTP}
	if options.Consumer.Credential == (sign.Credential{}) {
		signerOptions.Keychain = options.Consumer.Registry.AmbientKeychain()
	}
	verifier, err := sign.NewCosignOCISigner(signerOptions)
	if err != nil {
		return nil, err
	}
	return &InventoryExporter{reference: reference, options: options, authenticate: consumer.Authenticate, verifyManifest: verifier.VerifyReleaseManifest}, nil
}

// Export authenticates exact manifest bytes, then the complete publication under
// consumer policy. Only after their membership agrees is a descriptive inventory
// returned. JSON-form YAML follows the pinned upstream AICR contract; it carries
// no NVKR evidence fields and cannot restore either authentication capability.
func (exporter *InventoryExporter) Export(ctx context.Context, raw []byte) ([]byte, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "inventory export requires context")
	}
	if exporter == nil || exporter.authenticate == nil || exporter.verifyManifest == nil {
		return nil, e.New(e.CodeGateUnmet, "inventory exporter was not admitted")
	}
	ctx, cancel := context.WithTimeout(ctx, exporter.options.Consumer.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "inventory export canceled")
	}
	if int64(len(raw)) > sign.MaxQualificationPredicateBytes {
		return nil, e.New(e.CodeVerifyFailed, "release manifest exceeds the signing transport limit")
	}
	// Freeze caller bytes before authentication and projection.
	raw = slices.Clone(raw)
	manifest, err := exporter.authenticateManifest(ctx, raw)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "inventory export canceled after manifest verification")
	}
	accepted, err := exporter.authenticate(ctx, exporter.reference, exporter.options.Publisher)
	if err != nil {
		return nil, err
	}
	defer func() { _ = accepted.Close() }()
	metadata, err := accepted.MetadataDescription()
	if err != nil {
		return nil, err
	}
	if err := bindInventoryManifest(manifest, metadata); err != nil {
		return nil, err
	}
	result, err := projectInventory(manifest, metadata.Outputs, exporter.options.Platform)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "inventory export canceled")
	}
	return result, nil
}

func (exporter *InventoryExporter) authenticateManifest(ctx context.Context, raw []byte) (Manifest, error) {
	manifest, err := Decode(raw)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.Release.Reference != exporter.reference {
		return Manifest{}, e.New(e.CodeVerifyFailed, "manifest differs from selected release digest")
	}
	if manifest.Source.Provider != "" || manifest.Qualification.Invocation.Provider != "" {
		return Manifest{}, e.New(e.CodeEngineCapability, "inventory manifest consumer currently admits GitHub qualification only")
	}
	if manifest.Qualification.Invocation.BuilderID != exporter.options.Qualifier.QualificationIdentity {
		return Manifest{}, e.New(e.CodeUntrustedSigner, "manifest names another qualifier")
	}
	if manifest.Qualification.PolicyDigest != exporter.options.Qualifier.QualificationPolicyDigest {
		return Manifest{}, e.New(e.CodeTrustPolicyMissing, "manifest names another qualification policy")
	}
	if err := exporter.verifyManifest(ctx, sign.Subject{Ref: exporter.reference, Digest: manifest.Release.Digest}, raw); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func bindInventoryManifest(manifest Manifest, metadata delivery.PublicationDescription) error {
	inputs := manifest.Inputs
	if metadata.Reference != manifest.Release.Reference || metadata.RootDigest != manifest.Release.Digest ||
		metadata.Metadata.InvocationID != manifest.Source.InvocationID || metadata.Metadata.ContentSubject != inputs.ContentSubject ||
		metadata.Metadata.DefinitionDigest != inputs.DefinitionDigest || metadata.Metadata.EnvironmentDigest != inputs.EnvironmentDigest ||
		metadata.Metadata.PlanDigest != inputs.PlanDigest || !metadata.Metadata.Composition.Equal(manifest.Composition) {
		return e.New(e.CodeVerifyFailed, "manifest does not bind the authenticated publication inputs")
	}
	if len(metadata.Outputs) != len(manifest.Members) {
		return e.New(e.CodeReleaseIncomplete, "manifest membership differs from authenticated publication")
	}
	for i, output := range metadata.Outputs {
		member := manifest.Members[i]
		platform := ""
		if output.OS != "" {
			platform = output.OS + "/" + output.Arch
		}
		if member.Name != output.Name || member.Kind != output.Kind || member.Reference != output.Reference ||
			member.Digest != output.Digest || member.BuildDigest != output.BuildDigest || member.Platform != platform ||
			!slices.Equal(member.BuildSubjects, output.BuildSubjects) {
			return e.New(e.CodeVerifyFailed, "manifest member %q differs from authenticated publication", member.Name)
		}
	}
	return nil
}

func inventoryPlatform(platform string) error {
	if platform != "" && platform != "linux/amd64" && platform != "linux/arm64" {
		return e.New(e.CodeUsage, "inventory platform must be linux/amd64 or linux/arm64")
	}
	return nil
}

// These are output-only wire projections, not a second parser or validator.
// Upstream contract: NVIDIA/aicr@dc78e354f2a55985782cfe0d9d6b8f1591e75dd5
// pkg/artifact/{types,parse}.go (ArtifactInventory aicr.run/v1beta1).
type inventoryIdentity struct {
	Name           string                   `json:"name"`
	Type           string                   `json:"type"`
	Reference      string                   `json:"reference"`
	Version        string                   `json:"version"`
	ManifestDigest string                   `json:"manifestDigest"`
	ArchiveDigest  string                   `json:"archiveDigest,omitempty"`
	Platform       *inventoryNativePlatform `json:"platform,omitempty"`
}
type inventoryNativePlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

func projectInventory(manifest Manifest, outputs []delivery.PublicationOutput, platform string) ([]byte, error) {
	if err := inventoryPlatform(platform); err != nil {
		return nil, err
	}
	inventory := struct {
		APIVersion string              `json:"apiVersion"`
		Kind       string              `json:"kind"`
		Artifacts  []inventoryIdentity `json:"artifacts"`
	}{"aicr.run/v1beta1", "ArtifactInventory", []inventoryIdentity{}}
	// The label identifies a producer Run, never an existing registry tag.
	version := "nvkr-run-" + manifest.Source.RunID
	for _, output := range outputs {
		if output.Kind != lifecycle.OutputHelm && output.Kind != lifecycle.OutputOCI {
			continue
		}
		if err := output.Validate(); err != nil {
			return nil, err
		}
		if err := delivery.ValidatePublicationReference(output.Reference); err != nil {
			return nil, err
		}
		// Projection bounds follow the pinned upstream pkg/defaults/artifacts.go.
		if len(output.Name) > 128 || output.Reference != strings.ToLower(output.Reference) {
			return nil, e.New(e.CodeEngineCapability, "output %q exceeds AICR identity projection limits", output.Name)
		}
		item := inventoryIdentity{Name: output.Name, Reference: "oci://" + strings.Split(output.Reference, "@")[0], ManifestDigest: output.Digest, Version: version, Type: "image"}
		if output.Kind == lifecycle.OutputHelm {
			if output.ChartVersion == "" || !canonical.IsDigest(output.ChartArchiveDigest) {
				return nil, e.New(e.CodeVerifyFailed, "chart %q lacks version or archive binding", output.Name)
			}
			item.Type, item.Version, item.ArchiveDigest = "chart", strings.ReplaceAll(output.ChartVersion, "+", "_"), output.ChartArchiveDigest
			if len(item.Version) > 128 || !versionpolicy.PinSet(output.ChartVersion) {
				return nil, e.New(e.CodeEngineCapability, "chart %q requires an AICR-compatible full semantic version within the tag limit", output.Name)
			}
		} else if output.ImageIndex == nil {
			native := output.OS + "/" + output.Arch
			if native != "linux/amd64" && native != "linux/arm64" {
				return nil, e.New(e.CodeVerifyFailed, "image %q lacks an admitted native platform or index", output.Name)
			}
			if platform != "" && native != platform {
				continue
			}
			item.Platform = &inventoryNativePlatform{OS: output.OS, Architecture: output.Arch}
		} else if platform != "" && !slices.Contains(output.ImageIndex.RequiredPlatforms, platform) {
			continue
		}
		inventory.Artifacts = append(inventory.Artifacts, item)
	}
	slices.SortFunc(inventory.Artifacts, func(a, b inventoryIdentity) int { return strings.Compare(a.Name, b.Name) })
	if len(inventory.Artifacts) == 0 {
		return nil, e.New(e.CodeReleaseIncomplete, "no chart or image matches inventory selection")
	}
	if len(inventory.Artifacts) > 10000 {
		return nil, e.New(e.CodeEngineCapability, "inventory exceeds AICR entry limit")
	}
	raw, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding AICR inventory")
	}
	if len(raw)+1 > 8<<20 {
		return nil, e.New(e.CodeEngineCapability, "inventory exceeds AICR document limit")
	}
	return append(raw, '\n'), nil
}
