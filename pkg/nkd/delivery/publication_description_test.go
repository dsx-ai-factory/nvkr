// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPublicationDescriptionBindsEvidenceAndOwnsCopies(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publication.Close() })
	reference := "registry.example.test/product/releases@" + digest
	view, err := publication.Description(reference)
	if err != nil {
		t.Fatal(err)
	}
	if view.RootDigest != digest || view.Reference != reference || len(view.Outputs) != 1 || len(view.Evidence) != 1 {
		t.Fatalf("incomplete publication view: %+v", view)
	}
	output, evidence := view.Outputs[0], view.Evidence[0]
	if output.OS != "linux" || output.Arch != "amd64" {
		t.Fatalf("executable platform was not observed from exact bundle config: %+v", output)
	}
	if output.Reference != "registry.example.test/product/tools@"+output.Digest ||
		evidence.Reference != "registry.example.test/product/tools@"+evidence.ManifestDigest ||
		evidence.Binding.SubjectDigest != output.BuildSubjects[0].Digest ||
		evidence.Binding.Outputs[0] != output.Name {
		t.Fatalf("unbound output or evidence: %+v", view)
	}
	// A local caller can choose another namespace without changing checked bytes.
	// The projection must label those locators as derived, not registry observations.
	alternative, err := publication.Description("other.example.test/elsewhere/releases@" + digest)
	if err != nil {
		t.Fatal(err)
	}
	if view.ReferenceScope != "derived from supplied root reference; no registry availability claim" || alternative.ReferenceScope != view.ReferenceScope ||
		alternative.RootDigest != view.RootDigest || alternative.Metadata != view.Metadata ||
		alternative.Outputs[0].Reference != "other.example.test/elsewhere/tools@"+output.Digest ||
		alternative.Evidence[0].Reference != "other.example.test/elsewhere/tools@"+evidence.ManifestDigest {
		t.Fatalf("derived locator scope is ambiguous: original=%+v alternative=%+v", view, alternative)
	}
	before, err := publication.Description(reference)
	if err != nil {
		t.Fatal(err)
	}
	view.Metadata.ContentSubject = "mutated"
	view.Outputs[0].Components[0] = "mutated"
	view.Outputs[0].BuildSubjects[0].Digest = "mutated"
	view.Evidence[0].Binding.Outputs[0] = "mutated"
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("replaced source"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := publication.Description(reference)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("caller mutated held description: %+v, %v", after, err)
	}
	for _, tc := range []struct {
		name, reference string
		code            e.Code
	}{
		{"tag", "registry.example.test/product/releases:latest", e.CodeUnpinnedSource},
		{"wrong digest", "registry.example.test/product/releases@sha256:" + strings.Repeat("a", 64), e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := publication.Description(tc.reference)
			assertRegistryCode(t, err, tc.code)
			if !reflect.DeepEqual(got, PublicationDescription{}) {
				t.Fatal("failed projection leaked a view")
			}
		})
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 20 {
				description, err := publication.Description(reference)
				if err != nil {
					assertRegistryCode(t, err, e.CodeGateUnmet)
				} else if !reflect.DeepEqual(before, description) {
					t.Error("concurrent description changed")
				}
			}
		})
	}
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	for _, p := range []*Publication{publication, nil, {}} {
		_, err := p.Description(reference)
		assertRegistryCode(t, err, e.CodeGateUnmet)
	}
}

func TestPublicationRejectsContradictoryExecutablePlatform(t *testing.T) {
	for _, tc := range []struct {
		name, osName, arch string
		valid              bool
	}{
		{"unspecified", "", "", true},
		{"matching", "linux", "amd64", true},
		{"wrong architecture", "linux", "arm64", false},
		{"wrong OS", "windows", "amd64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, digest := releasePublicationPlatformFixture(t, tc.osName, tc.arch)
			publication, err := OpenPublication(t.Context(), root, digest)
			if !tc.valid {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if publication != nil {
					t.Fatal("contradictory platform returned a publication")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			view, err := publication.Description("registry.example.test/releases@" + digest)
			if err != nil || view.Outputs[0].OS != "linux" || view.Outputs[0].Arch != "amd64" {
				t.Fatalf("wrong payload platform: %+v, %v", view, err)
			}
		})
	}
}

func TestPublicationRejectsEvidenceWithoutOutputMembership(t *testing.T) {
	for _, outputs := range [][]string{nil, {}} {
		t.Run(map[bool]string{true: "nil", false: "empty"}[outputs == nil], func(t *testing.T) {
			root, _ := releasePublicationFixture(t)
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			for i := range inspection.Root.Objects {
				if inspection.Root.Objects[i].Evidence != nil {
					inspection.Root.Objects[i].Evidence.Outputs = outputs
				}
			}
			assertRegistryCode(t, verifyReleaseEvidence(t.Context(), root, inspection.Root), e.CodeVerifyFailed)
		})
	}
}
