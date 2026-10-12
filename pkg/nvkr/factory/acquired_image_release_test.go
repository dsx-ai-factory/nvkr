// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// The complete factory evidence/metadata join uses a synthetic offline package
// inventory. It establishes no real scanner execution, signing or acceptance.
func TestAcquiredImageReleaseEvidenceRetainsNativeSubject(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, media := range []types.MediaType{types.OCIManifestSchema1, types.DockerManifestSchema2} {
			t.Run(arch+"/"+string(media), func(t *testing.T) {
				original, _, _, _, subject := acquiredImageSBOMFixture(t, arch, media)
				definition, environment := original.Definition.Clone(), original.Environment.Clone()
				definition.CosignTool = "cosign"
				environment.Publication = &authored.Publication{RepositoryPrefix: "example.test/nvkr", Anonymous: true}
				environment.Signing = cosignSigningProfile()
				base := original.CatalogSnapshot
				base.Tools["cosign"] = catalog.ToolPin{Format: ToolFormatTarGzip, URL: "https://example.test/cosign.tar.gz",
					Platform: environment.UtilityPlatform, ExecutablePath: "cosign", ArchiveDigest: testCosignArchiveDigest,
					ExecutableDigest: testCosignExecutableDigest}
				stack, err := catalog.ApplyStack(base)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := Compile(t.Context(), definition, environment, stack, "test")
				if err != nil {
					t.Fatal(err)
				}
				nodes, artifacts := planIndex(plan)
				if err := executePackage(t.Context(), plan, nodes["package/image"], artifacts, io.Discard); err != nil {
					t.Fatal(err)
				}
				build := plan.Builds[0]
				bom, err := sbom.NewSubject(build.BuildDigest, "image", subject)
				if err != nil {
					t.Fatal(err)
				}
				dependency := "pkg:generic/acquired-release-test@1"
				bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "acquired-release-test", Version: "1",
					PURL: dependency, BOMRef: dependency}}, "syft-cyclonedx/v1", toolExecutableDigest(plan, "syft"))
				if err != nil {
					t.Fatal(err)
				}
				document, err := bom.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				semantic, err := bom.Digest()
				if err != nil {
					t.Fatal(err)
				}
				index := &sbom.Index{SchemaVersion: sbom.IndexSchemaVersion, BuildDigest: build.BuildDigest,
					Entries: []sbom.IndexEntry{{Artifact: "image", ArtifactSHA: subject, SBOMDigest: semantic}}}
				rawIndex, err := canonical.Marshal(index)
				if err != nil {
					t.Fatal(err)
				}
				set := artifacts["sbom/image"]
				for name, raw := range map[string][]byte{"index.json": rawIndex, "image.cdx.json": document} {
					if err := writeFactoryFile(t.Context(), filepath.Join(set.Path, name), raw, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				release := nodes["package/release"]
				evidence, err := releaseEvidenceObjects(t.Context(), plan, release, artifacts)
				if err != nil || len(evidence) != 1 || evidence[0].Evidence.SubjectDigest != subject || evidence[0].Evidence.InspectionInputDigest != "" {
					t.Fatalf("native acquired evidence must omit a distinct transport subject: %+v, %v", evidence, err)
				}
				inputs := make([]Artifact, 0, len(release.Inputs))
				for _, id := range release.Inputs {
					inputs = append(inputs, artifacts[id])
				}
				objects, cleanup, err := collectPublicationObjects(t.Context(), plan, artifacts["package/release"], inputs, evidence)
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				metadata, err := bindPublicationMetadata(t.Context(), plan, release, artifacts, objects)
				if err != nil {
					t.Fatal(err)
				}
				planDigest, err := plan.IdentityDigest()
				if err != nil {
					t.Fatal(err)
				}
				if metadata.PlanDigest != planDigest {
					t.Fatalf("release metadata Plan identity = %q, want %q", metadata.PlanDigest, planDigest)
				}
				request := delivery.PublicationRequest{Release: metadata, Objects: objects}
				path := filepath.Join(t.TempDir(), "diagnostic")
				if _, err := delivery.BuildPublicationRoot(t.Context(), path, request); err != nil {
					t.Fatal(err)
				}
				inspection, err := delivery.Inspect(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				for _, object := range inspection.Root.Objects {
					if object.Output != nil && (object.DescriptorDigest != subject || object.ArtifactType != string(media)) {
						t.Fatalf("release rewrote the upstream native manifest: %+v", object)
					}
				}
				// A separately supplied transport digest cannot fill the native
				// manifest's evidence slot or redirect its scanner-input identity.
				for i := range request.Objects {
					if request.Objects[i].Evidence != nil {
						binding := *request.Objects[i].Evidence
						binding.InspectionInputDigest = testCosignArchiveDigest
						request.Objects[i].Evidence = &binding
					}
				}
				invalid := filepath.Join(t.TempDir(), "substituted")
				_, err = delivery.BuildPublicationRoot(t.Context(), invalid, request)
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
				if _, err := os.Lstat(invalid); !os.IsNotExist(err) {
					t.Fatalf("substituted input identity exposed output: %v", err)
				}
			})
		}
	}
}
