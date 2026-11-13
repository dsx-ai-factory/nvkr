// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releasemanifest"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestReleaseInventoryAdmissionBeforeManifestIO(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		code  e.Code
	}{
		{"tag", []string{"--reference", "registry.example.test/releases:latest"}, e.CodeUnverifiedRef},
		{"platform", []string{"--platform", "windows/amd64"}, e.CodeUsage},
		{"qualifier policy", []string{"--qualification-policy-digest", "untrusted"}, e.CodeTrustPolicyMissing},
		{"timeout", []string{"--timeout", "0s"}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest := "sha256:" + strings.Repeat("a", 64)
			args := []string{"nvkr", "release", "export-inventory", "--reference", "registry.example.test/releases@" + digest, "--manifest", "/must-not-open",
				"--identity", "publisher", "--issuer", "issuer", "--qualifier-identity", "qualifier", "--qualification-policy-digest", digest, "--cosign", "/must-not-execute", "--cosign-digest", digest}
			args = append(args, tc.flags...)
			app := newApp()
			var out bytes.Buffer
			app.Writer = &out
			app.ErrWriter = &out
			err := app.RunContext(t.Context(), args)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code || out.Len() != 0 {
				t.Fatalf("admission: err=%v output=%q", err, out.String())
			}
		})
	}
}

func TestReleaseInventoryCanonicalManifestReader(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	source := qualificationorigin.Description{Repository: "example/nvkr", RunID: "12", Attempt: "1", Commit: strings.Repeat("c", 40), Workflow: ".github/workflows/build.yml", DefaultBranch: "main", Ref: "refs/heads/main", InvocationID: "https://github.com/example/nvkr/actions/runs/12/attempts/1", BuilderID: "https://github.com/example/nvkr/.github/workflows/build.yml@refs/heads/main"}
	qualifier := source
	qualifier.RunID = "13"
	qualifier.Workflow = ".github/workflows/qualify.yml"
	qualifier.InvocationID = "https://github.com/example/nvkr/actions/runs/13/attempts/1"
	qualifier.BuilderID = "https://github.com/example/nvkr/.github/workflows/qualify.yml@refs/heads/main"
	manifest := releasemanifest.Manifest{SchemaVersion: releasemanifest.SchemaVersion, Lane: releasemanifest.Lane, Source: source,
		Release:       releasemanifest.Release{Reference: "registry.example.test/releases@" + digest, Digest: digest},
		Inputs:        releasemanifest.Inputs{CatalogDigest: digest, ContentSubject: digest, DefinitionDigest: digest, EnvironmentDigest: digest, PlanDigest: digest, VerificationPolicyDigest: digest},
		Qualification: releasemanifest.Qualification{Invocation: qualifier, EvidenceDigest: digest, PolicyDigest: digest, GateVersion: qualification.GateVersion, PredicateType: sign.QualificationPredicateType},
		Members:       []releasemanifest.Member{{Name: "image", Kind: lifecycle.OutputOCI, Reference: "registry.example.test/image@" + digest, Digest: digest, BuildDigest: digest, BuildSubjects: []delivery.BuildSubject{{Component: "image", Digest: digest}}, Platform: "linux/amd64", SourceLane: lifecycle.SourceLaneHermeticBuild, Provenance: releasemanifest.Provenance{Digest: digest, PredicateType: sign.ProvenancePredicateType}}}}
	raw, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release-manifest.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var received json.RawMessage
	if err = readCanonicalJSONContext(t.Context(), path, &received); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, raw) {
		t.Fatal("CLI reader changed canonical manifest bytes")
	}
	if _, err = releasemanifest.Decode(received); err != nil {
		t.Fatal(err)
	}
}
