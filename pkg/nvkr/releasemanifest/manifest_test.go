// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

var (
	fixtureDigest     = "sha256:" + strings.Repeat("a", 64)
	fixtureProvenance = "sha256:" + strings.Repeat("d", 64)
)

var requiredArtifactChecks = []string{"build-signature-and-provenance", "embedded-declarations", "payload-pins", "run-and-inputs", "schema-and-membership", "signed-evidence"}

// evidenceFixture mirrors the qualification package's complete passing v2
// evidence: one executable with native smoke, one Helm chart and one disk.
func evidenceFixture(t *testing.T) qualification.Evidence {
	t.Helper()
	digest := fixtureDigest
	origin := qualificationorigin.Description{Repository: "example/nvkr", RunID: "12", Attempt: "1", Commit: strings.Repeat("c", 40), Workflow: ".github/workflows/build.yml", DefaultBranch: "main", Ref: "refs/heads/main", InvocationID: "https://github.com/example/nvkr/actions/runs/12/attempts/1", BuilderID: "https://github.com/example/nvkr/.github/workflows/build.yml@refs/heads/main"}
	invocation := origin
	invocation.RunID = "13"
	invocation.Workflow = ".github/workflows/qualify.yml"
	invocation.InvocationID = "https://github.com/example/nvkr/actions/runs/13/attempts/1"
	invocation.BuilderID = "https://github.com/example/nvkr/.github/workflows/qualify.yml@refs/heads/main"
	evidence := qualification.Evidence{SchemaVersion: qualification.SchemaVersion, GateVersion: qualification.GateVersion, Invocation: invocation, ArtifactChecks: lifecycle.ArtifactCheckDescription{
		GateVersion: lifecycle.ArtifactCheckGateVersion, Origin: origin, CatalogDigest: digest,
		Inputs: lifecycle.ExactInputDescription{ReleaseSubject: "registry.example.test/product@" + digest, PolicyDigest: digest, DefinitionDigest: digest, EnvironmentDigest: digest, PlanDigest: digest, ContentSubject: digest},
	}, ExternalSuites: []qualification.ExternalSuite{{Name: "nvke", Status: "not-evaluated", Reason: "outside MVP scope"}}}
	for _, output := range []struct{ name, kind string }{{"cli", lifecycle.OutputExecutable}, {"chart", lifecycle.OutputHelm}, {"disk", lifecycle.OutputDiskImage}} {
		check := lifecycle.ArtifactCheckOutput{Name: output.name, Kind: output.kind, Reference: "registry.example.test/" + output.name + "@" + digest, Subject: digest, BuildDigest: digest, Overall: "pass", BuildSubjects: []delivery.BuildSubject{{Component: output.name, Digest: digest}}}
		for _, name := range requiredArtifactChecks {
			check.Checks = append(check.Checks, lifecycle.ArtifactCheckResult{Name: name, Status: "pass"})
		}
		verdict := qualification.Verdict{Output: output.name, Subject: digest, Check: "delivered-tool-version", Status: "not-applicable", Reason: "non-tool artifact format"}
		switch output.kind {
		case lifecycle.OutputExecutable:
			check.Platform = "linux/amd64"
			verdict.Status, verdict.Reason = "pass", ""
		case lifecycle.OutputDiskImage:
			check.DiskEvidence = DiskScope
		}
		evidence.ArtifactChecks.Outputs = append(evidence.ArtifactChecks.Outputs, check)
		evidence.Smoke = append(evidence.Smoke, verdict)
	}
	sortChecks(&evidence)
	policy := lifecycle.QualificationPolicy{SchemaVersion: lifecycle.QualificationPolicySchema, Source: qualificationorigin.SourcePolicy{Repository: origin.Repository, Workflow: origin.Workflow, DefaultBranch: origin.DefaultBranch}, QualificationWorkflow: invocation.Workflow, Cosign: lifecycle.QualificationCosignPolicy{Digest: digest}, OutsideMVPSuites: []string{"nvke"}, Smoke: &lifecycle.QualificationSmokePolicy{Workflow: ".github/workflows/smoke.yml", Images: map[string]string{"linux/amd64": "registry.example.test/smoke@" + digest}, Tools: map[string]string{"cli": "helm-version"}}}
	policyDigest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	worker := lifecycle.NativeToolSmokeReport{SchemaVersion: lifecycle.NativeToolSmokeSchema, ArtifactChecks: evidence.ArtifactChecks, Invocation: invocation, PolicyDigest: policyDigest, Platform: "linux/amd64", Image: policy.Smoke.Images["linux/amd64"], RuntimeImage: digest, RuntimeIdentity: digest, Status: "pass", Members: []lifecycle.ToolSmokeMember{{Output: "cli", Member: "cli", Subject: digest, BuildDigest: digest, Platform: "linux/amd64", PayloadDigest: digest, Size: 100, Profile: "helm-version", ExpectedVersion: "v1.2.3", ObservedVersion: "v1.2.3"}}}
	evidence.ToolSmoke = lifecycle.ToolSmokeDescription{ArtifactChecks: evidence.ArtifactChecks, Invocation: invocation, PolicyDigest: policyDigest, Policy: policy, Workers: []lifecycle.NativeToolSmokeReport{worker}}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("fixture evidence is invalid: %v", err)
	}
	return evidence
}

// sortChecks keeps the fixture in the evidence contract's output-name order.
func sortChecks(evidence *qualification.Evidence) {
	outputs, smoke := evidence.ArtifactChecks.Outputs, evidence.Smoke
	for i := 1; i < len(outputs); i++ {
		for j := i; j > 0 && outputs[j].Name < outputs[j-1].Name; j-- {
			outputs[j], outputs[j-1] = outputs[j-1], outputs[j]
			smoke[j], smoke[j-1] = smoke[j-1], smoke[j]
		}
	}
}

func membersFixture(evidence qualification.Evidence) []lifecycle.QualificationReleaseMember {
	members := []lifecycle.QualificationReleaseMember{}
	for _, output := range evidence.ArtifactChecks.Outputs {
		lane := lifecycle.SourceLaneHermeticBuild
		if output.Kind == lifecycle.OutputHelm {
			lane = lifecycle.SourceLaneAcquired
		}
		members = append(members, lifecycle.QualificationReleaseMember{Output: output.Name, SourceLane: lane, ProvenanceDigest: fixtureProvenance})
	}
	return members
}

func manifestFixture(t *testing.T) Manifest {
	t.Helper()
	evidence := evidenceFixture(t)
	manifest, err := fromEvidence(evidence, membersFixture(evidence))
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func requireCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestManifestProjectsCompleteQualifiedSet(t *testing.T) {
	evidence := evidenceFixture(t)
	manifest := manifestFixture(t)
	evidenceDigest, err := evidence.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Release.Reference != evidence.ArtifactChecks.Inputs.ReleaseSubject || manifest.Release.Digest != fixtureDigest ||
		manifest.Source != evidence.ArtifactChecks.Origin || manifest.Qualification.Invocation != evidence.Invocation ||
		manifest.Qualification.EvidenceDigest != evidenceDigest || manifest.Qualification.PolicyDigest != evidence.ToolSmoke.PolicyDigest ||
		manifest.Inputs.DefinitionDigest != evidence.ArtifactChecks.Inputs.DefinitionDigest || manifest.Lane != Lane {
		t.Fatalf("manifest does not bind the qualified Run and inputs: %+v", manifest)
	}
	if len(manifest.Members) != len(evidence.ArtifactChecks.Outputs) {
		t.Fatalf("members=%+v", manifest.Members)
	}
	for i, member := range manifest.Members {
		output := evidence.ArtifactChecks.Outputs[i]
		if member.Name != output.Name || member.Digest != output.Subject || member.Reference != output.Reference ||
			member.Provenance.Digest != fixtureProvenance || member.Provenance.PredicateType != sign.ProvenancePredicateType {
			t.Fatalf("member %d differs from qualified output: %+v", i, member)
		}
		if (member.Kind == lifecycle.OutputDiskImage) != (member.DiskScope == DiskScope) {
			t.Fatalf("disk scope is not explicit: %+v", member)
		}
	}
	if manifest.Members[0].SourceLane != lifecycle.SourceLaneAcquired {
		t.Fatalf("source lane not projected: %+v", manifest.Members[0])
	}
}

func TestManifestDeterministicSerializationAndIdentity(t *testing.T) {
	manifest := manifestFixture(t)
	first, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	second, err := manifestFixture(t).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("equal manifests serialize differently")
	}
	digest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	other, err := canonical.Digest(canonical.KindQualificationEvidence, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !canonical.IsDigest(digest) || digest == other {
		t.Fatalf("manifest identity is not domain separated: %s", digest)
	}
	decoded, err := Decode(first)
	if err != nil {
		t.Fatal(err)
	}
	again, err := decoded.Digest()
	if err != nil || again != digest {
		t.Fatalf("decoded identity changed: %s (%v)", again, err)
	}
}

func TestManifestDecodeRejectsNonCanonicalOrUnknownDocuments(t *testing.T) {
	raw, err := manifestFixture(t).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"empty", nil},
		{"unknown field", []byte(strings.Replace(string(raw), `{"inputs"`, `{"extra":true,"inputs"`, 1))},
		{"trailing document", append(append([]byte(nil), raw...), []byte(` {}`)...)},
		{"pretty printed", []byte(strings.Replace(string(raw), `,"lane"`, `, "lane"`, 1))},
		{"oversized", make([]byte, sign.MaxQualificationPredicateBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(tc.raw)
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestManifestStrictContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Manifest)
		code e.Code
	}{
		{"unknown schema", func(m *Manifest) { m.SchemaVersion = "nvkr.dev/release-manifest/v0" }, e.CodeVerifyFailed},
		{"qualified lane claim", func(m *Manifest) { m.Lane = "qualified" }, e.CodeVerifyFailed},
		{"mutable release", func(m *Manifest) { m.Release.Reference = "registry.example.test/product:latest" }, e.CodeUnverifiedRef},
		{"release digest mismatch", func(m *Manifest) { m.Release.Digest = "sha256:" + strings.Repeat("b", 64) }, e.CodeVerifyFailed},
		{"definition missing", func(m *Manifest) { m.Inputs.DefinitionDigest = "" }, e.CodeVerifyFailed},
		{"builder qualifies itself", func(m *Manifest) { m.Qualification.Invocation = m.Source }, e.CodeVerifyFailed},
		{"other repository qualifies", func(m *Manifest) { m.Qualification.Invocation.Repository = "example/other" }, e.CodeVerifyFailed},
		{"evidence digest missing", func(m *Manifest) { m.Qualification.EvidenceDigest = "" }, e.CodeVerifyFailed},
		{"SLSA as qualification", func(m *Manifest) { m.Qualification.PredicateType = sign.ProvenancePredicateType }, e.CodeVerifyFailed},
		{"no members", func(m *Manifest) { m.Members = nil }, e.CodeReleaseIncomplete},
		{"unsorted members", func(m *Manifest) { m.Members[0], m.Members[1] = m.Members[1], m.Members[0] }, e.CodeVerifyFailed},
		{"duplicate member", func(m *Manifest) { m.Members[1] = m.Members[0] }, e.CodeVerifyFailed},
		{"unknown kind", func(m *Manifest) { m.Members[0].Kind = "tarball" }, e.CodeVerifyFailed},
		{"member tag reference", func(m *Manifest) { m.Members[0].Reference = "registry.example.test/chart:v1" }, e.CodeUnverifiedRef},
		{"member digest mismatch", func(m *Manifest) { m.Members[0].Digest = "sha256:" + strings.Repeat("b", 64) }, e.CodeVerifyFailed},
		{"build digest missing", func(m *Manifest) { m.Members[0].BuildDigest = "" }, e.CodeVerifyFailed},
		{"unknown source lane", func(m *Manifest) { m.Members[0].SourceLane = "rebuilt" }, e.CodeVerifyFailed},
		{"provenance missing", func(m *Manifest) { m.Members[0].Provenance.Digest = "" }, e.CodeProvenanceMissing},
		{"wrong provenance type", func(m *Manifest) { m.Members[0].Provenance.PredicateType = sign.QualificationPredicateType }, e.CodeProvenanceMissing},
		{"unsupported platform", func(m *Manifest) { m.Members[0].Platform = "darwin/arm64" }, e.CodeVerifyFailed},
		{"disk inspection claim", func(m *Manifest) { m.Members[2].DiskScope = "inspected" }, e.CodeVerifyFailed},
		{"disk scope omitted", func(m *Manifest) { m.Members[2].DiskScope = "" }, e.CodeVerifyFailed},
		{"non-disk disk scope", func(m *Manifest) { m.Members[0].DiskScope = DiskScope }, e.CodeVerifyFailed},
		{"build subjects omitted", func(m *Manifest) { m.Members[0].BuildSubjects = nil }, e.CodeVerifyFailed},
		{"unsorted build subjects", func(m *Manifest) {
			m.Members[0].BuildSubjects = []delivery.BuildSubject{{Component: "z", Digest: fixtureDigest}, {Component: "a", Digest: fixtureDigest}}
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := manifestFixture(t)
			tc.edit(&manifest)
			_, err := manifest.Canonical()
			requireCode(t, err, tc.code)
			_, err = manifest.Digest()
			requireCode(t, err, tc.code)
		})
	}
}

func TestManifestProjectionRequiresExactProvenanceCoverage(t *testing.T) {
	evidence := evidenceFixture(t)
	for _, tc := range []struct {
		name string
		edit func([]lifecycle.QualificationReleaseMember) []lifecycle.QualificationReleaseMember
		code e.Code
	}{
		{"missing member", func(m []lifecycle.QualificationReleaseMember) []lifecycle.QualificationReleaseMember { return m[1:] }, e.CodeReleaseIncomplete},
		{"extra member", func(m []lifecycle.QualificationReleaseMember) []lifecycle.QualificationReleaseMember {
			return append(m, lifecycle.QualificationReleaseMember{Output: "zz", SourceLane: lifecycle.SourceLaneAcquired, ProvenanceDigest: fixtureProvenance})
		}, e.CodeReleaseIncomplete},
		{"substituted member", func(m []lifecycle.QualificationReleaseMember) []lifecycle.QualificationReleaseMember {
			m[0].Output = "other"
			return m
		}, e.CodeReleaseIncomplete},
		{"unknown lane", func(m []lifecycle.QualificationReleaseMember) []lifecycle.QualificationReleaseMember {
			m[0].SourceLane = "unknown"
			return m
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fromEvidence(evidence, tc.edit(membersFixture(evidence)))
			requireCode(t, err, tc.code)
		})
	}
}
