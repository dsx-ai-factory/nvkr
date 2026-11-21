// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestStagedDiskStageAdmissionRejectsBeforeWork(t *testing.T) {
	for _, scenario := range []string{"production", "source token", "image token", "OIDC", "missing writer", "missing user"} {
		t.Run(scenario, func(t *testing.T) {
			plan := arcTestPlan(t, "linux/"+runtime.GOARCH)
			if scenario != "production" {
				plan = developmentPublicationPlan(t, plan, "ghcr.io/example/nvkr/development")
			}
			setDevelopmentRun(t)
			for _, key := range []string{sourceTokenEnv, githubImageToken, "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
				t.Setenv(key, "")
			}
			t.Setenv("NVKR_CREDENTIAL", "synthetic-stage-token")
			t.Setenv("NVKR_REGISTRY_USER", "synthetic-stage-user")
			code := e.CodeEngineCapability
			switch scenario {
			case "source token":
				t.Setenv(sourceTokenEnv, "synthetic-unrelated-token")
			case "image token":
				t.Setenv(githubImageToken, "synthetic-unrelated-token")
			case "OIDC":
				t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "synthetic-unrelated-token")
			case "missing writer":
				t.Setenv("NVKR_CREDENTIAL", "")
				code = e.CodeTrustPolicyMissing
			case "missing user":
				t.Setenv("NVKR_REGISTRY_USER", "")
				code = e.CodeTrustPolicyMissing
			}
			_, artifacts := planIndex(plan)
			route := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
			err := StageGitHubDisk(t.Context(), plan, plan.LifecycleOutputs[0].Name, GitHubStepOptions{Stdout: io.Discard})
			assertFactoryErrorCode(t, err, code)
			if staged := artifacts[stagedDiskReferenceID(route)]; staged.Path != "" {
				if _, err := os.Stat(staged.Path); !os.IsNotExist(err) {
					t.Fatalf("refusal wrote staged reference: %v", err)
				}
			}
		})
	}
}

// Synthetic observations exercise the factory/delivery handoff, not ARC execution,
// provider ACLs, cosign or OIDC. Registry requests use the real Distribution handler.
func TestStagedDiskPublishExecutor(t *testing.T) {
	for _, scenario := range []string{"success", "missing writer", "private admission denied", "wrong staged destination", "changed admitted root"} {
		t.Run(scenario, func(t *testing.T) {
			plan, artifacts, ref, signer := stagedDiskRuntimeFixture(t)
			route := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
			node := planNode(plan, NodePublish+"/"+route)
			t.Setenv("NVKR_CREDENTIAL", "synthetic-release-token")
			t.Setenv("NVKR_REGISTRY_USER", "synthetic-release-user")
			t.Setenv(githubRepositoryTokenEnv, "synthetic-repository-token")
			calls := 0
			admit := func(_ context.Context, repository string, destinations []string, options cistore.Options) error {
				calls++
				if repository != "example/nvkr" || options.Token != "synthetic-release-token" || options.Username != "synthetic-release-user" || options.RepositoryToken != "synthetic-repository-token" {
					t.Fatal("release admission lost credential or source binding")
				}
				for _, destination := range destinations {
					if !strings.HasPrefix(destination, "ghcr.io/example/nvkr/development/runs/12345/attempts/1/") {
						t.Fatalf("foreign admission destination %s", destination)
					}
				}
				if scenario == "private admission denied" {
					return e.New(e.CodeTrustPolicyMissing, "synthetic policy refusal")
				}
				if scenario == "changed admitted root" && calls == 2 {
					// Swap a well-formed preparation for signed controls during admission.
					controls := artifacts[attestedEvidenceMetadataID(route)].Path
					if err := os.Rename(controls, filepath.Join(t.TempDir(), "saved")); err != nil {
						t.Fatal(err)
					}
					if _, err := delivery.CopyMetadataWithExpectedRoot(t.Context(), artifacts[preparedEvidenceMetadataID(route)].Path, controls, strings.Split(ref, "@")[1]); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			code := e.CodeVerifyFailed
			switch scenario {
			case "missing writer":
				t.Setenv("NVKR_CREDENTIAL", "")
				code = e.CodeTrustPolicyMissing
			case "private admission denied":
				code = e.CodeTrustPolicyMissing
			case "wrong staged destination":
				writeReferenceFile(t, artifacts[stagedDiskReferenceID(route)].Path, strings.Replace(ref, "/attempts/1/", "/attempts/2/", 1))
			}
			var err error
			if scenario == "missing writer" {
				// Exercise the actual public dispatch and production adapter before I/O.
				err = executePublish(t.Context(), plan, node, artifacts, io.Discard)
			} else {
				err = executeStagedDiskPublishWithAdmission(t.Context(), plan, node, artifacts, io.Discard, admit, referenceSignerFactory(signer))
			}
			if scenario != "success" {
				assertFactoryErrorCode(t, err, code)
				for _, id := range node.Outputs {
					if _, err := os.Stat(artifacts[id].Path); !os.IsNotExist(err) {
						t.Fatalf("refusal wrote output %s: %v", id, err)
					}
				}
				if (scenario == "missing writer" || scenario == "wrong staged destination") && calls != 0 {
					t.Fatal("refusal reached registry policy")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("private closure admission calls=%d", calls)
			}
			published, err := readPublicationSubject(t.Context(), artifacts["publication/"+route].Path)
			if err != nil {
				t.Fatal(err)
			}
			signing, err := delivery.InspectMetadata(t.Context(), artifacts["signing-metadata/"+route].Path)
			if err != nil || signing.RootDigest != published.Digest {
				t.Fatalf("signing metadata/reference mismatch: %v", err)
			}
			if _, err := delivery.Pull(t.Context(), published.Ref, filepath.Join(t.TempDir(), "independent-read"), stagedRegistryOptions(plan.Targets[route])); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func stagedDiskRuntimeFixture(t *testing.T) (*Plan, map[string]Artifact, string, *referenceSigner) {
	t.Helper()
	plan := developmentPublicationPlan(t, arcTestPlan(t, "linux/amd64"), "ghcr.io/example/nvkr/development")
	setDevelopmentRun(t)
	t.Setenv("NVKR_CREDENTIAL", "synthetic-stage-token")
	t.Setenv("NVKR_REGISTRY_USER", "synthetic-stage-user")
	backend := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	original := remote.DefaultTransport
	remote.DefaultTransport = developmentRegistryTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "ghcr.io" {
			t.Errorf("unexpected registry host %s", request.URL.Host)
		}
		response := httptest.NewRecorder()
		incoming := request.Clone(request.Context())
		// net/http servers supply a non-nil body even for empty requests.
		if incoming.Body == nil {
			incoming.Body = http.NoBody
		}
		backend.ServeHTTP(response, incoming)
		result := response.Result()
		result.Request = request
		return result, nil
	})
	t.Cleanup(func() { remote.DefaultTransport = original })
	_, artifacts := planIndex(plan)
	expected := plan.LifecycleOutputs[0]
	routeName := outputPublicationRoute(expected.Name)
	var route Route
	for _, candidate := range plan.Routes {
		if candidate.Name == routeName {
			route = candidate
		}
	}
	wrapper := artifacts["package/"+expected.Name]
	raw := testQCOW2HeaderPayload()
	payloadPin := pipeline.FileDigest(raw)
	path := filepath.Join(t.TempDir(), "disk")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	built, err := delivery.BuildDiskArtifact(t.Context(), wrapper.Path, delivery.DiskArtifactRequest{Path: path, Digest: payloadPin, Size: int64(len(raw)), Platform: "linux/amd64", Format: "qcow2", Encryption: "none", ArtifactType: wrapper.MediaType})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.PayloadDescriptor(t.Context(), wrapper.Path)
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	component := artifacts[expected.ArtifactID].Component
	disk := &delivery.DiskOutput{PayloadDigest: payloadPin, Producer: "fixture", Component: component, Format: "qcow2", Encryption: "none", Size: int64(len(raw)), PlanDigest: planDigest, Outcome: "succeeded"}
	output := delivery.Output{Name: expected.Name, Kind: PackageDiskImage, Digest: descriptor.Digest, BuildDigest: expected.BuildDigest, Components: expected.Components, Disk: disk}
	binding := &delivery.OutputBinding{Repository: expected.Name, Format: PackageDiskImage, BuildDigest: expected.BuildDigest, Components: expected.Components, Disk: disk}
	objects := []delivery.ObjectInput{{Name: expected.Name, Kind: PackageDiskImage, Path: wrapper.Path, Layout: true, ArtifactType: wrapper.MediaType, SemanticDigest: payloadPin, BlobDigest: built.RootDigest, OS: "linux", Arch: "amd64", Output: binding}}
	for _, member := range expected.Components {
		subject := pipeline.FileDigest([]byte(member))
		if member == component {
			subject = payloadPin
		}
		binding.BuildSubjects = append(binding.BuildSubjects, delivery.BuildSubject{Component: member, Digest: subject})
		bom, err := sbom.NewSubject(expected.BuildDigest, member, subject)
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.ForSubject(bom, subject, "build-output", subject)
		if err != nil {
			t.Fatal(err)
		}
		if member == component {
			bom.Metadata.Component.Type = "operating-system"
			bom.Metadata.Component.BOMRef = "artifact:" + strings.TrimPrefix(payloadPin, "sha256:")
			dependency := "pkg:generic/fixture-installed-package@1"
			bom.Components = []sbom.Component{{Type: "library", Name: "fixture-installed-package", Version: "1", PURL: dependency, BOMRef: dependency}}
			bom.Dependencies = []sbom.Dependency{{Ref: bom.Metadata.Component.BOMRef, DependsOn: []string{dependency}}}
			bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "nvkr:dependencyInspection", Value: "disk-reconciled/v1"})
			for _, key := range []string{"nvkr:dependencyScannerDigest", "nvkr:diskInspectionRuntimeDigest", "nvkr:diskInspectorDigest", "nvkr:diskCodecDigest"} {
				bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: key, Value: expected.BuildDigest})
			}
			sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
		} else {
			bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "fixture-staged-package", Version: "1"}}, "disk-content/v1", expected.BuildDigest)
			if err != nil {
				t.Fatal(err)
			}
		}
		document, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, delivery.ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", Bytes: document, SemanticDigest: semantic, Evidence: &delivery.EvidenceBinding{Outputs: []string{expected.Name}, Component: member, Stage: "build-output", SubjectKind: "file", SubjectDigest: subject}})
	}
	content, err := delivery.ContentDigest([]delivery.Output{output})
	if err != nil {
		t.Fatal(err)
	}
	run, err := publicationRunContext(plan, planDigest, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	result, err := delivery.BuildPublicationRoot(t.Context(), artifacts[route.Input].Path, delivery.PublicationRequest{Objects: objects, Release: &delivery.ReleaseMetadata{DefinitionDigest: plan.DefinitionDigest, EnvironmentDigest: plan.EnvironmentDigest, PlanDigest: planDigest, ContentSubject: content, Composition: plan.Composition.Clone(), InvocationID: run.InvocationID()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = delivery.ExportMetadata(t.Context(), artifacts[route.Input].Path, artifacts[preparedEvidenceMetadataID(routeName)].Path); err != nil {
		t.Fatal(err)
	}
	target, err := runtimeTarget(plan, route.Target)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := delivery.StagePreparedDisk(t.Context(), artifacts[route.Input].Path, target.Ref, result.RootDigest, stagedRegistryOptions(target))
	if err != nil {
		t.Fatal(err)
	}
	staged := artifacts[stagedDiskReferenceID(routeName)].Path
	writeReferenceFile(t, staged, strings.Split(ref, "@")[0]+"@sha256:"+strings.Repeat("f", 64))
	signer := &referenceSigner{}
	attest := planNode(plan, NodeAttestEvidence+"/"+routeName)
	rejectedSigner := func(context.Context, *Plan, Node, map[string]Artifact) (sign.OCISigner, error) {
		t.Fatal("changed staged root reached signer")
		return nil, nil
	}
	assertFactoryErrorCode(t, executeAttestEvidenceMetadata(t.Context(), plan, attest, route, artifacts, rejectedSigner, io.Discard), e.CodeVerifyFailed)
	writeReferenceFile(t, staged, ref)
	// The attester needs only bounded controls and no registry credential.
	t.Setenv("NVKR_CREDENTIAL", "")
	t.Setenv("NVKR_REGISTRY_USER", "")
	if err := executeAttestEvidenceMetadata(t.Context(), plan, attest, route, artifacts, referenceSignerFactory(signer), io.Discard); err != nil {
		t.Fatal(err)
	}
	return plan, artifacts, ref, signer
}
