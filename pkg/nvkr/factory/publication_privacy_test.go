// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

func TestHostedPublicationPrivacyPrecedesLayoutAccess(t *testing.T) {
	t.Setenv("NVKR_CREDENTIAL", "publisher-credential")
	t.Setenv("NVKR_REGISTRY_USER", "publisher")
	t.Setenv("NVKR_CI_STORAGE_TOKEN", "must-not-be-used")
	t.Setenv("GITHUB_REPOSITORY", "example/project")
	for _, test := range []struct {
		name, actions, server, target string
		called                        bool
		code                          e.Code
	}{
		{"public package", "true", "https://github.com", "ghcr.io/example/project/releases", true, e.CodeTrustPolicyMissing},
		{"unknown package", "true", "https://github.com", "ghcr.io/example/project/releases", true, e.CodeTrustPolicyMissing},
		{"wrong host", "true", "https://github.example.com", "ghcr.io/example/project/releases", false, e.CodeEngineCapability},
		{"local unchanged", "", "", "ghcr.io/example/project/releases", false, ""},
		{"other registry unchanged", "true", "https://github.com", "registry.example.com/product/releases", false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GITHUB_ACTIONS", test.actions)
			t.Setenv("GITHUB_SERVER_URL", test.server)
			called := false
			_, err := admitGitHubPublication(t.Context(), Target{Ref: test.target}, "missing-layout", func(_ context.Context, source string, repositories []string, opts cistore.Options) error {
				called = true
				if source != "example/project" || !slices.Equal(repositories, []string{test.target}) || opts.Token != "publisher-credential" || opts.Username != "publisher" {
					t.Fatalf("publisher admission lost exact destination or credential: %s %v", source, repositories)
				}
				return e.New(e.CodeTrustPolicyMissing, "fixture package policy is not private")
			})
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, test.code)
			}
			if called != test.called {
				t.Fatalf("policy calls = %t, want %t", called, test.called)
			}
		})
	}
}

func TestHostedPublicationPrivacyUsesExactMetadataRepositoriesWithoutPayloads(t *testing.T) {
	plan, artifacts, _ := referenceMetadataFixture(t)
	output := plan.LifecycleOutputs[0]
	source := artifacts["package/"+outputPublicationPackage(output.Name)].Path
	metadata := filepath.Join(t.TempDir(), "metadata")
	result, err := delivery.ExportMetadata(t.Context(), source, metadata)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.Inspect(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Ref: "ghcr.io/example/project/releases-output"}
	refs, err := delivery.OutputReferences(target.Ref+"@"+result.RootDigest, inspection)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{target.Ref}
	for _, reference := range refs {
		ref, err := name.NewDigest(reference, name.StrictValidation)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, ref.Context().Name())
	}
	want = uniqueSorted(want)
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "private closure", true: "public sibling"}[denied], func(t *testing.T) {
			calls := 0
			digest, err := admitGitHubPublication(t.Context(), target, metadata, func(_ context.Context, _ string, repositories []string, _ cistore.Options) error {
				calls++
				if calls == 1 {
					if !slices.Equal(repositories, []string{target.Ref}) {
						t.Fatalf("first admission = %v", repositories)
					}
					return nil
				}
				if calls != 2 || !slices.Equal(repositories, want) {
					t.Fatalf("metadata repositories = %v, want %v", repositories, want)
				}
				if denied {
					return e.New(e.CodeTrustPolicyMissing, "sibling is public")
				}
				return nil
			})
			if denied {
				assertFactoryErrorCode(t, err, e.CodeTrustPolicyMissing)
				if digest != "" {
					t.Fatal("denied closure returned admitted subject")
				}
			} else if err != nil || digest != result.RootDigest {
				t.Fatalf("admitted subject = %s / %v", digest, err)
			}
			if calls != 2 {
				t.Fatalf("admission calls = %d", calls)
			}
		})
	}
}

func TestHostedPublicationRejectsMissingSourceBeforeOutput(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("NVKR_REGISTRY_USER", "publisher")
	t.Setenv("NVKR_CREDENTIAL", "credential")
	out := filepath.Join(t.TempDir(), "published.ref")
	plan := &Plan{Targets: map[string]Target{"release": {Kind: TargetOCI, Ref: "ghcr.io/example/project/releases"}}}
	node := Node{ID: "publish/release", Target: "release", Inputs: []string{"input"}, Outputs: []string{"result"}}
	err := executePublish(t.Context(), plan, node, map[string]Artifact{"input": {Path: "missing-layout"}, "result": {Path: out}}, io.Discard)
	assertFactoryErrorCode(t, err, e.CodeUsage)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("failed publication wrote output: %v", err)
	}
}

func TestHostedPublicationLegacyLayoutFailsClosed(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "example/project")
	root := filepath.Join(t.TempDir(), "diagnostic")
	_, err := delivery.BuildPublicationRoot(t.Context(), root, delivery.PublicationRequest{Objects: []delivery.ObjectInput{{
		Name: "record", Kind: "record", Bytes: []byte("diagnostic"), SemanticDigest: "sha256:" + strings.Repeat("a", 64),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = admitGitHubPublication(t.Context(), Target{Ref: "ghcr.io/example/project/releases"}, root,
		func(context.Context, string, []string, cistore.Options) error { calls++; return nil })
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if calls != 1 {
		t.Fatalf("legacy layout reached payload repository admission: %d", calls)
	}
}

func TestHostedPublicationCancellationPrecedesLayout(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "example/project")
	t.Setenv("NVKR_CREDENTIAL", "credential")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := admitGitHubPublication(ctx, Target{Ref: "ghcr.io/example/project/releases"}, "missing-layout", cistore.AdmitPrivatePublication)
	assertFactoryErrorCode(t, err, e.CodeTimeout)
}
