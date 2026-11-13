// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func inventoryFixture(t *testing.T) (Manifest, delivery.PublicationDescription) {
	t.Helper()
	manifest := manifestFixture(t)
	metadata := delivery.PublicationDescription{Reference: manifest.Release.Reference, RootDigest: manifest.Release.Digest,
		Metadata: delivery.ReleaseMetadata{InvocationID: manifest.Source.InvocationID, DefinitionDigest: manifest.Inputs.DefinitionDigest,
			EnvironmentDigest: manifest.Inputs.EnvironmentDigest, PlanDigest: manifest.Inputs.PlanDigest, ContentSubject: manifest.Inputs.ContentSubject}}
	for _, member := range manifest.Members {
		output := delivery.PublicationOutput{Output: delivery.Output{Name: member.Name, Kind: member.Kind, Reference: member.Reference, Digest: member.Digest, BuildDigest: member.BuildDigest}, BuildSubjects: member.BuildSubjects}
		if member.Platform != "" {
			parts := strings.Split(member.Platform, "/")
			output.OS, output.Arch = parts[0], parts[1]
		}
		if member.Kind == lifecycle.OutputHelm {
			output.ChartVersion, output.ChartArchiveDigest = "1.2.3+build.1", fixtureProvenance
		}
		metadata.Outputs = append(metadata.Outputs, output)
	}
	return manifest, metadata
}

func TestInventoryManifestMembership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*delivery.PublicationDescription)
		code   e.Code
	}{
		{"match", func(*delivery.PublicationDescription) {}, ""},
		{"root", func(m *delivery.PublicationDescription) { m.RootDigest = fixtureProvenance }, e.CodeVerifyFailed},
		{"source", func(m *delivery.PublicationDescription) { m.Metadata.InvocationID += "2" }, e.CodeVerifyFailed},
		{"inputs", func(m *delivery.PublicationDescription) { m.Metadata.PlanDigest = fixtureProvenance }, e.CodeVerifyFailed},
		{"missing member", func(m *delivery.PublicationDescription) { m.Outputs = m.Outputs[:1] }, e.CodeReleaseIncomplete},
		{"sibling name", func(m *delivery.PublicationDescription) { m.Outputs[0].Name = "sibling" }, e.CodeVerifyFailed},
		{"digest", func(m *delivery.PublicationDescription) { m.Outputs[0].Digest = fixtureProvenance }, e.CodeVerifyFailed},
		{"platform", func(m *delivery.PublicationDescription) { m.Outputs[0].OS, m.Outputs[0].Arch = "linux", "arm64" }, e.CodeVerifyFailed},
		{"build subjects", func(m *delivery.PublicationDescription) { m.Outputs[0].BuildSubjects = nil }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, metadata := inventoryFixture(t)
			tc.change(&metadata)
			err := bindInventoryManifest(manifest, metadata)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireCode(t, err, tc.code)
			}
		})
	}
}

func TestInventoryAuthenticationRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*InventoryExporter, *Manifest)
		code   e.Code
		calls  int
	}{
		{"wrong digest", func(x *InventoryExporter, _ *Manifest) {
			x.reference = "registry.example.test/product@" + fixtureProvenance
		}, e.CodeVerifyFailed, 0},
		{"wrong qualifier", func(x *InventoryExporter, _ *Manifest) { x.options.Qualifier.QualificationIdentity = "other" }, e.CodeUntrustedSigner, 0},
		{"wrong policy", func(x *InventoryExporter, _ *Manifest) { x.options.Qualifier.QualificationPolicyDigest = fixtureDigest }, e.CodeTrustPolicyMissing, 0},
		{"unauthenticated manifest", func(x *InventoryExporter, _ *Manifest) {
			x.verifyManifest = func(context.Context, sign.Subject, []byte) error { return e.New(e.CodeSignatureInvalid, "refused") }
		}, e.CodeSignatureInvalid, 0},
		{"unauthenticated publication", func(*InventoryExporter, *Manifest) {}, e.CodeSignatureInvalid, 1},
		{"forged receipt", func(x *InventoryExporter, _ *Manifest) {
			x.authenticate = func(context.Context, string, lifecycle.VerificationPolicy) (*lifecycle.AuthenticatedRelease, error) {
				return &lifecycle.AuthenticatedRelease{}, nil
			}
		}, e.CodeGateUnmet, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := manifestFixture(t)
			calls := 0
			exporter := &InventoryExporter{reference: manifest.Release.Reference, options: InventoryOptions{Consumer: lifecycle.ConsumerOptions{Timeout: time.Second}, Qualifier: Policy{QualificationIdentity: manifest.Qualification.Invocation.BuilderID, QualificationPolicyDigest: manifest.Qualification.PolicyDigest}}}
			exporter.verifyManifest = func(_ context.Context, subject sign.Subject, raw []byte) error {
				expected, _ := manifest.Canonical()
				if subject.Ref != manifest.Release.Reference || subject.Digest != manifest.Release.Digest || !bytes.Equal(raw, expected) {
					t.Fatal("manifest verification lost exact bytes/subject")
				}
				return nil
			}
			exporter.authenticate = func(context.Context, string, lifecycle.VerificationPolicy) (*lifecycle.AuthenticatedRelease, error) {
				calls++
				return nil, e.New(e.CodeSignatureInvalid, "publication refused")
			}
			tc.change(exporter, &manifest)
			raw, err := manifest.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			out, err := exporter.Export(t.Context(), raw)
			requireCode(t, err, tc.code)
			if len(out) != 0 || calls != tc.calls {
				t.Fatalf("partial output or unexpected authentication calls: %q %d", out, calls)
			}
		})
	}
}

func inventoryShape(t *testing.T) []delivery.PublicationOutput {
	t.Helper()
	// Six charts, 52 native images and four indexes mirror the scout's 62 identities.
	var outputs []delivery.PublicationOutput
	for i := 0; i < 62; i++ {
		name := fmt.Sprintf("artifact-%02d", i)
		digest := fmt.Sprintf("sha256:%064x", i+100)
		output := delivery.PublicationOutput{Output: delivery.Output{Name: name, Kind: lifecycle.OutputOCI, Digest: digest, Reference: "registry.example.test/" + name + "@" + digest, BuildDigest: fixtureDigest, OS: "linux", Arch: "amd64"}}
		switch {
		case i < 6:
			output.Kind, output.ChartVersion, output.ChartArchiveDigest = lifecycle.OutputHelm, "1.2.3+build.1", fixtureProvenance
		case i < 58:
			if i%2 == 1 {
				output.Arch = "arm64"
			}
		default:
			output.OS, output.Arch = "", ""
			output.Components = []string{name}
			output.ImageIndex = &delivery.ImageIndexBinding{RequiredPlatforms: []string{"linux/amd64", "linux/arm64"}}
			var children []ir.ImageIndexBuildChild
			for j, platform := range output.ImageIndex.RequiredPlatforms {
				output.ImageIndex.Children = append(output.ImageIndex.Children, delivery.ImageIndexChildBinding{Platform: platform, ManifestDigest: fmt.Sprintf("sha256:%064x", j+1), Size: 100, BuildDigest: fixtureDigest, Component: name, BuildSubject: fixtureDigest})
				children = append(children, ir.ImageIndexBuildChild{Platform: platform, BuildDigest: fixtureDigest, Component: name})
			}
			var err error
			output.BuildDigest, err = ir.ImageIndexBuildDigest(output.ImageIndex.RequiredPlatforms, children)
			if err != nil {
				t.Fatal(err)
			}
		}
		outputs = append(outputs, output)
	}
	return outputs
}

func TestInventoryProjection(t *testing.T) {
	for _, tc := range []struct {
		platform string
		count    int
	}{{"", 62}, {"linux/amd64", 36}, {"linux/arm64", 36}} {
		t.Run(tc.platform, func(t *testing.T) {
			manifest := manifestFixture(t)
			outputs := inventoryShape(t)
			raw, err := projectInventory(manifest, outputs, tc.platform)
			if err != nil {
				t.Fatal(err)
			}
			var document struct {
				APIVersion string              `json:"apiVersion"`
				Kind       string              `json:"kind"`
				Artifacts  []inventoryIdentity `json:"artifacts"`
			}
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			if document.APIVersion != "aicr.run/v1beta1" || document.Kind != "ArtifactInventory" || len(document.Artifacts) != tc.count {
				t.Fatalf("unexpected inventory: %+v", document)
			}
			for _, item := range document.Artifacts {
				if item.Type == "chart" {
					if item.Version != "1.2.3_build.1" || item.ArchiveDigest != fixtureProvenance || item.Platform != nil {
						t.Fatal(item)
					}
				} else {
					if item.Version != "nvkr-run-12" || item.ArchiveDigest != "" {
						t.Fatal(item)
					}
					if item.Platform != nil && tc.platform != "" && item.Platform.OS+"/"+item.Platform.Architecture != tc.platform {
						t.Fatal(item)
					}
				}
				var number int
				if _, err := fmt.Sscanf(item.Name, "artifact-%02d", &number); err != nil {
					t.Fatal(err)
				}
				if item.Type == "image" && ((number < 58) != (item.Platform != nil)) {
					t.Fatal("native/index platform assertion lost", item)
				}
				if item.ManifestDigest != fmt.Sprintf("sha256:%064x", number+100) || strings.Contains(item.Reference, "@") {
					t.Fatal(item)
				}
			}
			again, err := projectInventory(manifest, outputs, tc.platform)
			if err != nil || !bytes.Equal(raw, again) {
				t.Fatal("nondeterministic projection", err)
			}
		})
	}
}

func TestInventoryProjectionRefusal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func([]delivery.PublicationOutput)
		platform string
		code     e.Code
	}{
		{"missing platform", func(o []delivery.PublicationOutput) { o[6].OS, o[6].Arch = "", "" }, "", e.CodeVerifyFailed},
		{"missing chart archive", func(o []delivery.PublicationOutput) { o[0].ChartArchiveDigest = "" }, "", e.CodeVerifyFailed},
		{"duplicate index leaf", func(o []delivery.PublicationOutput) {
			o[58].ImageIndex.Children[1].ManifestDigest = o[58].ImageIndex.Children[0].ManifestDigest
		}, "", e.CodeVerifyFailed},
		{"abbreviated chart version", func(o []delivery.PublicationOutput) { o[0].ChartVersion = "1.2" }, "", e.CodeEngineCapability},
		{"long name", func(o []delivery.PublicationOutput) { o[0].Name = strings.Repeat("a", 129) }, "", e.CodeEngineCapability},
		{"unknown selection", func([]delivery.PublicationOutput) {}, "windows/amd64", e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputs := inventoryShape(t)
			tc.change(outputs)
			raw, err := projectInventory(manifestFixture(t), outputs, tc.platform)
			requireCode(t, err, tc.code)
			if len(raw) != 0 {
				t.Fatal("partial inventory")
			}
		})
	}
}

// Optional interoperability check against the pinned upstream CLI, not a local
// reimplementation of AICR validation. See docs/release-navigation.md.
func TestInventoryAICRCLI(t *testing.T) {
	binary := os.Getenv("NVKR_AICR_TEST_BINARY")
	if binary == "" {
		t.Skip("set NVKR_AICR_TEST_BINARY to run upstream schema validation")
	}
	raw, err := projectInventory(manifestFixture(t), inventoryShape(t), "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "inventory.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "recipe", "validate", path, "--format", "json")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("upstream AICR validation: %v\n%s", err, output)
	}
}

func TestInventoryCanceledAndNoncanonicalInputs(t *testing.T) {
	manifest := manifestFixture(t)
	raw, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		cancel bool
		raw    []byte
		code   e.Code
	}{
		{"canceled", true, raw, e.CodeTimeout},
		{"noncanonical", false, append(raw, '\n'), e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			exporter := &InventoryExporter{reference: manifest.Release.Reference, options: InventoryOptions{Consumer: lifecycle.ConsumerOptions{Timeout: time.Second}}}
			exporter.authenticate = func(context.Context, string, lifecycle.VerificationPolicy) (*lifecycle.AuthenticatedRelease, error) {
				t.Fatal("rejected input reached publication")
				return nil, nil
			}
			exporter.verifyManifest = func(context.Context, sign.Subject, []byte) error {
				t.Fatal("rejected input reached signature verification")
				return nil
			}
			output, err := exporter.Export(ctx, tc.raw)
			requireCode(t, err, tc.code)
			if len(output) != 0 {
				t.Fatal("partial output")
			}
		})
	}
}

func TestInventoryRegistryHostCompatibility(t *testing.T) {
	for _, tc := range []struct {
		host     string
		accepted bool
	}{
		{"registry.example.test", true}, {"registry.example.test:5000", true},
		{"localhost", true}, {"localhost:5000", true},
		{"[::1]:5000", false}, {"registry_name.example.test", false}, {"registry.example.test.", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			output := inventoryShape(t)[6]
			output.Reference = tc.host + "/image@" + output.Digest
			if err := delivery.ValidatePublicationReference(output.Reference); err != nil {
				t.Fatal("fixture must be NVKR-admitted", err)
			}
			raw, err := projectInventory(manifestFixture(t), []delivery.PublicationOutput{output}, "")
			if tc.accepted {
				if err != nil || len(raw) == 0 {
					t.Fatalf("accepted host: %v", err)
				}
			} else {
				requireCode(t, err, e.CodeEngineCapability)
				if len(raw) != 0 {
					t.Fatal("partial inventory")
				}
			}
		})
	}
}

func TestInventoryIndexPlatformExclusion(t *testing.T) {
	outputs := inventoryShape(t)
	index := outputs[58]
	index.ImageIndex.RequiredPlatforms = index.ImageIndex.RequiredPlatforms[:1]
	index.ImageIndex.Children = index.ImageIndex.Children[:1]
	child := index.ImageIndex.Children[0]
	var err error
	index.BuildDigest, err = ir.ImageIndexBuildDigest(index.ImageIndex.RequiredPlatforms, []ir.ImageIndexBuildChild{{Platform: child.Platform, BuildDigest: child.BuildDigest, Component: child.Component}})
	if err != nil {
		t.Fatal(err)
	}
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			selection := []delivery.PublicationOutput{index}
			if !empty {
				selection = append(selection, outputs[7])
			}
			raw, err := projectInventory(manifestFixture(t), selection, "linux/arm64")
			if empty {
				requireCode(t, err, e.CodeReleaseIncomplete)
				if len(raw) != 0 {
					t.Fatal("partial inventory")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Artifacts []inventoryIdentity `json:"artifacts"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Artifacts) != 1 || doc.Artifacts[0].Name != outputs[7].Name {
				t.Fatalf("index was not excluded: %s", raw)
			}
		})
	}
}

func TestInventoryConstructorAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		change     func(*InventoryOptions)
		code       e.Code
	}{
		{"negative timeout", "linux", func(o *InventoryOptions) { o.Consumer.Timeout = -time.Second }, e.CodeUsage},
		{"unsupported host", "windows", func(*InventoryOptions) {}, e.CodeEngineCapability},
		{"Darwin admitted inputs", "darwin", func(*InventoryOptions) {}, e.CodeEngineCapability},
		{"missing pin", "linux", func(o *InventoryOptions) { o.Consumer.CosignDigest = "" }, e.CodeUnpinnedInput},
		{"non-authenticity publisher", "linux", func(o *InventoryOptions) { o.Publisher.Scope = lifecycle.ScopeRelease }, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := manifestFixture(t)
			options := InventoryOptions{Consumer: lifecycle.ConsumerOptions{Timeout: time.Minute, CosignBinary: "/must-not-execute", CosignDigest: fixtureDigest}, Publisher: lifecycle.VerificationPolicy{Lane: lifecycle.LanePipeline, Scope: lifecycle.ScopeAuthenticity, IdentityRegexp: "publisher", Issuer: "issuer"}, Qualifier: Policy{QualificationIdentity: manifest.Qualification.Invocation.BuilderID, QualificationPolicyDigest: manifest.Qualification.PolicyDigest}}
			tc.change(&options)
			exporter, err := newInventoryExporter(manifest.Release.Reference, options, tc.host)
			requireCode(t, err, tc.code)
			if exporter != nil {
				t.Fatal("refused admission returned exporter")
			}
		})
	}
}

// This integration contract uses the public Consumer to issue real receipts;
// no test-only receipt factory or verifier injection is exposed by lifecycle.
// The config is JSON with ManifestPath, Reference and Options fields.
func TestInventoryAuthenticatedExport(t *testing.T) {
	path := os.Getenv("NVKR_INVENTORY_EXPORT_TEST_CONFIG")
	if path == "" {
		t.Skip("set NVKR_INVENTORY_EXPORT_TEST_CONFIG for Linux authenticated registry contract")
	}
	var config struct {
		ManifestPath, Reference string
		Options                 InventoryOptions
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(config.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Decode(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-filtered-member=%t", missing), func(t *testing.T) {
			candidate := manifest
			candidate.Members = append([]Member(nil), manifest.Members...)
			if missing {
				found := false
				for i, member := range candidate.Members {
					if member.Platform == "linux/arm64" {
						candidate.Members = append(candidate.Members[:i], candidate.Members[i+1:]...)
						found = true
						break
					}
				}
				if !found {
					t.Fatal("fixture needs filtered-out arm64 member")
				}
			}
			input, err := candidate.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			options := config.Options
			options.Platform = "linux/amd64"
			exporter, err := NewInventoryExporter(config.Reference, options)
			if err != nil {
				t.Fatal(err)
			}
			authenticate := exporter.authenticate
			var accepted *lifecycle.AuthenticatedRelease
			exporter.authenticate = func(ctx context.Context, ref string, policy lifecycle.VerificationPolicy) (*lifecycle.AuthenticatedRelease, error) {
				var err error
				accepted, err = authenticate(ctx, ref, policy)
				return accepted, err
			}
			verified := false
			exporter.verifyManifest = func(_ context.Context, subject sign.Subject, raw []byte) error {
				if subject.Ref != config.Reference || subject.Digest != manifest.Release.Digest || !bytes.Equal(raw, input) {
					t.Fatal("lost exact manifest binding")
				}
				verified = true
				return nil
			}
			output, err := exporter.Export(t.Context(), input)
			if missing {
				requireCode(t, err, e.CodeReleaseIncomplete)
				if len(output) != 0 {
					t.Fatal("partial inventory")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Artifacts []inventoryIdentity `json:"artifacts"`
				}
				if err = json.Unmarshal(output, &doc); err != nil {
					t.Fatal(err)
				}
				if len(doc.Artifacts) == 0 {
					t.Fatal("empty inventory")
				}
				for _, item := range doc.Artifacts {
					if item.Platform != nil && item.Platform.Architecture != "amd64" {
						t.Fatal("unfiltered inventory")
					}
				}
			}
			if !verified || accepted == nil {
				t.Fatal("authentication not exercised")
			}
			_, err = accepted.MetadataDescription()
			requireCode(t, err, e.CodeGateUnmet)
		})
	}
}
