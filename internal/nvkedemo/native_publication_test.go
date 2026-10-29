// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/name"
)

func assertNativePublicationCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestNativePublicationDescriptionBindingRejectsSubstitution(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*lifecycle.Release, *lifecycle.ExactInputDescription, *factory.Plan)
	}{
		{"lane", func(r *lifecycle.Release, _ *lifecycle.ExactInputDescription, _ *factory.Plan) { r.Lane = "local" }},
		{"scope", func(r *lifecycle.Release, _ *lifecycle.ExactInputDescription, _ *factory.Plan) {
			r.Scope = "authenticity"
		}},
		{"Plan", func(_ *lifecycle.Release, x *lifecycle.ExactInputDescription, _ *factory.Plan) {
			x.PlanDigest = "different"
		}},
		{"publication subject", func(r *lifecycle.Release, _ *lifecycle.ExactInputDescription, _ *factory.Plan) {
			r.Subject = "different"
		}},
		{"policy", func(r *lifecycle.Release, _ *lifecycle.ExactInputDescription, _ *factory.Plan) {
			r.PolicyDigest = "different"
		}},
		{"Definition", func(_ *lifecycle.Release, _ *lifecycle.ExactInputDescription, p *factory.Plan) {
			p.DefinitionDigest = "different"
		}},
		{"Environment", func(_ *lifecycle.Release, _ *lifecycle.ExactInputDescription, p *factory.Plan) {
			p.EnvironmentDigest = "different"
		}},
		{"content", func(r *lifecycle.Release, _ *lifecycle.ExactInputDescription, _ *factory.Plan) {
			r.ContentSubject = "different"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Defensive descriptions exercise only comparisons, never capability
			// issuance. The real entrypoint still requires both opaque owners.
			digest := "sha256:" + strings.Repeat("a", 64)
			release := lifecycle.Release{Lane: lifecycle.LanePipeline, Scope: lifecycle.ScopeArtifactPublication,
				Subject: "example.invalid/release@" + digest, PolicyDigest: digest, ContentSubject: digest}
			plan := factory.Plan{DefinitionDigest: digest, EnvironmentDigest: digest}
			exact := lifecycle.ExactInputDescription{ReleaseSubject: release.Subject, PolicyDigest: release.PolicyDigest,
				PlanDigest: digest, DefinitionDigest: plan.DefinitionDigest, EnvironmentDigest: plan.EnvironmentDigest, ContentSubject: release.ContentSubject}
			if err := checkNativePublicationBinding(release, exact, &plan, digest); err != nil {
				t.Fatal("matching descriptions rejected", err)
			}
			test.edit(&release, &exact, &plan)
			assertNativePublicationCode(t, checkNativePublicationBinding(release, exact, &plan, digest), e.CodeVerifyFailed)
		})
	}
}

func TestNativePublicationStartupChecksDeliveredPlatformAndMembers(t *testing.T) {
	for _, arch := range []string{"arm64", "amd64"} {
		for _, test := range []struct {
			name, os string
			members  []string
			other    bool
			valid    bool
		}{
			{"selected", "linux", []string{"nerdctl", nativeIdentityHelper}, false, true},
			{"host OS", "darwin", []string{"nerdctl", nativeIdentityHelper}, false, false},
			{"other architecture", "linux", []string{"nerdctl", nativeIdentityHelper}, true, false},
			{"missing helper", "linux", []string{"nerdctl"}, false, false},
			{"extra member", "linux", []string{"extra", "nerdctl", nativeIdentityHelper}, false, false},
			{"substituted helper", "linux", []string{"nerdctl", "sibling"}, false, false},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				root := t.TempDir()
				var files []delivery.BinaryFileInput
				for _, member := range test.members {
					file := filepath.Join(root, member)
					if err := os.WriteFile(file, []byte("synthetic executable "+member), 0755); err != nil {
						t.Fatal(err)
					}
					files = append(files, delivery.BinaryFileInput{Name: member, Path: file, InstallPath: "/usr/local/bin/" + member, Mode: 0755})
				}
				selected := arch
				if test.other {
					selected = map[string]string{"arm64": "amd64", "amd64": "arm64"}[arch]
				}
				bundleRoot := filepath.Join(root, "bundle")
				if _, err := delivery.BuildBinaryBundle(t.Context(), bundleRoot, delivery.BinaryBundleRequest{
					OS: test.os, Arch: selected, Purpose: "site-startup-tools-" + selected, Epoch: 1, Files: files,
				}); err != nil {
					t.Fatal(err)
				}
				bundle, _, err := delivery.InspectBinaryBundle(t.Context(), bundleRoot)
				if err != nil {
					t.Fatal(err)
				}
				err = checkNativePublicationStartup(bundle, arch)
				if test.valid {
					if err != nil {
						t.Fatal("selected startup bundle rejected", err)
					}
				} else {
					assertNativePublicationCode(t, err, e.CodeVerifyFailed)
				}
			})
		}
	}
}

func TestNativePublicationChecksRawPayloadAfterExport(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(string, *int64, *delivery.DiskOutput) error
	}{
		{"exported size", func(_ string, written *int64, _ *delivery.DiskOutput) error { *written++; return nil }},
		{"published size", func(_ string, _ *int64, payload *delivery.DiskOutput) error { payload.Size++; return nil }},
		{"payload digest", func(_ string, _ *int64, payload *delivery.DiskOutput) error {
			payload.PayloadDigest = "sha256:" + strings.Repeat("f", 64)
			return nil
		}},
		{"changed file", func(file string, _ *int64, _ *delivery.DiskOutput) error {
			return os.WriteFile(file, []byte("changed raw bytes"), 0600)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "node.qcow2")
			data := []byte("synthetic raw payload: format interpretation remains delivery-owned")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			digest, err := DigestFile(t.Context(), file)
			if err != nil {
				t.Fatal(err)
			}
			written := int64(len(data))
			payload := delivery.DiskOutput{Size: written, PayloadDigest: digest}
			if err := checkNativePublishedDisk(t.Context(), file, written, payload); err != nil {
				t.Fatal("exact exported payload rejected", err)
			}
			if err := test.edit(file, &written, &payload); err != nil {
				t.Fatal(err)
			}
			assertNativePublicationCode(t, checkNativePublishedDisk(t.Context(), file, written, payload), e.CodeVerifyFailed)
		})
	}
}

func TestNativePublicationRejectsChangedExportedImage(t *testing.T) {
	for _, arch := range []string{"arm64", "amd64"} {
		for _, manifest := range []string{"", "sha256:" + strings.Repeat("b", 64)} {
			t.Run(arch+"/"+manifest, func(t *testing.T) {
				output := lifecycle.Artifact{Name: "ovn-image-" + arch, Digest: "sha256:" + strings.Repeat("a", 64)}
				exported := delivery.PublishedImageExport{Platform: "linux/" + arch, ManifestDigest: output.Digest}
				if err := checkNativePublishedImage(output, exported); err != nil {
					t.Fatal("exact image export rejected", err)
				}
				exported.ManifestDigest = manifest
				assertNativePublicationCode(t, checkNativePublishedImage(output, exported), e.CodeVerifyFailed)
			})
		}
	}
}

func TestNativePublicationRequiresPlainSuccessfulDiskPayload(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*lifecycle.Artifact)
	}{
		{"missing disk", func(o *lifecycle.Artifact) { o.Disk = nil }},
		{"RAW format", func(o *lifecycle.Artifact) { o.Disk.Format = "raw" }},
		{"encrypted", func(o *lifecycle.Artifact) { o.Disk.Encryption = "luks2" }},
		{"undeclared encryption", func(o *lifecycle.Artifact) { o.Disk.Encryption = "" }},
		{"failed producer", func(o *lifecycle.Artifact) { o.Disk.Outcome = "failed" }},
		{"empty payload", func(o *lifecycle.Artifact) { o.Disk.Size = 0 }},
		{"negative size", func(o *lifecycle.Artifact) { o.Disk.Size = -1 }},
		{"missing payload digest", func(o *lifecycle.Artifact) { o.Disk.PayloadDigest = "" }},
		{"malformed payload digest", func(o *lifecycle.Artifact) { o.Disk.PayloadDigest = "sha256:invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := lifecycle.Artifact{Digest: "sha256:" + strings.Repeat("a", 64), Disk: &delivery.DiskOutput{
				PayloadDigest: "sha256:" + strings.Repeat("b", 64), Size: 1024, Format: "qcow2", Encryption: "none", Outcome: "succeeded",
			}}
			payload, err := nativePublicationDiskPayload(output)
			if err != nil || payload.PayloadDigest != output.Disk.PayloadDigest || payload.PayloadDigest == output.Digest || payload.Size != output.Disk.Size {
				t.Fatal("plain payload admission conflated raw QCOW and publication wrapper", payload, err)
			}
			test.edit(&output)
			payload, err = nativePublicationDiskPayload(output)
			assertNativePublicationCode(t, err, e.CodeVerifyFailed)
			if payload != (delivery.DiskOutput{}) {
				t.Fatal("rejected disk returned a runtime payload description", payload)
			}
		})
	}
}

func TestNativePublicationAliasesSatisfyNativeDeploymentConsumers(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	const source = "ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller:source"
	capnico, err := name.NewTag(source, name.StrictValidation)
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"arm64", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			cfg := startupDescription()
			cfg.arch, cfg.OVNVersion = arch, siteOVNVersion
			addStartupRuntimeImages(&cfg)
			cfg.imageTags = map[string]string{"capnico": source}
			for role := range cfg.ImageImports {
				cfg.ImageImports[role] = nativePublicationImageAlias(role, digest, capnico.Context().Name())
			}
			cfg.ControllerImage = cfg.ImageImports["controller"]
			cfg.ProviderImage = nativePublicationImageAlias("provisioned-provider", digest, capnico.Context().Name())
			cfg.OVNImage = nativePublicationImageAlias("ovn", digest, capnico.Context().Name())
			if _, err := nativeOVNValues(siteOVNVersion, cfg.OVNImage); err != nil {
				t.Fatal("publication OVN alias rejected by the deployment values owner", err)
			}
			if _, err := SiteStartupUserData(startupTestContext(t), cfg); err != nil {
				t.Fatal("publication image aliases rejected by the native startup owner", err)
			}
		})
	}
}

func TestNativePublicationRequiresLiveCapabilities(t *testing.T) {
	for _, test := range []struct {
		name     string
		verified *lifecycle.VerifiedRelease
		receipt  *lifecycle.ExactInputReceipt
	}{{"missing", nil, nil}, {"unissued release", &lifecycle.VerifiedRelease{}, nil}, {"unissued receipt", &lifecycle.VerifiedRelease{}, &lifecycle.ExactInputReceipt{}}} {
		t.Run(test.name, func(t *testing.T) {
			inputs, err := newNativePublicationInputs(t.Context(), test.verified, test.receipt, &factory.Plan{}, "https://github.com/example/nvkr/actions/runs/1/attempts/1", "amd64")
			assertNativePublicationCode(t, err, e.CodeGateUnmet)
			if inputs != nil {
				t.Fatal("unissued capabilities produced native inputs")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := newNativePublicationInputs(ctx, nil, nil, nil, "", "amd64"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled constructor reached capabilities", err)
	}
}

func TestNativePublicationRejectsSiblingAndChangedOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*nativePublicationInputs)
	}{
		{"sibling with same build", func(in *nativePublicationInputs) { delete(in.outputs, "nvkectl") }},
		{"name substituted", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.Name = "sibling"
			in.outputs["nvkectl"] = out
		}},
		{"different build", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.BuildDigest = "different"
			in.outputs["nvkectl"] = out
		}},
		{"different members", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.Components = []string{"sibling"}
			in.outputs["nvkectl"] = out
		}},
		{"different architecture", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.Arch = "arm64"
			in.outputs["nvkectl"] = out
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := lifecycle.Artifact{Name: "nvkectl", Kind: "executable", OS: "linux", Arch: "amd64", BuildDigest: "selected", Components: []string{"nvkectl"}}
			in := &nativePublicationInputs{arch: "amd64", outputs: map[string]lifecycle.Artifact{"nvkectl": output, "sibling": output},
				plan: &factory.Plan{Builds: []factory.BuildPlan{{Name: "nvkectl", BuildDigest: "selected", Platform: "linux/amd64", Components: []string{"nvkectl"}}}}}
			if got, err := in.selected("nvkectl", "executable"); err != nil || got.Name != "nvkectl" {
				t.Fatal("exact output rejected", err)
			}
			test.edit(in)
			_, err := in.selected("nvkectl", "executable")
			assertNativePublicationCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestNativePublicationRequiresCoherentSourceProducts(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, test := range []struct {
			name     string
			edit     func(*catalog.Component)
			accepted bool
		}{
			{"same source", func(*catalog.Component) {}, true},
			{"different NVKE commit", func(c *catalog.Component) { c.Commit = strings.Repeat("c", 40) }, false},
			{"different NVKE tree", func(c *catalog.Component) { c.TreeHash = strings.Repeat("d", 40) }, false},
			{"independent acquisition", func(c *catalog.Component) { c.FromSource = false; c.Commit = strings.Repeat("e", 40) }, true},
			{"independent source", func(c *catalog.Component) { c.Repo = "https://example.invalid/ovn"; c.Commit = strings.Repeat("e", 40) }, true},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				source := catalog.Component{FromSource: true, Repo: "https://example.invalid/nvke.git", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDate: 1}
				host := factory.BuildPlan{Name: "native-tools-linux-" + arch, BuildDigest: "host-build", Platform: "linux/" + arch}
				for _, key := range []string{"nvke-installer-sources", "nvkectl", "nico-sim", "nico-simctl", "clusterctl-linux"} {
					component := source
					if key == "clusterctl-linux" {
						component = catalog.Component{Tag: "v1.13.6"}
					}
					host.ResolvedComponents = append(host.ResolvedComponents, catalog.NamedComponent{Name: key, Component: component})
				}
				product := source
				product.Repo = strings.TrimSuffix(product.Repo, ".git")
				test.edit(&product)
				in := &nativePublicationInputs{arch: arch, outputs: map[string]lifecycle.Artifact{host.Name: {Name: host.Name, BuildDigest: host.BuildDigest}},
					plan: &factory.Plan{Builds: []factory.BuildPlan{host, {Name: "cwe", ResolvedComponents: []catalog.NamedComponent{{Name: "controller", Component: product}}}}}}
				selected, err := in.installer()
				if test.accepted {
					if err != nil || selected.arch != arch || selected.commit != source.Commit {
						t.Fatal("coherent source rejected", err)
					}
				} else {
					assertNativePublicationCode(t, err, e.CodeVerifyFailed)
				}
			})
		}
	}
}
